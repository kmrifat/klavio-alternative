package segments

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

func cond(field, op string, value any) Condition {
	raw, _ := json.Marshal(value)
	if value == nil {
		raw = nil
	}
	return Condition{Field: field, Op: op, Value: raw}
}

func one(c ...Condition) Rules {
	return Rules{Match: "all", Groups: []Group{{Match: "all", Conditions: c}}}
}

func TestCompileRejects(t *testing.T) {
	bad := map[string]Rules{
		"unknown field":  one(cond("password", "equals", "x")),
		"sql in field":   one(cond("email; DROP TABLE contacts", "equals", "x")),
		"op not allowed": one(cond("tier", "contains", "act")),
		"enum value":     one(cond("tier", "is", "vip'); DROP TABLE contacts; --")),
		"bad country":    one(cond("country", "is", "Germany")),
		"days range":     one(cond("last_click_at", "within_days", 0)),
		"days type":      one(cond("last_click_at", "within_days", "30 days")),
		"bad date":       one(cond("created_at", "before", "31/01/2026")),
		"topic not uuid": one(cond("topic", "subscribed", "Newsletter")),
		"empty group":    {Groups: []Group{{}}},
		"bad match":      {Match: "xor", Groups: []Group{{Conditions: []Condition{cond("tier", "is", "active")}}}},
		"empty text":     one(cond("email", "contains", "  ")),
		"too long":       one(cond("email", "contains", strings.Repeat("a", 201))),
		"list too long":  one(cond("country", "in", make([]string, 51))),
	}
	for name, r := range bad {
		if _, err := Compile(r, 2); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Values only ever travel as parameters.
	c, err := Compile(one(cond("email", "contains", "x' OR '1'='1"), cond("tag", "has", "a\"); --")), 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.SQL, "OR '1'") || strings.Contains(c.SQL, "--") || len(c.Args) != 2 {
		t.Fatalf("value leaked into SQL: %s %v", c.SQL, c.Args)
	}
	if !strings.Contains(c.SQL, "$2") || !strings.Contains(c.SQL, "$3") {
		t.Fatalf("parameters should start at $2: %s", c.SQL)
	}
	if c, _ := Compile(Rules{}, 2); c.SQL != "true" {
		t.Fatalf("no rules = everyone: %q", c.SQL)
	}
}

func TestSegmentsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	st := store.New(testdb.New(t))
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test",
		SESConfigurationSet: "acme", SESIdentityStatus: "SUCCESS", Status: store.BrandReady})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := st.CreateTopic(ctx, b.ID, "Deals")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ago := func(days int) *time.Time { t := now.AddDate(0, 0, -days); return &t }
	add := func(email, country string, status store.ContactStatus, clicked *time.Time, tags ...string) uuid.UUID {
		id, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: email, FirstName: strings.Split(email, "@")[0],
			Status: status, ConsentBasis: "double_opt_in", ConsentedAt: ago(400), LastClickAt: clicked,
			TrackingAllowed: true, Tags: tags, Country: country})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ann := add("ann@example.com", "DE", store.ContactSubscribed, ago(5), "vip")
	add("bob@example.org", "de", store.ContactSubscribed, ago(60))
	add("cid@example.com", "FR", store.ContactSubscribed, ago(120), "vip")
	add("dee@example.com", "US", store.ContactSubscribed, nil)
	add("eve@example.com", "DE", store.ContactQuarantined, ago(2))
	if err := st.AddContactTopics(ctx, ann, []uuid.UUID{topic.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.Suppress(ctx, "cid@example.com", store.SuppressComplaint, &b.ID, "", nil); err != nil {
		t.Fatal(err)
	}

	count := func(r Rules) store.SegmentCounts {
		t.Helper()
		c, err := Count(ctx, st, b.ID, r)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		name     string
		rules    Rules
		matching int
	}{
		{"everyone", Rules{}, 5},
		{"tier in", one(cond("tier", "in", []string{"active", "warm"})), 3},
		{"tier is not sunset", one(cond("tier", "is_not", "sunset")), 4},
		{"country case-insensitive", one(cond("country", "is", "de")), 3},
		{"country in", one(cond("country", "in", []string{"FR", "us"})), 2},
		{"tag", one(cond("tag", "has", "vip")), 2},
		{"no tag", one(cond("tag", "has_not", "vip")), 3},
		{"topic", one(cond("topic", "subscribed", topic.ID.String())), 1},
		{"email domain", one(cond("email", "ends_with", "@EXAMPLE.org")), 1},
		{"like wildcards are literal", one(cond("email", "contains", "%")), 0},
		{"clicked within 30 days", one(cond("last_click_at", "within_days", 30)), 2},
		{"no click in 90 days (incl. never)", one(cond("last_click_at", "older_than_days", 90)), 2},
		{"never clicked", one(cond("last_click_at", "is_empty", nil)), 1},
		// Rule dates are UTC days, like the database's.
		{"added after yesterday", one(cond("created_at", "after", now.UTC().AddDate(0, 0, -1).Format("2006-01-02"))), 5},
		{"opted in before", one(cond("consented_at", "before", now.UTC().Format("2006-01-02"))), 5},
		{"status", one(cond("status", "is", "quarantined")), 1},
		{"first name", one(cond("first_name", "starts_with", "A")), 1},
		{"all of", one(cond("country", "is", "DE"), cond("tag", "has", "vip")), 1},
		{"any of", Rules{Groups: []Group{{Match: "any", Conditions: []Condition{cond("country", "is", "FR"), cond("country", "is", "US")}}}}, 2},
		{"groups any", Rules{Match: "any", Groups: []Group{
			{Conditions: []Condition{cond("tag", "has", "vip")}},
			{Conditions: []Condition{cond("status", "is", "quarantined")}},
		}}, 3},
	}
	for _, tc := range cases {
		if got := count(tc.rules); got.Matching != tc.matching {
			t.Errorf("%s: %d matching, want %d", tc.name, got.Matching, tc.matching)
		}
	}
	// Mailable: subscribed, not suppressed (cid), not sunset (dee never clicked, opted in 400 days ago).
	all := count(Rules{})
	if all.Mailable != 2 || all.Tiers[store.TierActive] != 1 || all.Tiers[store.TierWarm] != 1 || all.Tiers[store.TierSunset] != 1 {
		t.Fatalf("mailable: %+v", all)
	}

	// Every field and operator compiles to SQL Postgres accepts.
	for _, f := range Fields {
		for _, op := range f.Ops {
			var v any
			switch {
			case op == "is_empty" || op == "is_not_empty":
			case op == "in" || op == "not_in":
				v = []string{"DE"}
				if f.Kind == kEnum {
					v = []string{f.Options[0]}
				}
			case op == "within_days" || op == "older_than_days":
				v = 7
			case op == "before" || op == "after":
				v = "2026-01-31"
			case f.Kind == kEnum:
				v = f.Options[0]
			case f.Kind == kTopic:
				v = topic.ID.String()
			case f.Kind == kCountry:
				v = "DE"
			default:
				v = "x"
			}
			if _, err := Count(ctx, st, b.ID, one(cond(f.Key, op, v))); err != nil {
				t.Errorf("%s %s: %v", f.Key, op, err)
			}
		}
	}
}
