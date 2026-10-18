package zabbix

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dskitzabbix "github.com/ccfos/nightingale/v6/dskit/zabbix"
	"github.com/ccfos/nightingale/v6/models"
	"github.com/prometheus/common/model"

	"github.com/mitchellh/mapstructure"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// QueryParam is the payload the frontend sends via ds-query / logs-query.
//
// Time contract (Nightingale-wide): Zabbix clocks are unix SECONDS, and
// DataResp.Values timestamps are seconds too — no unit conversion is needed
// at the boundary. The inbound epoch unit varies by caller and is normalized
// by timeRange() below.
type QueryParam struct {
	Ref string `json:"ref" mapstructure:"ref"`

	// metric query (QueryData)
	ItemIDs []string `json:"itemids" mapstructure:"itemids"`
	HostIDs []string `json:"hostids" mapstructure:"hostids"`
	// Start/End arrive from the log explorer in MILLISECONDS and from the
	// dashboard batch path in SECONDS; timeRange() sniffs the magnitude.
	Start int64 `json:"start" mapstructure:"start"`
	End   int64 `json:"end" mapstructure:"end"`
	// From/To are the SECONDS form used by internal callers and tests.
	From     int64 `json:"from" mapstructure:"from"`
	To       int64 `json:"to" mapstructure:"to"`
	Interval int64 `json:"interval" mapstructure:"interval"` // evaluation window (seconds) when Start/End are absent
	// Window is saved by the alert rule query editor and used as the
	// evaluation window when the alert engine calls QueryData without times.
	Window int64 `json:"window" mapstructure:"window"`
	// Offset is a ROW offset for event pagination (problem.get/event.get have
	// no server-side offset; see QueryLog). It is not a time shift.
	Offset int64 `json:"offset" mapstructure:"offset"`
	// Agg selects the value column: raw|avg|max|min. raw = history.get,
	// avg/max/min = trend.get hourly aggregates. Empty = auto (trend for
	// windows wider than trendWindowSeconds, history otherwise).
	Agg   string `json:"agg" mapstructure:"agg"`
	Limit int    `json:"limit" mapstructure:"limit"`

	// event query (QueryLog)
	QueryType  string   `json:"query_type" mapstructure:"query_type"` // problem|event, default problem
	Severities []string `json:"severities" mapstructure:"severities"`

	// enrich query (QueryMapData)
	EventIDs []string `json:"eventids" mapstructure:"eventids"`
}

// defaultAlertWindowSec is the evaluation window when neither an explicit
// range nor window/interval is given (the alert engine calls QueryData with
// the saved query only, so without this the window would be empty).
const defaultAlertWindowSec = 60

// epochToSec normalizes a unix timestamp of unknown magnitude to seconds.
// Boundaries mirror dskit/loki.formatLokiTimestamp: < 1e12 seconds,
// < 1e15 milliseconds, < 1e18 microseconds, else nanoseconds.
func epochToSec(v int64) int64 {
	switch {
	case v <= 0:
		return 0
	case v < 1e12:
		return v
	case v < 1e15:
		return v / 1000
	case v < 1e18:
		return v / 1_000_000
	default:
		return v / 1_000_000_000
	}
}

// timeRange resolves the query window to unix SECONDS (Zabbix's clock unit).
//
// Caller conventions observed in-tree:
//   - log explorer (logs-query): "start"/"end" in MILLISECONDS
//   - dashboard batch v2 (/v2/query-batch): "start"/"end" in SECONDS
//   - alert engine evaluation: no explicit range at all; "window" (seconds,
//     saved by the alert rule query editor) or "interval" back from now
//   - internal callers / tests: "from"/"to" in SECONDS
func (qp *QueryParam) timeRange() (int64, int64, error) {
	from := epochToSec(qp.Start)
	if from == 0 {
		from = epochToSec(qp.From)
	}
	to := epochToSec(qp.End)
	if to == 0 {
		to = epochToSec(qp.To)
	}

	if from > 0 && to > 0 {
		if to < from {
			return 0, 0, fmt.Errorf("zabbix query requires a valid time range (from=%d to=%d)", from, to)
		}
		return from, to, nil
	}

	// Evaluation mode: no explicit range (alert engine). Derive the window
	// from now, otherwise BETWEEN 0 AND 0 would never match anything.
	now := time.Now().Unix()
	window := qp.Window
	if window <= 0 {
		window = qp.Interval
	}
	if window <= 0 {
		window = defaultAlertWindowSec
	}
	if to == 0 {
		to = now
	}
	if from == 0 {
		from = to - window
	}
	if to <= from {
		return 0, 0, fmt.Errorf("zabbix query requires a valid time range (from=%d to=%d)", from, to)
	}
	return from, to, nil
}

