package blocklist

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fake map[string][]string

func (f fake) LookupHost(_ context.Context, host string) ([]string, error) {
	if host == "timeout.example.dbl.spamhaus.org" {
		return nil, errors.New("i/o timeout")
	}
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestCheck(t *testing.T) {
	c := Checker{R: fake{
		"bad.example.dbl.spamhaus.org":    {"127.0.1.2"},
		"bad.example.multi.surbl.org":     {"127.0.0.80"},
		"bad.example.multi.uribl.com":     {"127.0.0.2"},
		"public.example.dbl.spamhaus.org": {"127.255.255.254"},
		"public.example.multi.surbl.org":  {"127.0.0.1"},
		"public.example.multi.uribl.com":  {"127.0.0.1"},
		"grey.example.multi.uribl.com":    {"127.0.0.4"},
	}}
	want := map[string][3]Status{
		"bad.example":    {Listed, Listed, Listed},
		"good.example":   {Clean, Clean, Clean},
		"public.example": {Unknown, Unknown, Unknown},
		"grey.example":   {Clean, Clean, Listed},
	}
	for domain, statuses := range want {
		res := c.Check(context.Background(), domain)
		for i, r := range res {
			if r.Status != statuses[i] {
				t.Errorf("%s %s: %s (%s), want %s", domain, r.Zone, r.Status, r.Detail, statuses[i])
			}
		}
	}
	if r := c.Check(context.Background(), "timeout.example")[0]; r.Status != Unknown {
		t.Errorf("lookup error should be unknown, got %s", r.Status)
	}
	// SURBL bitmask names the lists.
	if r := c.Check(context.Background(), "bad.example")[1]; r.Detail != "SURBL abused" && r.Detail != "SURBL malware, abused" && r.Detail != "SURBL abused, malware" {
		t.Errorf("SURBL detail: %q", r.Detail)
	}
	// DQS key changes the Spamhaus zone.
	dqs := Checker{R: fake{"bad.example.KEY.dbl.dq.spamhaus.net": {"127.0.1.2"}}, DQSKey: "KEY"}
	if r := dqs.Check(context.Background(), "bad.example")[0]; r.Status != Listed {
		t.Errorf("DQS lookup: %s", r.Status)
	}
}
