package zabbix

import (
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

// Host maps host.get output.
type Host struct {
	HostID string `json:"hostid"`
	Host   string `json:"host"`   // technical name
	Name   string `json:"name"`   // visible name
	Status string `json:"status"` // 0=monitored, 1=not monitored
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
}

// IsNumeric reports whether the item can be charted (float / unsigned).
func (i Item) IsNumeric() bool {
	return i.ValueType == "0" || i.ValueType == "3"
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