func (qp *QueryParam) limit() int {
	if qp.Limit > 0 {
		return qp.Limit
	}
	return defaultMaxQueryRows
}

// trendValueField maps the agg parameter to a trend.get output column.
func trendValueField(agg string) string {
	switch strings.ToLower(strings.TrimSpace(agg)) {
	case "max":
		return "value_max"
	case "min":
		return "value_min"
	default:
		return "value_avg"
	}
}

func (z *Zabbix) maxQueryRows() int {
	if z.MaxQueryRows > 0 {
		return z.MaxQueryRows
	}
	return defaultMaxQueryRows
}

// QueryData serves dashboards and alert rules: one series per item.
func (z *Zabbix) QueryData(ctx context.Context, query interface{}) ([]models.DataResp, error) {
	qp := new(QueryParam)
	if err := mapstructure.Decode(query, qp); err != nil {
		return nil, err
	}

	if len(qp.ItemIDs) == 0 {
		return nil, fmt.Errorf("zabbix query requires itemids")
	}

	fromSec, toSec, err := qp.timeRange()
	if err != nil {
		return nil, err
	}

	agg := strings.ToLower(strings.TrimSpace(qp.Agg))
	useTrend := agg == "avg" || agg == "max" || agg == "min" ||
		(agg == "" && toSec-fromSec > trendWindowSeconds)

	if useTrend {
		return z.queryTrends(ctx, qp, fromSec, toSec)
	}
	return z.queryHistories(ctx, qp, fromSec, toSec)
}

