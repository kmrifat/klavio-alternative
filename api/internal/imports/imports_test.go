package imports

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/contacts"
)

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type fakeResolver struct {
	mx    map[string][]*net.MX
	hosts map[string][]string
	fail  map[string]bool // temporary failure
}

func notFoundErr(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if f.fail[name] {
		return nil, &net.DNSError{Err: "timeout", Name: name, IsTimeout: true}
	}
	if mx, ok := f.mx[name]; ok {
		return mx, nil
	}
	return nil, notFoundErr(name)
}

func (f fakeResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	if h, ok := f.hosts[name]; ok {
		return h, nil
	}
	return nil, notFoundErr(name)
}

type fakeSuppressions map[string]bool // by email

func (f fakeSuppressions) Suppressed(_ context.Context, hashes [][]byte) (map[string]bool, error) {
	out := map[string]bool{}
	for email := range f {
		h := string(contacts.SuppressionHash(email))
		for _, want := range hashes {
			if string(want) == h {
				out[h] = true
			}
		}
	}
	return out, nil
}

func readAll(t *testing.T, path string) ([]string, []Row) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	m := SuggestMapping(r.Headers())
	if err := m.Validate(r.Headers()); err != nil {
		t.Fatal(err)
	}
	var rows []Row
	for {
		rec, n, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, MapRecord(m, n, rec))
	}
	return r.Headers(), rows
}

