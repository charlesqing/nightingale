package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/nightingale/v6/pkg/ctx"
	"gorm.io/gorm"
)

// MCPServer 描述一个接入 Nightingale AI 的外部 MCP 服务器。AI Agent 通过
// AIAgent.MCPServerIds 绑定若干 MCP 服务器，运行时由 center 把这些服务器包成
// aiagent.ToolSource，把它们的工具注入对话（见 center/router 的
// defaultAgentToolSources）。
//
// 当前仅支持远程传输：streamable-http 与 sse（MCP Go SDK v1.6.1 的客户端
// 传输只有这两种；stdio 客户端传输 SDK 未提供，本地进程型 MCP 不在本期范围）。
type MCPServer struct {
	Id          int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Name        string `json:"name" gorm:"type:varchar(255);not null;default:''"`
	Description string `json:"description" gorm:"type:varchar(4096)"`

	// Transport：sse | streamable-http。
	Transport string `json:"transport" gorm:"type:varchar(32);not null;default:'streamable-http'"`

	// 远程地址（sse / streamable-http 必填）。
	URL string `json:"url" gorm:"type:varchar(2048)"`
	// Headers 以 JSON map[string]string 持久化（如鉴权头 / 自定义头）；读时遮蔽取值。
	Headers string `json:"-" gorm:"type:text"`

	// Timeout 单次工具调用超时（秒），0 表示取默认 30s。
	Timeout int `json:"timeout" gorm:"type:int;not null;default:30"`

	Enabled   bool   `json:"enabled" gorm:"type:boolean;not null;default:false"`
	CreatedAt int64  `json:"created_at" gorm:"type:bigint;not null;default:0"`
	CreatedBy string `json:"created_by" gorm:"type:varchar(64);not null;default:''"`
	UpdatedAt int64  `json:"updated_at" gorm:"type:bigint;not null;default:0"`
	UpdatedBy string `json:"updated_by" gorm:"type:varchar(64);not null;default:''"`

	// HeadersView 是 API 层对外暴露的 headers（列表接口遮蔽取值、详情接口返回明文）。
	// 不参与持久化（gorm:"-"），由 Create/Update 从它序列化进 Headers 列。
	HeadersView map[string]string `json:"headers,omitempty" gorm:"-"`
}

const (
	MCPServerTransportSSE            = "sse"
	MCPServerTransportStreamableHTTP = "streamable-http"

	mcpSecretMask = "********"
)

func (m *MCPServer) TableName() string {
	return "mcp_server"
}

func (m *MCPServer) Verify() error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch m.Transport {
	case "":
		m.Transport = MCPServerTransportStreamableHTTP
	case MCPServerTransportSSE, MCPServerTransportStreamableHTTP:
		// ok
	default:
		return fmt.Errorf("unsupported transport: %s (want sse or streamable-http)", m.Transport)
	}
	if m.Transport == MCPServerTransportSSE || m.Transport == MCPServerTransportStreamableHTTP {
		u := strings.TrimSpace(m.URL)
		if u == "" {
			return fmt.Errorf("url is required for %s transport", m.Transport)
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("url must be http:// or https://")
		}
		m.URL = u
	}
	if m.Timeout <= 0 {
		m.Timeout = 30
	}
	return nil
}

// sealSecrets 把 HeadersView（API 层视图）序列化成持久化列 Headers。Create/Update
// 前调用：请求体 bound 进 HeadersView（json:"headers"），原始 Headers 列不参与绑定。
func (m *MCPServer) sealSecrets() error {
	if m.HeadersView != nil {
		b, err := json.Marshal(m.HeadersView)
		if err != nil {
			return fmt.Errorf("marshal headers: %w", err)
		}
		m.Headers = string(b)
	}
	return nil
}

// Decorate 把持久化列 Headers 解析回 HeadersView。mask=true 时把取值替换成
// mcpSecretMask（仅保留键名），用于列表接口；detail 接口传 false 返回明文（与
// ai_skill.git_info.token 的处理口径一致：仅授权用户可见）。
func (m *MCPServer) Decorate(mask bool) error {
	headers, err := parseSecrets(m.Headers)
	if err != nil {
		return err
	}
	if mask {
		headers = maskSecretMap(headers)
	}
	m.HeadersView = headers
	return nil
}

