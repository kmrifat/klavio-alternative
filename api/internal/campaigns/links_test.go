package campaigns

import (
	"bytes"
	"context"
	"html"
	"strings"
	"testing"

	"github.com/google/uuid"
	nethtml "golang.org/x/net/html"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/templates"
	"github.com/kmrifat/klavio-alternative/api/internal/tracking"
)

// A bulletproof Outlook button: VML for Outlook desktop inside a conditional
// comment, next to the mj-button every other client shows. MJML's own
// mj-button has no VML and templates can't hold comments, so it comes from
// the brand's layout (brand admins, behind approval), in mj-raw.
const outlookButton = `<mj-section><mj-column>
<mj-raw><!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" href="https://shop.example.com/sale?a=1&amp;b=2" arcsize="10%" fillcolor="#F97316">Shop</v:roundrect><![endif]--></mj-raw>
<mj-button href="https://shop.example.com/sale?a=1&amp;b=2">Shop</mj-button>
<mj-text><a href="https://shop.example.com/other">Other</a></mj-text>
<mj-raw><!--[if mso]><a href="{{unsubscribe_url}}">Unsubscribe</a><![endif]--></mj-raw>
</mj-column></mj-section>`

func TestTrackLinksOutlookButtons(t *testing.T) {
	signer, _ := tracking.NewSigner("links-test-secret-aaaaaaaaaaaaaaaaaaaaaaa")
	s := &Service{}
	s.SetTracking(signer, false)
	b := store.Brand{ID: uuid.New()}
	cs := store.ClaimedSend{ID: 9, ContactID: uuid.New(), TrackingAllowed: true}
	base := "https://go.acme.test"
	unsub := base + "/u/tok"
	skip := map[string]bool{unsub: true}

	layout := strings.Replace(templates.DefaultLayout("#F97316"), "{{content}}", outlookButton+"\n{{content}}", 1)
	compiled, issues, err := templates.Compile(context.Background(), layout, templates.SampleContent)
	if err != nil || templates.HasErrors(issues) {
		t.Fatalf("compile (the footer fingerprint is one of its checks): %v %+v", err, issues)
	}
	body := templates.Render(compiled.HTML, map[string]string{"unsubscribe_url": unsub, "preferences_url": base + "/p/tok"})
	const vmlHref = `href="https://shop.example.com/sale?a=1&amp;b=2" arcsize`
	if !strings.Contains(body, vmlHref) {
		t.Fatalf("MJML dropped the VML button:\n%s", body)
	}
	out := s.trackLinks(body, b, cs, base, skip)

	click := func(after string) tracking.Link {
		t.Helper()
		i := strings.Index(out, after)
		if i < 0 {
			t.Fatalf("%q not in output", after)
		}
		j := i + strings.Index(out[i:], base+"/c/") + len(base+"/c/")
		l, err := signer.Click(out[j : j+strings.IndexAny(out[j:], `"'`)])
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	vml, button := click("<v:roundrect"), click(`<a href="`+base+"/c/")
	if vml.URL != "https://shop.example.com/sale?a=1&b=2" || vml.URL != button.URL || vml.LinkNo != button.LinkNo || vml.LinkCount != button.LinkCount {
		t.Fatalf("Outlook's button and the <a> must be one link:\nvml %+v\na   %+v", vml, button)
	}
	if !strings.Contains(out, `<!--[if mso]><a href="`+unsub+`">Unsubscribe</a><![endif]-->`) {
		t.Fatal("unsubscribe link inside a conditional changed")
	}

	// Otherwise byte for byte what tracking produced before: the same email
	// with the VML href made untrackable differs only in that value.
	link, _ := signer.ClickURL(base, tracking.Link{BrandID: b.ID, ContactID: cs.ContactID, SendID: cs.ID,
		LinkNo: button.LinkNo, LinkCount: button.LinkCount, URL: button.URL})
	before := s.trackLinks(strings.Replace(body, vmlHref, `href="#vml" arcsize`, 1), b, cs, base, skip)
	if want := strings.Replace(before, `href="#vml"`, `href="`+html.EscapeString(link)+`"`, 1); out != want {
		t.Fatalf("more than the VML href changed")
	}
	// And the footer is untouched by it.
	if f, g := footer(t, out), footer(t, before); f != g || !strings.Contains(f, unsub) {
		t.Fatalf("footer changed:\n%s\n---\n%s", f, g)
	}

	// No tracking consent: the VML link redirects without recording.
	cs.TrackingAllowed = false
	out = s.trackLinks(body, b, cs, base, skip)
	if l := click("<v:roundrect"); l.Flags&tracking.NoTrack == 0 || l.LinkNo != button.LinkNo {
		t.Fatalf("no-track VML link: %+v", l)
	}
}

func footer(t *testing.T, doc string) string {
	t.Helper()
	n, err := nethtml.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	fs := templates.FindFooters(n)
	if len(fs) != 1 {
		t.Fatalf("%d footers", len(fs))
	}
	var b bytes.Buffer
	_ = nethtml.Render(&b, fs[0])
	return b.String()
}

// Hand-written conditionals: attribute quoting and case as Outlook templates
// write them; only http(s) hrefs that match a link in the email change.
func TestTrackLinksConditionalComments(t *testing.T) {
	signer, _ := tracking.NewSigner("links-test-secret-aaaaaaaaaaaaaaaaaaaaaaa")
	s := &Service{}
	s.SetTracking(signer, false)
	b := store.Brand{ID: uuid.New()}
	cs := store.ClaimedSend{ID: 3, ContactID: uuid.New(), TrackingAllowed: true}
	base := "https://go.acme.test"
	cases := []struct {
		name, comment string
		tracked       bool
	}{
		{"single quotes, upper case", `<!--[if gte mso 9]><v:roundrect HREF='https://shop.example/x' arcsize="5%"/><![endif]-->`, true},
		{"unquoted", `<!--[if mso | IE]><v:rect href=https://shop.example/x stroke=f></v:rect><![endif]-->`, true},
		{"not a conditional", `<!-- <a href="https://shop.example/x">x</a> -->`, false},
		{"no matching <a>", `<!--[if mso]><v:roundrect href="https://elsewhere.example/"/><![endif]-->`, false},
		{"mailto", `<!--[if mso]><v:roundrect href="mailto:hi@acme.test"/><![endif]-->`, false},
		{"data-href is not href", `<!--[if mso]><v:roundrect data-href="https://shop.example/x"/><![endif]-->`, false},
	}
	for _, c := range cases {
		body := `<html><body>` + c.comment + `<a href="https://shop.example/x">x</a></body></html>`
		out := s.trackLinks(body, b, cs, base, nil)
		i := strings.Index(out, "<!--")
		got := out[i : i+strings.Index(out[i:], "-->")+3]
		if tracked := got != c.comment; tracked != c.tracked {
			t.Errorf("%s: tracked %v, want %v: %s", c.name, tracked, c.tracked, got)
		}
		if c.tracked && (strings.Count(got, base+"/c/") != 1 || strings.Contains(got, "shop.example/x")) {
			t.Errorf("%s: %s", c.name, got)
		}
		if c.tracked {
			// Everything around the value is as written.
			j := strings.Index(got, base)
			k := j + strings.IndexAny(got[j:], `"' `)
			if c.comment[:j] != got[:j] || !strings.HasSuffix(c.comment, got[k:]) {
				t.Errorf("%s: more than the value changed: %s", c.name, got)
			}
		}
	}
}

func TestTrackLinks(t *testing.T) {
	signer, _ := tracking.NewSigner("links-test-secret-aaaaaaaaaaaaaaaaaaaaaaa")
	s := &Service{}
	s.SetTracking(signer, false)
	b := store.Brand{ID: uuid.New()}
	cs := store.ClaimedSend{ID: 9, ContactID: uuid.New(), TrackingAllowed: true}
	base := "https://go.acme.test"
	unsub := base + "/u/tok"
	body := `<html><body><p style="color:red">Hi &amp; welcome</p>
<a href="https://shop.example/a?x=1&amp;y=2">A</a> <a href="mailto:hi@acme.test">mail</a> <a href="tel:+4930123">call</a>
<a href="#top">top</a> <A HREF="https://shop.example/b">B</A> <a href="` + unsub + `">Unsubscribe</a>
<style>a{color:blue}</style></body></html>`
	out := s.trackLinks(body, b, cs, base, map[string]bool{unsub: true})

	for _, kept := range []string{`href="mailto:hi@acme.test"`, `href="tel:+4930123"`, `href="#top"`, `href="` + unsub + `"`,
		`<p style="color:red">Hi &amp; welcome</p>`, `<style>a{color:blue}</style>`} {
		if !strings.Contains(out, kept) {
			t.Errorf("changed or lost %q:\n%s", kept, out)
		}
	}
	if strings.Contains(out, `href="https://shop.example`) || strings.Count(out, base+"/c/") != 2 {
		t.Fatalf("web links not tracked:\n%s", out)
	}
	if strings.Count(out, base+"/o/") != 1 || strings.Index(out, base+"/o/") > strings.Index(out, "</body>") {
		t.Fatalf("pixel missing or after </body>:\n%s", out)
	}
	if strings.Contains(out, "display:none") {
		t.Fatal("honeypot added while off")
	}
	// The destinations round-trip, query string intact.
	i := strings.Index(out, base+"/c/") + len(base+"/c/")
	tok := out[i : i+strings.IndexByte(out[i:], '"')]
	l, err := signer.Click(tok)
	if err != nil || l.URL != "https://shop.example/a?x=1&y=2" || l.LinkNo != 1 || l.LinkCount != 2 || l.SendID != 9 {
		t.Fatalf("link 1: %+v %v", l, err)
	}

	// No tracking consent: links still go through (flagged), no pixel.
	cs.TrackingAllowed = false
	out = s.trackLinks(body, b, cs, base, map[string]bool{unsub: true})
	if strings.Contains(out, "/o/") || strings.Count(out, base+"/c/") != 2 {
		t.Fatalf("no-track:\n%s", out)
	}

	// Honeypot, when turned on: one hidden link, flagged.
	s.SetTracking(signer, true)
	cs.TrackingAllowed = true
	out = s.trackLinks(body, b, cs, base, map[string]bool{unsub: true})
	if strings.Count(out, `style="display:none"`) != 1 || strings.Count(out, base+"/c/") != 3 {
		t.Fatalf("honeypot:\n%s", out)
	}
}
