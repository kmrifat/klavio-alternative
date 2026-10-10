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
	// First pass: how many links (to spot "clicked everything"), and each
	// destination's first link number, for Outlook's copies below.
	count := 0
	nums := map[string]int{}
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
					if u := strings.TrimSpace(href); nums[u] == 0 {
						nums[u] = count
					}
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
		if tt == nethtml.CommentToken {
			out.Write(s.trackMSO(raw, b, cs, base, skip, nums, count, flags))
			continue
		}
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

// trackMSO tracks the links inside an Outlook conditional comment
// (<!--[if mso]> … <![endif]-->): Outlook renders that markup, so a VML
// button (<v:roundrect href=…>) there is the link Outlook users click. Each
// trackable href whose destination also has an <a> in the email gets that
// link's number, so the two count as one link in reports; one without is
// left alone. Only the href values change: every other byte of the comment
// is copied as it was.
func (s *Service) trackMSO(raw []byte, b store.Brand, cs store.ClaimedSend, base string, skip map[string]bool, nums map[string]int, count int, flags byte) []byte {
	inner, ok := msoComment(raw)
	if !ok || len(nums) == 0 {
		return raw
	}
	var out []byte
	done, pos := 0, 4 // offsets in raw; inner starts after "<!--"
	z := nethtml.NewTokenizer(bytes.NewReader(inner))
	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			break
		}
		tag := z.Raw()
		start := pos
		pos += len(tag)
		if tt != nethtml.StartTagToken && tt != nethtml.SelfClosingTagToken {
			continue
		}
		vs, ve, quoted, ok := attrValue(tag, "href")
		if !ok {
			continue
		}
		href := html.UnescapeString(string(tag[vs:ve]))
		if parsed, _ := attr(z.Token(), "href"); parsed != href || !trackable(href, skip) {
			continue // not the href the parser reads, or not a web link
		}
		no := nums[strings.TrimSpace(href)]
		if no == 0 {
			continue
		}
		link, err := s.tracking.ClickURL(base, tracking.Link{BrandID: b.ID, ContactID: cs.ContactID, SendID: cs.ID,
			LinkNo: no, LinkCount: count, URL: strings.TrimSpace(href), Flags: flags})
		if err != nil || (!quoted && strings.ContainsAny(link, " \t\n\f\r>\"'=<`")) {
			continue
		}
		out = append(out, raw[done:start+vs]...)
		out = append(out, html.EscapeString(link)...)
		done = start + ve
	}
	if out == nil {
		return raw
	}
	return append(out, raw[done:]...)
}

// msoComment returns the inside of an Outlook conditional comment: one
// whose condition ([if mso], [if mso | IE], [if gte mso 9] …) names mso.
func msoComment(raw []byte) ([]byte, bool) {
	if len(raw) < 7 || !bytes.HasPrefix(raw, []byte("<!--")) || !bytes.HasSuffix(raw, []byte("-->")) {
		return nil, false
	}
	inner := raw[4 : len(raw)-3]
	cond := bytes.TrimLeft(inner, " \t\r\n")
	end := bytes.IndexByte(cond, ']')
	if !bytes.HasPrefix(cond, []byte("[if")) || end < 0 || !bytes.Contains(bytes.ToLower(cond[:end]), []byte("mso")) {
		return nil, false
	}
	return inner, true
}

// attrValue finds the first attribute called name in one raw start tag, the
// way the HTML tokenizer reads it, and returns where its value is (without
// quotes) and whether it was quoted.
func attrValue(tag []byte, name string) (vs, ve int, quoted, ok bool) {
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }
	i := 1
	for i < len(tag) && !space(tag[i]) && tag[i] != '/' && tag[i] != '>' {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (space(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= len(tag) || tag[i] == '>' {
			break
		}
		ks := i
		i++ // a name may start with '='
		for i < len(tag) && !space(tag[i]) && tag[i] != '/' && tag[i] != '>' && tag[i] != '=' {
			i++
		}
		key := tag[ks:i]
		for i < len(tag) && space(tag[i]) {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			continue // no value
		}
		i++
		for i < len(tag) && space(tag[i]) {
			i++
		}
		if i < len(tag) && (tag[i] == '"' || tag[i] == '\'') {
			q := tag[i]
			i++
			vs = i
			for i < len(tag) && tag[i] != q {
				i++
			}
			ve, quoted = i, true
			if i < len(tag) {
				i++
			}
		} else {
			vs = i
			for i < len(tag) && !space(tag[i]) && tag[i] != '>' {
				i++
			}
			ve, quoted = i, false
		}
		if strings.EqualFold(string(key), name) {
			return vs, ve, quoted, true
		}
	}
	return 0, 0, false, false
}

// extras: the open pixel, and the honeypot link if turned on, placed just
// before </body>.
func (s *Service) extras(b store.Brand, cs store.ClaimedSend, base string, count int) string {
	var e strings.Builder
	if s.honeypot {
		if link, err := s.tracking.ClickURL(base, tracking.Link{BrandID: b.ID, ContactID: cs.ContactID, SendID: cs.ID,
			LinkNo: 0, LinkCount: count, URL: base + "/", Flags: tracking.Honeypot}); err == nil {
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
