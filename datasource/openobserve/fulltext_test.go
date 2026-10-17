package openobserve

import "testing"

func TestMatchAllTermToLikePattern(t *testing.T) {
	cases := []struct {
		term string
		want string
	}{
		{"error", "%error%"},          // 包含（无星号默认包含）
		{"err*", "err%"},              // 前缀
		{"*err", "%err"},              // 后缀
		{"*err*", "%err%"},            // 包含
		{"**err**", "%err%"},          // 两端多星号
		{"key1 key2*", "%key1 key2%"}, // 短语前缀近似
		{"50%", `%50\%%`},             // LIKE 元字符转义
		{"a_b", `%a\_b%`},             // 下划线转义
		{`a\b`, `%a\\b%`},             // 反斜杠转义
		{"", "%%"},                    // 空 term（退化但不崩）
	}
	for _, c := range cases {
		if got := matchAllTermToLikePattern(c.term); got != c.want {
			t.Errorf("matchAllTermToLikePattern(%q) = %q, want %q", c.term, got, c.want)
		}
	}
}

func TestRewriteMatchAllSQL(t *testing.T) {
	fields := []string{"log", "message"}
	like := func(p string) string {
		return `("log" ILIKE '` + p + `' ESCAPE '\' OR "message" ILIKE '` + p + `' ESCAPE '\')`
	}
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "contains",
			sql:  `SELECT * FROM "default" WHERE match_all('error')`,
			want: `SELECT * FROM "default" WHERE ` + like("%error%"),
		},
		{
			name: "case insensitive function name",
			sql:  `SELECT * FROM "default" WHERE MATCH_ALL('error')`,
			want: `SELECT * FROM "default" WHERE ` + like("%error%"),
		},
		{
			name: "prefix pattern",
			sql:  `SELECT * FROM "default" WHERE match_all('err*')`,
			want: `SELECT * FROM "default" WHERE ` + like("err%"),
		},
		{
			name: "postfix pattern",
			sql:  `SELECT * FROM "default" WHERE match_all('*err')`,
			want: `SELECT * FROM "default" WHERE ` + like("%err"),
		},
		{
			name: "double quoted literal",
			sql:  `SELECT * FROM "default" WHERE match_all("error")`,
			want: `SELECT * FROM "default" WHERE ` + like("%error%"),
		},
		{
			name: "NOT form keeps prefix",
			sql:  `SELECT * FROM "default" WHERE NOT match_all('error')`,
			want: `SELECT * FROM "default" WHERE NOT ` + like("%error%"),
		},
		{
			name: "escaped quote in term",
			sql:  `SELECT * FROM "default" WHERE match_all('it''s')`,
			want: `SELECT * FROM "default" WHERE ` + like("%it''s%"),
		},
		{
			name: "percent in term is escaped",
			sql:  `SELECT * FROM "default" WHERE match_all('50%')`,
			want: `SELECT * FROM "default" WHERE ` + like(`%50\%%`),
		},
		{
			name: "combined with other conditions",
			sql:  `SELECT * FROM "default" WHERE match_all('error') AND code >= 500`,
			want: `SELECT * FROM "default" WHERE ` + like("%error%") + ` AND code >= 500`,
		},
		{
			name: "multiple calls",
			sql:  `SELECT * FROM "default" WHERE match_all('a') OR match_all('b')`,
			want: `SELECT * FROM "default" WHERE ` + like("%a%") + ` OR ` + like("%b%"),
		},
	}
	for _, c := range cases {
		if got := rewriteMatchAllSQL(c.sql, fields); got != c.want {
			t.Errorf("%s:\n got: %s\nwant: %s", c.name, got, c.want)
		}
	}
}

func TestRewriteMatchAllSQLNoFields(t *testing.T) {
	// rewriteMatchAllSQL 自身对空字段集保持原样（防御性契约）；调用方
	// rewriteFullTextSQL 在探测确认无可匹配行时会改走 rewriteMatchAllToFalse
	sql := `SELECT * FROM "default" WHERE match_all('error')`
	if got := rewriteMatchAllSQL(sql, nil); got != sql {
		t.Errorf("rewriteMatchAllSQL with no fields = %s, want unchanged", got)
	}
	if got := rewriteMatchAllSQL(sql, []string{}); got != sql {
		t.Errorf("rewriteMatchAllSQL with empty fields = %s, want unchanged", got)
	}
}

