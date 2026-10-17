package openobserve

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/toolkits/pkg/logger"
)

/**
 * OpenObserve 全文检索函数的 DataFusion 兼容层。
 *
 * 背景：OpenObserve 官方 SQL 在 DataFusion 之外扩展了一批全文检索函数
 * （match_all / str_match / re_match 等，见官方 SQL Reference）。部分部署
 * （新版 DataFusion 内核或未注册相应 UDF 的版本）并不支持这些函数，会把
 * match_all 当作普通字段名解析，查询直接失败：
 *
 *	Search field not found: Schema error: No field named match_all.
 *
 * 用户在 N9E 查询框里按官方文档/原版 UI 抄来的 SQL 因此不可用。本文件在
 * doSearch 转发前把这些函数机械改写为 DataFusion 原生谓词：
 *
 *	match_all('term')             → (f1 ILIKE '%term%' ESCAPE '\' OR ...)
 *	NOT match_all('term')         → NOT (...)（NOT 前缀原样保留）
 *	str_match(f, 'v')             → f LIKE '%v%' ESCAPE '\'（大小写敏感）
 *	str_match_ignore_case(f, 'v') → f ILIKE '%v%' ESCAPE '\'
 *	re_match(f, 'p')              → regexp_like(f, 'p')
 *	re_not_match(f, 'p')          → NOT regexp_like(f, 'p')
 *
 * match_all 同时支持官方文档的前缀/后缀/包含模式：ab* → ab%、*ab → %ab、
 * *ab* / ab → %ab%、key1 key2* → %key1 key2%（短语前缀的近似）。
 *
 * 字段范围：match_all 的官方语义是「检索配置了全文索引的字段」，默认候选集
 * （官方文档）：log/message/msg/content/data/body/json/error/llm_input/llm_output。
 * 改写前用样本行探测流中实际存在且为字符串值的候选字段（ILIKE 要求 Utf8，
 * 非字符串字段混入会引发类型报错），探测结果按 (org, stream) 缓存 5 分钟；
 * 探测不到候选字段时保持原样，让上游错误如实暴露，绝不生成必然失败的 SQL。
 *
 * 已知未覆盖：histogram(ts, '1 minute') 等聚合函数（DataFusion 原生
 * date_trunc 可替代，语义直观，留待用户自行适配）。
 */

// fullTextCandidateFields 是 OpenObserve 默认开启全文索引的字段（官方文档），
// match_all 只检索这些字段；改写时与流中实际存在的字符串字段取交集。
var fullTextCandidateFields = []string{"log", "message", "msg", "content", "data", "body", "json", "error", "llm_input", "llm_output"}

var (
	// match_all('term') / match_all("term")。RE2 不支持反向引用，两种引号各一条。
	// 字面量内允许 '' / ""  doubling 与 \ 转义。
	matchAllCallSingleQuoteRe = regexp.MustCompile(`(?i)\bmatch_all\s*\(\s*'((?:[^'\\]|\\.|'')*)'\s*\)`)
	matchAllCallDoubleQuoteRe = regexp.MustCompile(`(?i)\bmatch_all\s*\(\s*"((?:[^"\\]|\\.|"")*)"\s*\)`)

	// str_match(field, 'v') / str_match_ignore_case(field, 'v')，
	// 官方别名 match_field / match_field_ignore_case。group2 为 _ignore_case 后缀。
	strMatchCallRe = regexp.MustCompile(`(?i)\b(?:str_match|match_field)(_ignore_case)?\s*\(\s*("[A-Za-z_][A-Za-z0-9_]*"|[A-Za-z_][A-Za-z0-9_.]*)\s*,\s*'((?:[^'\\]|\\.|'')*)'\s*\)`)

	// re_match(field, 'p') / re_not_match(field, 'p')。group2 为 _not 后缀。
	reMatchCallRe = regexp.MustCompile(`(?i)\bre(_not)?_match\s*\(\s*("[A-Za-z_][A-Za-z0-9_]*"|[A-Za-z_][A-Za-z0-9_.]*)\s*,\s*'((?:[^'\\]|\\.|'')*)'\s*\)`)

	// 从 SQL 里提取 FROM "stream" 的流名（探测字段范围用）。
	fromStreamRe = regexp.MustCompile(`(?i)\bfrom\s+"([^"]+)"`)
)

