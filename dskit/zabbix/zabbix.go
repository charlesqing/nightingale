// Package zabbix implements a minimal Zabbix JSON-RPC 2.0 client.
//
// It only knows how to talk to api_jsonrpc.php: authenticate (user.login),
// invoke methods with a cached token, transparently re-login once when the
// session is terminated. Zabbix domain types live in types.go; semantic
// translation into Nightingale models belongs to datasource/zabbix.
package zabbix

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ZabbixType = "zabbix"

	// tokenTTL is how long a cached auth token is reused before a proactive
	// re-login. Zabbix tokens do not expire on their own, but a server-side
	// session timeout (or restart) can invalidate them at any moment, so we
	// refresh periodically instead of waiting for a request to fail.
	tokenTTL = 15 * time.Minute

	// maxBodyBytes caps an upstream response body so a pathological payload
	// can't exhaust memory.
	maxBodyBytes = 64 << 20
)

// Error is a JSON-RPC error object returned by Zabbix.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *Error) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("zabbix rpc error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("zabbix rpc error %d: %s", e.Code, e.Message)
}

// isSessionError reports whether err means the auth token is no longer valid.
// Zabbix reports this as -32602 "Session terminated, please login again" or
// -32500 "Not authorised". -32602 is also a generic "Invalid params", so the
// retry costs one extra login in the worst case — acceptable.
func isSessionError(err error) bool {
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		return false
	}
	if rpcErr.Code == -32602 || rpcErr.Code == -32500 {
		return true
	}
	return strings.Contains(rpcErr.Message, "Session terminated") ||
		strings.Contains(rpcErr.Message, "Not authorised")
}

type ZabbixBasic struct {
	User   string `json:"zabbix.basic_user" mapstructure:"zabbix.basic_user"`
	Pass   string `json:"zabbix.basic_password" mapstructure:"zabbix.basic_password"`
	Enable bool   `json:"zabbix.is_encrypt" mapstructure:"zabbix.is_encrypt"`
}

type ZabbixTLS struct {
	SkipTlsVerify bool `json:"zabbix.tls.skip_tls_verify" mapstructure:"zabbix.tls.skip_tls_verify"`
}

type Zabbix struct {
	Addr         string            `json:"zabbix.addr" mapstructure:"zabbix.addr"` // full endpoint, e.g. https://host/zabbix/api_jsonrpc.php
	User         string            `json:"zabbix.user" mapstructure:"zabbix.user"`
	Password     string            `json:"zabbix.password" mapstructure:"zabbix.password"`
	Basic        ZabbixBasic       `json:"zabbix.basic" mapstructure:"zabbix.basic"`
	TLS          ZabbixTLS         `json:"zabbix.tls" mapstructure:"zabbix.tls"`
	Headers      map[string]string `json:"zabbix.headers" mapstructure:"zabbix.headers"`
	Timeout      int64             `json:"zabbix.timeout" mapstructure:"zabbix.timeout"` // milliseconds
	MaxQueryRows int               `json:"zabbix.max_query_rows" mapstructure:"zabbix.max_query_rows"`

	HTTPClient *http.Client `json:"-" mapstructure:"-"`

	mu        sync.Mutex
	authToken string
	tokenExp  time.Time
}

var rpcIDSeed int64

func nextRequestID() int64 {
	return atomic.AddInt64(&rpcIDSeed, 1)
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	Auth    string      `json:"auth,omitempty"`
	ID      int64       `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *Error          `json:"error"`
	ID      int64           `json:"id"`
}

// InitHTTPClient builds the shared HTTP client. Safe to call more than once.
func (z *Zabbix) InitHTTPClient() error {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: z.TLS.SkipTlsVerify,
		},
	}
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second}).DialContext

	timeout := time.Duration(z.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	z.HTTPClient = &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
	return nil
}

// token returns a cached auth token, logging in when the cache is empty or
// stale. The whole check-and-login runs under z.mu so concurrent callers
// issue at most one user.login.
func (z *Zabbix) token(ctx context.Context) (string, error) {
	z.mu.Lock()
	defer z.mu.Unlock()

	if z.authToken != "" && time.Now().Before(z.tokenExp) {
		return z.authToken, nil
	}

	var tok string
	// Zabbix user.login takes "username"/"password" (NOT "user" — the API
	// rejects unknown params with -32602). Verified against Zabbix 7.0.x.
	params := map[string]string{"username": z.User, "password": z.Password}
	if err := z.rpc(ctx, "", "user.login", params, &tok); err != nil {
		return "", fmt.Errorf("zabbix user.login failed: %w", err)
	}
	if tok == "" {
		return "", fmt.Errorf("zabbix user.login returned empty token")
	}

	z.authToken = tok
	z.tokenExp = time.Now().Add(tokenTTL)
	return tok, nil
}

func (z *Zabbix) invalidateToken() {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.authToken = ""
	z.tokenExp = time.Time{}
}

// do invokes method with a cached token. On a session-terminated error it
// re-logins and retries exactly once; any further failure is returned as-is.
func (z *Zabbix) do(ctx context.Context, method string, params, result interface{}) error {
	tok, err := z.token(ctx)
	if err != nil {
		return err
	}

	err = z.rpc(ctx, tok, method, params, result)
	if !isSessionError(err) {
		return err
	}

	z.invalidateToken()
	tok, err = z.token(ctx)
	if err != nil {
		return err
	}
	return z.rpc(ctx, tok, method, params, result)
}

// rpc performs one JSON-RPC call. result may be nil to ignore the payload.
func (z *Zabbix) rpc(ctx context.Context, tok, method string, params, result interface{}) error {
	if z.HTTPClient == nil {
		if err := z.InitHTTPClient(); err != nil {
			return err
		}
	}

	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		Auth:    tok,
		ID:      nextRequestID(),
	})
	if err != nil {
		return fmt.Errorf("marshal zabbix request failed: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, z.Addr, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build zabbix request failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json-rpc")

	if z.Basic.User != "" {
		httpReq.SetBasicAuth(z.Basic.User, z.Basic.Pass)
	}
	for k, v := range z.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := z.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("zabbix %s request failed: %w", method, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("read zabbix %s response failed: %w", method, err)
	}

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("zabbix %s failed: status=%d body=%s", method, resp.StatusCode, truncateForError(data))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return fmt.Errorf("decode zabbix %s response failed: %w, body=%s", method, err, truncateForError(data))
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(rpcResp.Result, result); err != nil {
		return fmt.Errorf("decode zabbix %s result failed: %w", method, err)
	}
	return nil
}

func truncateForError(body []byte) string {
	const maxLen = 512
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "...(truncated)"
}

// CheckHealth implements datasource.HealthChecker: the datasource settings
// page calls it for "test connectivity & save".
func (z *Zabbix) CheckHealth(ctx context.Context) error {
	var version string
	// apiinfo.version must be called WITHOUT the auth parameter — Zabbix
	// rejects it with -32602 otherwise. So bypass do() (which always attaches
	// a token) and call rpc directly with an empty auth.
	if err := z.rpc(ctx, "", "apiinfo.version", map[string]interface{}{}, &version); err != nil {
		return err
	}
	if version == "" {
		return fmt.Errorf("zabbix apiinfo.version returned empty version")
	}
	return nil
}

// APIVersion returns the Zabbix server version (e.g. "7.0.22"). It is an
// unauthenticated probe and must be called without the auth parameter.
func (z *Zabbix) APIVersion(ctx context.Context) (string, error) {
	var version string
	if err := z.rpc(ctx, "", "apiinfo.version", map[string]interface{}{}, &version); err != nil {
		return "", err
	}
	return version, nil
}
