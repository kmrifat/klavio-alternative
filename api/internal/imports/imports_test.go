package imports

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

const mailchimpCSV = "\xef\xbb\xbf" + `"Email Address","First Name","Last Name","MEMBER_RATING","OPTIN_TIME","CONFIRM_TIME","TIMEZONE","CC","LAST_CHANGED","TAGS"
maya@example.com,Maya,Rodriguez,4,"2025-03-01 10:00:00","2025-03-12 09:30:00",America/New_York,US,"2026-01-02 00:00:00","""vip"",""customer"""
` + "\n" + `old@example.com,Old,Timer,1,"2019-01-01 10:00:00","2019-01-01 10:05:00",,GB,,
`

func TestParseMailchimpExport(t *testing.T) {
	tbl, err := Parse([]byte(mailchimpCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 2 || tbl.Headers[0] != "Email Address" {
		t.Fatalf("headers %v rows %d", tbl.Headers, len(tbl.Rows))
	}
	m := SuggestMapping(tbl.Headers)
	want := Mapping{Email: "Email Address", FirstName: "First Name", LastName: "Last Name", Tags: "TAGS",
		ConsentedAt: "CONFIRM_TIME", Rating: "MEMBER_RATING", Timezone: "TIMEZONE", Country: "CC"}
	if m != want {
		t.Fatalf("mapping = %+v\nwant      %+v", m, want)
	}
	rows := Rows(tbl, m, time.Now())
	ResolveTracking(rows, "") // default: by country; both rows are outside the EU
	r := rows[0]
	if r.Email != "maya@example.com" || r.FirstName != "Maya" || r.Country != "US" || r.Timezone != "America/New_York" {
		t.Errorf("row = %+v", r)
	}
	if r.Rating != 4 || !r.Engaged() || rows[1].Rating != 1 || rows[1].Engaged() {
		t.Errorf("ratings: %d %d", r.Rating, rows[1].Rating)
	}
	if !slices.Equal(r.Tags, []string{"vip", "customer"}) {
		t.Errorf("tags = %q", r.Tags)
	}
	if r.ConsentedAt == nil || r.ConsentedAt.Format("2006-01-02") != "2025-03-12" {
		t.Errorf("consented_at = %v", r.ConsentedAt)
	}
}

func TestParseSemicolonsAndErrors(t *testing.T) {
	tbl, err := Parse([]byte("email;name\na@x.com;A\n\n;\nb@y.com;B\n"))
	if err != nil || len(tbl.Rows) != 2 || tbl.Headers[1] != "name" {
		t.Fatalf("semicolon file: %v %+v", err, tbl)
	}
	for name, data := range map[string]string{
		"empty":       "",
		"header only": "email\n",
		"not utf-8":   "email\n\xff\xfe@x.com\n",
		"dup columns": "email,email\na@x.com,b@x.com\n",
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		" Maya@Example.COM ":        "maya@example.com",
		"a.b+tag@sub.example.co.uk": "a.b+tag@sub.example.co.uk",
		"no-at-sign.com":            "",
		"two@@example.com":          "",
		"user@localhost":            "",
		"user@example..com":         "",
		"Name <a@b.com>":            "",
		"user@.example.com":         "",
		"":                          "",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	recent := now.AddDate(0, -2, 0)
	old := now.AddDate(-2, 0, 0)
	existingID, unsubID := uuid.New(), uuid.New()

	rows := []Row{
		{Email: "ready@good.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "", TrackingAllowed: true},                                     // invalid
		{Email: "ready@good.com", ConsentedAt: &recent, TrackingAllowed: true}, // duplicate
		{Email: "blocked@good.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "info@good.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "x@mailinator.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "y@deadmail.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "known@good.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "gone@good.com", ConsentedAt: &recent, TrackingAllowed: true},
		{Email: "stale@good.com", ConsentedAt: &old, TrackingAllowed: true},
		{Email: "nodates@good.com", TrackingAllowed: true},
		{Email: "clicked@good.com", ConsentedAt: &old, LastEngagedAt: &recent, TrackingAllowed: true},
		{Email: "starred@good.com", ConsentedAt: &old, Rating: 4, TrackingAllowed: true},   // 4–5 stars = recent activity
		{Email: "unstarred@good.com", ConsentedAt: &old, Rating: 3, TrackingAllowed: true}, // 3 stars isn't enough
	}
	Classify(rows, Facts{
		Suppressed: map[string]bool{"blocked@good.com": true},
		Existing: map[string]store.Contact{
			"known@good.com": {ID: existingID, Status: store.ContactSubscribed},
			"gone@good.com":  {ID: unsubID, Status: store.ContactUnsubscribed},
		},
		NoMailbox: map[string]bool{"deadmail.com": true},
		Now:       now,
	})
	want := []Outcome{Ready, Invalid, Duplicate, Suppressed, Role, Disposable, NoMailbox, Existing, Unsubscribed, Quarantine, Quarantine, Ready, Ready, Quarantine}
	for i, w := range want {
		if rows[i].Outcome != w {
			t.Errorf("row %d (%q): %s, want %s", i, rows[i].Email, rows[i].Outcome, w)
		}
	}
	if rows[7].ExistingID != existingID {
		t.Error("existing contact id not set")
	}
}

func goodConsent() Consent {
	return Consent{Source: "platform_migration", SourceDetail: "Mailchimp", CollectedPeriod: "2023–2026",
		Wording: "brightside.co/subscribe", LegalBasis: "consent", Attested: true}
}

func TestConsentProblems(t *testing.T) {
	if p := goodConsent().Problems(); len(p) != 0 {
		t.Fatalf("good consent has problems: %v", p)
	}
	c := goodConsent()
	c.Source = "bought_list"
	c.Attested = false
	c.LegalBasis = "soft_opt_in"
	p := strings.Join(c.Problems(), "; ")
	for _, want := range []string{"where these contacts came from", "bought, rented or scraped", "soft opt-in"} {
		if !strings.Contains(p, want) {
			t.Errorf("problems %q missing %q", p, want)
		}
	}
	if c := (Consent{DoubleOptIn: true}); c.basis() != "double_opt_in" {
		t.Error("double opt-in basis")
	}
}

func rowsWith(outcomes map[Outcome]int) []Row {
	var rows []Row
	for o, n := range outcomes {
		for range n {
			rows = append(rows, Row{Email: string(o) + "@x.com", Outcome: o})
		}
	}
	return rows
}

func TestGate(t *testing.T) {
	now := time.Now()
	m := Mapping{Email: "email", ConsentedAt: "date"}

	ok := Summarize(rowsWith(map[Outcome]int{Ready: 97, Invalid: 1, Role: 2}), goodConsent(), m, nil, now)
	if !ok.Gate.Passed || ok.Total != 100 || ok.Counts[Ready] != 97 {
		t.Fatalf("expected pass: %+v", ok.Gate)
	}

	tooInvalid := Summarize(rowsWith(map[Outcome]int{Ready: 95, Invalid: 2, NoMailbox: 3}), goodConsent(), m, nil, now)
	if tooInvalid.Gate.Passed || tooInvalid.Gate.InvalidRate != 0.05 {
		t.Fatalf("5%% invalid should fail: %+v", tooInvalid.Gate)
	}

	tooRole := Summarize(rowsWith(map[Outcome]int{Ready: 90, Role: 4, Disposable: 6}), goodConsent(), m, nil, now)
	if tooRole.Gate.Passed {
		t.Fatalf("10%% role+disposable should fail: %+v", tooRole.Gate)
	}

	noAttest := goodConsent()
	noAttest.Attested = false
	unsigned := Summarize(rowsWith(map[Outcome]int{Ready: 10}), noAttest, m, nil, now)
	if unsigned.Gate.Passed || unsigned.Gate.ConsentRecorded {
		t.Fatalf("unsigned import should fail: %+v", unsigned.Gate)
	}

	nobody := Summarize(rowsWith(map[Outcome]int{Suppressed: 3, Unsubscribed: 2}), goodConsent(), m, nil, now)
	if nobody.Gate.Passed {
		t.Fatal("nothing importable should fail")
	}
}

type fakeMX struct {
	mx    map[string][]*net.MX
	hosts map[string]bool
	fail  map[string]bool
}

func (f fakeMX) LookupMX(_ context.Context, d string) ([]*net.MX, error) {
	if f.fail[d] {
		return nil, errors.New("i/o timeout")
	}
	if v, ok := f.mx[d]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: d, IsNotFound: true}
}

func (f fakeMX) LookupHost(_ context.Context, d string) ([]string, error) {
	if f.hosts[d] {
		return []string{"192.0.2.1"}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: d, IsNotFound: true}
}

func TestDomainsWithoutMailbox(t *testing.T) {
	r := fakeMX{
		mx: map[string][]*net.MX{
			"good.com":   {{Host: "mx.good.com.", Pref: 10}},
			"nullmx.com": {{Host: ".", Pref: 0}},
		},
		hosts: map[string]bool{"aonly.com": true},
		fail:  map[string]bool{"flaky.com": true},
	}
	got := DomainsWithoutMailbox(context.Background(), r, []string{"good.com", "nullmx.com", "aonly.com", "flaky.com", "nxdomain.com"})
	if !slices.Equal(got, []string{"nullmx.com", "nxdomain.com"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRoleAndDisposableLists(t *testing.T) {
	for _, e := range []string{"info@x.com", "sales+eu@x.com", "noreply@x.com", "customerservice@x.com"} {
		if !isRole(e) {
			t.Errorf("%s should be a role address", e)
		}
	}
	for _, e := range []string{"hello@studio.com", "maya@x.com", "information@x.com"} {
		if isRole(e) {
			t.Errorf("%s shouldn't be a role address", e)
		}
	}
	for _, e := range []string{"a@mailinator.com", "a@inbox.mailinator.com", "a@yopmail.net"} {
		if !isDisposable(e) {
			t.Errorf("%s should be disposable", e)
		}
	}
	for _, e := range []string{"a@33mail.com", "a@gmail.com", "a@notmailinator.com"} {
		if isDisposable(e) {
			t.Errorf("%s shouldn't be disposable", e)
		}
	}
}

func TestFutureDatesCountAsNoDate(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	tbl, err := Parse([]byte("email,confirm_time,last_click\n" +
		"future@good.com,2206-03-01,2206-03-01\n" +
		"skew@good.com,2026-10-10 20:00:00,\n" +
		"recent@good.com,2026-10-01,\n"))
	if err != nil {
		t.Fatal(err)
	}
	rows := Rows(tbl, Mapping{Email: "email", ConsentedAt: "confirm_time", LastEngagedAt: "last_click"}, now)
	ResolveTracking(rows, "all")
	if rows[0].ConsentedAt != nil || rows[0].LastEngagedAt != nil {
		t.Fatalf("future dates kept: %v %v", rows[0].ConsentedAt, rows[0].LastEngagedAt)
	}
	if rows[1].ConsentedAt == nil {
		t.Fatal("a date a few hours ahead (clock skew) should be kept")
	}
	Classify(rows, Facts{Now: now})
	if rows[0].Outcome != Quarantine || rows[2].Outcome != Ready {
		t.Fatalf("outcomes: %s %s", rows[0].Outcome, rows[2].Outcome)
	}
}

func TestTruncateKeepsWholeCharacters(t *testing.T) {
	long := strings.Repeat("田中倫", 50) // 150 characters, 450 bytes
	got := truncate(long, 100)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 100 {
		t.Fatalf("got %d runes, valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if truncate("Maya", 100) != "Maya" {
		t.Fatal("short strings must be unchanged")
	}
}

func TestResolveTracking(t *testing.T) {
	rows := func() []Row {
		return []Row{{Country: "DE", TrackingRaw: "yes"}, {Country: "US", TrackingRaw: ""}, {Country: "GB", TrackingRaw: "Granted"}}
	}
	cases := map[string][]bool{
		"":              {false, true, false}, // default: by country
		"eu_by_country": {false, true, false},
		"all":           {true, true, true},
		"none":          {false, false, false},
		"column":        {true, false, true},
	}
	for mode, want := range cases {
		rs := rows()
		ResolveTracking(rs, mode)
		for i, w := range want {
			if rs[i].TrackingAllowed != w {
				t.Errorf("mode %q row %d: %v, want %v", mode, i, rs[i].TrackingAllowed, w)
			}
		}
	}
}

func TestNoTrackingIgnoresStarsAndClicks(t *testing.T) {
	now := time.Now()
	old, recent := now.AddDate(-2, 0, 0), now.AddDate(0, 0, -3)
	rows := []Row{
		{Email: "eu@good.com", ConsentedAt: &old, LastEngagedAt: &recent, Rating: 5, TrackingAllowed: false},
		{Email: "us@good.com", ConsentedAt: &old, Rating: 5, TrackingAllowed: true},
	}
	Classify(rows, Facts{Now: now})
	if rows[0].Outcome != Quarantine || rows[1].Outcome != Ready {
		t.Fatalf("got %s / %s", rows[0].Outcome, rows[1].Outcome)
	}
}
