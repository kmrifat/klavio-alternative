// Package mcpserver lets AI agents (Claude Code, Codex, OpenCode…) use the
// console through MCP at /mcp. An agent authenticates with an agent token
// (package agents) and acts as that token's person.
//
// Every tool is one admin API route, called in-process as that person, so
// the agent meets exactly the checks, roles and audit a person meets. On
// top of that, only the routes listed in tools.go exist for agents: nothing
// that deletes, blocks, unsubscribes, pauses or changes settings. And an
// agent never has a passkey confirmation, so whatever needs one (approving,
// scheduling a send, API keys, limits) stays with people; request_send asks
// a person to approve instead.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmrifat/klavio-alternative/api/internal/agents"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
)

// Path is where the server is mounted.
const Path = "/mcp"

// CallsPerMinute an agent token may make.
const CallsPerMinute = 120

// maxOutput keeps tool results small enough for a model's context.
const maxOutput = 60_000

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

type Config struct {
	Agents  *agents.Service
	Admin   http.Handler // the admin API's routes, without the session middleware
	Limiter Limiter
	Log     *slog.Logger
	Version string
	// ResourceMetadataURL, when set, is sent with 401s so OAuth clients
	// (claude.ai, Claude Desktop) can find where to sign in.
	ResourceMetadataURL string
}

const instructions = `This is the email marketing console. You act as the person whose agent token you use, with at most their role.

You can read brands, health, campaigns and their reports, templates, segments, contacts and approvals, and prepare work: draft campaigns, templates and segments, and add single contacts with their consent.

You can't send email yourself. When a draft is ready, call check_campaign, fix any problems it lists, then call request_send: a person approves it with their passkey in the console. Large or risky sends also need a second admin.

Brands are named by their slug (from list_brands). Edits need the version you read (get_campaign, get_template); if it changed, read again and retry.`

// New returns the /mcp handler.
func New(cfg Config) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "mail-console", Title: "Mail Console", Version: cfg.Version},
		&mcp.ServerOptions{Instructions: instructions, Logger: cfg.Log})
	h := &handler{cfg: cfg}
	h.register(s)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: cfg.Log})
	return sdkauth.RequireBearerToken(h.verify, &sdkauth.RequireBearerTokenOptions{ResourceMetadataURL: cfg.ResourceMetadataURL})(h.rateLimit(streamable))
}

type handler struct{ cfg Config }

// caller is who's calling and from where, carried in the token info.
type caller struct {
	p  *auth.Principal
	ip string
}

func (h *handler) verify(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
	p, err := h.cfg.Agents.Principal(ctx, token)
	if errors.Is(err, agents.ErrInvalid) {
		return nil, sdkauth.ErrInvalidToken
	}
	if err != nil {
		h.cfg.Log.ErrorContext(ctx, "mcp: checking agent token", "err", err)
		return nil, err
	}
	return &sdkauth.TokenInfo{UserID: p.User.ID.String(), Expiration: p.Agent.ExpiresAt,
		Extra: map[string]any{"caller": &caller{p: p, ip: httpx.ClientIP(r)}}}, nil
}

func (h *handler) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := callerFrom(sdkauth.TokenInfoFromContext(r.Context()))
		if c != nil && h.cfg.Limiter != nil {
			ok, err := h.cfg.Limiter.Allow(r.Context(), "mcp:"+c.p.Agent.ID.String(), CallsPerMinute, time.Minute)
			if err == nil && !ok {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "too many requests: at most 120 a minute per agent token", http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func callerFrom(ti *sdkauth.TokenInfo) *caller {
	if ti == nil {
		return nil
	}
	c, _ := ti.Extra["caller"].(*caller)
	return c
}

// apiError is an admin API error, shown to the agent as a failed tool call.
type apiError struct {
	Status  int
	Code    string
	Message string
	Detail  json.RawMessage // the rest of the error body (problems, reasons)
}

func (e *apiError) Error() string { return e.Message }

// call runs one admin API request in-process as the agent's person.
func (h *handler) call(ctx context.Context, c *caller, method, path string, body any) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(auth.WithPrincipal(ctx, c.p), method, path, rd)
	if err != nil {
		return nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "MCP agent: "+c.p.Agent.Label())
	req.RemoteAddr = net.JoinHostPort(c.ip, "0")
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	h.cfg.Admin.ServeHTTP(rec, req)
	out := rec.body.Bytes()
	if rec.status >= 400 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(out, &e)
		if e.Error.Message == "" {
			e.Error.Message = strings.TrimSpace(string(out))
		}
		return nil, &apiError{Status: rec.status, Code: e.Error.Code, Message: e.Error.Message, Detail: out}
	}
	if rec.status == http.StatusNoContent || len(out) == 0 {
		return json.RawMessage(`{"ok":true}`), nil
	}
	return out, nil
}

// recorder is a minimal ResponseWriter for in-process calls.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

// result turns an API answer into a tool result: JSON text, without the
// compiled email HTML (large, and the agent edits MJML anyway).
func result(raw json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	var ae *apiError
	if errors.As(err, &ae) {
		msg := fmt.Sprintf("Refused (%d %s): %s", ae.Status, ae.Code, ae.Message)
		switch ae.Code {
		case "agent_not_allowed", "step_up_required":
			msg += " This needs a person in the console."
		}
		if extra := detail(ae.Detail); extra != "" {
			msg += "\n" + extra
		}
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		v = strip(v)
		if b, err := json.MarshalIndent(v, "", " "); err == nil {
			raw = b
		}
	}
	text := string(raw)
	if len(text) > maxOutput {
		text = text[:maxOutput] + "\n… (cut: the answer was too long; ask for less, e.g. a smaller limit)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

// detail: an error body's extra fields (problems, reasons), if any.
func detail(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	delete(m, "error")
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// strip drops compiled HTML and plain-text bodies from answers.
func strip(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "html" || k == "text_body" {
				delete(x, k)
				continue
			}
			x[k] = strip(val)
		}
	case []any:
		for i := range x {
			x[i] = strip(x[i])
		}
	}
	return v
}
