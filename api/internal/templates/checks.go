package templates

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// CheckContext is what the checks need to know about the brand.
type CheckContext struct {
	// Allowlist is the brand's allowed link domains. Subdomains match.
	Allowlist []string
	// BrandDomains are always allowed: the sending and link domains.
	BrandDomains  []string
	PostalAddress string
}

// GmailClipBytes: Gmail hides everything after ~102 KB behind "[Message clipped]",
// including the unsubscribe link.
const GmailClipBytes = 102 << 10

// Shorteners hide where a link goes. Spam filters punish them, and an
// allowlisted shortener would let any destination through.
var shorteners = []string{
	"adf.ly", "bit.do", "bit.ly", "bitly.com", "bl.ink", "buff.ly", "cutt.ly", "goo.gl", "is.gd",
	"lnkd.in", "ow.ly", "qr.ae", "rb.gy", "rebrand.ly", "s.id", "short.io", "shorte.st",
	"shorturl.at", "soo.gd", "t.co", "t.ly", "tiny.cc", "tinyurl.com", "tr.im", "v.gd", "x.co",
}

// IsShortener reports whether host is a link shortener (or a subdomain of one).
func IsShortener(host string) bool { return matchDomain(strings.ToLower(host), shorteners) }

func matchDomain(host string, domains []string) bool {
	host = strings.TrimSuffix(host, ".")
	return slices.ContainsFunc(domains, func(d string) bool {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		return host == d || strings.HasSuffix(host, "."+d)
	})
}

// Check inspects compiled HTML. Every issue it reports would hurt delivery,
// break the law (CAN-SPAM, GDPR) or put readers at risk.
func Check(htmlBody string, cc CheckContext) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	add := func(is Issue) {
		key := is.Code + "\x00" + is.Message
		if !seen[key] {
			seen[key] = true
			issues = append(issues, is)
		}
	}

	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return []Issue{errorf("bad_html", "the compiled email could not be read: %v", err)}
	}

	allowed := append(slices.Clone(cc.BrandDomains), cc.Allowlist...)
	for _, d := range cc.BrandDomains {
		// news.brand.com allows links anywhere on brand.com.
		if org, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
			allowed = append(allowed, org)
		}
	}

	var hasUnsubscribe, hasImages bool
	var text strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			if !inside(n, "style", "title", "head") {
				text.WriteString(n.Data)
				text.WriteByte(' ')
			}
		case html.ElementNode:
			for _, a := range n.Attr {
				if strings.HasPrefix(strings.ToLower(a.Key), "on") {
					add(errorf("unsafe_html", "remove the %s attribute: email clients block scripts", a.Key))
				}
			}
			switch n.Data {
			case "script", "iframe", "object", "embed", "form", "input", "button", "textarea", "select":
				add(errorf("unsafe_html", "remove <%s>: email clients strip it and spam filters flag it", n.Data))
			case "a":
				if href, ok := attr(n, "href"); ok {
					if strings.TrimSpace(href) == "{{unsubscribe_url}}" {
						hasUnsubscribe = true
					}
					for _, is := range checkLink(href, allowed) {
						add(is)
					}
				}
			case "img":
				hasImages = true
				for _, is := range checkImage(n) {
					add(is)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if !hasUnsubscribe {
		add(errorf("missing_unsubscribe", `add an unsubscribe link: <a href="{{unsubscribe_url}}">Unsubscribe</a>. It is required by law and by Gmail and Yahoo`))
	}
	body := collapse(text.String())
	if !strings.Contains(body, "{{postal_address}}") && !(cc.PostalAddress != "" && strings.Contains(body, collapse(cc.PostalAddress))) {
		add(errorf("missing_postal_address", "add the brand's postal address ({{postal_address}}); CAN-SPAM requires it in every marketing email"))
	}
	for _, name := range tagNames(htmlBody) {
		switch _, ok := knownTag(name); {
		case name == ContentTag:
			add(errorf("unknown_merge_tag", "{{content}} is only for layouts"))
		case !ok:
			add(errorf("unknown_merge_tag", "{{%s}} is not a merge tag we can fill", name))
		}
	}
	if len(htmlBody) > GmailClipBytes {
		add(warnf("gmail_clipping", "the email is %d KB; Gmail clips anything over 102 KB and hides the unsubscribe link", len(htmlBody)>>10))
	}
	if hasImages && len(strings.ReplaceAll(body, " ", "")) < 100 {
		add(warnf("image_only", "the email is mostly images; add real text, or many readers (and spam filters) see nothing"))
	}
	return issues
}

// checkLink validates one href.
func checkLink(href string, allowed []string) []Issue {
	href = strings.TrimSpace(href)
	if href == "" || href == "#" {
		return []Issue{warnf("empty_link", "a link has no destination")}
	}
	if m := mergeTagRe.FindStringSubmatch(href); m != nil && m[0] == href {
		if t, ok := knownTag(m[1]); ok && t.URL {
			return nil
		}
		return []Issue{errorf("bad_link", "{{%s}} is not a link; use {{unsubscribe_url}} or {{preferences_url}}", m[1])}
	}
	u, err := url.Parse(href)
	if err != nil {
		return []Issue{errorf("bad_link", "%q is not a valid link", href)}
	}
	switch strings.ToLower(u.Scheme) {
	case "mailto", "tel":
		return nil
	case "https", "http":
	default:
		return []Issue{errorf("unsafe_link", "%q: only https, mailto and tel links are allowed", clip(href))}
	}
	host := strings.ToLower(u.Hostname())
	var issues []Issue
	switch {
	case IsShortener(host):
		return []Issue{errorf("link_shortener", "%s is a link shortener; link to the real page instead", host)}
	case !matchDomain(host, allowed):
		issues = append(issues, errorf("link_not_allowed", "%s is not on this brand's link allowlist", host))
	}
	if strings.EqualFold(u.Scheme, "http") {
		issues = append(issues, warnf("insecure_link", "use https for %s", host))
	}
	return issues
}

func checkImage(n *html.Node) []Issue {
	var issues []Issue
	src, _ := attr(n, "src")
	alt, hasAlt := attr(n, "alt")
	if tiny(n) {
		return nil // spacer
	}
	if !hasAlt || strings.TrimSpace(alt) == "" {
		issues = append(issues, errorf("missing_alt", "image %s has no alt text; many readers see this text instead of the image", clip(src)))
	}
	switch lower := strings.ToLower(src); {
	case strings.HasPrefix(lower, "data:"):
		issues = append(issues, warnf("inline_image", "embedded (data:) images are blocked by Gmail and make the email heavy; host the image instead"))
	case strings.HasPrefix(lower, "http://"):
		issues = append(issues, warnf("insecure_image", "use https for image %s", clip(src)))
	}
	return issues
}

func tiny(n *html.Node) bool {
	for _, k := range []string{"width", "height"} {
		if v, ok := attr(n, k); ok {
			if px, err := strconv.Atoi(strings.TrimSuffix(v, "px")); err == nil && px <= 1 {
				return true
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func inside(n *html.Node, tags ...string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && slices.Contains(tags, p.Data) {
			return true
		}
	}
	return false
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string) string {
	if r := []rune(s); len(r) > 80 {
		return fmt.Sprintf("%s…", string(r[:77]))
	}
	return s
}
