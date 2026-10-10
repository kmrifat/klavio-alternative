package campaigns

import (
	"slices"
	"strings"
	"testing"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

func TestApprovalReasons(t *testing.T) {
	cases := []struct {
		name  string
		sum   store.AudienceSummary
		usual int
		want  []Reason
	}{
		{"small, usual", store.AudienceSummary{Included: 900}, 1000, nil},
		{"over the limit", store.AudienceSummary{Included: 10001}, 0, []Reason{ReasonLarge}},
		{"at the limit", store.AudienceSummary{Included: 10000}, 0, nil},
		{"over twice usual", store.AudienceSummary{Included: 2001}, 1000, []Reason{ReasonUnusual}},
		{"first campaign has no usual", store.AudienceSummary{Included: 5000}, 0, nil},
		{"at risk", store.AudienceSummary{Included: 10, AtRisk: 3}, 0, []Reason{ReasonAtRisk}},
		{"fresh import", store.AudienceSummary{Included: 10, FirstTimeImported: 1}, 0, []Reason{ReasonFirstImport}},
		{"everything", store.AudienceSummary{Included: 30000, AtRisk: 1, FirstTimeImported: 1}, 1000,
			[]Reason{ReasonLarge, ReasonUnusual, ReasonAtRisk, ReasonFirstImport}},
	}
	for _, c := range cases {
		if got := ApprovalReasons(c.sum, c.usual, 10000); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	for _, r := range []Reason{ReasonLarge, ReasonUnusual, ReasonAtRisk, ReasonFirstImport} {
		if r.Text() == "" {
			t.Errorf("%s has no text", r)
		}
	}
}

func TestNormalizeCount(t *testing.T) {
	cases := map[string]string{
		"2431": "2431", "2,431": "2431", "2 431": "2431", "2.431": "2431", " 12 ": "12", "0": "0", "000": "0",
		"007": "7", "": "", "abc": "", "-5": "", "12a": "", "1 000": "1000",
	}
	for in, want := range cases {
		if got := normalizeCount(in); got != want {
			t.Errorf("normalizeCount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithPreheader(t *testing.T) {
	got := withPreheader(`<html><head></head><BODY style="x"><p>hi</p></body></html>`, "Big <news>")
	if !strings.Contains(got, `<BODY style="x"><div style="display:none`) || !strings.Contains(got, "Big &lt;news&gt;</div><p>hi</p>") {
		t.Errorf("got %s", got)
	}
	if got := withPreheader("<p>x</p>", "  "); got != "<p>x</p>" {
		t.Errorf("empty preheader changed the body: %s", got)
	}
	if got := withPreheaderText("Body", "Peek"); got != "Peek\n\nBody" {
		t.Errorf("text: %q", got)
	}
}

func TestFromHeader(t *testing.T) {
	b := store.Brand{Name: `Kettle & "Co"`, SendingDomain: "mail.kettle.example"}
	if got := FromHeader(b, store.BrandSender{FromLocal: "hello"}); got != `"Kettle & \"Co\"" <hello@mail.kettle.example>` {
		t.Errorf("got %s", got)
	}
	if got := FromHeader(b, store.BrandSender{FromName: "Café", FromLocal: "news"}); !strings.HasSuffix(got, "<news@mail.kettle.example>") || !strings.HasPrefix(got, "=?utf-8?") {
		t.Errorf("non-ASCII name should be encoded: %s", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("Ann\r\nBcc: x@evil.example\n  Lee"); got != "Ann Bcc: x@evil.example Lee" {
		t.Errorf("got %q", got)
	}
}
