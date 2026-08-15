package openobserve

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/nightingale/v6/datasource"
	lokikit "github.com/ccfos/nightingale/v6/dskit/loki"
	"github.com/ccfos/nightingale/v6/models"
	"github.com/prometheus/common/model"

	"github.com/mitchellh/mapstructure"
)

const (
	OpenObserveType      = "openobserve"
	defaultOrg           = "default"
	defaultStream        = "default"
	defaultTimeout       = 60000
	defaultMaxQueryRows  = 500
	defaultHistogramSize = 10000
	// 告警引擎等调用方通常不给 Query 传 start/end，这里作为默认评估窗口（最近 60s）。
	defaultAlertWindow = 60 * time.Second
)

// timestamp / line column candidates when parsing OpenObserve rows.
var (
	timestampKeys = []string{"_timestamp", "timestamp", "time", "_time", "@timestamp", "ts"}
	lineKeys      = []string{"log", "_raw", "message", "body", "_raw_log", "content"}
)

type OpenObserve struct {
	Addr         string            `json:"openobserve.addr" mapstructure:"openobserve.addr"`
	Org          string            `json:"openobserve.org" mapstructure:"openobserve.org"`
	Basic        OpenObserveBasic  `json:"openobserve.basic" mapstructure:"openobserve.basic"`
	TLS          OpenObserveTLS    `json:"openobserve.tls" mapstructure:"openobserve.tls"`
	Headers      map[string]string `json:"openobserve.headers" mapstructure:"openobserve.headers"`
	Timeout      int64             `json:"openobserve.timeout" mapstructure:"openobserve.timeout"`
	Stream       string            `json:"openobserve.stream" mapstructure:"openobserve.stream"`
	ClusterName  string            `json:"openobserve.cluster_name" mapstructure:"openobserve.cluster_name"`
	MaxQueryRows int               `json:"openobserve.max_query_rows" mapstructure:"openobserve.max_query_rows"`

	HTTPClient *http.Client `json:"-" mapstructure:"-"`
}

type OpenObserveBasic struct {
	Enable bool   `json:"openobserve.basic.auth.enable" mapstructure:"openobserve.basic.auth.enable"`
	User   string `json:"openobserve.basic.user" mapstructure:"openobserve.basic.user"`
	Pass   string `json:"openobserve.basic.password" mapstructure:"openobserve.basic.password"`
}

type OpenObserveTLS struct {
	SkipTlsVerify bool `json:"openobserve.tls.skip_tls_verify" mapstructure:"openobserve.tls.skip_tls_verify"`
}

// Query mirrors the shape the frontend log explorer sends (same as Loki).
type Query struct {
	Query     string `json:"query"`
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	Step      string `json:"step"`
	Limit     int    `json:"limit"`
	Reverse   bool   `json:"reverse"`
	Direction string `json:"direction"`
	Ref       string `json:"ref"`
	SkipCount bool   `json:"skip_count"`
}

