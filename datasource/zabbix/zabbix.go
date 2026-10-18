package zabbix

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/nightingale/v6/datasource"
	dskitzabbix "github.com/ccfos/nightingale/v6/dskit/zabbix"

	"github.com/mitchellh/mapstructure"
)

const (
	zabbixType = dskitzabbix.ZabbixType

	defaultMaxQueryRows = 1000
	// trendWindowSeconds: windows wider than this switch to trend.get
	// (hourly aggregates) instead of raw history.
	trendWindowSeconds = 3 * 24 * 3600
)

// Zabbix adapts the Zabbix JSON-RPC client to the Nightingale Datasource
// interface. One datasource instance == one Zabbix frontend server.
type Zabbix struct {
	dskitzabbix.Zabbix `json:",inline" mapstructure:",squash"`
}

var _ datasource.Datasource = (*Zabbix)(nil)
var _ datasource.HealthChecker = (*Zabbix)(nil)

func init() {
	datasource.RegisterDatasource(zabbixType, new(Zabbix))
}

func (z *Zabbix) Init(settings map[string]interface{}) (datasource.Datasource, error) {
	newest := new(Zabbix)
	err := mapstructure.Decode(settings, newest)
	return newest, err
}

func (z *Zabbix) InitClient() error {
	return z.InitHTTPClient()
}

func (z *Zabbix) Validate(ctx context.Context) error {
	if strings.TrimSpace(z.Addr) == "" {
		return fmt.Errorf("zabbix addr is invalid, please check datasource setting")
	}
	if !strings.HasPrefix(z.Addr, "http://") && !strings.HasPrefix(z.Addr, "https://") {
		return fmt.Errorf("zabbix addr must start with http:// or https://")
	}
	if strings.TrimSpace(z.User) == "" {
		return fmt.Errorf("zabbix user is invalid, please check datasource setting")
	}
	if z.Password == "" {
		return fmt.Errorf("zabbix password is invalid, please check datasource setting")
	}
	return nil
}

// Equal compares all settings so dscache can detect configuration changes.
func (z *Zabbix) Equal(p datasource.Datasource) bool {
	newest, ok := p.(*Zabbix)
	if !ok {
		return false
	}

	if z.Addr != newest.Addr ||
		z.User != newest.User ||
		z.Password != newest.Password ||
		z.Basic != newest.Basic ||
		z.TLS != newest.TLS ||
		z.Timeout != newest.Timeout ||
		z.MaxQueryRows != newest.MaxQueryRows {
		return false
	}

	if len(z.Headers) != len(newest.Headers) {
		return false
	}
	for k, v := range z.Headers {
		if nv, ok := newest.Headers[k]; !ok || nv != v {
			return false
		}
	}
	return true
}

func (z *Zabbix) MakeLogQuery(ctx context.Context, query interface{}, eventTags []string, start, end int64) (interface{}, error) {
	return nil, nil
}

func (z *Zabbix) MakeTSQuery(ctx context.Context, query interface{}, eventTags []string, start, end int64) (interface{}, error) {
	return nil, nil
}

// QueryMapData backs the alert-rule "附加查询": after an event fires, pull the
// related Zabbix problem detail to attach to the event.
func (z *Zabbix) QueryMapData(ctx context.Context, query interface{}) ([]map[string]string, error) {
	qp := new(QueryParam)
	if err := mapstructure.Decode(query, qp); err != nil {
		return nil, err
	}

	// Only meaningful when the caller pinned specific events (eventids) or a
	// recent window; without either there is nothing to attach.
	if len(qp.EventIDs) == 0 && qp.Interval <= 0 && qp.Window <= 0 {
		return nil, nil
	}

	toSec := time.Now().Unix()
	window := qp.Window
	if window <= 0 {
		window = qp.Interval
	}
	if window <= 0 {
		window = datasource.DefaultEnrichInterval
	}
	fromSec := toSec - window

	problems, err := z.Problems(ctx, fromSec, toSec, qp.Severities, qp.HostIDs, qp.limit())
	if err != nil {
		return nil, err
	}

	out := make([]map[string]string, 0, len(problems))
	for _, p := range problems {
		row := map[string]string{
			"eventid":      p.EventID,
			"name":         p.Name,
			"severity":     p.Severity,
			"clock":        p.Clock,
			"acknowledged": p.Acknowledged,
			"opdata":       p.OpData,
		}
		if len(p.Hosts) > 0 {
			row["hostid"] = p.Hosts[0].HostID
			row["hostname"] = p.Hosts[0].DisplayName()
		}
		for _, tag := range p.Tags {
			row["tag_"+tag.Tag] = tag.Value
		}
		out = append(out, row)
	}
	return out, nil
}
