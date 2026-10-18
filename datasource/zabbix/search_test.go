package zabbix

import (
	"context"
	"strconv"
	"testing"
)

// mockHosts serves host.get with interfaces (selectInterfaces extend).
func mockHosts() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"hostid": "10084", "host": "zabbix-server", "name": "Zabbix server", "status": "0",
			"hostgroups": []map[string]string{{"groupid": "4", "name": "Zabbix servers"}},
			"interfaces": []map[string]string{
				{"interfaceid": "1", "ip": "192.168.1.10", "port": "10050", "type": "1", "main": "1", "available": "1"},
			},
		},
		{
			"hostid": "10105", "host": "web-01", "name": "Web Server 01", "status": "0",
			"hostgroups": []map[string]string{{"groupid": "5", "name": "Linux servers"}},
			"interfaces": []map[string]string{
				{"interfaceid": "2", "ip": "10.0.12.21", "port": "10050", "type": "1", "main": "1", "available": "1"},
				{"interfaceid": "3", "ip": "10.0.12.22", "port": "161", "type": "2", "main": "1"}, // SNMP, non-primary fallback
			},
		},
		{
			"hostid": "10106", "host": "db-01", "name": "Database 01", "status": "1", // not monitored
			"hostgroups": []map[string]string{{"groupid": "6", "name": "Databases"}},
			"interfaces": []map[string]string{
				{"interfaceid": "4", "ip": "10.0.20.30", "port": "10050", "type": "1", "main": "1", "available": "2"},
			},
		},
	}
}

func newSearchTestDatasource(t *testing.T, m *dsMockZabbix) *Zabbix {
	t.Helper()
	return newTestDatasource(t, m)
}

func TestSearchHostsByIP(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "host.get":
				return mockHosts()
			case "trigger.get":
				// 问题数聚合：默认无活动问题
				return []map[string]interface{}{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newSearchTestDatasource(t, m)

	rows, total, err := z.SearchHosts(context.Background(), "10.0.12.21", nil, 0, 10)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("rows=%d total=%d, want 1/1", len(rows), total)
	}
	if rows[0].HostID != "10105" {
		t.Fatalf("hostid=%s, want 10105", rows[0].HostID)
	}
	// PrimaryIP must prefer the main agent interface over the SNMP one.
	if rows[0].IP != "10.0.12.21" {
		t.Fatalf("ip=%s, want 10.0.12.21 (primary agent)", rows[0].IP)
	}
}

func TestSearchHostsByHostname(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "host.get":
				return mockHosts()
			case "trigger.get":
				// 问题数聚合：默认无活动问题
				return []map[string]interface{}{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newSearchTestDatasource(t, m)

	// technical name (case-insensitive)
	rows, total, err := z.SearchHosts(context.Background(), "DB-01", nil, 0, 10)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if total != 1 || rows[0].HostID != "10106" {
		t.Fatalf("by technical name: rows=%d total=%d hostid=%v", len(rows), total, rows)
	}

	// visible name substring
	rows, _, err = z.SearchHosts(context.Background(), "web server", nil, 0, 10)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if len(rows) != 1 || rows[0].HostID != "10105" {
		t.Fatalf("by visible name: rows=%d hostid=%v", len(rows), rows)
	}

	// IP substring (partial)
	rows, _, err = z.SearchHosts(context.Background(), "192.168.1", nil, 0, 10)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if len(rows) != 1 || rows[0].HostID != "10084" {
		t.Fatalf("by partial IP: rows=%d hostid=%v", len(rows), rows)
	}

	// no match
	rows, total, err = z.SearchHosts(context.Background(), "nonexistent", nil, 0, 10)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("no-match: rows=%d total=%d, want 0/0", len(rows), total)
	}
}

func TestSearchHostsEmptyKeywordReturnsAll(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "host.get":
				return mockHosts()
			case "trigger.get":
				// 问题数聚合：默认无活动问题
				return []map[string]interface{}{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newSearchTestDatasource(t, m)

	rows, total, err := z.SearchHosts(context.Background(), "  ", nil, 0, 10)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("rows=%d total=%d, want 3/3", len(rows), total)
	}
}

func TestSearchHostsPagination(t *testing.T) {
	// 12 hosts (host-00 .. host-11), one interface each: 10.0.0.<i>
	hosts := make([]map[string]interface{}, 0, 12)
	for i := 0; i < 12; i++ {
		hosts = append(hosts, map[string]interface{}{
			"hostid": strconv.Itoa(1000 + i), "host": "host-" + strconv.Itoa(i), "name": "Host " + strconv.Itoa(i), "status": "0",
			"interfaces": []map[string]string{
				{"interfaceid": strconv.Itoa(i), "ip": "10.0.0." + strconv.Itoa(i), "type": "1", "main": "1"},
			},
		})
	}
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "host.get":
				return hosts
			case "trigger.get":
				return []map[string]interface{}{}
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newSearchTestDatasource(t, m)

	// page 1: offset 0, limit 5
	rows, total, err := z.SearchHosts(context.Background(), "", nil, 0, 5)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if len(rows) != 5 || total != 12 {
		t.Fatalf("page1: rows=%d total=%d, want 5/12", len(rows), total)
	}
	if rows[0].HostID != "1000" || rows[4].HostID != "1004" {
		t.Fatalf("page1 ids: %s..%s, want 1000..1004", rows[0].HostID, rows[4].HostID)
	}

	// page 3 (last, partial): offset 10, limit 5 → 2 rows
	rows, total, err = z.SearchHosts(context.Background(), "", nil, 10, 5)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if len(rows) != 2 || total != 12 {
		t.Fatalf("page3: rows=%d total=%d, want 2/12", len(rows), total)
	}

	// offset beyond the end → empty, total still reported
	rows, total, err = z.SearchHosts(context.Background(), "", nil, 50, 5)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	if len(rows) != 0 || total != 12 {
		t.Fatalf("beyond-end: rows=%d total=%d, want 0/12", len(rows), total)
	}

	// keyword narrows the set, then pagination applies to the filtered set
	rows, total, err = z.SearchHosts(context.Background(), "10.0.0.1", nil, 0, 5)
	if err != nil {
		t.Fatalf("SearchHosts: %v", err)
	}
	// matches 10.0.0.1, 10.0.0.10, 10.0.0.11 (substring "10.0.0.1")
	if total != 3 || len(rows) != 3 {
		t.Fatalf("keyword+pagination: rows=%d total=%d, want 3/3", len(rows), total)
	}
}

