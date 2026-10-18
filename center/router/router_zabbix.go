package router

import (
	"net/http"

	"github.com/ccfos/nightingale/v6/datasource/zabbix"
	"github.com/ccfos/nightingale/v6/dscache"
	"github.com/ccfos/nightingale/v6/pkg/ginx"

	"github.com/gin-gonic/gin"
)

const (
	zabbixDefaultLimit = 100
	zabbixMaxLimit     = 1000
)

func validateZabbixLimit(limit int) int {
	if limit <= 0 {
		return zabbixDefaultLimit
	}
	if limit > zabbixMaxLimit {
		return zabbixMaxLimit
	}
	return limit
}

// getZabbix resolves the datasource instance from the shared cache, mirroring
// getOpenObserve.
func getZabbix(c *gin.Context, cate string, datasourceId int64) *zabbix.Zabbix {
	plug, exists := dscache.DsCache.Get(cate, datasourceId)
	if !exists {
		ginx.Bomb(200, "cluster not exists")
	}

	zb, ok := plug.(*zabbix.Zabbix)
	if !ok {
		ginx.Bomb(200, "cluster not zabbix")
	}
	return zb
}

type ZabbixDatasourceReq struct {
	Cate         string `json:"cate"`
	DatasourceId int64  `json:"datasource_id"`
}

type ZabbixHostsReq struct {
	Cate         string   `json:"cate"`
	DatasourceId int64    `json:"datasource_id"`
	GroupIDs     []string `json:"groupids"`
	Limit        int      `json:"limit"`
}

type ZabbixItemsReq struct {
	Cate         string   `json:"cate"`
	DatasourceId int64    `json:"datasource_id"`
	HostIDs      []string `json:"hostids"`
	Search       string   `json:"search"`
	Limit        int      `json:"limit"`
}

type ZabbixHostSearchReq struct {
	Cate         string   `json:"cate"`
	DatasourceId int64    `json:"datasource_id"`
	Keyword      string   `json:"keyword"` // legacy: name OR IP substring
	Name         string   `json:"name"`    // host name (visible or technical)
	IP           string   `json:"ip"`      // interface IP / DNS substring
	Status       string   `json:"status"`  // 0=monitored, 1=not monitored, ""=any
	GroupIDs     []string `json:"groupids"`
	Offset       int      `json:"offset"`
	Limit        int      `json:"limit"`
}

type ZabbixLatestDataReq struct {
	Cate         string   `json:"cate"`
	DatasourceId int64    `json:"datasource_id"`
	HostID       string   `json:"hostid"`
	Search       string   `json:"search"`
	ValueTypes   []string `json:"value_types"`
	Status       string   `json:"status"`
	State        string   `json:"state"`
	Offset       int      `json:"offset"`
	Limit        int      `json:"limit"`
}

// ZabbixVersion returns the upstream Zabbix server version; also serves as a
// connectivity probe for the datasource settings page.
func (rt *Router) ZabbixVersion(c *gin.Context) {
	var f ZabbixDatasourceReq
	ginx.BindJSON(c, &f)

	zb := getZabbix(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	version, err := zb.APIVersion(ctx)
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(version, nil)
}

// ListZabbixHostGroups powers the host-group selector.
func (rt *Router) ListZabbixHostGroups(c *gin.Context) {
	var f ZabbixDatasourceReq
	ginx.BindJSON(c, &f)

	zb := getZabbix(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	groups, err := zb.HostGroups(ctx)
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(groups, nil)
}

// ListZabbixHosts powers the host selector (optionally filtered by group).
func (rt *Router) ListZabbixHosts(c *gin.Context) {
	var f ZabbixHostsReq
	ginx.BindJSON(c, &f)

	zb := getZabbix(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	hosts, err := zb.Hosts(ctx, f.GroupIDs, validateZabbixLimit(f.Limit))
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(hosts, nil)
}

// ListZabbixItems powers the item selector: numeric items only, with an
// optional name search, for the given hosts.
func (rt *Router) ListZabbixItems(c *gin.Context) {
	var f ZabbixItemsReq
	ginx.BindJSON(c, &f)

	if len(f.HostIDs) == 0 {
		ginx.Bomb(http.StatusBadRequest, "hostids is required")
	}

	zb := getZabbix(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	items, err := zb.Items(ctx, f.HostIDs, f.Search, validateZabbixLimit(f.Limit))
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(items, nil)
}

// SearchZabbixHosts powers the Zabbix monitoring query page: search hosts by
// name / host group / IP with in-memory pagination (Zabbix cannot match
// interface IPs server-side). Response shape: {list: [...], total: n}.
func (rt *Router) SearchZabbixHosts(c *gin.Context) {
	var f ZabbixHostSearchReq
	ginx.BindJSON(c, &f)

	zb := getZabbix(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	rows, total, err := zb.SearchHostsBy(ctx, zabbix.HostSearchParam{
		Keyword:  f.Keyword,
		Name:     f.Name,
		IP:       f.IP,
		Status:   f.Status,
		GroupIDs: f.GroupIDs,
	}, f.Offset, validateZabbixLimit(f.Limit))
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(map[string]interface{}{
		"list":  rows,
		"total": total,
	}, nil)
}

// ListZabbixLatestData powers the "latest data" grid of one host: every item
// type (not only chartable ones) with its last value, previous value and
// delta. Response shape: {list: [...], total: n, truncated: bool}.
func (rt *Router) ListZabbixLatestData(c *gin.Context) {
	var f ZabbixLatestDataReq
	ginx.BindJSON(c, &f)

	if f.HostID == "" {
		ginx.Bomb(http.StatusBadRequest, "hostid is required")
	}

	zb := getZabbix(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	res, err := zb.LatestData(ctx, zabbix.LatestDataParam{
		HostID:     f.HostID,
		Search:     f.Search,
		ValueTypes: f.ValueTypes,
		Status:     f.Status,
		State:      f.State,
		Offset:     f.Offset,
		Limit:      validateZabbixLimit(f.Limit),
	})
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(res, nil)
}
