package openobserve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
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
 * 字段范围：match_all 的官方语义是「检索配置了全文索引的字段」。候选集优先
 * 取流自身的 full_text_search_keys 配置（GET /api/{org}/streams/{stream}/schema，
 * 流级权威且与时间窗无关），未配置时用官方默认候选集
 * log/message/msg/content/data/body/json/error/llm_input/llm_output，与 schema
 * 中的字符串类型字段取交集（ILIKE 要求 Utf8，非字符串字段混入会引发类型报错）。
 * schema 接口不可用（旧版本无此端点/鉴权失败等）时回退到样本行探测
 * （SELECT * LIMIT 1），探测结果按 (org, stream) 缓存 5 分钟。
 *
 * 探测成功但确认无可检索字段或无可匹配行时（空流、时间窗内无数据、流中不存在
 * 字符串字段），match_all 必然匹配空集，改写为永假谓词 false，让查询以 0 行
 * 正常返回——否则上游要么把 match_all 当字段名报 DataFusion schema 错误
 * （No field named match_all），要么在应用层拒绝（match_all() should only
 * apply to the stream that have full text search fields）。仅当 schema 接口
 * 与样本行探测均失败（网络/鉴权错误等）时才保持原样，让上游错误如实暴露。
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

// ---- stream schema probe（match_all 候选字段的流级权威来源） ----

// ooStreamSchemaField 是流 schema 接口返回的单个字段。
type ooStreamSchemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ooStreamSchemaResponse 是 GET /api/{org}/streams/{stream}/schema 的响应：
// 字段清单 + 流的全文索引配置（match_all 的权威字段来源）。
type ooStreamSchemaResponse struct {
	Schema   []ooStreamSchemaField `json:"schema"`
	Settings struct {
		FullTextSearchKeys []string `json:"full_text_search_keys"`
	} `json:"settings"`
}

// fetchStreamSchemaFields 拉取流 schema。ok=false 表示接口不可用或调用失败
// （旧版本无此端点、鉴权失败、流不存在等），调用方应回退到样本行探测。
func (oo *OpenObserve) fetchStreamSchemaFields(ctx context.Context, org, stream string) (ooStreamSchemaResponse, bool) {
	var empty ooStreamSchemaResponse
	if strings.TrimSpace(org) == "" || strings.TrimSpace(stream) == "" {
		return empty, false
	}
	body, err := oo.httpGet(ctx, "/api/"+url.PathEscape(org)+"/streams/"+url.PathEscape(stream)+"/schema")
	if err != nil {
		logger.Debugf("openobserve: fetch schema for stream %s/%s failed: %v", org, stream, err)
		return empty, false
	}
	var r ooStreamSchemaResponse
	if err := json.Unmarshal(body, &r); err != nil {
		logger.Debugf("openobserve: decode schema for stream %s/%s failed: %v", org, stream, err)
		return empty, false
	}
	return r, true
}

// isStringSchemaType 判断 schema 字段类型是否为字符串。ILIKE 要求 Utf8，
// Binary 等类型混入会引发 DataFusion 类型报错，故只认 Utf8 系与 Text/String。
func isStringSchemaType(t string) bool {
	return strings.Contains(t, "Utf8") || t == "Text" || t == "String"
}

