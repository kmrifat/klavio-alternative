package health

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/blocklist"
	"github.com/kmrifat/klavio-alternative/api/internal/postmaster"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
	"github.com/kmrifat/klavio-alternative/api/internal/warmup"
)

func okDNS() []store.DNSRecord {
	return []store.DNSRecord{
		{Purpose: "dkim", Status: OK}, {Purpose: "dkim", Status: OK}, {Purpose: "dkim", Status: OK},
		{Purpose: "mail_from_mx", Status: OK}, {Purpose: "mail_from_spf", Status: OK}, {Purpose: "dmarc", Status: OK},
	}
}

func comp(r Result, key string) Component {
	for _, c := range r.Components {
		if c.Key == key {
			return c
		}
	}
	panic(key)
}

func TestComputeNewBrand(t *testing.T) {
	// Only authentication has data: the score is authentication alone.
	r := Compute(Inputs{Brand: store.Brand{SESIdentityStatus: "SUCCESS"}, DNS: okDNS()})
	if r.Score == nil || *r.Score != 100 || r.Level != "healthy" || r.Weakest != "" {
		t.Fatalf("got %+v", r)
	}
	if comp(r, "engagement").Score != nil || comp(r, "reputation").Score != nil {
		t.Fatal("parts without data should have no score")
	}
	// SES not verified caps authentication.
	r = Compute(Inputs{Brand: store.Brand{SESIdentityStatus: "PENDING"}, DNS: okDNS()})
	if *r.Score != 40 || r.Level != "at_risk" || !strings.HasPrefix(r.Weakest, "Authentication") {
		t.Fatalf("pending SES: %+v", r)
	}
	if r := Compute(Inputs{}); r.Score != nil || r.Level != "unknown" {
		t.Fatalf("no data at all: %+v", r)
	}
}

func TestComputeWeightsAndWorstSignal(t *testing.T) {
	spam := 0.0005
	in := Inputs{
		Brand: store.Brand{SESIdentityStatus: "SUCCESS"},
		DNS:   okDNS(),
		Blocklists: []store.BlocklistResult{
			{Zone: "Spamhaus DBL", Domain: "x.com", Status: "clean"},
			{Zone: "URIBL", Domain: "x.com", Status: "unknown"},
		},
		Postmaster: &store.PostmasterStat{Day: time.Now(), SpamRatio: &spam, DomainReputation: "HIGH"},
		Contacts: store.ContactStats{
			Total: 1000, Tiers: map[store.Tier]int{store.TierActive: 500, store.TierWarm: 300, store.TierAtRisk: 100, store.TierSunset: 100},
		},
	}
	r := Compute(in)
	// engagement = 50 + 22.5 + 3 = 75.5 → 76; others 100 → (25+35+20)*100 + 20*76 = 95.2
	if got := *comp(r, "engagement").Score; got != 76 {
		t.Fatalf("engagement = %d", got)
	}
	if *r.Score != 95 || !strings.HasPrefix(r.Weakest, "Engagement") {
		t.Fatalf("score %d weakest %q", *r.Score, r.Weakest)
	}

	// One listing zeroes reputation, whatever Gmail says.
	in.Blocklists[0].Status = "listed"
	r = Compute(in)
	if *comp(r, "reputation").Score != 0 || r.Level != "watch" && r.Level != "at_risk" {
		t.Fatalf("listed: %+v", comp(r, "reputation"))
	}

	// Gmail spam at 0.3% is failing; bounce rates only count with enough volume.
	in.Blocklists[0].Status = "clean"
	high := 0.003
	in.Postmaster.SpamRatio = &high
	in.Signals = &ReputationSignals{Sent7d: 100, BounceRate7d: 0.05}
	rep := comp(Compute(in), "reputation")
	if *rep.Score != 0 || len(rep.Checks) != 3 {
		t.Fatalf("gmail spam: %+v", rep)
	}
	in.Postmaster = nil
	in.Signals.Sent7d = 5000
	rep = comp(Compute(in), "reputation")
	if *rep.Score != 0 || rep.Checks[len(rep.Checks)-2].Label != "Bounces (7 days)" {
		t.Fatalf("bounces: %+v", rep)
	}
}

func TestListQualityAndUnsubscribes(t *testing.T) {
	rate := 0.02
	in := Inputs{
		Contacts: store.ContactStats{Total: 1100, Unsubscribed: 100, Quarantined: 100,
			Tiers: map[store.Tier]int{store.TierActive: 900}},
		ImportInvalidRate: &rate,
		Unsubscribes30d:   10,
	}
	r := Compute(in)
	// quarantined 10% → 80; invalid 2% → 80
	if got := *comp(r, "list_quality").Score; got != 80 {
		t.Fatalf("list quality %d", got)
	}
	// 10 / 910 ≈ 1.1% unsubscribed → −11
	if got := *comp(r, "engagement").Score; got != 89 {
		t.Fatalf("engagement %d", got)
	}
}

