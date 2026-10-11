package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// bearer adds an agent token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// agentSession connects an MCP client with an agent token, as Claude Code,
// Codex or OpenCode would.
func agentSession(t *testing.T, base, token string) (*mcp.ClientSession, error) {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1,
	}, nil)
	if err == nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

// callTool calls a tool and returns its text and whether it failed.
func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

func mustTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	text, failed := callTool(t, s, name, args)
	if failed {
		t.Fatalf("%s failed: %s", name, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: not JSON: %s", name, text)
	}
	return out
}

func TestMCPAgent(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	for i := range 5 {
		w.contact(fmt.Sprintf("reader%d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	base := w.owner.base

	// The owner makes a token for their agent (with their passkey).
	if r := w.owner.do("POST", "/admin/v1/agent-tokens", map[string]any{"name": "Claude Code", "expires_in_days": 91}); r.errCode() != "invalid_expiry" {
		t.Fatalf("91 days: %s", r.raw)
	}
	w.owner.must(201, w.owner.do("POST", "/admin/v1/agent-tokens", map[string]any{"name": "Longest", "expires_in_days": 90}))
	made := w.owner.must(201, w.owner.do("POST", "/admin/v1/agent-tokens", map[string]any{"name": "Claude Code", "expires_in_days": 30}))
	secret := made.body["secret"].(string)
	tok := made.body["token"].(map[string]any)
	label := "Claude Code (" + tok["prefix"].(string) + ")"
	if !strings.HasPrefix(secret, "kag_") || tok["state"] != "active" {
		t.Fatalf("token: %s", made.raw)
	}

	// No token, or a wrong one: no access.
	if _, err := agentSession(t, base, "kag_nope_nope"); err == nil {
		t.Fatal("a wrong token connected")
	}
	agent, err := agentSession(t, base, secret)
	if err != nil {
		t.Fatal(err)
	}

	// Only read and prepare tools exist: nothing that sends, deletes, blocks or approves.
	tools, err := agent.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"list_brands", "check_campaign", "create_campaign_draft", "request_send", "search_contacts", "add_contact"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %s in %v", want, names)
		}
	}
	for _, name := range names {
		for _, banned := range []string{"delete", "suppress", "unsubscribe", "approve", "schedule", "pause", "cancel", "erase"} {
			if strings.Contains(name, banned) {
				t.Errorf("tool %s shouldn't exist", name)
			}
		}
	}

	brands := mustTool(t, agent, "list_brands", nil)
	if bs := brands["brands"].([]any); len(bs) != 1 || bs[0].(map[string]any)["slug"] != "brightside" {
		t.Fatalf("brands: %v", brands)
	}

	// Prepare a draft and check it, as an agent would.
	c := mustTool(t, agent, "create_campaign_draft", map[string]any{"brand": "brightside", "name": "Agent's autumn note",
		"topic_id": w.topic, "template_id": w.tpl, "subject": "Hello {{first_name | there}}"})["campaign"].(map[string]any)
	id := c["id"].(string)
	if _, has := c["html"]; has {
		t.Error("compiled HTML should be left out of answers")
	}
	if rv := mustTool(t, agent, "check_campaign", map[string]any{"brand": "brightside", "campaign_id": id}); rv["confirm_text"] != "5" {
		t.Fatalf("check: %v", rv)
	}

	// It can't send: request_send files an approval instead.
	req := mustTool(t, agent, "request_send", map[string]any{"brand": "brightside", "campaign_id": id})
	ap := req["approval"].(map[string]any)
	if !strings.Contains(ap["summary"].(string), "to 5 people") || !strings.Contains(ap["summary"].(string), label) {
		t.Fatalf("request: %v", req)
	}
	if text, failed := callTool(t, agent, "request_send", map[string]any{"brand": "brightside", "campaign_id": id}); !failed || !strings.Contains(text, "already_requested") {
		t.Fatalf("second request: %s", text)
	}
	// People use the normal Send button, not the agents' route.
	if r := w.owner.do("POST", campaignBase+"/campaigns/"+id+"/agent-send", map[string]any{}); r.errCode() != "agents_only" {
		t.Fatalf("person on agent route: %s", r.raw)
	}

	// The token's own person may approve it (with a passkey); it's then scheduled as if they'd confirmed it.
	waiting := w.owner.must(200, w.owner.do("GET", "/admin/v1/approvals?view=decide", nil)).body["approvals"].([]any)
	if len(waiting) != 1 || waiting[0].(map[string]any)["can_decide"] != true || waiting[0].(map[string]any)["kind"] != "agent_send" {
		t.Fatalf("waiting: %v", waiting)
	}
	w.owner.must(200, w.owner.stepUp())
	done := w.owner.must(200, w.owner.do("POST", "/admin/v1/approvals/"+ap["id"].(string)+"/approve", map[string]any{}))
	if done.body["status"] != "approved" {
		t.Fatalf("approve: %s", done.raw)
	}
	got, err := w.a.Store.CampaignByID(ctx, w.brand.ID, uuid.MustParse(id))
	if err != nil || got.State != store.CampaignScheduled || got.AudienceCount != 5 {
		t.Fatalf("after approval: %v %v %v", got.State, got.AudienceCount, err)
	}

	// An edit after the request makes it stale: approving then fails, on the record.
	c2 := mustTool(t, agent, "create_campaign_draft", map[string]any{"brand": "brightside", "name": "Second note",
		"topic_id": w.topic, "template_id": w.tpl, "subject": "Again"})["campaign"].(map[string]any)
	ap2 := mustTool(t, agent, "request_send", map[string]any{"brand": "brightside", "campaign_id": c2["id"]})["approval"].(map[string]any)
	mustTool(t, agent, "update_campaign_draft", map[string]any{"brand": "brightside", "campaign_id": c2["id"], "version": 1,
		"name": "Second note", "topic_id": w.topic, "template_id": w.tpl, "subject": "Again, changed"})
	stale := w.owner.must(200, w.owner.do("POST", "/admin/v1/approvals/"+ap2["id"].(string)+"/approve", map[string]any{}))
	if a := stale.body; a["status"] != "rejected" || !strings.Contains(a["decision_note"].(string), "changed") {
		t.Fatalf("stale approval: %s", stale.raw)
	}

	// The audit log says which agent did what.
	var n int
	if err := w.a.Store.QueryRowForTest(ctx, `SELECT count(*) FROM audit_log WHERE via_agent = $1 AND action IN ('campaign.created', 'approval.requested')`, &n, label); err != nil || n != 4 {
		t.Fatalf("audit via agent: %d %v", n, err)
	}

	// A viewer's agent can read but not prepare.
	inv := w.owner.must(201, w.owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "vic@acme.test", "name": "Vic Viewer", "company_role": "member",
		"brand_roles": map[string]any{w.brand.ID.String(): "viewer"},
	}))
	vic := newBrowser(t, base)
	vic.must(200, vic.acceptInvite(inv.body["invite_url"].(string)))
	vicSecret := vic.must(201, vic.do("POST", "/admin/v1/agent-tokens", map[string]any{"name": "Codex", "expires_in_days": 7})).body["secret"].(string)
	vicAgent, err := agentSession(t, base, vicSecret)
	if err != nil {
		t.Fatal(err)
	}
	mustTool(t, vicAgent, "list_campaigns", map[string]any{"brand": "brightside"})
	if text, failed := callTool(t, vicAgent, "create_campaign_draft", map[string]any{"brand": "brightside", "name": "x",
		"topic_id": w.topic, "subject": "x"}); !failed || !strings.Contains(text, "forbidden") {
		t.Fatalf("viewer's agent drafted: %s", text)
	}
	// Vic's own tokens only; Olivia's (2) aren't visible to Vic.
	if ts := vic.must(200, vic.do("GET", "/admin/v1/agent-tokens", nil)).body["tokens"].([]any); len(ts) != 1 {
		t.Fatalf("vic sees %d tokens", len(ts))
	}

	// A company approver's agent reads campaigns (approvers see every brand)
	// but can't draft or ask to send.
	apInv := w.owner.must(201, w.owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "ada@acme.test", "name": "Ada Approver", "company_role": "approver",
	}))
	ada := newBrowser(t, base)
	ada.must(200, ada.acceptInvite(apInv.body["invite_url"].(string)))
	adaSecret := ada.must(201, ada.do("POST", "/admin/v1/agent-tokens", map[string]any{"name": "Claude", "expires_in_days": 7})).body["secret"].(string)
	adaAgent, err := agentSession(t, base, adaSecret)
	if err != nil {
		t.Fatal(err)
	}
	mustTool(t, adaAgent, "list_campaigns", map[string]any{"brand": "brightside"})
	mustTool(t, adaAgent, "get_campaign", map[string]any{"brand": "brightside", "campaign_id": id})
	mustTool(t, adaAgent, "get_campaign_report", map[string]any{"brand": "brightside", "campaign_id": id})
	mustTool(t, adaAgent, "check_campaign", map[string]any{"brand": "brightside", "campaign_id": id})
	if text, failed := callTool(t, adaAgent, "create_campaign_draft", map[string]any{"brand": "brightside", "name": "x",
		"topic_id": w.topic, "subject": "x"}); !failed || !strings.Contains(text, "forbidden") {
		t.Fatalf("approver's agent drafted: %s", text)
	}
	if text, failed := callTool(t, adaAgent, "request_send", map[string]any{"brand": "brightside", "campaign_id": id}); !failed || !strings.Contains(text, "forbidden") {
		t.Fatalf("approver's agent requested a send: %s", text)
	}

	// Revoked: the agent is locked out at once.
	w.owner.must(200, w.owner.do("POST", "/admin/v1/agent-tokens/"+tok["id"].(string)+"/revoke", nil))
	if _, err := agent.CallTool(ctx, &mcp.CallToolParams{Name: "list_brands"}); err == nil {
		t.Fatal("revoked token still works")
	}
}
