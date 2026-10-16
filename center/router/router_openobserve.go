package router

import (
	"net/http"
	"sort"
	"strings"

	"github.com/ccfos/nightingale/v6/datasource/openobserve"
	"github.com/ccfos/nightingale/v6/dscache"
	"github.com/ccfos/nightingale/v6/pkg/ginx"
	"github.com/ccfos/nightingale/v6/pkg/logx"

	"github.com/gin-gonic/gin"
)

const (
	ooDefaultLabelNamesLimit  = 100
	ooDefaultLabelValuesLimit = 100
	ooDefaultParsedLimit      = 200
	ooMaxParsedLimit          = 500
)

type OpenObserveLabelNamesReq struct {
	Cate         string `json:"cate"`
	DatasourceId int64  `json:"datasource_id"`
	Query        string `json:"query"`
	Org          string `json:"org"`
	Stream       string `json:"stream"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	Filter       string `json:"filter"`
	Limit        int    `json:"limit"`
}

type OpenObserveLabelValuesReq struct {
	Cate         string `json:"cate"`
	DatasourceId int64  `json:"datasource_id"`
	Query        string `json:"query"`
	Org          string `json:"org"`
	Stream       string `json:"stream"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	Label        string `json:"label"`
	Filter       string `json:"filter"`
	Limit        int    `json:"limit"`
}

type OpenObserveParsedFieldsReq struct {
	Cate         string `json:"cate"`
	DatasourceId int64  `json:"datasource_id"`
	Query        string `json:"query"`
	Org          string `json:"org"`
	Stream       string `json:"stream"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	Limit        int    `json:"limit"`
}

type OpenObserveHistogramReq struct {
	Cate         string                    `json:"cate"`
	DatasourceId int64                     `json:"datasource_id"`
	Query        []openobserve.HistogramQuery `json:"query"`
}

func (rt *Router) QueryOpenObserveLabelNames(c *gin.Context) {
	var f OpenObserveLabelNamesReq
	ginx.BindJSON(c, &f)

	validateLokiTimeRange(f.Start, f.End)
	f.Limit = validateLokiLimit(f.Limit, ooDefaultLabelNamesLimit)
	if !rt.Center.AnonymousAccess.PromQuerier && !CheckDsPerm(c, f.DatasourceId, f.Cate, f) {
		ginx.Bomb(403, "no permission")
	}

	oo := getOpenObserve(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	ret, err := oo.QueryLabelNames(ctx, f.Query, f.Start, f.End, f.Filter, f.Limit, f.Org, f.Stream)
	ginx.Dangerous(err)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Field < ret[j].Field
	})

	ginx.NewRender(c).Data(ret, nil)
}

func (rt *Router) QueryOpenObserveLabelValues(c *gin.Context) {
	var f OpenObserveLabelValuesReq
	ginx.BindJSON(c, &f)

	validateLokiLabel(f.Label)
	validateLokiTimeRange(f.Start, f.End)
	f.Limit = validateLokiLimit(f.Limit, ooDefaultLabelValuesLimit)
	if !rt.Center.AnonymousAccess.PromQuerier && !CheckDsPerm(c, f.DatasourceId, f.Cate, f) {
		ginx.Bomb(403, "no permission")
	}

	oo := getOpenObserve(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	ret, err := oo.QueryLabelValues(ctx, f.Query, f.Start, f.End, f.Label, f.Filter, f.Limit, f.Org, f.Stream)
	ginx.Dangerous(err)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Value < ret[j].Value
	})

	ginx.NewRender(c).Data(ret, nil)
}

func (rt *Router) QueryOpenObserveParsedFields(c *gin.Context) {
	var f OpenObserveParsedFieldsReq
	ginx.BindJSON(c, &f)

	if strings.TrimSpace(f.Query) == "" {
		ginx.Bomb(http.StatusBadRequest, "query is required")
	}
	validateLokiTimeRange(f.Start, f.End)
	f.Limit = validateLokiParsedLimit(f.Limit)
	if !rt.Center.AnonymousAccess.PromQuerier && !CheckDsPerm(c, f.DatasourceId, f.Cate, f) {
		ginx.Bomb(403, "no permission")
	}

	oo := getOpenObserve(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	ret, err := oo.QueryParsedFields(ctx, f.Query, f.Start, f.End, f.Limit, f.Org, f.Stream)
	ginx.Dangerous(err)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Field < ret[j].Field
	})

	ginx.NewRender(c).Data(ret, nil)
}

func (rt *Router) QueryOpenObserveHistogram(c *gin.Context) {
	var f OpenObserveHistogramReq
	ginx.BindJSON(c, &f)

	if len(f.Query) == 0 {
		ginx.Bomb(http.StatusBadRequest, "query is required")
	}

	oo := getOpenObserve(c, f.Cate, f.DatasourceId)
	ret := make([]openobserve.HistogramValues, 0)
	for _, q := range f.Query {
		if strings.TrimSpace(q.Query) == "" {
			ginx.Bomb(http.StatusBadRequest, "query is required")
		}
		validateLokiTimeRange(q.Start, q.End)
		if !rt.Center.AnonymousAccess.PromQuerier && !CheckDsPerm(c, f.DatasourceId, f.Cate, q) {
			ginx.Bomb(403, "no permission")
		}

		ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
		data, err := oo.QueryHistogram(ctx, q)
		ginx.Dangerous(err)
		ret = append(ret, data...)
	}

	ginx.NewRender(c).Data(ret, nil)
}

type OpenObserveOrgsReq struct {
	Cate         string `json:"cate"`
	DatasourceId int64  `json:"datasource_id"`
}

type OpenObserveStreamsReq struct {
	Cate         string `json:"cate"`
	DatasourceId int64  `json:"datasource_id"`
	Org          string `json:"org"`
}

// ListOpenObserveOrgs 列出 OpenObserve 中的组织，供前端 Explorer 选择器使用。
func (rt *Router) ListOpenObserveOrgs(c *gin.Context) {
	var f OpenObserveOrgsReq
	ginx.BindJSON(c, &f)
	if !rt.Center.AnonymousAccess.PromQuerier && !CheckDsPerm(c, f.DatasourceId, f.Cate, f) {
		ginx.Bomb(403, "no permission")
	}

	oo := getOpenObserve(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	ret, err := oo.ListOrgs(ctx)
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(ret, nil)
}

// ListOpenObserveStreams 列出某组织下的数据流，供前端 Explorer 选择器使用。
func (rt *Router) ListOpenObserveStreams(c *gin.Context) {
	var f OpenObserveStreamsReq
	ginx.BindJSON(c, &f)
	if !rt.Center.AnonymousAccess.PromQuerier && !CheckDsPerm(c, f.DatasourceId, f.Cate, f) {
		ginx.Bomb(403, "no permission")
	}

	oo := getOpenObserve(c, f.Cate, f.DatasourceId)
	ctx := withCallContext(c.Request.Context(), f.DatasourceId, ginUser(c))
	ret, err := oo.ListStreams(ctx, f.Org)
	ginx.Dangerous(err)

	ginx.NewRender(c).Data(ret, nil)
}

func getOpenObserve(c *gin.Context, cate string, datasourceId int64) *openobserve.OpenObserve {
	plug, exists := dscache.DsCache.Get(cate, datasourceId)
	if !exists {
		logx.Warningf(c.Request.Context(), "cluster:%d not exists", datasourceId)
		ginx.Bomb(200, "cluster not exists")
	}

	oo, ok := plug.(*openobserve.OpenObserve)
	if !ok {
		ginx.Bomb(200, "cluster not openobserve")
	}

	return oo
}