// newFilterTestDatasource wires host.get + trigger.get for the filter tests.
func newFilterTestDatasource(t *testing.T, problems []map[string]interface{}) *Zabbix {
	t.Helper()
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "host.get":
				return mockHosts()
			case "trigger.get":
				return problems
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	return newSearchTestDatasource(t, m)
}

func TestSearchHostsDedicatedFilters(t *testing.T) {
	z := newFilterTestDatasource(t, nil)

	// 主机名筛选：只匹配显示名/技术名
	rows, total, err := z.SearchHostsBy(context.Background(), HostSearchParam{Name: "web"}, 0, 10)
	if err != nil {
		t.Fatalf("SearchHostsBy name: %v", err)
	}
	if total != 1 || rows[0].HostID != "10105" {
		t.Fatalf("name filter: total=%d rows=%v", total, rows)
	}
	if len(rows[0].Groups) != 1 || rows[0].Groups[0].Name != "Linux servers" {
		t.Fatalf("host groups not returned: %+v", rows[0].Groups)
	}
	if rows[0].Port != "10050" || rows[0].Available != "1" {
		t.Fatalf("interface fields: port=%q available=%q", rows[0].Port, rows[0].Available)
	}

	// IP 筛选：接口 IP 子串
	rows, total, err = z.SearchHostsBy(context.Background(), HostSearchParam{IP: "10.0.20"}, 0, 10)
	if err != nil {
		t.Fatalf("SearchHostsBy ip: %v", err)
	}
	if total != 1 || rows[0].HostID != "10106" {
		t.Fatalf("ip filter: total=%d rows=%v", total, rows)
	}

	// 状态筛选：仅未监控
	rows, total, err = z.SearchHostsBy(context.Background(), HostSearchParam{Status: "1"}, 0, 10)
	if err != nil {
		t.Fatalf("SearchHostsBy status: %v", err)
	}
	if total != 1 || rows[0].HostID != "10106" {
		t.Fatalf("status filter: total=%d rows=%v", total, rows)
	}

	// 多条件取交：名称命中但状态不符 → 0
	rows, total, err = z.SearchHostsBy(context.Background(), HostSearchParam{Name: "web", Status: "1"}, 0, 10)
	if err != nil {
		t.Fatalf("SearchHostsBy combined: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("combined filter should be empty, got total=%d", total)
	}
}

func TestSearchHostsProblemCounts(t *testing.T) {
	problems := []map[string]interface{}{
		{"triggerid": "1", "hosts": []map[string]string{{"hostid": "10084"}}},
		{"triggerid": "2", "hosts": []map[string]string{{"hostid": "10084"}}},
	}
	z := newFilterTestDatasource(t, problems)

	rows, _, err := z.SearchHostsBy(context.Background(), HostSearchParam{}, 0, 10)
	if err != nil {
		t.Fatalf("SearchHostsBy: %v", err)
	}
	byID := map[string]HostSearchRow{}
	for _, r := range rows {
		byID[r.HostID] = r
	}
	if byID["10084"].ProblemCount != 2 {
		t.Fatalf("problem count for 10084 = %d, want 2", byID["10084"].ProblemCount)
	}
	if byID["10105"].ProblemCount != 0 {
		t.Fatalf("problem count for 10105 = %d, want 0", byID["10105"].ProblemCount)
	}
}

// 问题数聚合失败不能拖垮主机列表：trigger.get 报错时仍返回主机行。
func TestSearchHostsSurvivesProblemCountFailure(t *testing.T) {
	m := &dsMockZabbix{
		handler: func(m *dsMockZabbix, method string, params map[string]interface{}) interface{} {
			switch method {
			case "user.login":
				return "tok"
			case "host.get":
				return mockHosts()
			case "trigger.get":
				return nil // 模拟上游异常：结果为 null
			}
			t.Fatalf("unexpected method %s", method)
			return nil
		},
	}
	z := newSearchTestDatasource(t, m)

	rows, total, err := z.SearchHostsBy(context.Background(), HostSearchParam{}, 0, 10)
	if err != nil {
		t.Fatalf("SearchHostsBy: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("rows=%d total=%d, want 3/3", len(rows), total)
	}
}