func TestAccountAttention(t *testing.T) {
	kinds := func(a accountMeters) string {
		var k []string
		for _, x := range accountAttention(a) {
			k = append(k, x.Kind)
		}
		return strings.Join(k, ",")
	}
	ok := accountMeters{Available: true, Account: ses.Account{Max24HourSend: 1000, SentLast24Hours: 100, ProductionAccess: true, SendingEnabled: true, EnforcementStatus: "HEALTHY"}}
	if k := kinds(ok); k != "" {
		t.Fatalf("healthy account: %s", k)
	}
	bad := ok
	bad.SendingEnabled, bad.EnforcementStatus, bad.ProductionAccess, bad.SentLast24Hours = false, "PROBATION", false, 900
	if k := kinds(bad); k != "ses_sending_disabled,ses_probation,ses_sandbox,ses_quota" {
		t.Fatalf("bad account: %s", k)
	}
	if k := kinds(accountMeters{}); k != "ses_unavailable" {
		t.Fatalf("unavailable: %s", k)
	}
}

func TestPullPostmaster(t *testing.T) {
	st := store.New(testdb.New(t))
	ctx := context.Background()
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test",
		SESConfigurationSet: "acme", SESIdentityStatus: "SUCCESS", Status: store.BrandReady})
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("20060102")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/domains/news.acme.test/trafficStats" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"trafficStats":[{"name":"domains/news.acme.test/trafficStats/` + today + `","userReportedSpamRatio":0.0015,"domainReputation":"MEDIUM"}]}`))
	}))
	defer srv.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(st, &ses.Fake{}, nil, warmup.NewService(st), Config{
		Postmaster: &postmaster.Client{HTTP: srv.Client(), BaseURL: srv.URL},
	}, log)
	if err := svc.PullPostmaster(ctx); err != nil {
		t.Fatal(err)
	}
	if s, err := st.GetPostmasterStatus(ctx, b.ID); err != nil || s.Status != "ok" {
		t.Fatalf("status %+v %v", s, err)
	}
	all, err := st.AllHealth(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("health saved: %v %v", all, err)
	}
	r, err := svc.Compute(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	// MEDIUM reputation → 70, 0.15% spam → 50: the worst wins.
	if rep := comp(r, "reputation"); rep.Score == nil || *rep.Score != 50 {
		t.Fatalf("reputation %+v", rep)
	}

	// A domain Postmaster doesn't know is reported, not an error.
	if err := st.ExecForTest(ctx, `UPDATE brands SET sending_domain = 'other.acme.test'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.PullPostmaster(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.GetPostmasterStatus(ctx, b.ID); s.Status != "domain_not_found" {
		t.Fatalf("status %+v", s)
	}

	// Not connected: nothing happens.
	off := NewService(st, &ses.Fake{}, nil, warmup.NewService(st), Config{}, log)
	if err := off.PullPostmaster(ctx); err != nil || off.PostmasterConnected() {
		t.Fatal("not connected should be a no-op")
	}
}

type failingResolver struct{}

func (failingResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	return nil, &net.DNSError{Err: "connection refused", Name: host, Server: "10.0.0.53:53"}
}

func TestDQSKeyNeverReachesViewers(t *testing.T) {
	const key = "SECRETDQSKEY123"
	st := store.New(testdb.New(t))
	ctx := context.Background()
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test",
		SESConfigurationSet: "acme", SESIdentityStatus: "SUCCESS", Status: store.BrandReady})
	if err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	svc := NewService(st, &ses.Fake{}, nil, warmup.NewService(st), Config{
		Blocklists: &blocklist.Checker{R: failingResolver{}, DQSKey: key},
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := svc.CheckBlocklists(ctx, b, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Recompute(ctx, b); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	(&Handlers{Service: svc, Store: st}).Routes(mux)
	req := httptest.NewRequest("GET", "/admin/v1/brands/acme/health", nil)
	req = req.WithContext(auth.WithPrincipalForTest(req.Context(), &auth.Principal{
		User: store.User{CompanyRole: store.CompanyMember}, Roles: store.BrandRoles{b.ID: store.RoleViewer},
	}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "lookup failed (resolver error)") {
		t.Fatalf("health: %d %s", rec.Code, body)
	}
	if strings.Contains(body, key) || strings.Contains(body, "dq.spamhaus.net") {
		t.Fatalf("API output leaks the DQS key: %s", body)
	}
	rows, err := st.BlocklistResults(ctx, b.ID)
	if err != nil || len(rows) == 0 {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		if strings.Contains(r.Detail, key) {
			t.Fatalf("stored detail leaks the key: %q", r.Detail)
		}
	}
	if strings.Contains(logs.String(), key) || !strings.Contains(logs.String(), "<dqs-key>") {
		t.Fatalf("log should have the error with the key redacted: %s", logs.String())
	}
}