func TestMailchimpExport(t *testing.T) {
	headers, rows := readAll(t, "testdata/subscribed_members_export_1a2b3c.csv")

	if headers[0] != "Email Address" {
		t.Fatalf("BOM not stripped: first header %q", headers[0])
	}
	m := SuggestMapping(headers)
	got := map[string]Field{}
	for i, f := range m {
		if f != FieldIgnore {
			got[headers[i]] = f
		}
	}
	want := map[string]Field{
		"Email Address": FieldEmail, "First Name": FieldFirstName, "Last Name": FieldLastName,
		"OPTIN_TIME": FieldOptedInAt, "OPTIN_IP": FieldOptInIP, "CONFIRM_TIME": FieldConfirmedAt,
		"TIMEZONE": FieldTimezone, "CC": FieldCountry, "TAGS": FieldTags, "MEMBER_RATING": FieldRating,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mapping:\n got %v\nwant %v", got, want)
	}

	if len(rows) != 12 {
		t.Fatalf("got %d rows, want 12 (blank line skipped)", len(rows))
	}
	ann := rows[0]
	if ann.FirstName != "Ann" || ann.Rating != 4 || !ann.Engaged() || ann.Timezone != "Europe/Dublin" || ann.Country != "IE" ||
		!reflect.DeepEqual(ann.Tags, []string{"VIP", "Spring sale"}) || ann.OptInIP != "203.0.113.7" ||
		ann.ConfirmedAt == nil || !ann.ConfirmedAt.Equal(time.Date(2026, 3, 1, 9, 16, 10, 0, time.UTC)) {
		t.Errorf("ann = %+v", ann)
	}
	if carol := rows[2]; carol.Line != 5 || carol.Timezone != "" || carol.Country != "" {
		t.Errorf("carol: bad timezone/3-letter country should be dropped, line counts the blank line: %+v", carol)
	}

	c := Checker{
		Now: func() time.Time { return now },
		Resolver: fakeResolver{
			mx:    map[string][]*net.MX{"example.com": {{Host: "mx.example.com.", Pref: 10}}, "nomail.example": {{Host: "."}}},
			hosts: map[string][]string{},
		},
		Suppressions: fakeSuppressions{"gone@example.com": true},
	}
	counts, err := c.Check(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	results := make([]Result, len(rows))
	for i, r := range rows {
		results[i] = r.Result
	}
	wantResults := []Result{
		ResultOK,            // ann
		ResultOK,            // bob, normalized
		ResultQuarantine,    // carol, 2019, 3 stars
		ResultInvalidSyntax, // not-an-email
		ResultRole,          // info@
		ResultDisposable,    // mailinator
		ResultDuplicate,     // ann again
		ResultNoMX,          // null MX
		ResultSuppressed,    // gone@
		ResultQuarantine,    // no dates at all
		ResultOK,            // loyal, 2018 but 5 stars
		ResultQuarantine,    // meh, 2018 and 2 stars
	}
	if !reflect.DeepEqual(results, wantResults) {
		t.Errorf("results:\n got %v\nwant %v", results, wantResults)
	}
	if rows[1].Email != "bob@example.com" {
		t.Errorf("bob not normalized: %q", rows[1].Email)
	}
	if counts.Total() != len(rows) {
		t.Errorf("counts total %d, rows %d", counts.Total(), len(rows))
	}
}

func TestReaderSemicolonsAndShortRows(t *testing.T) {
	r, err := NewReader(strings.NewReader("email;name\nann@example.com;Ann Lee\nbob@example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := SuggestMapping(r.Headers())
	if !reflect.DeepEqual(m, Mapping{FieldEmail, FieldFullName}) {
		t.Fatalf("mapping %v", m)
	}
	rec, _, _ := r.Next()
	if row := MapRecord(m, 1, rec); row.FirstName != "Ann" || row.LastName != "Lee" {
		t.Errorf("full name split: %+v", row)
	}
	rec, _, _ = r.Next()
	if row := MapRecord(m, 2, rec); row.Email != "bob@example.com" {
		t.Errorf("short row: %+v", row)
	}
	if _, _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("want EOF, got %v", err)
	}
}

func TestReaderEmpty(t *testing.T) {
	if _, err := NewReader(strings.NewReader("")); err == nil {
		t.Error("want error for empty file")
	}
}

func TestMappingValidate(t *testing.T) {
	h := []string{"a", "b"}
	cases := []struct {
		m       Mapping
		wantErr bool
	}{
		{Mapping{FieldEmail, FieldIgnore}, false},
		{Mapping{FieldIgnore, FieldIgnore}, true},
		{Mapping{FieldEmail, FieldEmail}, true},
		{Mapping{FieldEmail, "shoe_size"}, true},
		{Mapping{FieldEmail}, true},
	}
	for _, c := range cases {
		if err := c.m.Validate(h); (err != nil) != c.wantErr {
			t.Errorf("%v: err = %v", c.m, err)
		}
	}
}

func TestParseTime(t *testing.T) {
	for _, v := range []string{"2026-03-01T09:15:00Z", "2026-03-01T10:15:00+01:00", "2026-03-01 09:15:00", "2026/03/01 09:15:00"} {
		got := parseTime(v)
		if got == nil || !got.Equal(time.Date(2026, 3, 1, 9, 15, 0, 0, time.UTC)) {
			t.Errorf("parseTime(%q) = %v", v, got)
		}
	}
	for _, v := range []string{"03/01/2026", "yesterday", "1709284500"} {
		if got := parseTime(v); got != nil {
			t.Errorf("parseTime(%q) = %v, want nil", v, got)
		}
	}
}

func TestReceivesMail(t *testing.T) {
	res := fakeResolver{
		mx:    map[string][]*net.MX{"mx.example": {{Host: "mail.mx.example."}}, "null.example": {{Host: "."}}},
		hosts: map[string][]string{"a-only.example": {"192.0.2.1"}},
		fail:  map[string]bool{"flaky.example": true},
	}
	cases := map[string]bool{
		"mx.example":     true,
		"null.example":   false,
		"a-only.example": true, // implicit MX
		"gone.example":   false,
		"flaky.example":  true, // never fail an address on our resolver's problem
	}
	for d, want := range cases {
		if got := receivesMail(context.Background(), res, d); got != want {
			t.Errorf("%s: got %v", d, got)
		}
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		name   string
		c      Counts
		passed bool
	}{
		{"clean", Counts{ResultOK: 1000}, true},
		{"2% invalid is allowed", Counts{ResultOK: 980, ResultInvalidSyntax: 10, ResultNoMX: 10}, true},
		{"2.1% invalid is not", Counts{ResultOK: 979, ResultInvalidSyntax: 11, ResultNoMX: 10}, false},
		{"5% role+disposable is allowed", Counts{ResultOK: 950, ResultRole: 30, ResultDisposable: 20}, true},
		{"5.1% is not", Counts{ResultOK: 949, ResultRole: 31, ResultDisposable: 20}, false},
		{"duplicates and suppressed do not count", Counts{ResultOK: 98, ResultInvalidSyntax: 2, ResultDuplicate: 500, ResultSuppressed: 500}, true},
		{"quarantined rows count as checked", Counts{ResultQuarantine: 98, ResultNoMX: 2}, true},
		{"nothing importable", Counts{ResultDuplicate: 3}, false},
		{"empty", Counts{}, false},
	}
	for _, c := range cases {
		g := c.c.Gate()
		if g.Passed != c.passed {
			t.Errorf("%s: passed = %v (%+v)", c.name, g.Passed, g)
		}
		if !g.Passed && len(g.Reasons) == 0 {
			t.Errorf("%s: failed without a reason", c.name)
		}
	}
	if g := (Counts{ResultOK: 979, ResultInvalidSyntax: 21}).Gate(); g.InvalidPercent != 2.1 {
		t.Errorf("InvalidPercent = %v", g.InvalidPercent)
	}
}

func TestAttestation(t *testing.T) {
	good := Attestation{Source: "Mailchimp main list", Text: AttestationText, SignedBy: "u1"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for want, a := range map[error]Attestation{
		ErrNoSource:       {Source: " ", Text: AttestationText, SignedBy: "u1"},
		ErrNotAttested:    {Source: "x", Text: AttestationText},
		ErrWrongStatement: {Source: "x", Text: "I promise", SignedBy: "u1"},
	} {
		if err := a.Validate(); !errors.Is(err, want) {
			t.Errorf("%+v: got %v, want %v", a, err, want)
		}
	}
}

func TestGuessKind(t *testing.T) {
	cases := map[string]ListKind{
		"subscribed_members_export_1a2b.csv":   KindSubscribed,
		"unsubscribed_members_export_1a2b.csv": KindUnsubscribed,
		"cleaned_members_export_1a2b.csv":      KindCleaned,
		"my list.csv":                          KindSubscribed,
	}
	for name, want := range cases {
		if got := GuessKind(name); got != want {
			t.Errorf("%s: %s", name, got)
		}
	}
}
