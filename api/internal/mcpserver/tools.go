package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A route is the admin API call a tool makes.
type route struct {
	method, path string
	body         any
}

// seg escapes one path segment; an empty one is a caller mistake.
func seg(name, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", &apiError{Status: http.StatusBadRequest, Code: "missing_" + name, Message: name + " is required"}
	}
	return url.PathEscape(v), nil
}

func brandPath(brand string, rest ...string) (string, error) {
	b, err := seg("brand", brand)
	if err != nil {
		return "", err
	}
	return "/admin/v1/brands/" + b + strings.Join(rest, ""), nil
}

// add registers a tool that makes one admin API call.
func add[In any](h *handler, s *mcp.Server, t *mcp.Tool, readOnly bool, build func(In) (route, error)) {
	if t.Annotations == nil {
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: readOnly}
		if !readOnly {
			no := false
			t.Annotations.DestructiveHint = &no // prepare-only: drafts, never sends or deletes
		}
	}
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		var ti *sdkauth.TokenInfo
		if req.Extra != nil {
			ti = req.Extra.TokenInfo
		}
		c := callerFrom(ti)
		if c == nil {
			return nil, nil, errors.New("mcp: no agent token")
		}
		r, err := build(in)
		if err != nil {
			return result(nil, err)
		}
		if personalData[t.Name] {
			if err := h.contactQuota(ctx, c); err != nil {
				return result(nil, err)
			}
		}
		return result(h.call(ctx, c, r.method, r.path, r.body))
	})
}

// --- inputs ---

type noInput struct{}

type brandIn struct {
	Brand string `json:"brand" jsonschema:"the brand's slug, from list_brands"`
}

type campaignIn struct {
	Brand      string `json:"brand" jsonschema:"the brand's slug"`
	CampaignID string `json:"campaign_id" jsonschema:"the campaign's id"`
}

type draftIn struct {
	Brand         string `json:"brand" jsonschema:"the brand's slug"`
	Name          string `json:"name" jsonschema:"internal name, e.g. 'October newsletter'"`
	TopicID       string `json:"topic_id" jsonschema:"who it goes to: a topic id from list_topics (its subscribers)"`
	TemplateID    string `json:"template_id,omitempty" jsonschema:"the template to send, from list_templates; can be set later"`
	Subject       string `json:"subject" jsonschema:"the subject line; merge tags like {{first_name | there}} work"`
	Preheader     string `json:"preheader,omitempty" jsonschema:"preview text shown after the subject in the inbox"`
	IncludeAtRisk bool   `json:"include_at_risk,omitempty" jsonschema:"also send to contacts who haven't engaged in 3-6 months (needs approval)"`
}

type draftUpdateIn struct {
	draftIn
	CampaignID string `json:"campaign_id" jsonschema:"the campaign's id"`
	Version    int    `json:"version" jsonschema:"the version from get_campaign; every field is replaced, so send them all"`
}

type requestSendIn struct {
	Brand       string `json:"brand" jsonschema:"the brand's slug"`
	CampaignID  string `json:"campaign_id" jsonschema:"the draft to send"`
	ScheduledAt string `json:"scheduled_at,omitempty" jsonschema:"when to send, RFC 3339 (e.g. 2026-10-20T09:00:00Z); empty = as soon as approved"`
}

type templateIn struct {
	Brand      string `json:"brand" jsonschema:"the brand's slug"`
	TemplateID string `json:"template_id" jsonschema:"the template's id"`
}

type templateCreateIn struct {
	Brand string `json:"brand" jsonschema:"the brand's slug"`
	Name  string `json:"name" jsonschema:"the template's name"`
	MJML  string `json:"mjml" jsonschema:"the email body as MJML sections (the children of mj-body; the brand layout adds header, footer, address and unsubscribe link). Merge tags: {{first_name | there}}, {{last_name}}, {{brand_name}}"`
}

type templateUpdateIn struct {
	templateCreateIn
	TemplateID string `json:"template_id" jsonschema:"the template's id"`
	Version    int    `json:"version" jsonschema:"the version from get_template"`
}

type mjmlIn struct {
	Brand string `json:"brand" jsonschema:"the brand's slug"`
	MJML  string `json:"mjml" jsonschema:"MJML sections to check, as for create_template"`
}

type segmentIn struct {
	Brand     string `json:"brand" jsonschema:"the brand's slug"`
	SegmentID string `json:"segment_id" jsonschema:"the segment's id"`
}

type rulesDoc = map[string]any

type segmentCreateIn struct {
	Brand string   `json:"brand" jsonschema:"the brand's slug"`
	Name  string   `json:"name" jsonschema:"the segment's name"`
	Rules rulesDoc `json:"rules" jsonschema:"the rules: {match: all|any, groups: [{match: all|any, conditions: [{field: 'tier', op: 'in', value: ['active', 'warm']}]}]}; the fields and their ops come from segment_fields"`
}

