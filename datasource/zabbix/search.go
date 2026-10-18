package zabbix

import (
	"context"
	"strings"

	dskitzabbix "github.com/ccfos/nightingale/v6/dskit/zabbix"
)

// GroupRef is one host-group tag of a host row.
type GroupRef struct {
	GroupID string `json:"groupid"`
	Name    string `json:"name"`
}

// HostSearchRow is one row of the host search result (POST /zabbix-host-search).
//
// The shape mirrors what the Zabbix "Monitoring > Hosts" grid shows: name,
// interface (IP:port), host groups, status. ProblemCount is the number of open
// PROBLEM triggers on that host (Zabbix's "问题" column).
type HostSearchRow struct {
	HostID       string     `json:"hostid"`
	Host         string     `json:"host"`          // technical name
	Name         string     `json:"name"`          // visible name
	Status       string     `json:"status"`        // 0=monitored, 1=not monitored
	IP           string     `json:"ip"`            // primary interface IP (may be empty)
	Port         string     `json:"port"`          // primary interface port
	IfaceType    string     `json:"iface_type"`    // 1=agent, 2=SNMP, 3=IPMI, 4=JMX
	Available    string     `json:"available"`     // 0=unknown, 1=available, 2=unavailable
	Groups       []GroupRef `json:"groups"`        // host groups (may be empty)
	ProblemCount int        `json:"problem_count"` // open PROBLEM triggers
}

// HostSearchParam is the filter set of the host query page.
//
// Keyword is the legacy "match name or IP" box; Name / IP / Status are the
// dedicated filters of the Zabbix-style filter form. All conditions are ANDed,
// so an empty struct returns every host.
type HostSearchParam struct {
	Keyword  string
	Name     string
	IP       string
	Status   string // "0" monitored, "1" not monitored, "" any
	GroupIDs []string
}

// SearchHosts keeps the original signature (keyword + groups) for callers and
// tests that predate the filter form.
func (z *Zabbix) SearchHosts(ctx context.Context, keyword string, groupIDs []string, offset, limit int) ([]HostSearchRow, int64, error) {
	return z.SearchHostsBy(ctx, HostSearchParam{Keyword: keyword, GroupIDs: groupIDs}, offset, limit)
}

// SearchHostsBy implements the host page: fetch the (capped) host inventory,
// filter it in memory, then paginate.
//
// Zabbix host.get cannot match interface IPs server-side (search only covers
// host/name), so IP filtering has to happen here. Host inventories are small
// relative to maxQueryRows (default 1000); beyond the cap total is a lower
// bound — the UI surfaces that as a truncation hint.
//
// The open-problem counts are fetched only for the rows of the current page
// (one trigger.get call), and a failure there is deliberately non-fatal: the
// host list is still useful without the badge, so a broken trigger.get must
// not take down the whole page.
func (z *Zabbix) SearchHostsBy(ctx context.Context, p HostSearchParam, offset, limit int) ([]HostSearchRow, int64, error) {
	hosts, err := z.Hosts(ctx, p.GroupIDs, z.maxQueryRows())
	if err != nil {
		return nil, 0, err
	}

	filtered := filterHosts(hosts, p)

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultMaxQueryRows
	}
	if offset >= len(filtered) {
		return []HostSearchRow{}, int64(len(filtered)), nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}

	page := filtered[offset:end]
	rows := make([]HostSearchRow, 0, len(page))
	ids := make([]string, 0, len(page))
	for _, h := range page {
		rows = append(rows, hostToRow(h))
		ids = append(ids, h.HostID)
	}

	if len(ids) > 0 {
		if counts, err := z.ProblemCounts(ctx, ids, z.maxQueryRows()); err == nil {
			for i := range rows {
				rows[i].ProblemCount = counts[rows[i].HostID]
			}
		}
	}

	return rows, int64(len(filtered)), nil
}

// hostToRow flattens a host, preferring the agent interface for the address
// column (see Host.PrimaryInterface).
func hostToRow(h dskitzabbix.Host) HostSearchRow {
	row := HostSearchRow{
		HostID: h.HostID,
		Host:   h.Host,
		Name:   h.Name,
		Status: h.Status,
		Groups: make([]GroupRef, 0, len(h.Groups)),
	}
	for _, g := range h.Groups {
		row.Groups = append(row.Groups, GroupRef{GroupID: g.GroupID, Name: g.Name})
	}
	if iface, ok := h.PrimaryInterface(); ok {
		row.IP = iface.IP
		row.Port = iface.Port
		row.IfaceType = iface.Type
		row.Available = iface.Available
	}
	return row
}

// filterHosts applies every active condition (AND).
func filterHosts(hosts []dskitzabbix.Host, p HostSearchParam) []dskitzabbix.Host {
	keyword := strings.ToLower(strings.TrimSpace(p.Keyword))
	name := strings.ToLower(strings.TrimSpace(p.Name))
	ip := strings.ToLower(strings.TrimSpace(p.IP))
	status := strings.TrimSpace(p.Status)

	out := make([]dskitzabbix.Host, 0, len(hosts))
	for _, h := range hosts {
		if status != "" && h.Status != status {
			continue
		}
		if name != "" && !hostMatchesName(h, name) {
			continue
		}
		if ip != "" && !hostMatchesIP(h, ip) {
			continue
		}
		// Legacy keyword: name OR address.
		if keyword != "" && !hostMatchesName(h, keyword) && !hostMatchesIP(h, keyword) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// hostMatchesName reports whether the lowercased needle is a substring of the
// technical or the visible host name.
func hostMatchesName(h dskitzabbix.Host, needle string) bool {
	return strings.Contains(strings.ToLower(h.Host), needle) ||
		strings.Contains(strings.ToLower(h.Name), needle)
}

// hostMatchesIP reports whether the needle is a substring of any interface IP
// or DNS name — DNS matters for hosts addressed by name only.
func hostMatchesIP(h dskitzabbix.Host, needle string) bool {
	for _, iface := range h.Interfaces {
		if iface.IP != "" && strings.Contains(strings.ToLower(iface.IP), needle) {
			return true
		}
		if iface.DNS != "" && strings.Contains(strings.ToLower(iface.DNS), needle) {
			return true
		}
	}
	return false
}
