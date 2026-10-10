package brands

import (
	"errors"
	"testing"

	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

func valid() CreateInput {
	return CreateInput{
		Name: "Brightside", Slug: "brightside", Color: "#F97316", PostalAddress: "221B Market St",
		SendingDomain: "News.Brightside.co.", LinkDomain: "go.brightside.co",
	}
}

func TestNormalize(t *testing.T) {
	in := valid()
	if err := in.normalize(); err != nil {
		t.Fatal(err)
	}
	if in.SendingDomain != "news.brightside.co" || in.Color != "#f97316" {
		t.Errorf("not normalized: %+v", in)
	}

	cases := map[string]struct {
		mutate func(*CreateInput)
		code   string
	}{
		"empty name":           {func(i *CreateInput) { i.Name = " " }, "invalid_name"},
		"bad slug":             {func(i *CreateInput) { i.Slug = "Bright Side" }, "invalid_slug"},
		"bad color":            {func(i *CreateInput) { i.Color = "orange" }, "invalid_color"},
		"no postal address":    {func(i *CreateInput) { i.PostalAddress = "" }, "invalid_postal_address"},
		"apex sending domain":  {func(i *CreateInput) { i.SendingDomain = "brightside.co" }, "invalid_sending_domain"},
		"apex on public sfx":   {func(i *CreateInput) { i.SendingDomain = "brightside.co.uk" }, "invalid_sending_domain"},
		"not a domain":         {func(i *CreateInput) { i.SendingDomain = "news_brightside" }, "invalid_sending_domain"},
		"link = sending":       {func(i *CreateInput) { i.LinkDomain = "news.brightside.co" }, "invalid_link_domain"},
		"apex link domain":     {func(i *CreateInput) { i.LinkDomain = "brightside.co" }, "invalid_link_domain"},
		"public suffix itself": {func(i *CreateInput) { i.SendingDomain = "co.uk" }, "invalid_sending_domain"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := valid()
			c.mutate(&in)
			var he *httpx.Error
			if err := in.normalize(); !errors.As(err, &he) || he.Code != c.code {
				t.Fatalf("got %v, want code %s", err, c.code)
			}
		})
	}
}

func TestRequiredRecords(t *testing.T) {
	b := store.Brand{SendingDomain: "news.brightside.co", MailFromDomain: "bounce.news.brightside.co"}
	recs := RequiredRecords(b, []string{"t1", "t2", "t3"}, "eu-west-1")
	want := []struct{ purpose, typ, name, value string }{
		{"dkim", "CNAME", "t1._domainkey.news.brightside.co", "t1.dkim.amazonses.com"},
		{"dkim", "CNAME", "t2._domainkey.news.brightside.co", "t2.dkim.amazonses.com"},
		{"dkim", "CNAME", "t3._domainkey.news.brightside.co", "t3.dkim.amazonses.com"},
		{"mail_from_mx", "MX", "bounce.news.brightside.co", "10 feedback-smtp.eu-west-1.amazonses.com"},
		{"mail_from_spf", "TXT", "bounce.news.brightside.co", "v=spf1 include:amazonses.com ~all"},
		{"dmarc", "TXT", "_dmarc.news.brightside.co", "v=DMARC1; p=none; rua=mailto:dmarc-reports@brightside.co"},
	}
	if len(recs) != len(want) {
		t.Fatalf("got %d records", len(recs))
	}
	for i, w := range want {
		r := recs[i]
		if r.Purpose != w.purpose || r.Type != w.typ || r.Name != w.name || r.Value != w.value {
			t.Errorf("record %d = %+v, want %+v", i, r, w)
		}
	}
}