func TestRewriteMatchAllToFalse(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "single call",
			sql:  `SELECT * FROM "default" WHERE match_all('error')`,
			want: `SELECT * FROM "default" WHERE false`,
		},
		{
			name: "double quoted literal",
			sql:  `SELECT * FROM "default" WHERE match_all("error")`,
			want: `SELECT * FROM "default" WHERE false`,
		},
		{
			name: "case insensitive function name",
			sql:  `SELECT * FROM "default" WHERE MATCH_ALL('error')`,
			want: `SELECT * FROM "default" WHERE false`,
		},
		{
			name: "NOT form keeps prefix",
			sql:  `SELECT * FROM "default" WHERE NOT match_all('error')`,
			want: `SELECT * FROM "default" WHERE NOT false`,
		},
		{
			name: "wrapped in parens",
			sql:  `SELECT * FROM "default" WHERE (match_all('a'))`,
			want: `SELECT * FROM "default" WHERE (false)`,
		},
		{
			name: "multiple calls and combined conditions",
			sql:  `SELECT * FROM "default" WHERE match_all('a') OR match_all("b") AND code >= 500`,
			want: `SELECT * FROM "default" WHERE false OR false AND code >= 500`,
		},
		{
			name: "no match_all call untouched",
			sql:  `SELECT * FROM "default" WHERE log LIKE '%error%'`,
			want: `SELECT * FROM "default" WHERE log LIKE '%error%'`,
		},
	}
	for _, c := range cases {
		if got := rewriteMatchAllToFalse(c.sql); got != c.want {
			t.Errorf("%s:\n got: %s\nwant: %s", c.name, got, c.want)
		}
	}
}

func TestFullTextSearchFields(t *testing.T) {
	f := func(name, typ string) ooStreamSchemaField { return ooStreamSchemaField{Name: name, Type: typ} }
	cases := []struct {
		name   string
		schema []ooStreamSchemaField
		fts    []string
		want   []string
	}{
		{
			name:   "default candidates intersect schema strings",
			schema: []ooStreamSchemaField{f("_timestamp", "Int64"), f("code", "Int64"), f("log", "Utf8"), f("message", "Utf8"), f("level", "Utf8")},
			want:   []string{"log", "message"},
		},
		{
			name:   "empty schema keeps only _timestamp (empty stream)",
			schema: []ooStreamSchemaField{f("_timestamp", "Int64")},
			want:   nil,
		},
		{
			name:   "fts keys take precedence over default candidates",
			schema: []ooStreamSchemaField{f("_timestamp", "Int64"), f("log", "Utf8"), f("level", "Utf8"), f("event", "Utf8")},
			fts:    []string{"level", "event"},
			want:   []string{"level", "event"},
		},
		{
			name:   "fts keys missing from schema yield nothing",
			schema: []ooStreamSchemaField{f("log", "Utf8")},
			fts:    []string{"not_exist"},
			want:   nil,
		},
		{
			name:   "duplicate fts keys deduplicated",
			schema: []ooStreamSchemaField{f("log", "Utf8")},
			fts:    []string{"log", "log"},
			want:   []string{"log"},
		},
		{
			name:   "non-utf8 types excluded, utf8 family accepted",
			schema: []ooStreamSchemaField{f("log", "LargeUtf8"), f("error", "Binary"), f("msg", "Text"), f("body", "Int64"), f("content", "Utf8View")},
			want:   []string{"log", "msg", "content"},
		},
		{
			name:   "no string fields at all",
			schema: []ooStreamSchemaField{f("_timestamp", "Int64"), f("code", "Int64"), f("took", "Float64")},
			want:   nil,
		},
	}
	for _, c := range cases {
		if got := fullTextSearchFields(c.schema, c.fts); !equalStrings(got, c.want) {
			t.Errorf("%s:\n got: %v\nwant: %v", c.name, got, c.want)
		}
	}
}

