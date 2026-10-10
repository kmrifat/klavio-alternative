package blocklist

import (
	"context"
	"net"
	"strings"
	"testing"
)

// fake answers fully qualified names; anything else is NXDOMAIN, so a
// query without the trailing dot shows up as "clean" and fails the test.
type fake map[string][]string

func (f fake) LookupHost(_ context.Context, host string) ([]string, error) {
	if strings.HasPrefix(host, "timeout.") {
		return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
	}
	if strings.HasPrefix(host, "refused.") {
		return nil, &net.DNSError{Err: "connection refused", Name: host, Server: "10.0.0.53:53"}
	}
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func statuses(c Checker, domain string) (out [3]Status, details [3]string) {
	for i, r := range c.Check(context.Background(), domain) {
		out[i], details[i] = r.Status, r.Detail
	}
	return
}

func TestCheck(t *testing.T) {
	c := Checker{R: fake{
		"bad.example.dbl.spamhaus.org.":    {"127.0.1.2"},
		"bad.example.multi.surbl.org.":     {"127.0.0.80"},
		"bad.example.multi.uribl.com.":     {"127.0.0.2"},
		"public.example.dbl.spamhaus.org.": {"127.255.255.254"},
		"public.example.multi.surbl.org.":  {"127.0.0.1"},
		"public.example.multi.uribl.com.":  {"127.0.0.1"},
		"grey.example.multi.uribl.com.":    {"127.0.0.4"},
		// An NXDOMAIN-hijacking resolver answers every name with an ad server.
		"hijack.example.dbl.spamhaus.org.": {"92.242.140.21"},
		"hijack.example.multi.surbl.org.":  {"92.242.140.21"},
		"hijack.example.multi.uribl.com.":  {"92.242.140.21"},
		// Codes that aren't listings.
		"odd.example.dbl.spamhaus.org.":    {"127.255.255.253"},
		"odd.example.multi.surbl.org.":     {"127.0.0.2"}, // no known SURBL bit
		"odd.example.multi.uribl.com.":     {"127.0.0.16"},
		"abused.example.dbl.spamhaus.org.": {"127.0.1.104"},
	}}
	want := map[string][3]Status{
		"bad.example":    {Listed, Listed, Listed},
		"good.example":   {Clean, Clean, Clean},
		"public.example": {Unknown, Unknown, Unknown},
		"grey.example":   {Clean, Clean, Listed},
		"hijack.example": {Unknown, Unknown, Unknown},
		"odd.example":    {Unknown, Unknown, Unknown},
		"abused.example": {Listed, Clean, Clean},
	}
	for domain, w := range want {
		got, details := statuses(c, domain)
		if got != w {
			t.Errorf("%s: %v %q, want %v", domain, got, details, w)
		}
	}
	if _, d := statuses(c, "hijack.example"); !strings.Contains(d[0], "resolver problem") {
		t.Errorf("hijack detail: %q", d[0])
	}
	if _, d := statuses(c, "bad.example"); d[0] != "spam domain" || d[1] != "SURBL abused, malware" || d[2] != "URIBL black" {
		t.Errorf("details: %q", d)
	}
	if _, d := statuses(c, "abused.example"); d[0] != "abused legit phish" {
		t.Errorf("DBL abused detail: %q", d[0])
	}
	// SURBL reasons are sorted, so they don't flip between runs.
	for range 20 {
		if _, d := statuses(Checker{R: fake{"x.example.multi.surbl.org.": {"127.0.0.216"}}}, "x.example"); d[1] != "SURBL abused, cracked, malware, phishing" {
			t.Fatalf("SURBL detail: %q", d[1])
		}
	}
	// The DQS key changes the Spamhaus zone.
	dqs := Checker{R: fake{"bad.example.KEY.dbl.dq.spamhaus.net.": {"127.0.1.2"}}, DQSKey: "KEY"}
	if got, _ := statuses(dqs, "bad.example"); got[0] != Listed {
		t.Errorf("DQS lookup: %s", got[0])
	}
}

func TestLookupErrorsNeverLeakTheKey(t *testing.T) {
	const key = "SECRETDQSKEY123"
	c := Checker{R: fake{}, DQSKey: key}
	for _, domain := range []string{"timeout.example", "refused.example"} {
		for _, r := range c.Check(context.Background(), domain) {
			if r.Status != Unknown {
				t.Errorf("%s %s: %s, want unknown", domain, r.Zone, r.Status)
			}
			if strings.Contains(r.Detail, key) || strings.Contains(r.Cause, key) || strings.Contains(r.Detail, "spamhaus.net") {
				t.Errorf("%s %s leaks the query: detail %q cause %q", domain, r.Zone, r.Detail, r.Cause)
			}
			if !strings.HasPrefix(r.Detail, "lookup failed (") {
				t.Errorf("detail should be generic: %q", r.Detail)
			}
		}
	}
	if r := c.Check(context.Background(), "timeout.example")[0]; r.Detail != "lookup failed (timeout)" || !strings.Contains(r.Cause, "<dqs-key>") {
		t.Errorf("timeout: %+v", r)
	}
}