// unescapeSQLLiteral 还原 SQL 字符串字面量里的转义：” 与 \' 都表示单引号。
func unescapeSQLLiteral(s string) string {
	s = strings.ReplaceAll(s, "''", "'")
	s = strings.ReplaceAll(s, `\'`, "'")
	return s
}

// escapeSQLLiteral 把值包进单引号字面量前转义单引号。
func escapeSQLLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// escapeLikeValue 转义 LIKE 模式串中的通配符（配合 ESCAPE '\' 使用）。
func escapeLikeValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// matchAllTermToLikePattern 把官方模式串转成 LIKE 模式：
// ab* → ab%（单 token 前缀）、*ab → %ab（后缀）、*ab* / ab → %ab%（包含）、
// key1 key2* → %key1 key2%（短语前缀近似：多 token 的前缀模式退化为包含，
// 避免只匹配字段开头而漏掉正文中的短语）。
func matchAllTermToLikePattern(term string) string {
	prefixStar := strings.HasPrefix(term, "*")
	suffixStar := strings.HasSuffix(term, "*")
	core := escapeLikeValue(strings.TrimSpace(strings.Trim(term, "*")))
	multiToken := strings.Contains(core, " ")
	switch {
	case prefixStar && suffixStar:
		return "%" + core + "%"
	case prefixStar:
		return "%" + core
	case suffixStar && !multiToken:
		return core + "%"
	default:
		return "%" + core + "%"
	}
}

// buildILikeDisjunction 生成 (f1 ILIKE 'p' ESCAPE '\' OR f2 ...) 谓词。
// likePattern 已是 LIKE 转义后的模式串（尚未做 SQL 字面量转义）。
func buildILikeDisjunction(fields []string, likePattern string) string {
	quoted := escapeSQLLiteral(likePattern)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf(`"%s" ILIKE '%s' ESCAPE '\'`, f, quoted))
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func hasMatchAllCall(sql string) bool {
	return matchAllCallSingleQuoteRe.MatchString(sql) || matchAllCallDoubleQuoteRe.MatchString(sql)
}

// replaceMatchAllCalls 用 fields 重写一类引号风格的 match_all 调用。
func replaceMatchAllCalls(sql string, re *regexp.Regexp, fields []string) string {
	return re.ReplaceAllStringFunc(sql, func(matched string) string {
		m := re.FindStringSubmatch(matched)
		if m == nil {
			return matched
		}
		term := unescapeSQLLiteral(m[1])
		return buildILikeDisjunction(fields, matchAllTermToLikePattern(term))
	})
}

// rewriteMatchAllSQL 把 match_all('term') 改写为 DataFusion 原生 ILIKE 析取。
// fields 为数据流中实际存在的候选全文字段；为空时原样返回（调用方负责
// 在探测失败时不进入本函数）。
func rewriteMatchAllSQL(sql string, fields []string) string {
	if len(fields) == 0 {
		return sql
	}
	out := replaceMatchAllCalls(sql, matchAllCallSingleQuoteRe, fields)
	out = replaceMatchAllCalls(out, matchAllCallDoubleQuoteRe, fields)
	return out
}

// rewriteSearchFunctionSQL 改写 str_match / re_match 家族（不依赖字段探测，
// 字段由用户显式给出，语义一对一映射）。
// strMatchCallRe 分组：1=_ignore_case 后缀，2=字段，3=值。
// reMatchCallRe 分组：1=_not 后缀，2=字段，3=正则。
func rewriteSearchFunctionSQL(sql string) string {
	out := strMatchCallRe.ReplaceAllStringFunc(sql, func(matched string) string {
		m := strMatchCallRe.FindStringSubmatch(matched)
		if m == nil {
			return matched
		}
		op := "LIKE" // str_match 大小写敏感，DataFusion LIKE 亦大小写敏感
		if m[1] != "" {
			op = "ILIKE"
		}
		value := escapeSQLLiteral(escapeLikeValue(unescapeSQLLiteral(m[3])))
		return fmt.Sprintf(`%s %s '%%%s%%' ESCAPE '\'`, m[2], op, value)
	})
	out = reMatchCallRe.ReplaceAllStringFunc(out, func(matched string) string {
		m := reMatchCallRe.FindStringSubmatch(matched)
		if m == nil {
			return matched
		}
		pattern := escapeSQLLiteral(unescapeSQLLiteral(m[3]))
		expr := fmt.Sprintf(`regexp_like(%s, '%s')`, m[2], pattern)
		if m[1] != "" { // re_not_match
			return "NOT " + expr
		}
		return expr
	})
	return out
}