// fullTextSearchFields 计算 match_all 的候选字段集：流配置了
// full_text_search_keys 时以其为准，否则用官方默认候选集；与 schema 中的
// 字符串字段取交集（去重、保持候选集顺序）。无交集（空流、流中不存在字符串
// 字段）时返回 nil。
func fullTextSearchFields(schemaFields []ooStreamSchemaField, ftsKeys []string) []string {
	stringFields := make(map[string]struct{}, len(schemaFields))
	for _, f := range schemaFields {
		if f.Name != "" && isStringSchemaType(f.Type) {
			stringFields[f.Name] = struct{}{}
		}
	}
	candidates := ftsKeys
	if len(candidates) == 0 {
		candidates = fullTextCandidateFields
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		if _, ok := stringFields[c]; !ok {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// searchableFieldsFromRow 从样本行提取可检索字段（schema 接口不可用时的回退）：
// 优先取默认候选集与行内字符串字段的交集；交集为空时退化为行内全部字符串字段
// （兼容自定义字段名的流），按字段名排序保证输出稳定。行内无任何字符串字段时
// 返回 nil。
func searchableFieldsFromRow(row map[string]interface{}) []string {
	stringFields := make([]string, 0, len(row))
	for k, v := range row {
		if _, isString := v.(string); isString {
			stringFields = append(stringFields, k)
		}
	}
	if len(stringFields) == 0 {
		return nil
	}
	sort.Strings(stringFields)
	inString := make(map[string]struct{}, len(stringFields))
	for _, f := range stringFields {
		inString[f] = struct{}{}
	}
	out := make([]string, 0, len(fullTextCandidateFields))
	for _, c := range fullTextCandidateFields {
		if _, ok := inString[c]; ok {
			out = append(out, c)
		}
	}
	if len(out) > 0 {
		return out
	}
	return stringFields
}

// discoverFullTextFields 确定 match_all 的候选字段集，返回 (fields, probed)：
//   - probed=false：探测失败（schema 接口与样本行查询均出错，或缺少流名/时间
//     范围无法探测），调用方应保持 SQL 原样，让上游错误如实暴露；
//   - probed=true 且 fields 为空：已确认该流/时间窗中没有可匹配的行（空流、
//     窗内无数据、流中不存在可全文检索的字符串字段），调用方应将 match_all
//     改写为永假谓词；
//   - probed=true 且 fields 非空：候选字段集（已按 org+stream 缓存 5 分钟）。
//
// 注意：本函数经 doSearch 发起的探测 SQL 不含任何待改写函数，不会递归。
func (oo *OpenObserve) discoverFullTextFields(ctx context.Context, org, stream string, startMicros, endMicros int64) ([]string, bool) {
	if strings.TrimSpace(stream) == "" || startMicros <= 0 || endMicros <= 0 {
		return nil, false
	}
	cacheKey := org + "/" + stream

	fullTextFieldsCacheMu.Lock()
	if entry, ok := fullTextFieldsCache[cacheKey]; ok && time.Now().Before(entry.expireAt) {
		fullTextFieldsCacheMu.Unlock()
		return entry.fields, true
	}
	fullTextFieldsCacheMu.Unlock()

	// 首选流 schema：流级权威，空流/空窗都能给出确定答案，且字段集完整
	// （不受单行样本恰好缺字段的影响）。
	if schemaResp, ok := oo.fetchStreamSchemaFields(ctx, org, stream); ok {
		fields := fullTextSearchFields(schemaResp.Schema, schemaResp.Settings.FullTextSearchKeys)
		if len(fields) > 0 {
			oo.cacheFullTextFields(cacheKey, fields)
			return fields, true
		}
		// 流级确认无可检索字段（如仅有 _timestamp 的空流）：不缓存，
		// 用户后续配置全文索引或摄入文本字段后，下一条查询即可恢复。
		return nil, true
	}

	// 回退：样本行探测（旧版本无 schema 端点等场景的兼容路径）。
	sql := fmt.Sprintf(`SELECT * FROM "%s" WHERE %s BETWEEN %d AND %d LIMIT 1`, stream, oo.timeField(), startMicros, endMicros)
	resp, err := oo.doSearch(ctx, org, sql, startMicros, endMicros, 0, 1)
	if err != nil {
		logger.Debugf("openobserve: discover full text fields for stream %s failed: %v", stream, err)
		return nil, false
	}
	rows := resp.rows()
	if len(rows) == 0 {
		// 窗内无数据：主查询同样返回 0 行，match_all 必然匹配空集
		return nil, true
	}
	fields := searchableFieldsFromRow(rows[0])
	if len(fields) == 0 {
		// 样本行无任何字符串字段：该行不可能被 match_all 命中
		return nil, true
	}
	oo.cacheFullTextFields(cacheKey, fields)
	return fields, true
}

func (oo *OpenObserve) cacheFullTextFields(cacheKey string, fields []string) {
	fullTextFieldsCacheMu.Lock()
	fullTextFieldsCache[cacheKey] = fullTextFieldsCacheEntry{fields: fields, expireAt: time.Now().Add(fullTextFieldsCacheTTL)}
	fullTextFieldsCacheMu.Unlock()
}

// rewriteMatchAllToFalse 把 match_all 调用改写为永假谓词 false。用于探测
// 成功但确认无可匹配行的场景（空流、时间窗内无数据、流中不存在可全文检索的
// 字符串字段）：此时 match_all 必然匹配空集，查询应以 0 行正常返回。
// NOT match_all('x') → NOT false = true：窗内无数据时同样返回 0 行；流中有
// 数据但确实无任何字符串字段时 NOT 命中全量，亦符合 match_all 对无可检索
// 字段恒为 false 的语义。
func rewriteMatchAllToFalse(sql string) string {
	out := matchAllCallSingleQuoteRe.ReplaceAllString(sql, "false")
	return matchAllCallDoubleQuoteRe.ReplaceAllString(out, "false")
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
	fields, probed := oo.discoverFullTextFields(ctx, org, stream, startMicros, endMicros)
	if !probed {
		// schema 接口与样本行探测均失败：保持原样，让上游错误如实暴露
		return out
	}
	if len(fields) == 0 {
		// 已确认无可匹配行：改写为永假谓词，查询以 0 行返回，
		// 避免上游把 match_all 当字段名报 400
		return rewriteMatchAllToFalse(out)
	}
	return rewriteMatchAllSQL(out, fields)
}
