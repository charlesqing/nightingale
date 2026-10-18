package zabbix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// dsMockZabbix serves the subset of the Zabbix API the datasource layer uses:
// user.login, item.get, history.get, trend.get, problem.get, event.get.
type dsMockZabbix struct {
	historyCalls int
	trendCalled  bool
	itemCalls    int
	handler      func(m *dsMockZabbix, method string, params map[string]interface{}) interface{}
}

func newDSMockServer(t *testing.T, m *dsMockZabbix) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
			ID     int64                  `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := m.handler(m, req.Method, req.Params)
		resp := map[string]interface{}{"jsonrpc": "2.0", "result": result, "id": req.ID}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestDatasource(t *testing.T, m *dsMockZabbix) *Zabbix {
	t.Helper()
	srv := newDSMockServer(t, m)
	z := &Zabbix{}
	z.Addr = srv.URL
	z.User = "Admin"
	z.Password = "zabbix"
	if err := z.InitClient(); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	return z
}

func TestQueryDataHistories(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "item.get":
				m.itemCalls++
				return []map[string]interface{}{
					{
						"itemid": "10001", "name": "CPU utilization", "key_": "system.cpu.util",
						"value_type": "0", "units": "%", "hostid": "10084",
						"hosts": []map[string]string{{"hostid": "10084", "host": "web01", "name": "Web Server 01"}},
					},
				}
			case "history.get":
				m.historyCalls++
				// Zabbix returns newest first; one row is non-numeric and must be dropped.
				return []map[string]string{
					{"itemid": "10001", "clock": "1784456222", "value": "7.5"},
					{"itemid": "10001", "clock": "1784452622", "value": "3.2"},
					{"itemid": "10001", "clock": "1784454422", "value": "ZBX_NOTSUPPORTED"},
				}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	resp, err := z.QueryData(context.Background(), map[string]interface{}{
		"ref":     "A",
		"itemids": []string{"10001"},
		"from":    int64(1784452622),
		"to":      int64(1784456222),
	})
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("series count = %d, want 1", len(resp))
	}

	series := resp[0]
	if series.Ref != "A" {
		t.Fatalf("ref = %q", series.Ref)
	}
	if got := string(series.Metric["itemname"]); got != "CPU utilization" {
		t.Fatalf("itemname = %q", got)
	}
	if got := string(series.Metric["key_"]); got != "system.cpu.util" {
		t.Fatalf("key_ = %q", got)
	}
	if got := string(series.Metric["units"]); got != "%" {
		t.Fatalf("units = %q", got)
	}
	if got := string(series.Metric["hostname"]); got != "Web Server 01" {
		t.Fatalf("hostname = %q", got)
	}

	values := series.Values
	if len(values) != 2 {
		t.Fatalf("values = %v, want 2 numeric points (newest-first input, non-numeric dropped)", values)
	}
	// Ascending time order is the chart contract.
	if values[0][0] != 1784452622 || values[1][0] != 1784456222 {
		t.Fatalf("timestamps = %v, want ascending [1784452622 1784456222]", values)
	}
	if values[0][1] != 3.2 || values[1][1] != 7.5 {
		t.Fatalf("sample values = %v, want [3.2 7.5]", values)
	}
}

func TestQueryDataRequiresItemIDs(t *testing.T) {
	m := &dsMockZabbix{handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} { return nil }}
	z := newTestDatasource(t, m)

	if _, err := z.QueryData(context.Background(), map[string]interface{}{
		"from": int64(1784452622), "to": int64(1784456222),
	}); err == nil {
		t.Fatal("expected error when itemids is empty")
	}
}

func TestQueryDataRejectsInvalidTimeRange(t *testing.T) {
	m := &dsMockZabbix{handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} { return nil }}
	z := newTestDatasource(t, m)

	if _, err := z.QueryData(context.Background(), map[string]interface{}{
		"itemids": []string{"10001"},
		"from":    int64(1784456222), "to": int64(1784452622),
	}); err == nil {
		t.Fatal("expected error for inverted time range")
	}
}

func TestQueryDataTrendAutoSwitch(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "item.get":
				return []map[string]interface{}{
					{"itemid": "10001", "name": "CPU util", "value_type": "0", "hostid": "10084"},
				}
			case "trend.get":
				m.trendCalled = true
				return []map[string]string{
					{"itemid": "10001", "clock": "1784452800", "value_avg": "12.5"},
				}
			case "history.get":
				m.historyCalls++
				return []map[string]string{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	// Window wider than 3 days must go to trend.get, not history.get.
	resp, err := z.QueryData(context.Background(), map[string]interface{}{
		"itemids": []string{"10001"},
		"from":    int64(1784452622),
		"to":      int64(1784452622 + 4*24*3600),
	})
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	if !m.trendCalled {
		t.Fatal("trend.get was not called for a 4-day window")
	}
	if m.historyCalls != 0 {
		t.Fatalf("history.get called %d times, want 0 for wide window", m.historyCalls)
	}
	if len(resp) != 1 || len(resp[0].Values) != 1 || resp[0].Values[0][1] != 12.5 {
		t.Fatalf("trend series = %+v", resp)
	}
}

func TestQueryLogProblems(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "problem.get":
				return []map[string]interface{}{
					{
						"eventid": "999001", "name": "High CPU utilization", "clock": "1784452622",
						"severity": "4", "acknowledged": "0", "opdata": "CPU > 90% for 5m",
						"hosts": []map[string]string{{"hostid": "10084", "host": "web01", "name": "Web Server 01"}},
						"tags":  []map[string]string{{"tag": "scope", "value": "performance"}},
					},
				}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	rows, total, err := z.QueryLog(context.Background(), map[string]interface{}{
		"query_type": "problem",
		"from":       int64(1784452622),
		"to":         int64(1784456222),
	})
	if err != nil {
		t.Fatalf("QueryLog: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total = %d, rows = %d", total, len(rows))
	}
	row, ok := rows[0].(map[string]string)
	if !ok {
		t.Fatalf("row type = %T, want map[string]string", rows[0])
	}
	if row["eventid"] != "999001" || row["hostname"] != "Web Server 01" {
		t.Fatalf("row = %v", row)
	}
	if row["_time"] != "1784452622000" {
		t.Fatalf("_time = %q, want clock in milliseconds", row["_time"])
	}
	if row["tag_scope"] != "performance" {
		t.Fatalf("tag_scope = %q", row["tag_scope"])
	}
}

func TestHistoriesChunkedSplitsOnLimit(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "history.get":
				m.historyCalls++
				// Every window returns exactly `limit` rows, forcing the split.
				return []map[string]string{
					{"itemid": "10001", "clock": "1784452622", "value": "1.0"},
					{"itemid": "10001", "clock": "1784452623", "value": "2.0"},
				}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	from, to := int64(1784452622), int64(1784452624) // 3-second window
	rows, err := z.historiesChunked(context.Background(), 0, "10001", from, to, 2, 0)
	if err != nil {
		t.Fatalf("historiesChunked: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (2 windows x 2 rows after split)", len(rows))
	}
	if m.historyCalls != 3 {
		t.Fatalf("history.get calls = %d, want 3 (1 initial + 2 halves)", m.historyCalls)
	}
}

func TestEpochToSec(t *testing.T) {
	cases := []struct {
		in, want int64
	}{
		{0, 0},
		{-5, 0},
		{1784452622, 1784452622},          // seconds
		{1784452622000, 1784452622},       // milliseconds
		{1784452622000000, 1784452622},    // microseconds
		{1784452622000000000, 1784452622}, // nanoseconds
		{999999999999, 999999999999},      // last second-magnitude value
	}
	for _, c := range cases {
		if got := epochToSec(c.in); got != c.want {
			t.Errorf("epochToSec(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestQueryLogStartEndMilliseconds covers the log explorer convention:
// moment().valueOf() milliseconds must reach Zabbix as seconds.
func TestQueryLogStartEndMilliseconds(t *testing.T) {
	var gotFrom, gotTo float64
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "problem.get":
				gotFrom, _ = params["time_from"].(float64)
				gotTo, _ = params["time_till"].(float64)
				return []map[string]interface{}{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	rows, total, err := z.QueryLog(context.Background(), map[string]interface{}{
		"query_type": "problem",
		"start":      int64(1784452622000), // milliseconds
		"end":        int64(1784456222000),
	})
	if err != nil {
		t.Fatalf("QueryLog: %v", err)
	}
	if len(rows) != 0 || total != 0 {
		t.Fatalf("rows = %d total = %d, want empty", len(rows), total)
	}
	if int64(gotFrom) != 1784452622 || int64(gotTo) != 1784456222 {
		t.Fatalf("problem.get time_from/time_till = %v/%v, want 1784452622/1784456222 (seconds)", gotFrom, gotTo)
	}
}

// TestQueryLogPaginationOffset covers the event browser paging contract:
// offset is a ROW offset; one extra row is fetched to signal "has more".
func TestQueryLogPaginationOffset(t *testing.T) {
	problems := []map[string]interface{}{
		{"eventid": "900003", "name": "p3", "clock": "1784456222", "severity": "2", "hosts": []map[string]string{{"hostid": "1", "name": "h"}}},
		{"eventid": "900002", "name": "p2", "clock": "1784455222", "severity": "3", "hosts": []map[string]string{{"hostid": "1", "name": "h"}}},
		{"eventid": "900001", "name": "p1", "clock": "1784454222", "severity": "4", "hosts": []map[string]string{{"hostid": "1", "name": "h"}}},
	}
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "problem.get":
				limit, _ := params["limit"].(float64)
				// Server-side limit caps the fetch (no native offset).
				if int(limit) < len(problems) {
					return problems[:int(limit)]
				}
				return problems
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	// Page 1: offset=0 limit=2 -> 2 rows, total is a lower bound (3) because
	// the +1 probe row exists.
	rows, total, err := z.QueryLog(context.Background(), map[string]interface{}{
		"query_type": "problem",
		"from":       int64(1784453000),
		"to":         int64(1784457000),
		"limit":      2,
		"offset":     0,
	})
	if err != nil {
		t.Fatalf("QueryLog page1: %v", err)
	}
	if len(rows) != 2 || total != 3 {
		t.Fatalf("page1 rows = %d total = %d, want 2/3", len(rows), total)
	}
	if rows[0].(map[string]string)["eventid"] != "900003" {
		t.Fatalf("page1 newest-first broken: %v", rows[0])
	}

	// Page 2: offset=2 limit=2 -> last row, exact total (no probe row left).
	rows, total, err = z.QueryLog(context.Background(), map[string]interface{}{
		"query_type": "problem",
		"from":       int64(1784453000),
		"to":         int64(1784457000),
		"limit":      2,
		"offset":     2,
	})
	if err != nil {
		t.Fatalf("QueryLog page2: %v", err)
	}
	if len(rows) != 1 || total != 3 {
		t.Fatalf("page2 rows = %d total = %d, want 1/3", len(rows), total)
	}
}

// TestQueryDataAlertEngineWindow covers the alert engine convention: no
// explicit times, window (seconds) saved by the rule editor.
func TestQueryDataAlertEngineWindow(t *testing.T) {
	var gotFrom, gotTo float64
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "item.get":
				return []map[string]interface{}{
					{"itemid": "10001", "name": "CPU util", "value_type": "0", "hostid": "10084"},
				}
			case "history.get":
				gotFrom, _ = params["time_from"].(float64)
				gotTo, _ = params["time_till"].(float64)
				return []map[string]string{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newTestDatasource(t, m)

	before := time.Now().Unix()
	if _, err := z.QueryData(context.Background(), map[string]interface{}{
		"itemids": []string{"10001"},
		"window":  120,
	}); err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	after := time.Now().Unix()

	if int64(gotTo) < before || int64(gotTo) > after {
		t.Fatalf("history.get time_till = %v, want ~now (%d..%d)", gotTo, before, after)
	}
	if int64(gotTo-gotFrom) != 120 {
		t.Fatalf("window = %v, want 120 seconds", gotTo-gotFrom)
	}
}