type segmentUpdateIn struct {
	segmentCreateIn
	SegmentID string `json:"segment_id" jsonschema:"the segment's id"`
}

type countIn struct {
	Brand string   `json:"brand" jsonschema:"the brand's slug"`
	Rules rulesDoc `json:"rules" jsonschema:"the rules: {match: all|any, groups: [{match: all|any, conditions: [{field: 'tier', op: 'in', value: ['active', 'warm']}]}]}; the fields and their ops come from segment_fields"`
}

type contactsIn struct {
	Brand  string `json:"brand" jsonschema:"the brand's slug"`
	Query  string `json:"query,omitempty" jsonschema:"search in email and name"`
	Status string `json:"status,omitempty" jsonschema:"subscribed, pending_confirmation, quarantined or unsubscribed"`
	Tier   string `json:"tier,omitempty" jsonschema:"engagement tiers, comma-separated: active, warm, at_risk, sunset"`
	Tag    string `json:"tag,omitempty" jsonschema:"only contacts with this tag"`
	Limit  int    `json:"limit,omitempty" jsonschema:"at most this many (1-50, default 50)"`
	After  string `json:"after,omitempty" jsonschema:"the 'next' cursor from the previous page"`
}

type contactIn struct {
	Brand     string `json:"brand" jsonschema:"the brand's slug"`
	ContactID string `json:"contact_id" jsonschema:"the contact's id"`
}

type consentIn struct {
	Basis       string `json:"basis" jsonschema:"how they agreed: double_opt_in, single_opt_in or soft_opt_in (existing customer)"`
	Source      string `json:"source" jsonschema:"where and how they agreed, kept as proof, e.g. 'Signed up at the Portland store'"`
	ConsentedAt string `json:"consented_at,omitempty" jsonschema:"when, as a past date like 2026-03-12; empty = now"`
}

type addContactIn struct {
	Brand           string    `json:"brand" jsonschema:"the brand's slug"`
	Email           string    `json:"email" jsonschema:"their email address"`
	FirstName       string    `json:"first_name,omitempty"`
	LastName        string    `json:"last_name,omitempty"`
	Tags            []string  `json:"tags,omitempty"`
	TopicIDs        []string  `json:"topic_ids" jsonschema:"the topics they agreed to, from list_topics"`
	TrackingAllowed *bool     `json:"tracking_allowed,omitempty" jsonschema:"false if they didn't agree to click tracking (common in the EU)"`
	Consent         consentIn `json:"consent" jsonschema:"their consent: only add people who really agreed to these emails"`
}

type approvalsIn struct {
	View string `json:"view,omitempty" jsonschema:"mine (requests made as your person, including yours; default), decide (waiting for your person) or recent"`
}

