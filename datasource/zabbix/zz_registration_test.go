package zabbix

import (
	"testing"

	"github.com/ccfos/nightingale/v6/datasource"
)

// TestRegisteredAndHealthChecker guards the center-side wiring: the datasource
// must be registered (so GetDatasourceByType resolves it, both for the upsert
// connectivity probe and dscache.PutDatasources) and implement HealthChecker.
func TestRegisteredAndHealthChecker(t *testing.T) {
	plug, err := datasource.GetDatasourceByType(zabbixType, map[string]interface{}{
		"zabbix.addr":     "http://127.0.0.1:5081",
		"zabbix.user":     "mockuser",
		"zabbix.password": "mockpass",
	})
	if err != nil {
		t.Fatalf("GetDatasourceByType(%s): %v", zabbixType, err)
	}
	zb, ok := plug.(*Zabbix)
	if !ok {
		t.Fatalf("plug type = %T, want *Zabbix", plug)
	}
	if zb.Addr != "http://127.0.0.1:5081" || zb.User != "mockuser" || zb.Password != "mockpass" {
		t.Fatalf("settings not decoded: addr=%q user=%q password=%q", zb.Addr, zb.User, zb.Password)
	}
	if _, ok := plug.(datasource.HealthChecker); !ok {
		t.Fatal("*Zabbix does not implement datasource.HealthChecker")
	}
}
