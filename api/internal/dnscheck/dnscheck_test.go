package dnscheck

import (
	"context"
	"net"
	"strings"
	"testing"
)

type fakeDNS struct {
	cname map[string]string
	mx    map[string][]*net.MX
	txt   map[string][]string
}

func nx(name string) error { return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true} }

func (f fakeDNS) LookupCNAME(_ context.Context, host string) (string, error) {
	if v, ok := f.cname[host]; ok {
		return v, nil
	}
	return "", nx(host)
}

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, nx(name)
}

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, nx(name)
}

func TestCheck(t *testing.T) {
	dns := fakeDNS{
		cname: map[string]string{"abc._domainkey.news.brand.co": "abc.dkim.amazonses.com."},
		mx:    map[string][]*net.MX{"bounce.news.brand.co": {{Host: "feedback-smtp.us-east-1.amazonses.com.", Pref: 10}}},
		txt: map[string][]string{
			"bounce.news.brand.co":   {"v=spf1 include:amazonses.com ~all"},
			"double.news.brand.co":   {"v=spf1 include:amazonses.com ~all", "v=spf1 -all"},
			"_dmarc.news.brand.co":   {"v=DMARC1; p=none; rua=mailto:d@brand.co"},
			"_dmarc.strict.brand.co": {"v=DMARC1; p=quarantine"},
			"_dmarc.other.co":        {"v=DMARC1; p=reject"},
		},
	}
	c := Checker{R: dns}

	cases := []struct {
		name   string
		rec    Record
		status Status
		detail string
	}{
		{"dkim ok", Record{"dkim", "CNAME", "abc._domainkey.news.brand.co", "abc.dkim.amazonses.com"}, OK, ""},
		{"dkim wrong target", Record{"dkim", "CNAME", "abc._domainkey.news.brand.co", "xyz.dkim.amazonses.com"}, Failing, "points to"},
		{"dkim missing", Record{"dkim", "CNAME", "zzz._domainkey.news.brand.co", "zzz.dkim.amazonses.com"}, Failing, "not found"},
		{"mx ok", Record{"mail_from_mx", "MX", "bounce.news.brand.co", "10 feedback-smtp.us-east-1.amazonses.com"}, OK, ""},
		{"mx wrong region", Record{"mail_from_mx", "MX", "bounce.news.brand.co", "10 feedback-smtp.eu-west-1.amazonses.com"}, Failing, "no MX"},
		{"spf ok", Record{"mail_from_spf", "TXT", "bounce.news.brand.co", "v=spf1 include:amazonses.com ~all"}, OK, ""},
		{"spf duplicated", Record{"mail_from_spf", "TXT", "double.news.brand.co", ""}, Failing, "more than one"},
		{"spf missing", Record{"mail_from_spf", "TXT", "none.news.brand.co", ""}, Failing, "not found"},
		{"dmarc p=none warns", Record{"dmarc", "TXT", "_dmarc.news.brand.co", ""}, Warning, "p=none"},
		{"dmarc quarantine ok", Record{"dmarc", "TXT", "_dmarc.strict.brand.co", ""}, OK, "p=quarantine"},
		{"dmarc inherited from org domain", Record{"dmarc", "TXT", "_dmarc.mail.other.co", ""}, OK, "inherited from other.co"},
		{"dmarc missing", Record{"dmarc", "TXT", "_dmarc.news.nothing.co", ""}, Failing, "no DMARC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Check(context.Background(), tc.rec)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Fatalf("got %+v, want status %s with detail containing %q", got, tc.status, tc.detail)
			}
		})
	}
}
