package brakes

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

func TestCampaignRules(t *testing.T) {
	l := CampaignLimits{BouncePct: 2, ComplaintPct: 0.08, UnsubPct: 1}
	if pause, r := Campaign(Counts{Delivered: 990, Bounces: 10, Complaints: 1, Unsubscribes: 5}, l); pause {
		t.Fatalf("healthy campaign paused: %v", r)
	}
	if pause, r := Campaign(Counts{Delivered: 970, Bounces: 30}, l); !pause || len(r) != 1 {
		t.Fatalf("3%% bounces: %v %v", pause, r)
	}
	// One complaint in 500 is 0.2%, but the rule needs at least two.
	if pause, _ := Campaign(Counts{Delivered: 500, Complaints: 1}, l); pause {
		t.Fatal("paused on a single complaint")
	}
	if pause, _ := Campaign(Counts{Delivered: 500, Complaints: 2}, l); !pause {
		t.Fatal("two complaints in 500 should pause")
	}
	// Too small to judge: 3 bounces in 20.
	if pause, _ := Campaign(Counts{Delivered: 17, Bounces: 3}, l); pause {
		t.Fatal("paused on a tiny sample")
	}
	if pause, _ := Campaign(Counts{Delivered: 1000, Unsubscribes: 10}, l); !pause {
		t.Fatal("1% unsubscribes should pause")
	}
}

func TestBrandRules(t *testing.T) {
	l := BrandLimits{PauseBouncePct: 3, PauseComplaintPct: 0.08, GmailSpamPct: 0.2}
	cases := []struct {
		c     Counts
		gmail *float64
		want  State
	}{
		{Counts{Delivered: 1000, Bounces: 5}, nil, Healthy},
		{Counts{Delivered: 990, Bounces: 10}, nil, Watch},
		{Counts{Delivered: 975, Bounces: 25}, nil, Slowed},
		{Counts{Delivered: 960, Bounces: 40}, nil, Paused},
		{Counts{Delivered: 10000, Complaints: 6}, nil, Watch},
		{Counts{Delivered: 10000, Complaints: 7}, nil, Slowed},
		{Counts{Delivered: 10000, Complaints: 8}, nil, Paused},
		{Counts{Delivered: 100, Bounces: 20}, nil, Healthy},               // too few to judge
		{Counts{Delivered: 1000}, ptr(0.0025), Paused},                    // Gmail says so
		{Counts{Delivered: 975, Bounces: 25, Complaints: 8}, nil, Paused}, // the worst signal wins
	}
	for i, tc := range cases {
		if got, r := Brand(tc.c, tc.gmail, l); got != tc.want {
			t.Errorf("case %d: %s (%v), want %s", i, got, r, tc.want)
		}
	}
}

func ptr(f float64) *float64 { return &f }

func TestAccountRules(t *testing.T) {
	if stop, _ := Account(Counts{Delivered: 5000, Complaints: 3}, 0.08, true, "HEALTHY"); stop {
		t.Fatal("0.06% stopped the account")
	}
	if stop, _ := Account(Counts{Delivered: 5000, Complaints: 4}, 0.08, true, "HEALTHY"); !stop {
		t.Fatal("0.08% didn't stop the account")
	}
	if stop, _ := Account(Counts{}, 0.08, false, ""); !stop {
		t.Fatal("SES sending off didn't stop the account")
	}
	if stop, _ := Account(Counts{}, 0.08, true, "PROBATION"); stop {
		t.Fatal("probation alone stops nothing (it's an alert)")
	}
}

type fixture struct {
	st    *store.Store
	svc   *Service
	fake  *ses.Fake
	brand store.Brand
	n     int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st := store.New(testdb.New(t))
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test", SESConfigurationSet: "brand-acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ExecForTest(ctx, `UPDATE brands SET status = 'ready'`); err != nil {
		t.Fatal(err)
	}
	fake := &ses.Fake{}
	return &fixture{st: st, svc: NewService(st, fake, slog.New(slog.NewTextHandler(io.Discard, nil))), fake: fake, brand: b}
}

// events adds n SES events of a type for the brand, `ago` in the past.
func (f *fixture) events(t *testing.T, typ, subtype string, n int, ago string) {
	t.Helper()
	f.n++
	if err := f.st.ExecForTest(context.Background(), fmt.Sprintf(`
		INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, email_hash, occurred_at)
		SELECT 'ev-%d-' || g, $1, $2, $3, 'm-%d-' || g, decode(md5('%d-' || g), 'hex'), now() - interval '%s'
		FROM generate_series(1, %d) g`, f.n, f.n, f.n, ago, n), typ, subtype, f.brand.ID); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) state(t *testing.T) (string, store.BrandStatus) {
	t.Helper()
	b, err := f.st.BrandByID(context.Background(), f.brand.ID)
	if err != nil {
		t.Fatal(err)
	}
	return b.BrakeState, b.Status
}

