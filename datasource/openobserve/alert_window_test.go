package openobserve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type sqlCapture struct {
	sql string
}

func (c *sqlCapture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query struct {
				SQL string `json:"sql"`
			} `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		c.sql = req.Query.SQL
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"total":1,"hits":[{"count":5}]}`))
	}
}

func newTestOO(handler http.HandlerFunc) (*OpenObserve, func()) {
	srv := httptest.NewServer(handler)
	oo := &OpenObserve{
		Addr:   srv.URL,
		Org:    "default",
		Stream: "default",
	}
	oo.InitClient()
	return oo, srv.Close
}

func parseBetween(t *testing.T, sql string) (int64, int64) {
	re := regexp.MustCompile(`BETWEEN (\d+) AND (\d+)`)
	m := re.FindStringSubmatch(sql)
	if len(m) != 3 {
		t.Fatalf("cannot find BETWEEN in sql: %s", sql)
	}
	var start, end int64
	var err error
	if start, err = strconv.ParseInt(m[1], 10, 64); err != nil {
		t.Fatal(err)
	}
	if end, err = strconv.ParseInt(m[2], 10, 64); err != nil {
		t.Fatal(err)
	}
	return start, end
}

func TestAlertWindowDefault(t *testing.T) {
	cap := &sqlCapture{}
	oo, closeFn := newTestOO(cap.handler())
	defer closeFn()
	if _, err := oo.QueryData(context.Background(), map[string]interface{}{"query": "", "ref": "A"}); err != nil {
		t.Fatal(err)
	}
	start, end := parseBetween(t, cap.sql)
	diff := end - start
	if diff != 60_000_000 {
		t.Errorf("default window should be 60s (60000000 micros), got %d", diff)
	}
	// ingest delay default 120s applied => end ~ nowSec*1e6 - 120e6
	nowSec := time.Now().Unix()
	wantEnd := nowSec*1_000_000 - 120_000_000
	if abs(end-wantEnd) > 3_000_000 {
		t.Errorf("end should reflect default ingest delay ~%d, got %d", wantEnd, end)
	}
	if strings.Contains(cap.sql, "_timestamp") == false {
		t.Errorf("expected default time field _timestamp, sql=%s", cap.sql)
	}
}

func TestAlertWindowCustom(t *testing.T) {
	cap := &sqlCapture{}
	oo, closeFn := newTestOO(cap.handler())
	defer closeFn()
	if _, err := oo.QueryData(context.Background(), map[string]interface{}{"query": "", "ref": "A", "window": 300}); err != nil {
		t.Fatal(err)
	}
	start, end := parseBetween(t, cap.sql)
	diff := end - start
	if diff != 300_000_000 {
		t.Errorf("custom window should be 300s (300000000 micros), got %d", diff)
	}
}

func TestAlertTimeFieldOverride(t *testing.T) {
	cap := &sqlCapture{}
	oo, closeFn := newTestOO(cap.handler())
	defer closeFn()
	if _, err := oo.QueryData(context.Background(), map[string]interface{}{"query": "", "ref": "A", "time_field": "my_ts"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.sql, "my_ts BETWEEN") {
		t.Errorf("expected custom time field my_ts, sql=%s", cap.sql)
	}
}

func TestAlertDatasourceTimeField(t *testing.T) {
	cap := &sqlCapture{}
	oo, closeFn := newTestOO(cap.handler())
	defer closeFn()
	oo.TimeField = "ds_ts"
	oo.IngestDelay = 60
	if _, err := oo.QueryData(context.Background(), map[string]interface{}{"query": "", "ref": "A"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.sql, "ds_ts BETWEEN") {
		t.Errorf("expected datasource time field ds_ts, sql=%s", cap.sql)
	}
	// ingest delay from datasource config (60s)
	nowSec := time.Now().Unix()
	wantEnd := nowSec*1_000_000 - 60_000_000
	_, end := parseBetween(t, cap.sql)
	if abs(end-wantEnd) > 3_000_000 {
		t.Errorf("end should reflect datasource ingest delay ~%d, got %d", wantEnd, end)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
