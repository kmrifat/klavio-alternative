package campaigns

import (
	"bytes"
	"html"
	"io"
	"net/url"
	"strings"

	nethtml "golang.org/x/net/html"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/tracking"
)

// trackable: a web link worth counting. Unsubscribe and preference links
// (their own signed URLs), mailto:, tel:, anchors and anything that isn't
// http(s) are left alone.
func trackable(href string, skip map[string]bool) bool {
	href = strings.TrimSpace(href)
	if skip[href] {
		return false
	}
	u, err := url.Parse(href)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && tracking.SafeURL(href) == nil
}

// trackLinks points every trackable <a href> at the click redirect and adds
// the open pixel, for one recipient. Everything else in the HTML is copied
// byte for byte. Recipients without tracking consent get working links that
// record nothing, and no pixel.
func (s *Service) trackLinks(body string, b store.Brand, cs store.ClaimedSend, base string, skip map[string]bool) string {
	if s.tracking == nil {
		return body
	}
	var flags byte
	if !cs.TrackingAllowed {
		flags = tracking.NoTrack
	}
	// First pass: how many links (to spot "clicked everything").
	count := 0
	z := nethtml.NewTokenizer(strings.NewReader(body))
	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			break
		}
		if tt == nethtml.StartTagToken || tt == nethtml.SelfClosingTagToken {
			if t := z.Token(); t.Data == "a" {
				if href, ok := attr(t, "href"); ok && trackable(href, skip) {
					count++
				}
			}
		}
	}
	if count == 0 && !cs.TrackingAllowed {
		return body
	}

	var out bytes.Buffer
	no, closed := 0, false
	z = nethtml.NewTokenizer(strings.NewReader(body))
	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			if z.Err() != io.EOF {
				return body // not HTML we understand: send it as it was
			}
			break
		}
		raw := append([]byte(nil), z.Raw()...) // later calls may reuse the buffer
		if tt != nethtml.StartTagToken {
			if tt == nethtml.EndTagToken && cs.TrackingAllowed && !closed {
				if name, _ := z.TagName(); string(name) == "body" {
					out.WriteString(s.extras(b, cs, base, count))
					closed = true
				}
			}
			out.Write(raw)
			continue
		}
		t := z.Token()
		href, ok := attr(t, "href")
		if t.Data != "a" || !ok || !trackable(href, skip) {
			out.Write(raw)
			continue
		}
		no++
		link, err := s.tracking.ClickURL(base, tracking.Link{BrandID: b.ID, ContactID: cs.ContactID, SendID: cs.ID,
			LinkNo: no, LinkCount: count, URL: strings.TrimSpace(href), Flags: flags})
		if err != nil {
			out.Write(raw)
			continue
		}
		for i := range t.Attr {
			if strings.EqualFold(t.Attr[i].Key, "href") {
				t.Attr[i].Val = link
			}
		}
		out.WriteString(t.String())
	}
	if cs.TrackingAllowed && !closed {
		out.WriteString(s.extras(b, cs, base, count))
	}
	return out.String()
}

// extras: the open pixel, and the honeypot link if turned on, placed just
// before </body>.
func (s *Service) extras(b store.Brand, cs store.ClaimedSend, base string, count int) string {
	var e strings.Builder
	if s.honeypot {
		if link, err := s.tracking.ClickURL(base, tracking.Link{BrandID: b.ID, ContactID: cs.ContactID, SendID: cs.ID,
			LinkNo: 1, LinkCount: max(count, 1), URL: base + "/", Flags: tracking.Honeypot}); err == nil {
			e.WriteString(`<a href="` + html.EscapeString(link) + `" style="display:none" aria-hidden="true" tabindex="-1"></a>`)
		}
	}
	px := s.tracking.OpenURL(base, tracking.Pixel{BrandID: b.ID, ContactID: cs.ContactID, SendID: cs.ID})
	e.WriteString(`<img src="` + html.EscapeString(px) + `" width="1" height="1" alt="" style="display:block;width:1px;height:1px;border:0">`)
	return e.String()
}

func attr(t nethtml.Token, key string) (string, bool) {
	for _, a := range t.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}
