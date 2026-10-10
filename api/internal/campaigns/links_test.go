package campaigns

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/tracking"
)

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
