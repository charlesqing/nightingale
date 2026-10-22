package router

import (
	"net/http"

	"github.com/ccfos/nightingale/v6/aiagent/mcpclient"
	"github.com/ccfos/nightingale/v6/models"
	"github.com/ccfos/nightingale/v6/pkg/ginx"

	"github.com/gin-gonic/gin"
	"github.com/toolkits/pkg/logger"
)

// mcpServerGets 列出全部 MCP 服务器。列表接口遮蔽 headers 取值，避免凭据出现在表格
// 或网络响应里（detail 接口同样遮蔽，理由见 models.MCPServer.Decorate）。
func (rt *Router) mcpServerGets(c *gin.Context) {
	search := ginx.QueryStr(c, "search", "")
	lst, err := models.MCPServerGets(rt.Ctx, search)
	ginx.Dangerous(err)
	ginx.NewRender(c).Data(lst, nil)
}

// mcpServerMetaGets 轻量列表：只回 id/name，供 Agent 表单的 MCP 多选器使用，不触达凭据列。
func (rt *Router) mcpServerMetaGets(c *gin.Context) {
	search := ginx.QueryStr(c, "search", "")
	lst, err := models.MCPServerMetaGets(rt.Ctx, search)
	ginx.Dangerous(err)
	ginx.NewRender(c).Data(lst, nil)
}

func (rt *Router) mcpServerGet(c *gin.Context) {
	id := ginx.UrlParamInt64(c, "id")
	obj, err := models.MCPServerGetById(rt.Ctx, id)
	ginx.Dangerous(err)
	if obj == nil {
		ginx.Bomb(http.StatusNotFound, "mcp server not found")
	}
	// 详情同样遮蔽取值：MCP token 是可直接调用外部服务的活凭据，读接口一律
	// 不回落明文。编辑表单里掩码值原样保存时，Update 的 mergeMaskedHeaders
	// 会保留库中原值（见 models/mcp_server.go）。
	if err := obj.Decorate(true); err != nil {
		ginx.Dangerous(err)
	}
	ginx.NewRender(c).Data(obj, nil)
}

func (rt *Router) mcpServerAdd(c *gin.Context) {
	var obj models.MCPServer
	ginx.BindJSON(c, &obj)
	ginx.Dangerous(obj.Verify())

	me := c.MustGet("user").(*models.User)
	err := obj.Create(rt.Ctx, me.Username)
	ginx.Dangerous(err)
	logger.Infof("[MCP] user=%s created mcp server id=%d name=%q url=%q transport=%s", me.Username, obj.Id, obj.Name, obj.URL, obj.Transport)
	ginx.NewRender(c).Data(obj.Id, nil)
}

func (rt *Router) mcpServerPut(c *gin.Context) {
	id := ginx.UrlParamInt64(c, "id")
	obj, err := models.MCPServerGetById(rt.Ctx, id)
	ginx.Dangerous(err)
	if obj == nil {
		ginx.Bomb(http.StatusNotFound, "mcp server not found")
	}

	var ref models.MCPServer
	ginx.BindJSON(c, &ref)
	ginx.Dangerous(ref.Verify())

	me := c.MustGet("user").(*models.User)
	ginx.Dangerous(obj.Update(rt.Ctx, me.Username, ref))
	logger.Infof("[MCP] user=%s updated mcp server id=%d name=%q url=%q transport=%s enabled=%v", me.Username, id, ref.Name, ref.URL, ref.Transport, ref.Enabled)
	ginx.NewRender(c).Message(nil)
}

func (rt *Router) mcpServerDel(c *gin.Context) {
	id := ginx.UrlParamInt64(c, "id")
	obj, err := models.MCPServerGetById(rt.Ctx, id)
	ginx.Dangerous(err)
	if obj == nil {
		ginx.Bomb(http.StatusNotFound, "mcp server not found")
	}
	me := c.MustGet("user").(*models.User)
	ginx.NewRender(c).Message(obj.Delete(rt.Ctx))
	logger.Infof("[MCP] user=%s deleted mcp server id=%d name=%q", me.Username, id, obj.Name)
}

// mcpServerTest 不落库，直接按提交配置连一次 MCP 服务器并列举工具，供前端「测试连接」。
func (rt *Router) mcpServerTest(c *gin.Context) {
	var obj models.MCPServer
	ginx.BindJSON(c, &obj)
	ginx.Dangerous(obj.Verify())

	src := mcpclient.NewMCPSource(&obj)
	tools, err := src.DiscoverTools(c.Request.Context())
	if err != nil {
		ginx.NewRender(c).Data(gin.H{"ok": false, "error": err.Error()}, nil)
		return
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	ginx.NewRender(c).Data(gin.H{"ok": true, "tool_count": len(names), "tools": names}, nil)
}