func parseSecrets(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	return m, nil
}

func maskSecretMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v != "" {
			out[k] = mcpSecretMask
		} else {
			out[k] = ""
		}
	}
	return out
}

// ==================== CRUD ====================

func MCPServerGets(c *ctx.Context, search string) ([]*MCPServer, error) {
	var lst []*MCPServer
	session := DB(c).Order("id")
	if search != "" {
		session = session.Where("name like ? or description like ?", "%"+search+"%", "%"+search+"%")
	}
	if err := session.Find(&lst).Error; err != nil {
		return nil, err
	}
	for _, s := range lst {
		// 列表遮蔽取值，避免凭据出现在表格/网络响应里。
		if err := s.Decorate(true); err != nil {
			return nil, err
		}
	}
	return lst, nil
}

func MCPServerGet(c *ctx.Context, where string, args ...interface{}) (*MCPServer, error) {
	var obj MCPServer
	err := DB(c).Where(where, args...).First(&obj).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &obj, nil
}

func MCPServerGetById(c *ctx.Context, id int64) (*MCPServer, error) {
	return MCPServerGet(c, "id = ?", id)
}

func MCPServerGetByName(c *ctx.Context, name string) (*MCPServer, error) {
	return MCPServerGet(c, "name = ?", name)
}

// MCPServersByIds 返回 ids 中已启用（enabled=true）的服务器，供运行时装配工具源。
// 被禁用的服务器从活跃集合里静默剔除，而不是报错——保留运维意图。
func MCPServersByIds(c *ctx.Context, ids []int64) ([]*MCPServer, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var lst []*MCPServer
	err := DB(c).Where("id IN ? AND enabled = ?", ids, true).Order("id").Find(&lst).Error
	return lst, err
}

func (m *MCPServer) Create(c *ctx.Context, username string) error {
	exist, err := MCPServerGetByName(c, m.Name)
	if err != nil {
		return err
	}
	if exist != nil {
		return fmt.Errorf("mcp server name %s already exists", m.Name)
	}
	if err := m.sealSecrets(); err != nil {
		return err
	}
	now := time.Now().Unix()
	m.CreatedAt = now
	m.UpdatedAt = now
	m.CreatedBy = username
	m.UpdatedBy = username
	return Insert(c, m)
}

func (m *MCPServer) Update(c *ctx.Context, username string, ref MCPServer) error {
	if ref.Name != m.Name {
		exist, err := MCPServerGetByName(c, ref.Name)
		if err != nil {
			return err
		}
		if exist != nil {
			return fmt.Errorf("mcp server name %s already exists", ref.Name)
		}
	}
	if err := ref.sealSecrets(); err != nil {
		return err
	}
	ref.UpdatedAt = time.Now().Unix()
	ref.UpdatedBy = username
	return DB(c).Model(m).Select("name", "description", "transport", "url",
		"headers", "timeout", "enabled", "updated_at", "updated_by").Updates(ref).Error
}

func (m *MCPServer) Delete(c *ctx.Context) error {
	return DB(c).Where("id = ?", m.Id).Delete(&MCPServer{}).Error
}

// MCPServerMeta is the lightweight projection returned by the service list
// endpoint used by the Agent form's MCP multi-select. Only id+name are needed;
// secrets are never projected here.
type MCPServerMeta struct {
	Id   int64  `json:"id"`
	Name string `json:"name"`
}

// MCPServerMetaGets 供 Agent 表单的多选器使用：只回 id/name，不触达任何凭据列。
func MCPServerMetaGets(c *ctx.Context, search string) ([]*MCPServerMeta, error) {
	var lst []*MCPServerMeta
	session := DB(c).Model(&MCPServer{}).Select("id", "name").Order("id")
	if search != "" {
		session = session.Where("name like ?", "%"+search+"%")
	}
	err := session.Find(&lst).Error
	return lst, err
}
