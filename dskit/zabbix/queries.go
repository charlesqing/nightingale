package zabbix

import (
	"context"
	"fmt"
	"strings"
)

// TrendValueFields are the aggregation columns trend.get can return.
var TrendValueFields = map[string]bool{
	"value_avg":   true,
	"value_min":   true,
	"value_max":   true,
	"value_count": true,
}

// Histories queries history.get for one history object type
// (0=float, 3=unsigned integer). itemIDs may span multiple items.
func (z *Zabbix) Histories(ctx context.Context, historyType int, itemIDs []string, fromSec, toSec int64, limit int) ([]History, error) {
	if len(itemIDs) == 0 {
		return nil, fmt.Errorf("itemids is required")
	}
	if fromSec <= 0 || toSec <= 0 || toSec < fromSec {
		return nil, fmt.Errorf("invalid time range: from=%d to=%d", fromSec, toSec)
	}

	params := map[string]interface{}{
		"output":    "extend",
		"history":   historyType,
		"itemids":   itemIDs,
		"time_from": fromSec,
		"time_to":   toSec,
		"sortfield": "clock",
		"sortorder": "DESC",
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []History
	if err := z.do(ctx, "history.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Trends queries trend.get (hourly aggregates). valueField must be one of
// TrendValueFields.
func (z *Zabbix) Trends(ctx context.Context, itemIDs []string, fromSec, toSec int64, valueField string, limit int) ([]Trend, error) {
	if len(itemIDs) == 0 {
		return nil, fmt.Errorf("itemids is required")
	}
	if fromSec <= 0 || toSec <= 0 || toSec < fromSec {
		return nil, fmt.Errorf("invalid time range: from=%d to=%d", fromSec, toSec)
	}
	if !TrendValueFields[valueField] {
		return nil, fmt.Errorf("unsupported trend value field: %s", valueField)
	}

	params := map[string]interface{}{
		"output":    []string{"itemid", "clock", valueField},
		"itemids":   itemIDs,
		"time_from": fromSec,
		"time_to":   toSec,
		"sortfield": "clock",
		"sortorder": "DESC",
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []Trend
	if err := z.do(ctx, "trend.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Problems queries problem.get for trigger problem events.
func (z *Zabbix) Problems(ctx context.Context, fromSec, toSec int64, severities []string, hostIDs []string, limit int) ([]Problem, error) {
	if fromSec <= 0 || toSec <= 0 || toSec < fromSec {
		return nil, fmt.Errorf("invalid time range: from=%d to=%d", fromSec, toSec)
	}

	params := map[string]interface{}{
		"output":                "extend",
		"time_from":             fromSec,
		"time_to":               toSec,
		"sortfield":             "eventid",
		"sortorder":             "DESC",
		"selectHosts":           []string{"hostid", "host", "name"},
		"selectTags":            "extend",
		"selectSuppressionData": "extend",
	}
	if len(severities) > 0 {
		params["severities"] = severities
	}
	if len(hostIDs) > 0 {
		params["hostids"] = hostIDs
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []Problem
	if err := z.do(ctx, "problem.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Events queries event.get for trigger events (object=0).
func (z *Zabbix) Events(ctx context.Context, fromSec, toSec int64, severities []string, hostIDs []string, limit int) ([]Event, error) {
	if fromSec <= 0 || toSec <= 0 || toSec < fromSec {
		return nil, fmt.Errorf("invalid time range: from=%d to=%d", fromSec, toSec)
	}

	params := map[string]interface{}{
		"output":      "extend",
		"object":      0, // trigger events
		"time_from":   fromSec,
		"time_to":     toSec,
		"sortfield":   "clock",
		"sortorder":   "DESC",
		"selectHosts": []string{"hostid", "host", "name"},
		"selectTags":  "extend",
	}
	if len(severities) > 0 {
		params["severities"] = severities
	}
	if len(hostIDs) > 0 {
		params["hostids"] = hostIDs
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []Event
	if err := z.do(ctx, "event.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// HostGroups lists host groups.
func (z *Zabbix) HostGroups(ctx context.Context) ([]HostGroup, error) {
	params := map[string]interface{}{
		"output":     []string{"groupid", "name"},
		"sortfield":  "name",
		"sortorder":  "ASC",
		"real_hosts": true,
	}
	var rows []HostGroup
	if err := z.do(ctx, "hostgroup.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Hosts lists hosts, optionally filtered by host group.
func (z *Zabbix) Hosts(ctx context.Context, groupIDs []string, limit int) ([]Host, error) {
	params := map[string]interface{}{
		"output":    []string{"hostid", "host", "name", "status"},
		"sortfield": "host",
		"sortorder": "ASC",
	}
	if len(groupIDs) > 0 {
		params["groupids"] = groupIDs
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []Host
	if err := z.do(ctx, "host.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Items lists numeric (chartable) items, optionally filtered by host and a
// name/key substring search. Only value_type 0 (float) and 3 (unsigned) are
// returned — text/log items cannot be charted.
func (z *Zabbix) Items(ctx context.Context, hostIDs []string, search string, limit int) ([]Item, error) {
	params := map[string]interface{}{
		"output":      []string{"itemid", "name", "key_", "value_type", "units", "status", "state", "hostid", "lastvalue", "lastclock"},
		"filter":      map[string]interface{}{"value_type": []string{"0", "3"}},
		"selectHosts": []string{"hostid", "host", "name"},
		"sortfield":   "name",
		"sortorder":   "ASC",
	}
	if len(hostIDs) > 0 {
		params["hostids"] = hostIDs
	}
	if strings.TrimSpace(search) != "" {
		params["search"] = map[string]interface{}{"name": search}
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []Item
	if err := z.do(ctx, "item.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetItems fetches full metadata (name/units/value_type) for specific items.
// QueryData uses it to label series and to split items by history type.
func (z *Zabbix) GetItems(ctx context.Context, itemIDs []string) ([]Item, error) {
	if len(itemIDs) == 0 {
		return nil, fmt.Errorf("itemids is required")
	}
	params := map[string]interface{}{
		"output":      []string{"itemid", "name", "key_", "value_type", "units", "status", "state", "hostid", "lastvalue", "lastclock"},
		"itemids":     itemIDs,
		"selectHosts": []string{"hostid", "host", "name"},
	}
	var rows []Item
	if err := z.do(ctx, "item.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