func TestBrandBrakeOnlyTightensOnItsOwn(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.events(t, "delivery", "", 975, "1 hour")
	f.events(t, "bounce", "permanent/General", 25, "1 hour")
	if err := f.svc.EvaluateBrand(ctx, f.brand.ID); err != nil {
		t.Fatal(err)
	}
	if s, st := f.state(t); s != "slowed" || st != store.BrandReady {
		t.Fatalf("2.5%% bounces: %s %s", s, st)
	}
	g, _ := f.svc.CheckGate(ctx, nil, f.brand.ID)
	if !g.Allowed || g.RateFactor != 0.5 || !g.NeedsApproval {
		t.Fatalf("slowed gate: %+v", g)
	}
	// Rates recover (the bounces age out), and then the brand stops sending
	// (too few emails to judge): neither loosens the brake. That takes a
	// second admin (Recovery).
	if err := f.st.ExecForTest(ctx, `UPDATE email_events SET occurred_at = now() - interval '8 days' WHERE type = 'bounce'`); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.EvaluateBrand(ctx, f.brand.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.state(t); s != "slowed" {
		t.Fatalf("slowed loosened on its own: %s", s)
	}
	if err := f.st.ExecForTest(ctx, `UPDATE email_events SET occurred_at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.EvaluateBrand(ctx, f.brand.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.state(t); s != "slowed" {
		t.Fatalf("an empty week loosened the brake: %s", s)
	}
	f.events(t, "delivery", "", 975, "1 hour")
	// Then complaints: 8 in 975 delivered (0.8%) → paused, brand stopped.
	f.events(t, "complaint", "abuse", 8, "1 hour")
	if err := f.svc.EvaluateBrand(ctx, f.brand.ID); err != nil {
		t.Fatal(err)
	}
	if s, st := f.state(t); s != "paused" || st != store.BrandPaused {
		t.Fatalf("complaints: %s %s", s, st)
	}
	if g, _ := f.svc.CheckGate(ctx, nil, f.brand.ID); g.Allowed {
		t.Fatal("paused brand allowed to send")
	}
	// Paused stays paused even when the rates look fine: recovery needs people.
	if err := f.st.ExecForTest(ctx, `DELETE FROM email_events WHERE type = 'complaint'`); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.EvaluateBrand(ctx, f.brand.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.state(t); s != "paused" {
		t.Fatalf("paused brand relaxed on its own: %s", s)
	}
	evs, _ := f.st.SafetyEvents(ctx, f.brand.ID, 10)
	if len(evs) != 2 || evs[0].Action != "paused" || evs[1].Action != "slowed" {
		t.Fatalf("safety events: %+v", evs)
	}
	alerts, _ := f.st.ListAlerts(ctx, true, 10)
	if len(alerts) != 2 || alerts[0].Kind != "brand_paused" || alerts[0].Severity != "critical" {
		t.Fatalf("alerts: %+v", alerts)
	}
	// The health score gets real rates.
	rep, err := f.svc.Reputation(ctx, f.brand.ID)
	if err != nil || rep.Sent7d != 975 || rep.BounceRate7d != 0 {
		t.Fatalf("reputation: %+v %v", rep, err)
	}
}

func TestAccountBrakeStopsEveryone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.events(t, "delivery", "", 2000, "2 hours")
	f.events(t, "complaint", "abuse", 2, "1 hour") // 0.1%
	if err := f.svc.EvaluateAccount(ctx); err != nil {
		t.Fatal(err)
	}
	g, err := f.svc.CheckGate(ctx, nil, f.brand.ID)
	if err != nil || g.Allowed {
		t.Fatalf("gate after the account brake: %+v %v", g, err)
	}
	// Idempotent: a second run adds nothing.
	if err := f.svc.EvaluateAccount(ctx); err != nil {
		t.Fatal(err)
	}
	evs, _ := f.st.SafetyEvents(ctx, uuid.New(), 10)
	if len(evs) != 1 || evs[0].Scope != "account" || evs[0].Action != "stopped" {
		t.Fatalf("account events: %+v", evs)
	}

	// Amazon turning sending off stops it too.
	g2 := setup(t)
	g2.fake.SetAccount(ses.Account{SendingEnabled: false, EnforcementStatus: "SHUTDOWN"})
	if err := g2.svc.EvaluateAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if g, _ := g2.svc.CheckGate(ctx, nil, g2.brand.ID); g.Allowed {
		t.Fatal("SES shutdown didn't stop sending")
	}
}