func TestSearchableFieldsFromRow(t *testing.T) {
	cases := []struct {
		name string
		row  map[string]interface{}
		want []string
	}{
		{
			name: "candidate intersection wins",
			row:  map[string]interface{}{"_timestamp": 1786801681627412, "code": float64(200), "log": "ok", "message": "ok"},
			want: []string{"log", "message"},
		},
		{
			name: "non-string candidate skipped",
			row:  map[string]interface{}{"log": float64(123), "message": "text"},
			want: []string{"message"},
		},
		{
			name: "fallback to all string fields sorted when no candidate matches",
			row:  map[string]interface{}{"code": float64(500), "event": "deploy", "app": "nginx"},
			want: []string{"app", "event"},
		},
		{
			name: "row without any string field",
			row:  map[string]interface{}{"_timestamp": 1786801681627412, "code": float64(200)},
			want: nil,
		},
	}
	for _, c := range cases {
		if got := searchableFieldsFromRow(c.row); !equalStrings(got, c.want) {
			t.Errorf("%s:\n got: %v\nwant: %v", c.name, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRewriteSearchFunctionSQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "str_match",
			sql:  `SELECT * FROM "default" WHERE str_match(log, 'error')`,
			want: `SELECT * FROM "default" WHERE log LIKE '%error%' ESCAPE '\'`,
		},
		{
			name: "str_match_ignore_case",
			sql:  `SELECT * FROM "default" WHERE str_match_ignore_case(log, 'Error')`,
			want: `SELECT * FROM "default" WHERE log ILIKE '%Error%' ESCAPE '\'`,
		},
		{
			name: "match_field alias",
			sql:  `SELECT * FROM "default" WHERE match_field(log, 'error')`,
			want: `SELECT * FROM "default" WHERE log LIKE '%error%' ESCAPE '\'`,
		},
		{
			name: "match_field_ignore_case alias",
			sql:  `SELECT * FROM "default" WHERE match_field_ignore_case(log, 'Error')`,
			want: `SELECT * FROM "default" WHERE log ILIKE '%Error%' ESCAPE '\'`,
		},
		{
			name: "quoted field name",
			sql:  `SELECT * FROM "default" WHERE str_match("log", 'error')`,
			want: `SELECT * FROM "default" WHERE "log" LIKE '%error%' ESCAPE '\'`,
		},
		{
			name: "NOT str_match",
			sql:  `SELECT * FROM "default" WHERE NOT str_match(log, 'health')`,
			want: `SELECT * FROM "default" WHERE NOT log LIKE '%health%' ESCAPE '\'`,
		},
		{
			name: "percent in value is escaped",
			sql:  `SELECT * FROM "default" WHERE str_match(log, '50%')`,
			want: `SELECT * FROM "default" WHERE log LIKE '%50\%%' ESCAPE '\'`,
		},
		{
			name: "re_match",
			sql:  `SELECT * FROM "default" WHERE re_match(log, 'err.*')`,
			want: `SELECT * FROM "default" WHERE regexp_like(log, 'err.*')`,
		},
		{
			name: "re_not_match",
			sql:  `SELECT * FROM "default" WHERE re_not_match(log, 'err.*')`,
			want: `SELECT * FROM "default" WHERE NOT regexp_like(log, 'err.*')`,
		},
		{
			name: "plain sql untouched",
			sql:  `SELECT * FROM "default" WHERE log LIKE '%error%'`,
			want: `SELECT * FROM "default" WHERE log LIKE '%error%'`,
		},
	}
	for _, c := range cases {
		if got := rewriteSearchFunctionSQL(c.sql); got != c.want {
			t.Errorf("%s:\n got: %s\nwant: %s", c.name, got, c.want)
		}
	}
}

func TestRewriteMatchAllAndSearchFunctionsCombined(t *testing.T) {
	fields := []string{"log", "message"}
	like := func(p string) string {
		return `("log" ILIKE '` + p + `' ESCAPE '\' OR "message" ILIKE '` + p + `' ESCAPE '\')`
	}
	sql := `SELECT * FROM "default" WHERE match_all('a') AND str_match(log, 'b') AND code >= 500`
	want := `SELECT * FROM "default" WHERE ` + like("%a%") + ` AND log LIKE '%b%' ESCAPE '\' AND code >= 500`
	if got := rewriteMatchAllSQL(rewriteSearchFunctionSQL(sql), fields); got != want {
		t.Errorf("combined rewrite:\n got: %s\nwant: %s", got, want)
	}
}

func TestExtractStreamFromSQL(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT * FROM "default" WHERE _timestamp > 0`, "default"},
		{`select count(*) from "k8s" group by ts`, "k8s"},
		{`SELECT * FROM default WHERE 1=1`, ""}, // 未加引号不识别，交给调用方兜底
		{`SELECT 1`, ""},
	}
	for _, c := range cases {
		if got := extractStreamFromSQL(c.sql); got != c.want {
			t.Errorf("extractStreamFromSQL(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}

func TestHasMatchAllCall(t *testing.T) {
	// 作为普通字段名出现（无括号调用）不算函数调用，不能误改写
	if hasMatchAllCall(`SELECT match_all FROM "default"`) {
		t.Error("bare column match_all should not be treated as a function call")
	}
	if !hasMatchAllCall(`SELECT * FROM "default" WHERE match_all('x')`) {
		t.Error("match_all('x') should be detected")
	}
}
