package zabbix

import (
	"context"
	"testing"
)

// mockLatestItems mirrors a Zabbix 7.0 item.get response for one host:
// a float metric, an enum metric behind a value map (currently unsupported)
// and a log item (not chartable).
func mockLatestItems() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"itemid": "1", "name": "CPU utilization", "key_": "system.cpu.util",
			"value_type": "0", "units": "%", "status": "0", "state": "0",
			"lastvalue": "1.5", "prevvalue": "1.2", "lastclock": "1792321043",
			"delay": "1m", "description": "CPU busy time", "hostid": "10084",
			"valuemap": []map[string]interface{}{},
		},
		{
			"itemid": "2", "name": "Agent availability", "key_": "agent.ping",
			"value_type": "3", "units": "", "status": "0", "state": "1",
			"lastvalue": "0", "prevvalue": "1", "lastclock": "1792321044",
			"error": "Timeout while executing a shell script.", "hostid": "10084",
			"valuemap": []map[string]interface{}{
				{
					"valuemapid": "1", "name": "Zabbix agent ping",
					"mappings": []map[string]string{
						{"type": "0", "value": "0", "newvalue": "Down"},
						{"type": "0", "value": "1", "newvalue": "Up"},
					},
				},
			},
		},
		{
			"itemid": "3", "name": "Log tail", "key_": "log[/tmp/app.log]",
			"value_type": "2", "units": "", "status": "1", "state": "0",
			"lastvalue": "line", "prevvalue": "", "lastclock": "0",
			"hostid": "10084",
		},
	}
}

func newLatestTestDatasource(t *testing.T, m *dsMockZabbix) *Zabbix {
	t.Helper()
	return newTestDatasource(t, m)
}

func TestLatestDataRequiresHostID(t *testing.T) {
	m := &dsMockZabbix{handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
		t.Fatalf("unexpected method %s", method)
		return nil
	}}
	z := newLatestTestDatasource(t, m)

	if _, err := z.LatestData(context.Background(), LatestDataParam{}); err == nil {
		t.Fatal("expected an error for an empty hostid")
	}
}

func TestLatestDataRows(t *testing.T) {
	var gotParams map[string]interface{}
	m := &dsMockZabbix{handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
		switch method {
		case "user.login":
			return "tok"
		case "item.get":
			gotParams = params
			return mockLatestItems()
		}
		t.Fatalf("unexpected method %s", method)
		return nil
	}}
	z := newLatestTestDatasource(t, m)

	res, err := z.LatestData(context.Background(), LatestDataParam{HostID: "10084", Limit: 10})
	if err != nil {
		t.Fatalf("LatestData: %v", err)
	}
	if res.Total != 3 || len(res.List) != 3 {
		t.Fatalf("total=%d rows=%d, want 3/3", res.Total, len(res.List))
	}

	// 全部类型都要返回（不只可图表化的 0/3）。
	filter, _ := gotParams["filter"].(map[string]interface{})
	if _, has := filter["value_type"]; has {
		t.Fatalf("latest data must not filter value_type, got %v", filter["value_type"])
	}
	if _, has := gotParams["search"]; has {
		t.Fatalf("empty keyword must not send a search filter, got %v", gotParams["search"])
	}

	cpu := res.List[0]
	if !cpu.Chartable {
		t.Fatal("float item must be chartable")
	}
	if cpu.Change == nil || *cpu.Change < 0.299 || *cpu.Change > 0.301 {
		t.Fatalf("change = %v, want ~0.3", cpu.Change)
	}
	if cpu.ChangePct == nil || *cpu.ChangePct < 24.9 || *cpu.ChangePct > 25.1 {
		t.Fatalf("change pct = %v, want ~25", cpu.ChangePct)
	}
	if cpu.LastClock != 1792321043 {
		t.Fatalf("lastclock = %d", cpu.LastClock)
	}

	agent := res.List[1]
	if agent.LastValueDisplay != "Down" {
		t.Fatalf("value map not applied: %q", agent.LastValueDisplay)
	}
	if agent.State != "1" || agent.Error == "" {
		t.Fatalf("unsupported state/error lost: state=%q error=%q", agent.State, agent.Error)
	}

	logItem := res.List[2]
	if logItem.Chartable {
		t.Fatal("log item must not be chartable")
	}
	if logItem.Change != nil {
		t.Fatalf("text/log items must not have a delta, got %v", *logItem.Change)
	}
}

func TestLatestDataPaginationAndFilters(t *testing.T) {
	var gotParams map[string]interface{}
	m := &dsMockZabbix{handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
		switch method {
		case "user.login":
			return "tok"
		case "item.get":
			gotParams = params
			return mockLatestItems()
		}
		t.Fatalf("unexpected method %s", method)
		return nil
	}}
	z := newLatestTestDatasource(t, m)

	res, err := z.LatestData(context.Background(), LatestDataParam{
		HostID:     "10084",
		Search:     "agent",
		ValueTypes: []string{"3", "4"},
		Status:     "0",
		State:      "1",
		Offset:     1,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("LatestData: %v", err)
	}
	if res.Total != 3 || len(res.List) != 1 {
		t.Fatalf("paging: total=%d rows=%d, want 3/1", res.Total, len(res.List))
	}
	if res.List[0].ItemID != "2" {
		t.Fatalf("second row = %s, want itemid 2", res.List[0].ItemID)
	}

	filter, _ := gotParams["filter"].(map[string]interface{})
	if !equalStringSlice(filter["value_type"], []string{"3", "4"}) {
		t.Fatalf("value_type filter = %v", filter["value_type"])
	}
	if !equalStringSlice(filter["status"], []string{"0"}) {
		t.Fatalf("status filter = %v", filter["status"])
	}
	if !equalStringSlice(filter["state"], []string{"1"}) {
		t.Fatalf("state filter = %v", filter["state"])
	}
	search, _ := gotParams["search"].(map[string]interface{})
	if search["name"] != "agent" || search["key_"] != "agent" {
		t.Fatalf("search = %v", search)
	}
}

// equalStringSlice compares a decoded JSON array ([]interface{} of strings)
// with the expected []string.
func equalStringSlice(got interface{}, want []string) bool {
	arr, ok := got.([]interface{})
	if !ok || len(arr) != len(want) {
		return false
	}
	for i := range want {
		if arr[i] != want[i] {
			return false
		}
	}
	return true
}