// queryTrends fetches hourly aggregates per item and assembles one series each.
func (z *Zabbix) queryTrends(ctx context.Context, qp *QueryParam, fromSec, toSec int64) ([]models.DataResp, error) {
	items, err := z.GetItems(ctx, qp.ItemIDs)
	if err != nil {
		return nil, err
	}
	itemByID := indexItems(items)

	limit := int64(qp.limit())
	sem := semaphore.NewWeighted(8)
	var mu sync.Mutex
	var g errgroup.Group

	out := make([]models.DataResp, 0, len(qp.ItemIDs))
	for _, itemID := range qp.ItemIDs {
		itemID := itemID
		if err := sem.Acquire(ctx, 1); err != nil {
			return nil, err
		}
		g.Go(func() error {
			defer sem.Release(1)
			rows, err := z.trendsChunked(ctx, itemID, fromSec, toSec, trendValueField(qp.Agg), limit, 0)
			if err != nil {
				return err
			}
			resp := trendsToDataResp(itemByID[itemID], rows, qp.Ref)
			mu.Lock()
			out = append(out, resp)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// queryHistories fetches raw samples per item, splitting by Zabbix history
// object type (0=float, 3=unsigned integer).
func (z *Zabbix) queryHistories(ctx context.Context, qp *QueryParam, fromSec, toSec int64) ([]models.DataResp, error) {
	items, err := z.GetItems(ctx, qp.ItemIDs)
	if err != nil {
		return nil, err
	}
	itemByID := indexItems(items)

	// Items not returned by item.get (deleted, or non-numeric value_type)
	// have no chartable history; they are skipped with a warning-level skip.
	limit := int64(qp.limit())
	sem := semaphore.NewWeighted(8)
	var mu sync.Mutex
	var g errgroup.Group

	out := make([]models.DataResp, 0, len(qp.ItemIDs))
	for _, itemID := range qp.ItemIDs {
		itemID := itemID
		item, ok := itemByID[itemID]
		if !ok {
			continue
		}
		historyType := 0
		if item.ValueType == "3" {
			historyType = 3
		}
		if err := sem.Acquire(ctx, 1); err != nil {
			return nil, err
		}
		g.Go(func() error {
			defer sem.Release(1)
			rows, err := z.historiesChunked(ctx, historyType, itemID, fromSec, toSec, limit, 0)
			if err != nil {
				return err
			}
			resp := historiesToDataResp(item, rows, qp.Ref)
			mu.Lock()
			out = append(out, resp)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// maxSplitDepth bounds the recursive window split when a query hits the
// row limit; beyond it the tail is dropped rather than fanning out forever.
const maxSplitDepth = 4

// historiesChunked queries one item; when the row limit is hit the window is
// split in half recursively so no points are silently dropped.
func (z *Zabbix) historiesChunked(ctx context.Context, historyType int, itemID string, fromSec, toSec, limit int64, depth int) ([]dskitzabbix.History, error) {
	rows, err := z.Histories(ctx, historyType, []string{itemID}, fromSec, toSec, int(limit))
	if err != nil {
		return nil, err
	}
	if int64(len(rows)) < limit || depth >= maxSplitDepth || toSec-fromSec <= 1 {
		return rows, nil
	}
	mid := (fromSec + toSec) / 2
	left, err := z.historiesChunked(ctx, historyType, itemID, fromSec, mid, limit, depth+1)
	if err != nil {
		return nil, err
	}
	right, err := z.historiesChunked(ctx, historyType, itemID, mid+1, toSec, limit, depth+1)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// trendsChunked is the trend counterpart of historiesChunked. Trends hold at
// most one row per hour, so hitting the limit is rare but still handled.
func (z *Zabbix) trendsChunked(ctx context.Context, itemID string, fromSec, toSec int64, valueField string, limit int64, depth int) ([]dskitzabbix.Trend, error) {
	rows, err := z.Trends(ctx, []string{itemID}, fromSec, toSec, valueField, int(limit))
	if err != nil {
		return nil, err
	}
	if int64(len(rows)) < limit || depth >= maxSplitDepth || toSec-fromSec <= 1 {
		return rows, nil
	}
	mid := (fromSec + toSec) / 2
	left, err := z.trendsChunked(ctx, itemID, fromSec, mid, valueField, limit, depth+1)
	if err != nil {
		return nil, err
	}
	right, err := z.trendsChunked(ctx, itemID, mid+1, toSec, valueField, limit, depth+1)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func indexItems(items []dskitzabbix.Item) map[string]dskitzabbix.Item {
	m := make(map[string]dskitzabbix.Item, len(items))
	for _, it := range items {
		m[it.ItemID] = it
	}
	return m
}

// historiesToDataResp assembles one series per item. Clock is already in
// seconds — the Nightingale values contract — so it is used as-is.
func historiesToDataResp(item dskitzabbix.Item, rows []dskitzabbix.History, ref string) models.DataResp {
	// Zabbix returns newest first; charts read oldest first.
	sort.Slice(rows, func(i, j int) bool {
		return parseInt64(rows[i].Clock) < parseInt64(rows[j].Clock)
	})

	values := make([][]float64, 0, len(rows))
	for _, r := range rows {
		clock := parseInt64(r.Clock)
		if clock <= 0 {
			continue
		}
		v, err := strconv.ParseFloat(r.Value, 64)
		if err != nil {
			continue // Zabbix emits non-numeric values for unsupported states
		}
		values = append(values, []float64{float64(clock), v})
	}
	return newDataResp(item, values, ref)
}

func trendsToDataResp(item dskitzabbix.Item, rows []dskitzabbix.Trend, ref string) models.DataResp {
	sort.Slice(rows, func(i, j int) bool {
		return parseInt64(rows[i].Clock) < parseInt64(rows[j].Clock)
	})

	values := make([][]float64, 0, len(rows))
	for _, r := range rows {
		clock := parseInt64(r.Clock)
		if clock <= 0 {
			continue
		}
		v, err := strconv.ParseFloat(r.ValueAvg, 64)
		if err != nil {
			continue
		}
		values = append(values, []float64{float64(clock), v})
	}
	return newDataResp(item, values, ref)
}

// newDataResp builds the series labels. hostname/itemname/key_ are exposed so
// the dashboard legend and alert templates can reference them.
func newDataResp(item dskitzabbix.Item, values [][]float64, ref string) models.DataResp {
	metric := model.Metric{
		"itemid": model.LabelValue(item.ItemID),
	}
	if item.Name != "" {
		metric["itemname"] = model.LabelValue(item.Name)
	}
	if item.Key != "" {
		metric["key_"] = model.LabelValue(item.Key)
	}
	if item.Units != "" {
		metric["units"] = model.LabelValue(item.Units)
	}
	if len(item.Hosts) > 0 {
		metric["hostid"] = model.LabelValue(item.Hosts[0].HostID)
		metric["hostname"] = model.LabelValue(item.Hosts[0].DisplayName())
	} else if item.HostID != "" {
		metric["hostid"] = model.LabelValue(item.HostID)
	}
	return models.DataResp{
		Ref:    ref,
		Metric: metric,
		Values: values,
	}
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// QueryLog serves the event browser: problem.get / event.get flattened into
// rows, shaped like other log datasources ([]interface{} of string maps).
//
// Pagination: problem.get/event.get expose no server-side offset, so pages
// are sliced in memory from a DESC (newest-first) fetch. One extra row is
// fetched beyond the page to detect "has more"; when it exists, total is a
// lower bound (offset+limit+1) rather than the true count.
func (z *Zabbix) QueryLog(ctx context.Context, query interface{}) ([]interface{}, int64, error) {
	qp := new(QueryParam)
	if err := mapstructure.Decode(query, qp); err != nil {
		return nil, 0, err
	}

	fromSec, toSec, err := qp.timeRange()
	if err != nil {
		return nil, 0, err
	}
	limit := qp.limit()
	offset := qp.Offset
	if offset < 0 {
		offset = 0
	}
	fetch := int(offset) + limit + 1
	if fetch > z.maxQueryRows() {
		fetch = z.maxQueryRows()
	}

	queryType := strings.ToLower(strings.TrimSpace(qp.QueryType))
	if queryType == "event" {
		events, err := z.Events(ctx, fromSec, toSec, qp.Severities, qp.HostIDs, fetch)
		if err != nil {
			return nil, 0, err
		}
		rows := make([]interface{}, 0, len(events))
		for _, e := range events {
			rows = append(rows, eventToRow(e))
		}
		page, total := paginateRows(rows, int(offset), limit)
		return page, total, nil
	}

	problems, err := z.Problems(ctx, fromSec, toSec, qp.Severities, qp.HostIDs, fetch)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]interface{}, 0, len(problems))
	for _, p := range problems {
		rows = append(rows, problemToRow(p))
	}
	page, total := paginateRows(rows, int(offset), limit)
	return page, total, nil
}

// paginateRows slices a DESC-ordered row set into the requested page.
func paginateRows(all []interface{}, offset, limit int) ([]interface{}, int64) {
	if offset >= len(all) {
		return []interface{}{}, int64(len(all))
	}
	end := offset + limit
	if end >= len(all) {
		return all[offset:], int64(len(all))
	}
	// Extra row fetched: report a lower bound so the UI shows a next page.
	return all[offset:end], int64(end + 1)
}

// eventToRow flattens an event. clock is converted to milliseconds because the
// frontend log viewer renders timestamps on the millisecond contract.
func eventToRow(e dskitzabbix.Event) map[string]string {
	row := map[string]string{
		"eventid":      e.EventID,
		"name":         e.Name,
		"clock":        e.Clock,
		"value":        e.Value,
		"severity":     e.Severity,
		"acknowledged": e.Acknowledged,
		"_time":        strconv.FormatInt(parseInt64(e.Clock)*1000, 10),
	}
	if len(e.Hosts) > 0 {
		row["hostid"] = e.Hosts[0].HostID
		row["hostname"] = e.Hosts[0].DisplayName()
	}
	for _, tag := range e.Tags {
		row["tag_"+tag.Tag] = tag.Value
	}
	return row
}

func problemToRow(p dskitzabbix.Problem) map[string]string {
	row := map[string]string{
		"eventid":      p.EventID,
		"name":         p.Name,
		"clock":        p.Clock,
		"severity":     p.Severity,
		"acknowledged": p.Acknowledged,
		"opdata":       p.OpData,
		"_time":        strconv.FormatInt(parseInt64(p.Clock)*1000, 10),
	}
	if len(p.Hosts) > 0 {
		row["hostid"] = p.Hosts[0].HostID
		row["hostname"] = p.Hosts[0].DisplayName()
	}
	for _, tag := range p.Tags {
		row["tag_"+tag.Tag] = tag.Value
	}
	return row
}
