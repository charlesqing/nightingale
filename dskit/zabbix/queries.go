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
		"time_till": toSec,
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
		"time_till": toSec,
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
		"time_till":             toSec,
		"sortfield":             "eventid",
		"sortorder":             "DESC",
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
		"output":     "extend",
		"object":     0, // trigger events
		"time_from":  fromSec,
		"time_till":  toSec,
		"sortfield":  "clock",
		"sortorder":  "DESC",
		"selectTags": "extend",
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

// Hosts lists hosts, optionally filtered by host group. selectInterfaces and
// selectHostGroups are included so callers can render the interface column,
// search/filter by IP (host.get cannot match interface IPs server-side) and
// show the host-group tags of each row.
func (z *Zabbix) Hosts(ctx context.Context, groupIDs []string, limit int) ([]Host, error) {
	params := map[string]interface{}{
		"output":           []string{"hostid", "host", "name", "status"},
		"selectInterfaces": []string{"interfaceid", "ip", "dns", "port", "type", "main", "available", "error"},
		"selectHostGroups": []string{"groupid", "name"},
		"sortfield":        "host",
		"sortorder":        "ASC",
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

// Item fields needed by every item view. The last* / prevvalue trio is what
// Zabbix's "Latest data" grid renders; description/delay/error feed the detail
// tooltip and the "not supported" hint.
var itemOutputFields = []string{
	"itemid", "name", "key_", "value_type", "units", "status", "state",
	"hostid", "lastvalue", "lastclock", "prevvalue", "lastns",
	"delay", "description", "error",
}

// ItemFilter narrows item.get.
//
// The zero value keeps the historical behaviour of the item selector: only
// chartable value types (float / unsigned). Set AllTypes to drop the
// value_type filter entirely (Latest data), or ValueTypes to pick specific
// ones.
type ItemFilter struct {
	Search     string   // name OR key_ substring (case-insensitive, server side)
	ValueTypes []string // explicit value_type whitelist; ignored when AllTypes
	AllTypes   bool     // no value_type filter
	Status     string   // 0=enabled, 1=disabled, ""=any
	State      string   // 0=normal, 1=not supported, ""=any
}

// Items lists numeric (chartable) items, optionally filtered by host and a
// name/key substring search. It is kept for the item selector / alert rule
// editor; use ItemsByFilter for the latest-data view.
func (z *Zabbix) Items(ctx context.Context, hostIDs []string, search string, limit int) ([]Item, error) {
	return z.ItemsByFilter(ctx, hostIDs, ItemFilter{Search: search}, limit)
}

// ItemsByFilter lists items with the given filter. Value maps are always
// selected so callers can render enum items the way Zabbix does.
func (z *Zabbix) ItemsByFilter(ctx context.Context, hostIDs []string, f ItemFilter, limit int) ([]Item, error) {
	params := map[string]interface{}{
		"output":         itemOutputFields,
		"selectHosts":    []string{"hostid", "host", "name"},
		"selectValueMap": "extend",
		"sortfield":      "name",
		"sortorder":      "ASC",
	}

	filter := map[string]interface{}{}
	switch {
	case f.AllTypes:
	case len(f.ValueTypes) > 0:
		filter["value_type"] = f.ValueTypes
	default:
		filter["value_type"] = []string{"0", "3"}
	}
	if f.Status != "" {
		filter["status"] = []string{f.Status}
	}
	if f.State != "" {
		filter["state"] = []string{f.State}
	}
	if len(filter) > 0 {
		params["filter"] = filter
	}

	if len(hostIDs) > 0 {
		params["hostids"] = hostIDs
	}
	// searchByAny turns the map into an OR: a keyword hits either the visible
	// name or the key, which is what the operators expect from the Zabbix
	// latest-data search box.
	if s := strings.TrimSpace(f.Search); s != "" {
		params["search"] = map[string]interface{}{"name": s, "key_": s}
		params["searchByAny"] = true
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
		"output":         itemOutputFields,
		"itemids":        itemIDs,
		"selectHosts":    []string{"hostid", "host", "name"},
		"selectValueMap": "extend",
	}
	var rows []Item
	if err := z.do(ctx, "item.get", params, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// triggerProblemRow is the trigger.get projection used by ProblemCounts.
type triggerProblemRow struct {
	TriggerID string    `json:"triggerid"`
	Hosts     []HostRef `json:"hosts"`
}

// ProblemCounts returns the number of open PROBLEM triggers per host.
//
// trigger.get (unlike problem.get) supports selectHosts, so one call covers a
// whole page of hosts. Only monitored triggers are counted — disabled hosts
// and triggers should not inflate the badge.
func (z *Zabbix) ProblemCounts(ctx context.Context, hostIDs []string, limit int) (map[string]int, error) {
	params := map[string]interface{}{
		"output":      []string{"triggerid"},
		"filter":      map[string]interface{}{"value": 1},
		"selectHosts": []string{"hostid"},
		"monitored":   true,
		"sortfield":   "priority",
		"sortorder":   "DESC",
	}
	if len(hostIDs) > 0 {
		params["hostids"] = hostIDs
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var rows []triggerProblemRow
	if err := z.do(ctx, "trigger.get", params, &rows); err != nil {
		return nil, err
	}

	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		for _, host := range row.Hosts {
			counts[host.HostID]++
		}
	}
	return counts, nil
}
