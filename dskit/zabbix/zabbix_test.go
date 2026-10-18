package zabbix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockZabbix is a tiny JSON-RPC test double. Methods are dispatched by name;
// callSeq records (method, auth) pairs so tests can assert retry behaviour.
type mockZabbix struct {
	mu      sync.Mutex
	calls   []mockCall
	token   string
	handler func(m *mockZabbix, req rpcRequest) (interface{}, *Error)
}

type mockCall struct {
	Method string
	Auth   string
	Params string
}

func (m *mockZabbix) record(req rpcRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	params, _ := json.Marshal(req.Params)
	m.calls = append(m.calls, mockCall{Method: req.Method, Auth: req.Auth, Params: string(params)})
}

func (m *mockZabbix) callCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (m *mockZabbix) authsFor(method string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.calls {
		if c.Method == method {
			out = append(out, c.Auth)
		}
	}
	return out
}

func newMockServer(t *testing.T, m *mockZabbix) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("mock server: decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.record(req)

		result, rpcErr := m.handler(m, req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result, _ = json.Marshal(result)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testZabbix(addr string) *Zabbix {
	z := &Zabbix{
		Addr:     addr,
		User:     "Admin",
		Password: "zabbix",
		Timeout:  5000,
	}
	return z
}

func TestLoginAndTokenCache(t *testing.T) {
	m := &mockZabbix{
		token: "tok-1",
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			switch req.Method {
			case "user.login":
				return m.token, nil
			case "apiinfo.version":
				if req.Auth != "tok-1" {
					return nil, &Error{Code: -32602, Message: "Session terminated, please login again"}
				}
				return "7.0.22", nil
			}
			return nil, &Error{Code: -32602, Message: "unknown method " + req.Method}
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	ctx := context.Background()
	var v1, v2 string
	if err := z.do(ctx, "apiinfo.version", map[string]interface{}{}, &v1); err != nil {
		t.Fatalf("first apiinfo.version: %v", err)
	}
	if err := z.do(ctx, "apiinfo.version", map[string]interface{}{}, &v2); err != nil {
		t.Fatalf("second apiinfo.version: %v", err)
	}
	if v1 != "7.0.22" || v2 != "7.0.22" {
		t.Fatalf("versions = %q, %q", v1, v2)
	}

	if got := m.callCount("user.login"); got != 1 {
		t.Fatalf("user.login called %d times, want 1 (token should be cached)", got)
	}
}

// TestLoginParamsUseUsernameField guards the exact user.login parameter names:
// Zabbix rejects unknown params with -32602, so "user" (instead of the
// correct "username") breaks authentication against every real server.
func TestLoginParamsUseUsernameField(t *testing.T) {
	m := &mockZabbix{
		token: "tok-1",
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			return m.token, nil
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	if _, err := z.token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var loginParams string
	for _, c := range m.calls {
		if c.Method == "user.login" {
			loginParams = c.Params
		}
	}
	want := `{"password":"zabbix","username":"Admin"}`
	if loginParams != want {
		t.Fatalf("user.login params = %s, want %s", loginParams, want)
	}
}

func TestSessionTerminatedTriggersReLoginAndRetry(t *testing.T) {
	m := &mockZabbix{
		token: "tok-new",
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			switch req.Method {
			case "user.login":
				return m.token, nil
			case "history.get":
				// First attempt with the stale token fails; the retry with
				// the fresh token succeeds.
				if req.Auth == "tok-stale" {
					return nil, &Error{Code: -32602, Message: "Session terminated, please login again"}
				}
				return []History{{ItemID: "10001", Clock: "1784452622", Value: "42"}}, nil
			}
			return nil, &Error{Code: -32602, Message: "unknown method " + req.Method}
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	// Seed a stale token directly (simulates a token invalidated server-side).
	z.mu.Lock()
	z.authToken = "tok-stale"
	z.tokenExp = time.Now().Add(time.Hour)
	z.mu.Unlock()

	var rows []History
	if err := z.do(context.Background(), "history.get", map[string]interface{}{"itemids": []string{"10001"}}, &rows); err != nil {
		t.Fatalf("history.get after session loss: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "42" {
		t.Fatalf("rows = %+v", rows)
	}

	if got := m.callCount("user.login"); got != 1 {
		t.Fatalf("user.login called %d times, want 1 (single re-login)", got)
	}
	auths := m.authsFor("history.get")
	if len(auths) != 2 || auths[0] != "tok-stale" || auths[1] != "tok-new" {
		t.Fatalf("history.get auth sequence = %v, want [tok-stale tok-new]", auths)
	}
}

func TestRPCErrorSurfaces(t *testing.T) {
	m := &mockZabbix{
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			if req.Method == "user.login" {
				return "tok", nil
			}
			return nil, &Error{Code: -32500, Message: "Not authorised", Data: "Session terminated"}
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	err := z.do(context.Background(), "history.get", map[string]interface{}{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error %v is not *zabbix.Error", err)
	}
	if rpcErr.Code != -32500 {
		t.Fatalf("code = %d, want -32500", rpcErr.Code)
	}
	if !isSessionError(err) {
		t.Fatal("-32500 Not authorised should be treated as session error")
	}
	// The original attempt and the post-re-login retry both fail with the
	// same session error: user.login runs twice (initial + re-login), then
	// the error surfaces to the caller.
	if got := m.callCount("user.login"); got != 2 {
		t.Fatalf("user.login called %d times, want 2 (initial + re-login)", got)
	}
	if auths := m.authsFor("history.get"); len(auths) != 2 {
		t.Fatalf("history.get attempts = %d, want 2", len(auths))
	}
}

func TestHTTPNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>gateway error</html>"))
	}))
	defer srv.Close()
	z := testZabbix(srv.URL)

	err := z.do(context.Background(), "apiinfo.version", map[string]interface{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "status=502") {
		t.Fatalf("err = %v, want status=502 mention", err)
	}
}

func TestLoginFailure(t *testing.T) {
	m := &mockZabbix{
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			return nil, &Error{Code: -32602, Message: "Invalid params", Data: "Login name or password is incorrect"}
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	err := z.do(context.Background(), "apiinfo.version", map[string]interface{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "user.login failed") {
		t.Fatalf("err = %v, want login failure", err)
	}
}

func TestCheckHealth(t *testing.T) {
	m := &mockZabbix{
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			if req.Method == "user.login" {
				return "tok", nil
			}
			return "7.0.22", nil
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	if err := z.CheckHealth(context.Background()); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
}

// TestAPIVersionUnauthenticated guards that apiinfo.version is invoked WITHOUT
// the auth parameter — Zabbix 7.0.x rejects it with -32602 otherwise. A mock
// that ignores auth hides this, so assert the auth field explicitly.
func TestAPIVersionUnauthenticated(t *testing.T) {
	m := &mockZabbix{
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			if req.Method == "user.login" {
				return "tok", nil
			}
			return "7.0.31", nil
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	if v, err := z.APIVersion(context.Background()); err != nil || v != "7.0.31" {
		t.Fatalf("APIVersion = %q, %v", v, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.Method == "apiinfo.version" && c.Auth != "" {
			t.Fatalf("apiinfo.version sent auth %q; must be unauthenticated", c.Auth)
		}
	}
}

// TestQueriesUseTimeTill guards that the Zabbix time-window param is
// "time_till" (NOT "time_to") and that problem.get/event.get don't send
// "selectHosts" (rejected by Zabbix 7.0.x). A mock that ignores params hides
// this; assert the recorded params explicitly.
func TestQueriesUseTimeTill(t *testing.T) {
	m := &mockZabbix{
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			if req.Method == "user.login" {
				return "tok", nil
			}
			return []interface{}{}, nil
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	now := int64(1700000000)
	if _, err := z.Problems(context.Background(), now-3600, now, nil, nil, 10); err != nil {
		t.Fatalf("Problems: %v", err)
	}
	if _, err := z.Events(context.Background(), now-3600, now, nil, nil, 10); err != nil {
		t.Fatalf("Events: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, method := range []string{"problem.get", "event.get"} {
		var params string
		for _, c := range m.calls {
			if c.Method == method {
				params = c.Params
			}
		}
		if params == "" {
			t.Fatalf("no recorded params for %s", method)
		}
		if !strings.Contains(params, "time_till") {
			t.Fatalf("%s params missing time_till: %s", method, params)
		}
		if strings.Contains(params, "time_to") {
			t.Fatalf("%s params must not use time_to: %s", method, params)
		}
		if strings.Contains(params, "selectHosts") {
			t.Fatalf("%s params must not use selectHosts: %s", method, params)
		}
	}
}

func TestHistoriesParams(t *testing.T) {
	var gotParams map[string]interface{}
	m := &mockZabbix{
		handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
			if req.Method == "user.login" {
				return "tok", nil
			}
			json.Unmarshal([]byte(m.calls[len(m.calls)-1].Params), &gotParams)
			return []History{}, nil
		},
	}
	srv := newMockServer(t, m)
	z := testZabbix(srv.URL)

	if _, err := z.Histories(context.Background(), 0, nil, 100, 200, 100); err == nil {
		t.Fatal("expected error for empty itemids")
	}
	if _, err := z.Histories(context.Background(), 0, []string{"1"}, 200, 100, 100); err == nil {
		t.Fatal("expected error for inverted time range")
	}

	rows, err := z.Histories(context.Background(), 0, []string{"10001", "10002"}, 1784452622, 1784456222, 500)
	if err != nil {
		t.Fatalf("Histories: %v", err)
	}
	if rows == nil {
		t.Fatal("rows should be non-nil empty slice")
	}
	if gotParams["history"] != float64(0) || gotParams["limit"] != float64(500) {
		t.Fatalf("params = %+v", gotParams)
	}
	if gotParams["time_from"] != float64(1784452622) {
		t.Fatalf("time_from = %v", gotParams["time_from"])
	}
	if ids, ok := gotParams["itemids"].([]interface{}); !ok || len(ids) != 2 {
		t.Fatalf("itemids = %v", gotParams["itemids"])
	}
}

func TestTrendsRejectsUnknownValueField(t *testing.T) {
	z := testZabbix("http://127.0.0.1:1")
	if _, err := z.Trends(context.Background(), []string{"1"}, 100, 200, "value_sum", 10); err == nil {
		t.Fatal("expected error for unsupported value field")
	}
}

func TestBasicAuthAndHeadersSent(t *testing.T) {
	var gotAuth, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok {
			gotAuth = user + ":" + pass
		}
		gotHeader = r.Header.Get("X-Proxy-Key")
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		result, _ := json.Marshal("7.0.22")
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Result: result, ID: req.ID})
	}))
	defer srv.Close()

	z := testZabbix(srv.URL)
	z.Basic.User = "proxy"
	z.Basic.Pass = "secret"
	z.Headers = map[string]string{"X-Proxy-Key": "abc123"}

	if err := z.CheckHealth(context.Background()); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if gotAuth != "proxy:secret" {
		t.Fatalf("basic auth = %q", gotAuth)
	}
	if gotHeader != "abc123" {
		t.Fatalf("X-Proxy-Key = %q", gotHeader)
	}
}

// ---- monitoring query page: host groups / latest data / problem counts ----

func TestHostsSelectsHostGroupsAndInterfaceAvailability(t *testing.T) {
	var gotParams map[string]interface{}
	m := &mockZabbix{handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
		switch req.Method {
		case "user.login":
			return "tok", nil
		case "host.get":
			gotParams, _ = req.Params.(map[string]interface{})
			return []map[string]interface{}{
				{
					"hostid": "10084", "host": "zbx-srv", "name": "Zabbix server", "status": "0",
					"hostgroups": []map[string]string{{"groupid": "4", "name": "Zabbix servers"}},
					"interfaces": []map[string]string{
						{"interfaceid": "1", "ip": "127.0.0.1", "port": "10050", "type": "1", "main": "1", "available": "1"},
					},
				},
			}, nil
		}
		return nil, &Error{Code: -32602, Message: "unexpected method " + req.Method}
	}}
	z := testZabbix(newMockServer(t, m).URL)

	hosts, err := z.Hosts(context.Background(), []string{"4"}, 10)
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(hosts))
	}
	if _, ok := gotParams["selectHostGroups"]; !ok {
		t.Fatal("host.get must request selectHostGroups for the group column")
	}
	if len(hosts[0].Groups) != 1 || hosts[0].Groups[0].Name != "Zabbix servers" {
		t.Fatalf("host groups not decoded: %+v", hosts[0].Groups)
	}
	iface, ok := hosts[0].PrimaryInterface()
	if !ok || iface.Port != "10050" || iface.Available != "1" {
		t.Fatalf("primary interface = %+v ok=%v", iface, ok)
	}
}

func TestItemsByFilterAllTypes(t *testing.T) {
	var gotParams map[string]interface{}
	m := &mockZabbix{handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
		switch req.Method {
		case "user.login":
			return "tok", nil
		case "item.get":
			gotParams, _ = req.Params.(map[string]interface{})
			return []map[string]interface{}{}, nil
		}
		return nil, &Error{Code: -32602, Message: "unexpected method " + req.Method}
	}}
	z := testZabbix(newMockServer(t, m).URL)

	if _, err := z.ItemsByFilter(context.Background(), []string{"10084"}, ItemFilter{Search: "cpu", AllTypes: true}, 100); err != nil {
		t.Fatalf("ItemsByFilter: %v", err)
	}

	if filter, ok := gotParams["filter"]; ok {
		if m, _ := filter.(map[string]interface{}); len(m) > 0 {
			t.Fatalf("AllTypes must not send a filter, got %v", filter)
		}
	}
	search, _ := gotParams["search"].(map[string]interface{})
	if search["name"] != "cpu" || search["key_"] != "cpu" {
		t.Fatalf("search = %v, want name+key_ OR search", search)
	}
	if gotParams["searchByAny"] != true {
		t.Fatalf("searchByAny = %v, want true", gotParams["searchByAny"])
	}
	if _, ok := gotParams["selectValueMap"]; !ok {
		t.Fatal("item.get must select value maps for enum items")
	}

	// 默认（数值型选择器）仍只取 0/3
	if _, err := z.ItemsByFilter(context.Background(), []string{"10084"}, ItemFilter{}, 100); err != nil {
		t.Fatalf("ItemsByFilter numeric: %v", err)
	}
	filter, _ := gotParams["filter"].(map[string]interface{})
	values, _ := filter["value_type"].([]interface{})
	if len(values) != 2 || values[0] != "0" || values[1] != "3" {
		t.Fatalf("default value_type filter = %v, want [0 3]", filter["value_type"])
	}
}

func TestProblemCounts(t *testing.T) {
	m := &mockZabbix{handler: func(m *mockZabbix, req rpcRequest) (interface{}, *Error) {
		switch req.Method {
		case "user.login":
			return "tok", nil
		case "trigger.get":
			return []map[string]interface{}{
				{"triggerid": "1", "hosts": []map[string]string{{"hostid": "10084"}}},
				{"triggerid": "2", "hosts": []map[string]string{{"hostid": "10084"}}},
				{"triggerid": "3", "hosts": []map[string]string{{"hostid": "10105"}}},
			}, nil
		}
		return nil, &Error{Code: -32602, Message: "unexpected method " + req.Method}
	}}
	z := testZabbix(newMockServer(t, m).URL)

	counts, err := z.ProblemCounts(context.Background(), []string{"10084", "10105"}, 100)
	if err != nil {
		t.Fatalf("ProblemCounts: %v", err)
	}
	if counts["10084"] != 2 || counts["10105"] != 1 {
		t.Fatalf("counts = %v, want map[10084:2 10105:1]", counts)
	}
}

func TestItemDisplayValueAndHistoryType(t *testing.T) {
	item := Item{
		ValueType: "3",
		ValueMap: []ValueMap{{
			Mappings: []ValueMapMapping{
				{Type: "0", Value: "0", NewValue: "Down"},
				{Type: "0", Value: "1", NewValue: "Up"},
			},
		}},
	}
	if got := item.DisplayValue("0"); got != "Down" {
		t.Fatalf("DisplayValue(0) = %q, want Down", got)
	}
	if got := item.DisplayValue("7"); got != "7" {
		t.Fatalf("unmapped value = %q, want 7", got)
	}
	if item.HistoryType() != 3 {
		t.Fatalf("HistoryType(unsigned) = %d, want 3", item.HistoryType())
	}
	if (Item{ValueType: "2"}).HistoryType() != 2 {
		t.Fatal("log items must map to history type 2")
	}
}

// 真机 7.0.31 观测：item.get 对只有一张值映射的监控项会把 valuemap 返回成
// 单个对象而非数组，解码必须两种形态都兼容，否则整个 latest-data 挂 500。
func TestItemValuemapObjectOrArray(t *testing.T) {
	payload := []map[string]interface{}{
		{
			"itemid": "1", "name": "Agent ping", "key_": "agent.ping", "value_type": "3",
			"valuemap": map[string]interface{}{
				"valuemapid": "4",
				"name":       "Availability",
				"mappings": []map[string]string{
					{"type": "0", "value": "1", "newvalue": "Available"},
				},
			},
		},
		{
			"itemid": "2", "name": "CPU util", "key_": "system.cpu.util", "value_type": "0",
			"valuemap": []interface{}{}, // 空数组也要能解
		},
	}
	var items []Item
	blob, _ := json.Marshal(payload)
	if err := json.Unmarshal(blob, &items); err != nil {
		t.Fatalf("unmarshal valuemap-as-object: %v", err)
	}
	if len(items[0].ValueMap) != 1 || items[0].ValueMap[0].Name != "Availability" {
		t.Fatalf("object form not decoded: %+v", items[0].ValueMap)
	}
	if items[0].DisplayValue("1") != "Available" {
		t.Fatalf("mapping lost: %q", items[0].DisplayValue("1"))
	}
	if len(items[1].ValueMap) != 0 {
		t.Fatalf("empty array should decode to empty list, got %+v", items[1].ValueMap)
	}
}
