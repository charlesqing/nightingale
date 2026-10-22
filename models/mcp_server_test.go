package models

import "testing"

func TestMergeMaskedHeaders(t *testing.T) {
	existing := map[string]string{
		"Authorization": "Bearer real-token",
		"X-Team":        "sre",
	}
	cases := []struct {
		name     string
		incoming map[string]string
		want     map[string]string
		wantErr  bool
	}{
		{
			name:     "掩码值保留原值",
			incoming: map[string]string{"Authorization": mcpSecretMask},
			want:     map[string]string{"Authorization": "Bearer real-token"},
		},
		{
			name:     "新值覆盖",
			incoming: map[string]string{"Authorization": "Bearer new-token"},
			want:     map[string]string{"Authorization": "Bearer new-token"},
		},
		{
			name:     "混合：新键 + 掩码键 + 新值键",
			incoming: map[string]string{"Authorization": mcpSecretMask, "X-Team": "dba", "X-New": "1"},
			want:     map[string]string{"Authorization": "Bearer real-token", "X-Team": "dba", "X-New": "1"},
		},
		{
			name:     "新键不允许掩码占位",
			incoming: map[string]string{"X-Brand-New": mcpSecretMask},
			wantErr:  true,
		},
		{
			name:     "空值清空（显式置空）",
			incoming: map[string]string{"X-Team": ""},
			want:     map[string]string{"X-Team": ""},
		},
	}
	for _, c := range cases {
		got, err := mergeMaskedHeaders(existing, c.incoming)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: mergeMaskedHeaders err = nil, want error", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: mergeMaskedHeaders err = %v, want nil", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: got[%s] = %q, want %q (full: %v)", c.name, k, got[k], v, got)
			}
		}
	}

	if got, err := mergeMaskedHeaders(existing, nil); err != nil || got != nil {
		t.Errorf("nil incoming: got %v, %v; want nil, nil", got, err)
	}
}

func TestIsDuplicateKeyErr(t *testing.T) {
	hits := []string{
		"Error 1062: Duplicate entry 'foo' for key 'idx_name'",
		"UNIQUE constraint failed: mcp_server.name",
		`ERROR: duplicate key value violates unique constraint "mcp_server_name_uk" (SQLSTATE 23505)`,
	}
	for _, s := range hits {
		if !isDuplicateKeyErr(&fakeErr{s}) {
			t.Errorf("isDuplicateKeyErr(%q) = false, want true", s)
		}
	}
	misses := []string{
		"connection refused",
		"Error 1146: Table 'n9e.mcp_server' doesn't exist",
	}
	for _, s := range misses {
		if isDuplicateKeyErr(&fakeErr{s}) {
			t.Errorf("isDuplicateKeyErr(%q) = true, want false", s)
		}
	}
	if isDuplicateKeyErr(nil) {
		t.Error("isDuplicateKeyErr(nil) = true, want false")
	}
}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }

func TestMCPServerVerifyTimeoutClamp(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 30},      // 0 → 默认 30（字段语义：0 表示取默认）
		{-5, 30},     // 负值同样按未设置处理 → 默认 30
		{1, 1},       // 下限保留
		{30, 30},     // 区间内保留
		{300, 300},   // 上限保留
		{86400, 300}, // 超上限 → clamp 到 300
	}
	for _, c := range cases {
		m := &MCPServer{
			Name:      "x",
			Transport: MCPServerTransportStreamableHTTP,
			URL:       "http://127.0.0.1:19000/mcp",
			Timeout:   c.in,
		}
		if err := m.Verify(); err != nil {
			t.Fatalf("Verify() err = %v", err)
		}
		if m.Timeout != c.want {
			t.Errorf("Verify() timeout = %d, want %d", m.Timeout, c.want)
		}
	}
}
