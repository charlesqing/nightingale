package zabbix

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	dskitzabbix "github.com/ccfos/nightingale/v6/dskit/zabbix"
)

// LatestDataParam is the payload of POST /zabbix-latest-data — one host's
// "latest data" grid (Zabbix Monitoring > Hosts > Latest data).
type LatestDataParam struct {
	HostID     string   `json:"hostid"`
	Search     string   `json:"search"`
	ValueTypes []string `json:"value_types"` // empty = all types
	Status     string   `json:"status"`      // 0=enabled, 1=disabled, ""=any
	State      string   `json:"state"`       // 0=normal, 1=not supported, ""=any
	Offset     int      `json:"offset"`
	Limit      int      `json:"limit"`
}

// LatestDataRow is one item of the latest-data grid.
//
// Values stay raw (Zabbix's string contract) because the frontend owns unit
// humanisation (B -> GB, bps -> Mbps); LastValueDisplay is the value after the
// item's value map, i.e. what an operator should read for enum items.
type LatestDataRow struct {
	ItemID           string   `json:"itemid"`
	Name             string   `json:"name"`
	Key              string   `json:"key_"`
	ValueType        string   `json:"value_type"`
	Units            string   `json:"units"`
	LastValue        string   `json:"lastvalue"`
	LastValueDisplay string   `json:"lastvalue_display"`
	PrevValue        string   `json:"prevvalue"`
	Change           *float64 `json:"change"`     // last - prev, nil when not numeric
	ChangePct        *float64 `json:"change_pct"` // percent, nil when prev is 0 / not numeric
	LastClock        int64    `json:"lastclock"`
	Status           string   `json:"status"`
	State            string   `json:"state"`
	Error            string   `json:"error"`
	Delay            string   `json:"delay"`
	Description      string   `json:"description"`
	Chartable        bool     `json:"chartable"` // numeric items only
}

// LatestDataResult is the paginated latest-data response.
type LatestDataResult struct {
	List      []LatestDataRow `json:"list"`
	Total     int64           `json:"total"`
	Truncated bool            `json:"truncated"` // fetch hit max_query_rows
}

// LatestData lists a host's items with their last value, previous value and
// delta — the data behind the Zabbix "最新数据" grid.
//
// item.get has no server-side offset, so the (capped) result set is fetched
// once and paginated in memory. Text/log items are included: they cannot be
// charted (Chartable=false) but they are exactly the rows operators look for
// in the latest-data view.
func (z *Zabbix) LatestData(ctx context.Context, p LatestDataParam) (*LatestDataResult, error) {
	if strings.TrimSpace(p.HostID) == "" {
		return nil, fmt.Errorf("hostid is required")
	}

	fetchLimit := z.maxQueryRows()
	items, err := z.ItemsByFilter(ctx, []string{p.HostID}, toItemFilter(p), fetchLimit)
	if err != nil {
		return nil, err
	}

	result := &LatestDataResult{
		Total:     int64(len(items)),
		Truncated: len(items) >= fetchLimit,
	}

	offset := p.Offset
	if offset < 0 {
		offset = 0
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultMaxQueryRows
	}
	if offset >= len(items) {
		result.List = []LatestDataRow{}
		return result, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}

	result.List = make([]LatestDataRow, 0, end-offset)
	for _, item := range items[offset:end] {
		result.List = append(result.List, itemToLatestRow(item))
	}
	return result, nil
}

// toItemFilter converts the HTTP-facing filter to the dskit filter.
// An empty ValueTypes means "all types" here (the opposite of the item
// selector default), because the latest-data grid shows text/log items too.
func toItemFilter(p LatestDataParam) dskitzabbix.ItemFilter {
	return dskitzabbix.ItemFilter{
		Search:     p.Search,
		ValueTypes: p.ValueTypes,
		AllTypes:   len(p.ValueTypes) == 0,
		Status:     p.Status,
		State:      p.State,
	}
}

// itemToLatestRow flattens one item, computing the delta against the previous
// value for numeric types only (text/log deltas are meaningless).
func itemToLatestRow(item dskitzabbix.Item) LatestDataRow {
	row := LatestDataRow{
		ItemID:           item.ItemID,
		Name:             item.Name,
		Key:              item.Key,
		ValueType:        item.ValueType,
		Units:            item.Units,
		LastValue:        item.LastValue,
		LastValueDisplay: item.DisplayValue(item.LastValue),
		PrevValue:        item.PrevValue,
		LastClock:        parseInt64(item.LastClock),
		Status:           item.Status,
		State:            item.State,
		Error:            item.Error,
		Delay:            item.Delay,
		Description:      item.Description,
		Chartable:        item.IsNumeric(),
	}

	if item.IsNumeric() && row.LastValue != "" && row.PrevValue != "" {
		last, errLast := strconv.ParseFloat(row.LastValue, 64)
		prev, errPrev := strconv.ParseFloat(row.PrevValue, 64)
		if errLast == nil && errPrev == nil {
			change := last - prev
			row.Change = &change
			// prev == 0 would divide by zero; leave the percentage empty so the
			// UI shows only the absolute delta instead of +Inf.
			if prev != 0 {
				pct := change / prev * 100
				row.ChangePct = &pct
			}
		}
	}
	return row
}
