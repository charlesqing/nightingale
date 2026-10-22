package mcpclient

import (
	"context"
	"net/netip"
	"testing"
)

func TestParseHostIP(t *testing.T) {
	cases := []struct {
		host string
		want string // "" means not a literal IP
	}{
		{"127.0.0.1", "127.0.0.1"},
		{"192.168.1.10", "192.168.1.10"},
		{"169.254.169.254", "169.254.169.254"},
		{"::1", "::1"},
		{"::ffff:169.254.169.254", "169.254.169.254"}, // IPv6-mapped 归一化
		{"[::1]", ""},                                 // 带括号不是合法字面量（url.Hostname 会先剥掉）
		{"2852039166", "169.254.169.254"},             // 十进制整型 IPv4
		{"0xA9FEA9FE", "169.254.169.254"},             // 十六进制整型 IPv4
		{"0x7f000001", "127.0.0.1"},                   // 十六进制回环
		{"2130706433", "127.0.0.1"},                   // 十进制回环
		{"mcp.example.com", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, ok := parseHostIP(c.host)
		if c.want == "" {
			if ok {
				t.Errorf("parseHostIP(%q) = %v, want not-an-ip", c.host, got)
			}
			continue
		}
		if !ok {
			t.Errorf("parseHostIP(%q) failed, want %s", c.host, c.want)
			continue
		}
		if got.String() != c.want {
			t.Errorf("parseHostIP(%q) = %s, want %s", c.host, got, c.want)
		}
	}
}

func TestIsForbiddenIP(t *testing.T) {
	forbidden := []string{
		"169.254.169.254", // 云元数据
		"169.254.1.1",     // link-local 段内其他地址
		"fe80::1",         // IPv6 link-local
		"0.0.0.0",         // 未指定
		"::",              // 未指定
		"224.0.0.1",       // 多播
		"ff02::1",         // IPv6 多播
	}
	for _, s := range forbidden {
		ip := netip.MustParseAddr(s)
		if !isForbiddenIP(ip) {
			t.Errorf("isForbiddenIP(%s) = false, want true", s)
		}
	}
	allowed := []string{
		"127.0.0.1",     // 回环：本地 MCP / e2e 正常用法，放行（见 validateMCPURL 注释）
		"::1",           // 回环
		"10.0.0.5",      // 私网
		"192.168.245.128", // 内网部署实例
		"172.16.0.9",    // 私网
		"8.8.8.8",       // 公网
	}
	for _, s := range allowed {
		ip := netip.MustParseAddr(s)
		if isForbiddenIP(ip) {
			t.Errorf("isForbiddenIP(%s) = true, want false", s)
		}
	}
}

func TestValidateMCPURL(t *testing.T) {
	ctx := context.Background()
	rejected := []string{
		"http://169.254.169.254/latest/meta-data/",         // 字面量元数据
		"http://2852039166/",                               // 十进制绕过
		"http://0xA9FEA9FE/",                               // 十六进制绕过
		"http://[::ffff:169.254.169.254]/",                 // IPv6-mapped 绕过
		"http://0.0.0.0:8080/mcp",                          // 未指定地址
		"https://169.254.1.2/mcp",                          // link-local 段
		"ftp://mcp.example.com",                            // 非 http(s)
		"gopher://127.0.0.1:70/",                           // 非 http(s)
		"http://",                                          // 空 host
		"not a url at all",                                 // 解析失败
	}
	for _, raw := range rejected {
		if err := validateMCPURL(ctx, raw); err == nil {
			t.Errorf("validateMCPURL(%q) = nil, want error", raw)
		}
	}
	accepted := []string{
		"http://127.0.0.1:19000/mcp", // 回环放行（本地调试/e2e）
		"http://192.168.245.128:8123/mcp",
		"http://localhost:19000/mcp", // DNS 名解析到回环，放行
		"https://8.8.8.8/mcp",
	}
	for _, raw := range accepted {
		if err := validateMCPURL(ctx, raw); err != nil {
			t.Errorf("validateMCPURL(%q) = %v, want nil", raw, err)
		}
	}
}
