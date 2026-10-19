package mcpclient

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ccfos/nightingale/v6/aiagent"
	"github.com/ccfos/nightingale/v6/models"
)

// TestMCPSourceCallToolE2E 走真实 MCP 服务器验证 DiscoverTools + CallTool 全链路
// （与聊天路径用的是同一个 MCPSource 实现）。默认跳过，保证 `go test ./...` 密闭：
//
//	# 先起一个本地 FastMCP 测试服务器（暴露 echo/add/now_note 三个工具）：
//	#   uvx --with "mcp<2" python <test_server>.py   # streamable-http :19000/mcp
//	MCP_E2E_URL=http://127.0.0.1:19000/mcp \
//	  go test ./aiagent/mcpclient/ -run TestMCPSourceCallToolE2E -v
//
// 本机回环地址记得加 NO_PROXY=127.0.0.1,localhost——Go 的 http.DefaultTransport
// 会读取 HTTP(S)_PROXY 环境变量，代理通常不转发回环连接。
func TestMCPSourceCallToolE2E(t *testing.T) {
	url := os.Getenv("MCP_E2E_URL")
	if url == "" {
		t.Skip("MCP_E2E_URL not set; skipping live MCP e2e test")
	}

	server := &models.MCPServer{
		Name:      "e2e",
		Transport: models.MCPServerTransportStreamableHTTP,
		URL:       url,
		Timeout:   30,
	}
	src := NewMCPSource(server)
	ctx := context.Background()

	tools, err := src.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("expected >=1 tool from MCP server, got 0")
	}
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	t.Logf("discovered %d tools: %s", len(tools), strings.Join(names, ", "))

	// add(a,b) 是测试服务器的确定性工具，结果文本应为和值。
	got, err := src.CallTool(ctx, &aiagent.AgentTool{Name: "add"},
		map[string]interface{}{"a": 17, "b": 25})
	if err != nil {
		t.Fatalf("CallTool(add): %v", err)
	}
	t.Logf("CallTool(add, {a:17,b:25}) -> %q", got)
	if !strings.Contains(got, "42") {
		t.Fatalf("expected tool result to contain 42, got %q", got)
	}
}
