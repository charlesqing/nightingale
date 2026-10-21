// Package mcpclient 把 models.MCPServer 适配成 aiagent.ToolSource，使外部 MCP
// 服务器暴露的工具能被注入到 AI 对话的工具循环里。
//
// 传输：仅支持远程型 sse 与 streamable-http（MCP Go SDK v1.6.1 客户端传输只提供
// 这两种）。每次 DiscoverTools / CallTool 都新建一次 MCP 会话（connect → list/call
// → close），不保活长连接：MCP 网关背后的服务器常不保证持久会话，且按调用开闭能
// 自然随对话轮生命周期回收，避免中心进程堆积空闲 socket。
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ccfos/nightingale/v6/aiagent"
	"github.com/ccfos/nightingale/v6/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/toolkits/pkg/logger"
)

// MCPSource 实现 aiagent.ToolSource，代理单个外部 MCP 服务器。
type MCPSource struct {
	server *models.MCPServer
}

// NewMCPSource 构造一个代理 server 的工具源。
func NewMCPSource(server *models.MCPServer) *MCPSource {
	return &MCPSource{server: server}
}

// DiscoverTools 列出该 MCP 服务器当前提供的全部工具，转成 aiagent.AgentTool。
func (s *MCPSource) DiscoverTools(ctx context.Context) ([]aiagent.AgentTool, error) {
	var tools []aiagent.AgentTool
	err := s.withSession(ctx, func(session *mcp.ClientSession) error {
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			return err
		}
		for _, t := range res.Tools {
			tools = append(tools, aiagent.AgentTool{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  convertInputSchema(t.InputSchema),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

// CallTool 调用本源中名为 tool.Name 的工具，返回给 LLM 的纯文本结果。
func (s *MCPSource) CallTool(ctx context.Context, tool *aiagent.AgentTool, args map[string]interface{}) (string, error) {
	var sb strings.Builder
	isError := false
	err := s.withSession(ctx, func(session *mcp.ClientSession) error {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      tool.Name,
			Arguments: args,
		})
		if err != nil {
			return err
		}
		// IsError 表示工具在服务端执行失败（协议调用本身成功）：
		// 透传内容但打上错误标记，让模型把本轮观测当失败处理并决定重试或换路。
		isError = res.IsError
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	out := sb.String()
	if isError {
		out = "Error (MCP tool reported failure): " + out
	}
	return out, nil
}

// withSession 以 server.Timeout 为上限开一次 MCP 会话，执行 fn 后确保关闭。
func (s *MCPSource) withSession(ctx context.Context, fn func(session *mcp.ClientSession) error) error {
	timeout := time.Duration(s.server.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport, err := s.buildTransport()
	if err != nil {
		return err
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "nightingale-center",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect mcp server %q failed: %w", s.server.Name, err)
	}
	defer func() {
		if e := session.Close(); e != nil {
			logger.Warningf("[MCP] close session for %q: %v", s.server.Name, e)
		}
	}()

	return fn(session)
}

// buildTransport 根据 server 配置构造 SDK 客户端传输，并把 headers 注入到每个出站请求。
func (s *MCPSource) buildTransport() (mcp.Transport, error) {
	if err := validateMCPURL(s.server.URL); err != nil {
		return nil, err
	}

	// 优先用持久化列 Headers；测试接口等未落库场景该列为空，回退到 API 视图
	// HeadersView（请求体 bound 进该字段），两者取其一即可。
	raw := s.server.Headers
	if raw == "" && s.server.HeadersView != nil {
		if b, err := json.Marshal(s.server.HeadersView); err == nil {
			raw = string(b)
		}
	}
	headers := map[string]string{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return nil, fmt.Errorf("parse headers for %q: %w", s.server.Name, err)
		}
	}

	httpClient := &http.Client{
		Transport: &headerRoundTripper{rt: http.DefaultTransport, headers: headers},
	}

	switch s.server.Transport {
	case models.MCPServerTransportSSE:
		return &mcp.SSEClientTransport{Endpoint: s.server.URL, HTTPClient: httpClient}, nil
	case models.MCPServerTransportStreamableHTTP:
		// 关掉独立 SSE 流：我们按请求-响应模式使用，不需要服务端主动推送通知，
		// 也避免按调用建连时维持一条常驻 GET 连接。
		return &mcp.StreamableClientTransport{
			Endpoint:            s.server.URL,
			HTTPClient:          httpClient,
			DisableStandaloneSSE: true,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported transport %q for server %q", s.server.Transport, s.server.Name)
	}
}

// headerRoundTripper 在每个出站请求上追加固定的 headers（如鉴权头 / 自定义头）。
type headerRoundTripper struct {
	rt      http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTrip 约定不得修改传入请求：clone 后再注入 headers（并发安全）。
	r := req.Clone(req.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.rt.RoundTrip(r)
}

// validateMCPURL 做最小化的 SSRF 防护：仅放行 http/https，并拒绝云元数据地址
// 169.254.169.254（防止借 MCP 服务器地址窃取实例凭据）。本机/内网地址放行——内部
// MCP 常跑在 localhost 或内网，属正常用法。
func validateMCPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https")
	}
	if u.Hostname() == "169.254.169.254" {
		return fmt.Errorf("link-local metadata endpoint is not allowed")
	}
	return nil
}

// convertInputSchema 把 MCP 工具的 JSON Schema inputSchema 抽成 aiagent 的工具参数列表。
func convertInputSchema(schema any) []aiagent.ToolParameter {
	var params []aiagent.ToolParameter
	sm, ok := schema.(map[string]any)
	if !ok {
		return params
	}
	props, ok := sm["properties"].(map[string]any)
	if !ok {
		return params
	}
	requiredSet := map[string]bool{}
	if req, ok := sm["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				requiredSet[s] = true
			}
		}
	}
	for name, raw := range props {
		p := aiagent.ToolParameter{Name: name}
		if pm, ok := raw.(map[string]any); ok {
			if t, ok := pm["type"].(string); ok {
				p.Type = t
			}
			if d, ok := pm["description"].(string); ok {
				p.Description = d
			}
		}
		p.Required = requiredSet[name]
		params = append(params, p)
	}
	return params
}
