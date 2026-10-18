package zabbix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Zabbix JSON-RPC returns most identifiers, clocks and values as strings
// (since Zabbix 5.4 every ID is a string). Types below mirror that wire
// format; helpers convert on demand.

// HostRef is the "hosts" element nested in events/problems.
type HostRef struct {
	HostID string `json:"hostid"`
	Name   string `json:"name"`
	Host   string `json:"host"`
}

// DisplayName prefers the visible name, falling back to the technical name.
func (h HostRef) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Host
}

// HostGroupRef maps the "hostgroups" element nested in host.get
// (selectHostGroups). It is the host-side mirror of HostGroup.
type HostGroupRef struct {
	GroupID string `json:"groupid"`
	Name    string `json:"name"`
}

// Host maps host.get output.
type Host struct {
	HostID     string         `json:"hostid"`
	Host       string         `json:"host"`   // technical name
	Name       string         `json:"name"`   // visible name
	Status     string         `json:"status"` // 0=monitored, 1=not monitored
	Groups     []HostGroupRef `json:"hostgroups,omitempty"`
	Interfaces []Interface    `json:"interfaces,omitempty"`
}

// Interface maps the host.get selectInterfaces element. IP is the agent or
// SNMP/JMX address depending on type; only IP-based interfaces are useful for
// the "search by IP" feature.
type Interface struct {
	InterfaceID string `json:"interfaceid"`
	IP          string `json:"ip"`
	DNS         string `json:"dns"`
	Port        string `json:"port"`
	Type        string `json:"type"` // 1=agent, 2=SNMP, 3=IPMI, 4=JMX
	Main        string `json:"main"` // 1=primary interface of its type
	// Available is the interface reachability flag maintained by the server:
	// 0=unknown, 1=available, 2=unavailable. It is read-only and only
	// meaningful for polled interface types.
	Available string `json:"available,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PrimaryInterface returns the primary agent interface, falling back to the
// primary interface of any type, then to the first IP-bearing interface.
//
// The fallback matters for SNMP-only hosts, whose primary interface type is
// 2: without it those hosts would show no address at all in the host list.
func (h Host) PrimaryInterface() (Interface, bool) {
	var fallback Interface
	var found bool
	for _, iface := range h.Interfaces {
		if iface.Main == "1" && iface.Type == "1" {
			return iface, true
		}
		if iface.IP == "" && iface.DNS == "" {
			continue
		}
		if found && iface.Main != "1" && fallback.Main == "1" {
			continue
		}
		if !found || (iface.Main == "1" && fallback.Main != "1") {
			fallback = iface
			found = true
		}
	}
	return fallback, found
}

// PrimaryIP returns the primary agent interface IP, falling back to the first
// non-empty IP of any type.
func (h Host) PrimaryIP() string {
	iface, ok := h.PrimaryInterface()
	if !ok {
		return ""
	}
	return iface.IP
}

func (h Host) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Host
}

// HostGroup maps hostgroup.get output.
type HostGroup struct {
	GroupID string `json:"groupid"`
	Name    string `json:"name"`
	UUID    string `json:"uuid,omitempty"`
}

// ValueMapMapping is one valuemap entry: Zabbix renders the raw value through
// it before display (e.g. 0 -> "Up"). Type follows the server enum:
// 0=equals, 1=greater or equal, 2=less or equal, 3=in range, 4=regexp,
// 5=default.
type ValueMapMapping struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	NewValue string `json:"newvalue"`
}

// ValueMap maps item.get selectValueMap output.
type ValueMap struct {
	ValueMapID string            `json:"valuemapid"`
	Name       string            `json:"name"`
	Mappings   []ValueMapMapping `json:"mappings"`
}

// ValueMapList is the item.valuemap field. Zabbix usually returns an array,
// but item.get emits a single object when the item has exactly one map (seen
// against 7.0.31) — accept both, null decodes to nil.
type ValueMapList []ValueMap

func (l *ValueMapList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '{' {
		var single ValueMap
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return err
		}
		*l = ValueMapList{single}
		return nil
	}
	var list []ValueMap
	if err := json.Unmarshal(trimmed, &list); err != nil {
		return err
	}
	*l = list
	return nil
}

// Item maps item.get output. ValueType follows the Zabbix contract:
// 0=float, 1=character, 2=log, 3=unsigned integer, 4=text.
type Item struct {
	ItemID     string    `json:"itemid"`
	Name       string    `json:"name"`
	Key        string    `json:"key_"`
	ValueType  string    `json:"value_type"`
	Units      string    `json:"units"`
	Status     string    `json:"status"` // 0=monitored, 1=not monitored
	State      string    `json:"state"`  // 0=normal, 1=not supported
	HostID     string    `json:"hostid"`
	Hosts      []HostRef `json:"hosts"`
	TemplateID string    `json:"templateid,omitempty"`
	LastValue  string    `json:"lastvalue"`
	LastClock  string    `json:"lastclock"`
	// PrevValue/LastNS/Delay/Description/Error/ValueMap back the "latest data"
	// view (Zabbix Monitoring > Hosts > Latest data).
	PrevValue   string       `json:"prevvalue,omitempty"`
	LastNS      string       `json:"lastns,omitempty"`
	Delay       string       `json:"delay,omitempty"`
	Description string       `json:"description,omitempty"`
	Error       string       `json:"error,omitempty"`
	ValueMap    ValueMapList `json:"valuemap,omitempty"`
}

// IsNumeric reports whether the item can be charted (float / unsigned).
func (i Item) IsNumeric() bool {
	return i.ValueType == "0" || i.ValueType == "3"
}

// HistoryType maps value_type to the history.get "history" object type:
// 0=float, 1=character, 2=log, 3=unsigned integer, 4=text.
func (i Item) HistoryType() int {
	switch i.ValueType {
	case "0":
		return 0
	case "1":
		return 1
	case "2":
		return 2
	case "3":
		return 3
	case "4":
		return 4
	default:
		return 0
	}
}

// DisplayValue renders a raw value through the item's first value map.
// Unmapped values (and items without a map) are returned unchanged.
func (i Item) DisplayValue(raw string) string {
	if raw == "" || len(i.ValueMap) == 0 {
		return raw
	}
	for _, mapping := range i.ValueMap[0].Mappings {
		if mapping.Type == "0" && mapping.Value == raw {
			return mapping.NewValue
		}
	}
	return raw
}

// History maps one history.get row. Clock is unix seconds.
type History struct {
	ItemID string `json:"itemid"`
	Clock  string `json:"clock"`
	Value  string `json:"value"`
	NS     string `json:"ns,omitempty"`
}

// Trend maps one trend.get row. Clock is the hour boundary (unix seconds).
type Trend struct {
	ItemID     string `json:"itemid"`
	Clock      string `json:"clock"`
	ValueAvg   string `json:"value_avg"`
	ValueMin   string `json:"value_min"`
	ValueMax   string `json:"value_max"`
	ValueCount string `json:"value_count"`
}

// EventTag maps the "tags" element of events/problems.
type EventTag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// Problem maps problem.get output (recent problems by default).
type Problem struct {
	EventID      string     `json:"eventid"`
	Name         string     `json:"name"`
	Severity     string     `json:"severity"`
	Clock        string     `json:"clock"`
	NS           string     `json:"ns"`
	Acknowledged string     `json:"acknowledged"`
	OpData       string     `json:"opdata"`
	Hosts        []HostRef  `json:"hosts"`
	Tags         []EventTag `json:"tags"`
	Suppression  []Suppress `json:"suppression_data,omitempty"`
}

// Suppress maps problem.suppression_data element (maintenance suppression).
type Suppress struct {
	MaintenanceID string `json:"maintenanceid"`
	SuppressUntil string `json:"suppress_until"`
}

// Event maps event.get output for trigger events (object=0).
type Event struct {
	EventID      string     `json:"eventid"`
	Name         string     `json:"name"`
	Clock        string     `json:"clock"`
	NS           string     `json:"ns"`
	Value        string     `json:"value"` // 0=OK close, 1=PROBLEM open
	Severity     string     `json:"severity"`
	Acknowledged string     `json:"acknowledged"`
	Hosts        []HostRef  `json:"hosts"`
	Tags         []EventTag `json:"tags"`
}

// ParseInt64 parses a Zabbix string-encoded integer.
func ParseInt64(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty numeric string")
	}
	return strconv.ParseInt(s, 10, 64)
}

// ParseFloat parses a Zabbix string-encoded float. Zabbix may emit "Unsupported"
// or empty values for unsupported items; callers should skip those rows.
func ParseFloat(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty numeric string")
	}
	return strconv.ParseFloat(s, 64)
}