// extractStreamFromSQL 提取 FROM "stream" 的流名；提取不到返回空串。
func extractStreamFromSQL(sql string) string {
	m := fromStreamRe.FindStringSubmatch(sql)
	if m == nil {
		return ""
	}
	return m[1]
}

type fullTextFieldsCacheEntry struct {
	fields   []string
	expireAt time.Time
}

var (
	fullTextFieldsCache    = map[string]fullTextFieldsCacheEntry{}
	fullTextFieldsCacheMu  sync.Mutex
	fullTextFieldsCacheTTL = 5 * time.Minute
)

// discoverFullTextFields 用样本行探测 stream 中实际存在且为字符串值的候选
// 全文字段。时间范围为 0（未注入）时跳过探测：没有范围就无法定位样本行。
// 注意：本函数经 doSearch 发起的探测 SQL 不含任何待改写函数，不会递归。
func (oo *OpenObserve) discoverFullTextFields(ctx context.Context, org, stream string, startMicros, endMicros int64) []string {
	if strings.TrimSpace(stream) == "" || startMicros <= 0 || endMicros <= 0 {
		return nil
	}
	cacheKey := org + "/" + stream

	fullTextFieldsCacheMu.Lock()
	if entry, ok := fullTextFieldsCache[cacheKey]; ok && time.Now().Before(entry.expireAt) {
		fullTextFieldsCacheMu.Unlock()
		return entry.fields
	}
	fullTextFieldsCacheMu.Unlock()

	sql := fmt.Sprintf(`SELECT * FROM "%s" WHERE %s BETWEEN %d AND %d LIMIT 1`, stream, oo.timeField(), startMicros, endMicros)
	resp, err := oo.doSearch(ctx, org, sql, startMicros, endMicros, 0, 1)
	if err != nil {
		logger.Debugf("openobserve: discover full text fields for stream %s failed: %v", stream, err)
		return nil
	}
	rows := resp.rows()
	if len(rows) == 0 {
		return nil
	}

	fields := make([]string, 0, len(fullTextCandidateFields))
	for _, candidate := range fullTextCandidateFields {
		v, ok := rows[0][candidate]
		if !ok {
			continue
		}
		// ILIKE 要求 Utf8：样本行里非字符串值的字段跳过，避免改写后类型报错
		if _, isString := v.(string); !isString {
			continue
		}
		fields = append(fields, candidate)
	}
	if len(fields) == 0 {
		return nil
	}

	fullTextFieldsCacheMu.Lock()
	fullTextFieldsCache[cacheKey] = fullTextFieldsCacheEntry{fields: fields, expireAt: time.Now().Add(fullTextFieldsCacheTTL)}
	fullTextFieldsCacheMu.Unlock()
	return fields
}

// rewriteFullTextSQL 转发前的兼容改写入口：先改 str_match/re_match 家族，
// 再按需探测字段并改写 match_all。无待改写函数时零开销返回。
func (oo *OpenObserve) rewriteFullTextSQL(ctx context.Context, org, sql string, startMicros, endMicros int64) string {
	if !strings.Contains(sql, "(") {
		return sql
	}
	out := rewriteSearchFunctionSQL(sql)
	if !hasMatchAllCall(out) {
		return out
	}
	stream := extractStreamFromSQL(out)
	if stream == "" {
		stream = oo.stream()
	}
	fields := oo.discoverFullTextFields(ctx, org, stream, startMicros, endMicros)
	if len(fields) == 0 {
		// 探测不到候选字段：保持原样，让上游错误如实暴露
		return out
	}
	return rewriteMatchAllSQL(out, fields)
}