func (h *handler) register(s *mcp.Server) {
	// --- who and what ---
	add(h, s, &mcp.Tool{Name: "whoami", Description: "Who you act as: their name, company role and role on each brand."}, true,
		func(noInput) (route, error) { return route{"GET", "/admin/v1/me", nil}, nil })
	add(h, s, &mcp.Tool{Name: "list_brands", Description: "The brands you can see, with their slug (used by every other tool), status and your role."}, true,
		func(noInput) (route, error) { return route{"GET", "/admin/v1/brands", nil}, nil })
	add(h, s, &mcp.Tool{Name: "get_brand_health", Description: "A brand's health score (0-100) and its parts, warm-up, DNS, blocklists, Gmail data and recent alerts and safety events."}, true,
		func(in brandIn) (route, error) {
			p, err := brandPath(in.Brand, "/health")
			return route{"GET", p, nil}, err
		})
	add(h, s, &mcp.Tool{Name: "get_safety_center", Description: "Company overview: every brand's health, the shared Amazon SES account and what needs attention. Owners and approvers only."}, true,
		func(noInput) (route, error) { return route{"GET", "/admin/v1/safety-center", nil}, nil })
	add(h, s, &mcp.Tool{Name: "list_topics", Description: "A brand's topics (lists people subscribe to). Campaigns go to one topic's subscribers."}, true,
		func(in brandIn) (route, error) {
			p, err := brandPath(in.Brand, "/topics")
			return route{"GET", p, nil}, err
		})

	// --- campaigns ---
	add(h, s, &mcp.Tool{Name: "list_campaigns", Description: "A brand's campaigns, newest first, with their state."}, true,
		func(in brandIn) (route, error) {
			p, err := brandPath(in.Brand, "/campaigns")
			return route{"GET", p, nil}, err
		})
	add(h, s, &mcp.Tool{Name: "get_campaign", Description: "One campaign: name, topic, template, subject, state and version (needed to edit it)."}, true,
		func(in campaignIn) (route, error) { return campaignRoute("GET", in.Brand, in.CampaignID, "", nil) })
	add(h, s, &mcp.Tool{Name: "get_campaign_report", Description: "A sent campaign's results: delivered, bounces, complaints, unsubscribes, human and bot clicks, top links, opens (estimated), by provider."}, true,
		func(in campaignIn) (route, error) {
			return campaignRoute("GET", in.Brand, in.CampaignID, "/report", nil)
		})
	add(h, s, &mcp.Tool{Name: "check_campaign", Description: "The pre-send check of a draft, changing nothing: who it would go to and who is left out (and why), problems that block sending, and what would need a second admin's approval."}, true,
		func(in campaignIn) (route, error) {
			return campaignRoute("GET", in.Brand, in.CampaignID, "/review", nil)
		})
	add(h, s, &mcp.Tool{Name: "create_campaign_draft", Description: "Create a draft campaign. Nothing is sent; use check_campaign and request_send when it's ready."}, false,
		func(in draftIn) (route, error) {
			p, err := brandPath(in.Brand, "/campaigns")
			return route{"POST", p, draftBody(in, 0)}, err
		})
	add(h, s, &mcp.Tool{Name: "update_campaign_draft", Description: "Edit a draft campaign. Every field is replaced: read it with get_campaign, change what you need, send it all with its version."}, false,
		func(in draftUpdateIn) (route, error) {
			return campaignRoute("PUT", in.Brand, in.CampaignID, "", draftBody(in.draftIn, in.Version))
		})
	add(h, s, &mcp.Tool{Name: "request_send", Description: "Ask a person to approve sending a draft. It runs the pre-send check and files an approval request; a person approves it with their passkey in the console, and large or risky sends also need a second admin. You can't send without this. Needs the sender role."}, false,
		func(in requestSendIn) (route, error) {
			body := map[string]any{}
			if t := strings.TrimSpace(in.ScheduledAt); t != "" {
				at, err := time.Parse(time.RFC3339, t)
				if err != nil {
					return route{}, &apiError{Status: http.StatusBadRequest, Code: "bad_time", Message: "scheduled_at must be RFC 3339, like 2026-10-20T09:00:00Z"}
				}
				body["scheduled_at"] = at
			}
			return campaignRoute("POST", in.Brand, in.CampaignID, "/agent-send", body)
		})

	// --- templates ---
	add(h, s, &mcp.Tool{Name: "list_templates", Description: "A brand's email templates."}, true,
		func(in brandIn) (route, error) {
			p, err := brandPath(in.Brand, "/templates")
			return route{"GET", p, nil}, err
		})
	add(h, s, &mcp.Tool{Name: "get_template", Description: "One template: its MJML, version, and any problems the checks found."}, true,
		func(in templateIn) (route, error) {
			return idRoute("GET", in.Brand, "/templates/", "template_id", in.TemplateID, nil)
		})
	add(h, s, &mcp.Tool{Name: "check_template", Description: "Compile and check MJML in the brand's layout without saving: returns problems to fix (links not allowed, missing alt text, too large…)."}, true,
		func(in mjmlIn) (route, error) {
			p, err := brandPath(in.Brand, "/templates/preview")
			return route{"POST", p, map[string]any{"mjml": in.MJML}}, err
		})
	add(h, s, &mcp.Tool{Name: "create_template", Description: "Save a new template. It's compiled and checked on save; problems come back with it."}, false,
		func(in templateCreateIn) (route, error) {
			p, err := brandPath(in.Brand, "/templates")
			return route{"POST", p, map[string]any{"name": in.Name, "mjml": in.MJML}}, err
		})
	add(h, s, &mcp.Tool{Name: "update_template", Description: "Change a template's name and MJML, with the version you read."}, false,
		func(in templateUpdateIn) (route, error) {
			return idRoute("PUT", in.Brand, "/templates/", "template_id", in.TemplateID, map[string]any{"name": in.Name, "mjml": in.MJML, "version": in.Version})
		})

	// --- segments ---
	add(h, s, &mcp.Tool{Name: "segment_fields", Description: "What segment rules can test: each field, its kind, operators and options."}, true,
		func(noInput) (route, error) { return route{"GET", "/admin/v1/segments/fields", nil}, nil })
	add(h, s, &mcp.Tool{Name: "list_segments", Description: "A brand's saved segments with their last counts."}, true,
		func(in brandIn) (route, error) {
			p, err := brandPath(in.Brand, "/segments")
			return route{"GET", p, nil}, err
		})
	add(h, s, &mcp.Tool{Name: "get_segment", Description: "One segment, its rules and a fresh count."}, true,
		func(in segmentIn) (route, error) {
			return idRoute("GET", in.Brand, "/segments/", "segment_id", in.SegmentID, nil)
		})
	add(h, s, &mcp.Tool{Name: "count_segment", Description: "Count who matches some rules, without saving them."}, true,
		func(in countIn) (route, error) {
			p, err := brandPath(in.Brand, "/segments/preview")
			return route{"POST", p, map[string]any{"rules": in.Rules}}, err
		})
	add(h, s, &mcp.Tool{Name: "create_segment", Description: "Save a segment (a named set of rules)."}, false,
		func(in segmentCreateIn) (route, error) {
			p, err := brandPath(in.Brand, "/segments")
			return route{"POST", p, map[string]any{"name": in.Name, "rules": in.Rules}}, err
		})
	add(h, s, &mcp.Tool{Name: "update_segment", Description: "Change a segment's name and rules."}, false,
		func(in segmentUpdateIn) (route, error) {
			return idRoute("PUT", in.Brand, "/segments/", "segment_id", in.SegmentID, map[string]any{"name": in.Name, "rules": in.Rules})
		})

	// --- contacts ---
	add(h, s, &mcp.Tool{Name: "contact_stats", Description: "A brand's contact counts by status and engagement tier."}, true,
		func(in brandIn) (route, error) {
			p, err := brandPath(in.Brand, "/contacts/stats")
			return route{"GET", p, nil}, err
		})
	add(h, s, &mcp.Tool{Name: "search_contacts", Description: "Find a brand's contacts (personal data: needs the editor role). At most 50 a page, pages with 'after'; contact reads are limited to 60 an hour, so search narrowly."}, true,
		func(in contactsIn) (route, error) {
			p, err := brandPath(in.Brand, "/contacts")
			q := url.Values{}
			for k, v := range map[string]string{"q": in.Query, "status": in.Status, "tier": in.Tier, "tag": in.Tag, "after": in.After} {
				if v = strings.TrimSpace(v); v != "" {
					q.Set(k, v)
				}
			}
			q.Set("limit", strconv.Itoa(MaxContactsPerPage))
			if in.Limit > 0 && in.Limit < MaxContactsPerPage {
				q.Set("limit", strconv.Itoa(in.Limit))
			}
			if len(q) > 0 {
				p += "?" + q.Encode()
			}
			return route{"GET", p, nil}, err
		})
	add(h, s, &mcp.Tool{Name: "get_contact", Description: "One contact: status, topics, tags, engagement and their consent history."}, true,
		func(in contactIn) (route, error) {
			return idRoute("GET", in.Brand, "/contacts/", "contact_id", in.ContactID, nil)
		})
	add(h, s, &mcp.Tool{Name: "add_contact", Description: "Add one person who agreed to the brand's email, with proof of how they agreed. Never add people who didn't agree. Blocked addresses (bounced, complained, erased) are refused. Needs the brand admin role."}, false,
		func(in addContactIn) (route, error) {
			p, err := brandPath(in.Brand, "/contacts")
			body := map[string]any{"email": in.Email, "first_name": in.FirstName, "last_name": in.LastName, "tags": in.Tags,
				"topic_ids": in.TopicIDs, "consent": map[string]any{"basis": in.Consent.Basis, "source": in.Consent.Source, "consented_at": in.Consent.ConsentedAt}}
			if in.TrackingAllowed != nil {
				body["tracking_allowed"] = *in.TrackingAllowed
			}
			return route{"POST", p, body}, err
		})

	// --- approvals ---
	add(h, s, &mcp.Tool{Name: "list_approvals", Description: "Approval requests: by default the ones made as your person (including your request_send requests) and what happened to them."}, true,
		func(in approvalsIn) (route, error) {
			v := in.View
			if v == "" {
				v = "mine"
			}
			return route{"GET", "/admin/v1/approvals?view=" + url.QueryEscape(v), nil}, nil
		})
}

func draftBody(in draftIn, version int) map[string]any {
	b := map[string]any{"name": in.Name, "topic_id": in.TopicID, "subject": in.Subject, "preheader": in.Preheader,
		"include_at_risk": in.IncludeAtRisk, "version": version}
	if t := strings.TrimSpace(in.TemplateID); t != "" {
		b["template_id"] = t
	}
	return b
}

func campaignRoute(method, brand, id, rest string, body any) (route, error) {
	return idRoute(method, brand, "/campaigns/", "campaign_id", id, body, rest)
}

func idRoute(method, brand, coll, name, id string, body any, rest ...string) (route, error) {
	i, err := seg(name, id)
	if err != nil {
		return route{}, err
	}
	p, err := brandPath(brand, coll, i, strings.Join(rest, ""))
	return route{method, p, body}, err
}