// HistogramQuery / HistogramValues mirror the Loki plugin's shapes so the
// router can pass queries through unchanged. They are defined locally to avoid
// a cross-plugin dependency on the loki datasource package.
type HistogramQuery struct {
	Query       string `json:"query"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	Step        string `json:"step"`
	GroupBy     string `json:"group_by"`
	FieldsLimit int    `json:"fields_limit"`
}

type HistogramValues struct {
	Ref    string                 `json:"ref"`
	Metric map[string]interface{} `json:"metric"`
	Values [][]interface{}        `json:"values"`
}

func init() {
	datasource.RegisterDatasource(OpenObserveType, new(OpenObserve))
}

func (oo *OpenObserve) stream() string {
	if strings.TrimSpace(oo.Stream) == "" {
		return defaultStream
	}
	return oo.Stream
}

func (oo *OpenObserve) org() string {
	if strings.TrimSpace(oo.Org) == "" {
		return defaultOrg
	}
	return oo.Org
}

func (oo *OpenObserve) Init(settings map[string]interface{}) (datasource.Datasource, error) {
	newest := new(OpenObserve)
	if err := mapstructure.Decode(settings, newest); err != nil {
		return nil, fmt.Errorf("failed to decode openobserve settings: %w", err)
	}
	return newest, nil
}

func (oo *OpenObserve) InitClient() error {
	maxIdle := 10
	dialTimeout := 30 * time.Second
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: maxIdle,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: oo.TLS.SkipTlsVerify},
		DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
	}

	timeout := time.Duration(oo.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = defaultTimeout * time.Millisecond
	}
	oo.HTTPClient = &http.Client{Transport: transport, Timeout: timeout}
	return nil
}

func (oo *OpenObserve) Validate(ctx context.Context) error {
	if strings.TrimSpace(oo.Addr) == "" {
		return fmt.Errorf("openobserve.addr is required")
	}
	if _, err := url.Parse(oo.Addr); err != nil {
		return fmt.Errorf("invalid openobserve.addr: %w", err)
	}
	if oo.Basic.Enable && (oo.Basic.User == "" || oo.Basic.Pass == "") {
		return fmt.Errorf("basic auth requires both user and password")
	}
	if oo.Timeout == 0 {
		oo.Timeout = defaultTimeout
	}
	if oo.MaxQueryRows == 0 {
		oo.MaxQueryRows = defaultMaxQueryRows
	}
	return nil
}

func (oo *OpenObserve) Equal(other datasource.Datasource) bool {
	o, ok := other.(*OpenObserve)
	if !ok {
		return false
	}
	return oo.Addr == o.Addr &&
		oo.Org == o.Org &&
		oo.Basic.User == o.Basic.User &&
		oo.Basic.Pass == o.Basic.Pass &&
		oo.TLS.SkipTlsVerify == o.TLS.SkipTlsVerify &&
		oo.Timeout == o.Timeout &&
		oo.Stream == o.Stream &&
		oo.ClusterName == o.ClusterName &&
		oo.MaxQueryRows == o.MaxQueryRows &&
		reflectDeepEqual(oo.Headers, o.Headers)
}

func reflectDeepEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (oo *OpenObserve) MakeLogQuery(ctx context.Context, query interface{}, eventTags []string, start, end int64) (interface{}, error) {
	q := &Query{Start: start, End: end, Limit: defaultMaxQueryRows}
	if s, ok := query.(string); ok {
		q.Query = s
	} else if m, ok := query.(map[string]interface{}); ok {
		if s, ok := m["query"]; ok {
			q.Query = fmt.Sprintf("%v", s)
		}
		if l, ok := m["limit"]; ok {
			switch v := l.(type) {
			case int:
				q.Limit = v
			case float64:
				q.Limit = int(v)
			}
		}
	}
	return q, nil
}

func (oo *OpenObserve) MakeTSQuery(ctx context.Context, query interface{}, eventTags []string, start, end int64) (interface{}, error) {
	q := &Query{Start: start, End: end}
	if s, ok := query.(string); ok {
		q.Query = s
	} else if m, ok := query.(map[string]interface{}); ok {
		if s, ok := m["query"]; ok {
			q.Query = fmt.Sprintf("%v", s)
		}
		if s, ok := m["step"]; ok {
			q.Step = fmt.Sprintf("%v", s)
		}
	}
	return q, nil
}

func (oo *OpenObserve) QueryData(ctx context.Context, queryParam interface{}) ([]models.DataResp, error) {
	// 既服务于仪表盘/告警预览（ds-query，前端带 start/end），也服务于告警引擎评估。
	// 告警引擎调用时往往不传 start/end（由评估时刻决定），这里补一个默认评估窗口，
	// 否则 _timestamp BETWEEN 0 AND 0 查不到数据，日志告警永远不触发。
	param := new(Query)
	if err := decodeQueryParam(queryParam, param); err != nil {
		return nil, err
	}

	endMicros, startMicros := toMicros(param.End), toMicros(param.Start)
	nowSec := time.Now().Unix()
	if endMicros == 0 {
		endMicros = nowSec * 1_000_000
	}
	if startMicros == 0 {
		startMicros = endMicros - int64(defaultAlertWindow/time.Microsecond)
	}

	q := strings.TrimSpace(param.Query)

	// 完整 SELECT：用户自写聚合/时间过滤（例如带 histogram() 的时间序列），直接透传。
	if strings.HasPrefix(strings.ToLower(q), "select") {
		resp, err := oo.doSearch(ctx, q, startMicros, endMicros, 0, defaultHistogramSize)
		if err != nil {
			return nil, err
		}
		return oo.rowsToDataResp(resp.rows(), param.Ref, endMicros/1_000_000), nil
	}

	// 日志告警场景：用户填的是 WHERE 片段（或空），包成「匹配条数」序列，
	// 触发器按 count > N 判断。每次评估产出一个瞬时点（等价于 Prometheus 即时向量），
	// 由告警引擎的阈值触发器决定是否产生异常点。
	var b strings.Builder
	fmt.Fprintf(&b, `SELECT count(*) AS count FROM "%s" WHERE _timestamp BETWEEN %d AND %d`, oo.stream(), startMicros, endMicros)
	if q != "" {
		fmt.Fprintf(&b, " AND (%s)", q)
	}
	resp, err := oo.doSearch(ctx, b.String(), startMicros, endMicros, 0, 1)
	if err != nil {
		return nil, err
	}
	rows := resp.rows()
	var total float64
	if len(rows) > 0 {
		if v, ok := toFloat(rows[0]["count"]); ok {
			total = v
		}
	}
	tsSec := endMicros / 1_000_000
	if tsSec == 0 {
		tsSec = nowSec
	}
	return []models.DataResp{
		{
			Ref:    param.Ref,
			Metric: model.Metric{},
			Values: [][]float64{{float64(tsSec), total}},
		},
	}, nil
}

func (oo *OpenObserve) QueryLog(ctx context.Context, queryParam interface{}) ([]interface{}, int64, error) {
	param := new(Query)
	if err := decodeQueryParam(queryParam, param); err != nil {
		return nil, 0, err
	}
	param.Limit = oo.normalizeLimit(param.Limit)

	start, end := toMicros(param.Start), toMicros(param.End)
	sql := oo.buildLogSQL(param.Query, start, end, param.Limit)
	resp, err := oo.doSearch(ctx, sql, start, end, 0, param.Limit)
	if err != nil {
		return nil, 0, err
	}

	logs := oo.rowsToNormalizedLogs(resp.rows())
	total := resp.Total
	if param.SkipCount || total == 0 {
		total = int64(len(logs))
	}
	ret := make([]interface{}, 0, len(logs))
	for _, l := range logs {
		ret = append(ret, l)
	}
	return ret, total, nil
}

func (oo *OpenObserve) QueryMapData(ctx context.Context, queryParam interface{}) ([]map[string]string, error) {
	param := new(Query)
	if err := decodeQueryParam(queryParam, param); err != nil {
		return nil, err
	}
	param.Limit = 1
	if param.Start > 0 && param.End > 0 {
		// extend a little to avoid edge timing issues
		param.Start -= 30 * 1000
	}

	start, end := toMicros(param.Start), toMicros(param.End)
	sql := oo.buildLogSQL(param.Query, start, end, 1)
	resp, err := oo.doSearch(ctx, sql, start, end, 0, 1)
	if err != nil {
		return nil, err
	}
	rows := resp.rows()
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]string)
	for k, v := range rows[0] {
		out[k] = fmt.Sprintf("%v", v)
	}
	return []map[string]string{out}, nil
}

func (oo *OpenObserve) Test(ctx context.Context) error {
	if err := oo.Validate(ctx); err != nil {
		return err
	}
	if _, err := oo.doSearch(ctx, "SELECT 1", 0, 0, 0, 1); err != nil {
		return fmt.Errorf("openobserve connection test failed: %w", err)
	}
	return nil
}

// --- label / field / histogram (UI support) ---

func (oo *OpenObserve) QueryLabelNames(ctx context.Context, query string, start, end int64, filter string, limit int) ([]lokikit.FieldMeta, error) {
	startU, endU := toMicros(start), toMicros(end)
	sql := fmt.Sprintf(`SELECT * FROM "%s" WHERE _timestamp BETWEEN %d AND %d%s LIMIT 1`,
		oo.stream(), startU, endU, oo.andClause(query))
	resp, err := oo.doSearch(ctx, sql, startU, endU, 0, 1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, row := range resp.rows() {
		for k := range row {
			if isTimestampKey(k) {
				continue
			}
			names = append(names, k)
		}
	}
	sort.Strings(names)
	names = filterAndLimitStrings(names, filter, limit)
	ret := make([]lokikit.FieldMeta, 0, len(names))
	for _, k := range names {
		ret = append(ret, lokikit.FieldMeta{Field: k})
	}
	return ret, nil
}

func (oo *OpenObserve) QueryLabelValues(ctx context.Context, query string, start, end int64, label, filter string, limit int) ([]lokikit.LabelValue, error) {
	if strings.TrimSpace(label) == "" {
		return nil, fmt.Errorf("label is required")
	}
	startU, endU := toMicros(start), toMicros(end)
	lim := limit
	if lim <= 0 {
		lim = 100
	}
	sql := fmt.Sprintf(`SELECT DISTINCT "%s" FROM "%s" WHERE _timestamp BETWEEN %d AND %d%s LIMIT %d`,
		label, oo.stream(), startU, endU, oo.andClause(query), lim)
	resp, err := oo.doSearch(ctx, sql, startU, endU, 0, lim)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, row := range resp.rows() {
		if v, ok := row[label]; ok {
			values = append(values, fmt.Sprintf("%v", v))
		}
	}
	values = filterAndLimitStrings(values, filter, limit)
	ret := make([]lokikit.LabelValue, 0, len(values))
	for _, v := range values {
		ret = append(ret, lokikit.LabelValue{Value: v})
	}
	return ret, nil
}

func (oo *OpenObserve) QueryParsedFields(ctx context.Context, query string, start, end int64, limit int) ([]lokikit.FieldMeta, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	startU, endU := toMicros(start), toMicros(end)
	sql := fmt.Sprintf(`SELECT * FROM "%s" WHERE _timestamp BETWEEN %d AND %d%s LIMIT %d`,
		oo.stream(), startU, endU, oo.andClause(query), limit)
	resp, err := oo.doSearch(ctx, sql, startU, endU, 0, limit)
	if err != nil {
		return nil, err
	}
	fieldSet := make(map[string]struct{})
	for _, row := range resp.rows() {
		for k := range row {
			if !isTimestampKey(k) {
				fieldSet[k] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(fieldSet))
	for k := range fieldSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ret := make([]lokikit.FieldMeta, 0, len(keys))
	for _, k := range keys {
		ret = append(ret, lokikit.FieldMeta{Field: k})
	}
	return ret, nil
}

func (oo *OpenObserve) QueryHistogram(ctx context.Context, q HistogramQuery) ([]HistogramValues, error) {
	startU, endU := toMicros(q.Start), toMicros(q.End)
	unit := stepToUnit(q.Step)
	sql := fmt.Sprintf(`SELECT count(*) AS value, date_trunc('%s', _timestamp) AS ts FROM "%s" WHERE _timestamp BETWEEN %d AND %d%s GROUP BY ts ORDER BY ts`,
		unit, oo.stream(), startU, endU, oo.andClause(q.Query))

	resp, err := oo.doSearch(ctx, sql, startU, endU, 0, defaultHistogramSize)
	if err != nil {
		// Fallback: a single aggregate point so the UI still renders something.
		fallback := fmt.Sprintf(`SELECT count(*) AS value FROM "%s" WHERE _timestamp BETWEEN %d AND %d%s`,
			oo.stream(), startU, endU, oo.andClause(q.Query))
		fr, ferr := oo.doSearch(ctx, fallback, startU, endU, 0, 1)
		if ferr != nil {
			return nil, err
		}
		total := aggCount(fr.rows())
		mid := float64((q.Start + q.End) / 2)
		return []HistogramValues{{Values: [][]interface{}{{mid / 1e3, float64(total)}}}}, nil
	}

	vals := make([][]interface{}, 0)
	for _, row := range resp.rows() {
		tsNanos, ok := lokikit.ParseTimestampNanos(row["ts"])
		if !ok {
			continue
		}
		v, ok := toFloat(row["value"])
		if !ok {
			continue
		}
		vals = append(vals, []interface{}{float64(tsNanos) / 1e9, v})
	}
	return []HistogramValues{{Values: vals}}, nil
}

// --- helpers ---

func (oo *OpenObserve) andClause(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	// A full custom SELECT is the caller's responsibility (no auto time filter).
	if strings.HasPrefix(strings.ToLower(q), "select") {
		return ""
	}
	return " AND (" + q + ")"
}

func (oo *OpenObserve) buildLogSQL(query string, startMicros, endMicros int64, limit int) string {
	q := strings.TrimSpace(query)
	if strings.HasPrefix(strings.ToLower(q), "select") {
		// user-provided full SQL; they own the time filter
		return q
	}
	var b strings.Builder
	fmt.Fprintf(&b, `SELECT * FROM "%s" WHERE _timestamp BETWEEN %d AND %d`, oo.stream(), startMicros, endMicros)
	if q != "" {
		fmt.Fprintf(&b, " AND (%s)", q)
	}
	fmt.Fprintf(&b, " ORDER BY _timestamp DESC LIMIT %d", limit)
	return b.String()
}

func (oo *OpenObserve) normalizeLimit(limit int) int {
	if limit <= 0 {
		if oo.MaxQueryRows > 0 {
			return oo.MaxQueryRows
		}
		return defaultMaxQueryRows
	}
	return limit
}

func (oo *OpenObserve) doSearch(ctx context.Context, sql string, startMicros, endMicros int64, from, size int) (*ooSearchResponse, error) {
	if oo.HTTPClient == nil {
		if err := oo.InitClient(); err != nil {
			return nil, err
		}
	}
	u, err := url.Parse(oo.Addr)
	if err != nil {
		return nil, fmt.Errorf("invalid oo.addr: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/" + oo.org() + "/_search"

	q := url.Values{}
	q.Set("type", "logs")
	body := map[string]interface{}{
		"query":       map[string]string{"sql": sql},
		"from":        from,
		"size":        size,
		"search_type": "ui",
	}
	if startMicros > 0 {
		body["start_time"] = startMicros
	}
	if endMicros > 0 {
		body["end_time"] = endMicros
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal search body failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if oo.Basic.User != "" {
		req.SetBasicAuth(oo.Basic.User, oo.Basic.Pass)
	}
	for k, v := range oo.Headers {
		req.Header.Set(k, v)
	}

	resp, err := oo.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openobserve search failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var out ooSearchResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode search response failed: %w body=%s", err, string(respBody))
	}
	return &out, nil
}

func (oo *OpenObserve) rowsToNormalizedLogs(rows []map[string]interface{}) []lokikit.NormalizedLog {
	items := make([]lokikit.QueryItem, 0, len(rows))
	for _, row := range rows {
		tsKey, tsNanos, ok := extractTimestamp(row)
		if !ok {
			continue
		}
		line := extractLine(row, tsKey)
		stream := make(map[string]string, len(row))
		for k, v := range row {
			stream[k] = fmt.Sprintf("%v", v)
		}
		items = append(items, lokikit.QueryItem{
			Stream: stream,
			Values: [][]interface{}{{tsNanos / int64(time.Millisecond), line}},
		})
	}
	resp := &lokikit.QueryResponse{
		Status: "success",
		Data:   lokikit.QueryData{ResultType: "streams", Result: items},
	}
	return lokikit.NormalizeLogs(resp)
}

func (oo *OpenObserve) rowsToDataResp(rows []map[string]interface{}, ref string, defaultTsSec int64) []models.DataResp {
	out := make([]models.DataResp, 0)
	for _, row := range rows {
		_, tsNanos, ok := extractTimestamp(row)
		if !ok && defaultTsSec > 0 {
			// 聚合查询（如 SELECT count(*) ...）可能不含时间列，用评估时刻兜底，
			// 保证该点仍能被告警引擎的阈值触发器消费。
			tsNanos = defaultTsSec * 1e9
			ok = true
		}
		if !ok {
			continue
		}
		var value float64
		var found bool
		for _, k := range []string{"value", "count", "cnt", "_value"} {
			if v, ok2 := toFloat(row[k]); ok2 {
				value, found = v, true
				break
			}
		}
		if !found {
			continue
		}
		metric := make(map[string]string)
		for k, v := range row {
			if isTimestampKey(k) {
				continue
			}
			metric[k] = fmt.Sprintf("%v", v)
		}
		out = append(out, models.DataResp{
			Ref:    ref,
			Metric: toModelMetric(metric),
			Values: [][]float64{{float64(tsNanos) / 1e9, value}},
		})
	}
	return out
}

func toModelMetric(m map[string]string) model.Metric {
	mm := make(model.Metric, len(m))
	for k, v := range m {
		mm[model.LabelName(k)] = model.LabelValue(v)
	}
	return mm
}

// ---- response + parsing helpers ----

type ooSearchResponse struct {
	Total   int64                    `json:"total"`
	Hits    []map[string]interface{} `json:"hits"`
	Records []map[string]interface{} `json:"records"`
}

func (r *ooSearchResponse) rows() []map[string]interface{} {
	if len(r.Hits) > 0 {
		return r.Hits
	}
	return r.Records
}

func isTimestampKey(k string) bool {
	for _, t := range timestampKeys {
		if k == t {
			return true
		}
	}
	return false
}

func extractTimestamp(row map[string]interface{}) (string, int64, bool) {
	for _, k := range timestampKeys {
		if v, ok := row[k]; ok {
			if nanos, ok := lokikit.ParseTimestampNanos(v); ok {
				return k, nanos, true
			}
		}
	}
	return "", 0, false
}

func extractLine(row map[string]interface{}, tsKey string) string {
	for _, k := range lineKeys {
		if v, ok := row[k]; ok && v != nil {
			return fmt.Sprintf("%v", v)
		}
	}
	// fallback: marshal everything except the timestamp column
	cp := make(map[string]interface{})
	for k, v := range row {
		if k == tsKey {
			continue
		}
		cp[k] = v
	}
	if b, err := json.Marshal(cp); err == nil {
		return string(b)
	}
	return ""
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func aggCount(rows []map[string]interface{}) int64 {
	for _, row := range rows {
		if v, ok := toFloat(row["value"]); ok {
			return int64(v)
		}
		if v, ok := toFloat(row["count"]); ok {
			return int64(v)
		}
	}
	return 0
}

func toMicros(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms * 1000
}

func stepToUnit(step string) string {
	s := strings.TrimSpace(step)
	seconds := 0
	switch {
	case strings.HasSuffix(s, "s") && s != "":
		seconds, _ = strconv.Atoi(strings.TrimSuffix(s, "s"))
	case strings.HasSuffix(s, "m"):
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "m"))
		seconds = n * 60
	case strings.HasSuffix(s, "h"):
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "h"))
		seconds = n * 3600
	default:
		seconds, _ = strconv.Atoi(s)
	}
	switch {
	case seconds <= 0 || seconds < 60:
		return "second"
	case seconds < 3600:
		return "minute"
	default:
		return "hour"
	}
}

func filterAndLimitStrings(in []string, filter string, limit int) []string {
	sort.Strings(in)
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{})
	for _, v := range in {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		if filter != "" && !strings.Contains(v, filter) {
			continue
		}
		out = append(out, v)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func decodeQueryParam(input interface{}, output interface{}) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		Result:           output,
	})
	if err != nil {
		return err
	}
	return decoder.Decode(input)
}
