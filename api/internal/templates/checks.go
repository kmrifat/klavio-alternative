package templates

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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

// FooterClass marks the layout's footer (css-class on its mj-section). The
// unsubscribe link and postal address must be inside it, and a template
// must leave it exactly as the layout made it.
const FooterClass = "klv-footer"

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

// parseHTML parses with scripting off, the way email clients see it:
// <noscript> content is markup, not text.
func parseHTML(s string) (*html.Node, error) {
	return html.ParseWithOptions(strings.NewReader(s), html.ParseOptionEnableScripting(false))
}

// Check inspects compiled HTML. Every issue it reports would hurt delivery,
// break the law (CAN-SPAM, GDPR) or put readers at risk.
func Check(htmlBody string, cc CheckContext) []Issue {
	doc, err := parseHTML(htmlBody)
	if err != nil {
		return []Issue{errorf("bad_html", "the compiled email could not be read: %v", err)}
	}
	c := checker{allowed: allowedDomains(cc), seen: map[string]bool{}}
	c.walk(doc)

	c.footer(doc, cc.PostalAddress)
	for _, name := range tagNames(htmlBody) {
		switch _, ok := knownTag(name); {
		case name == ContentTag:
			c.add(errorf("unknown_merge_tag", "{{content}} is only for layouts"))
		case !ok:
			c.add(errorf("unknown_merge_tag", "{{%s}} is not a merge tag we can fill", name))
		}
	}
	if len(htmlBody) > GmailClipBytes {
		c.add(warnf("gmail_clipping", "the email is %d KB; Gmail clips anything over 102 KB and hides the unsubscribe link", len(htmlBody)>>10))
	}
	if c.images && len(strings.ReplaceAll(collapse(c.text.String()), " ", "")) < 100 {
		c.add(warnf("image_only", "the email is mostly images; add real text, or many readers (and spam filters) see nothing"))
	}
	return c.issues
}

func allowedDomains(cc CheckContext) []string {
	allowed := append(slices.Clone(cc.BrandDomains), cc.Allowlist...)
	for _, d := range cc.BrandDomains {
		// news.brand.com allows links anywhere on brand.com.
		if org, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
			allowed = append(allowed, org)
		}
	}
	return allowed
}

type checker struct {
	allowed []string
	issues  []Issue
	seen    map[string]bool
	images  bool
	text    strings.Builder
}

func (c *checker) add(is Issue) {
	key := is.Code + "\x00" + is.Message
	if !c.seen[key] {
		c.seen[key] = true
		c.issues = append(c.issues, is)
	}
}

// Attributes that load an image or other resource, checked with the image
// rules. Any href, on any element, is checked as a link.
var resourceAttrs = map[string]bool{"src": true, "background": true, "poster": true, "lowsrc": true, "dynsrc": true}

var cssURLRe = regexp.MustCompile(`(?i)url\(\s*['"]?([^'")]*)['"]?\s*\)`)

func (c *checker) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if !inside(n, "style", "title", "head") {
			c.text.WriteString(n.Data)
			c.text.WriteByte(' ')
		}
	case html.CommentNode:
		// Outlook renders <!--[if mso]> … <![endif]--> blocks, so their
		// markup is checked like everything else.
		if strings.Contains(n.Data, "<") {
			body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
			if nodes, err := html.ParseFragmentWithOptions(strings.NewReader(n.Data), body, html.ParseOptionEnableScripting(false)); err == nil {
				for _, f := range nodes {
					c.walk(f)
				}
			}
		}
	case html.ElementNode:
		c.element(n)
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

func (c *checker) element(n *html.Node) {
	switch n.Data {
	case "script", "iframe", "object", "embed", "form", "input", "button", "textarea", "select", "frame", "frameset", "applet":
		c.add(errorf("unsafe_html", "remove <%s>: email clients strip it and spam filters flag it", n.Data))
	case "img":
		c.images = true
		if !tiny(n) {
			if alt, ok := attr(n, "alt"); !ok || strings.TrimSpace(alt) == "" {
				src, _ := attr(n, "src")
				c.add(errorf("missing_alt", "image %s has no alt text; many readers see this text instead of the image", clip(src)))
			}
		}
	}
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if a.Namespace != "" {
			key = strings.ToLower(a.Namespace) + ":" + key
		}
		switch {
		case strings.HasPrefix(key, "on"):
			c.add(errorf("unsafe_html", "remove the %s attribute: email clients block scripts", a.Key))
		case key == "href" || strings.HasSuffix(key, ":href"):
			for _, is := range checkLink(a.Val, c.allowed) {
				c.add(is)
			}
		case resourceAttrs[key] || strings.HasSuffix(key, ":src"):
			c.resource(a.Val)
		case key == "srcset":
			for _, part := range strings.Split(a.Val, ",") {
				if f := strings.Fields(part); len(f) > 0 {
					c.resource(f[0])
				}
			}
		case key == "style":
			for _, m := range cssURLRe.FindAllStringSubmatch(a.Val, -1) {
				c.resource(m[1])
			}
		}
	}
}

// resource checks a URL that is loaded rather than clicked: images and backgrounds.
func (c *checker) resource(src string) {
	src = strings.TrimSpace(src)
	if src == "" {
		return
	}
	lower := strings.ToLower(src)
	if strings.HasPrefix(lower, "data:") {
		c.add(warnf("inline_image", "embedded (data:) images are blocked by Gmail and make the email heavy; host the image instead"))
		return
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		c.add(errorf("bad_image", "%q: images need a full https:// address", clip(src)))
		return
	}
	if IsShortener(u.Hostname()) {
		c.add(errorf("link_shortener", "%s is a link shortener; use the image's real address", u.Hostname()))
	}
	if u.Scheme == "http" {
		c.add(warnf("insecure_image", "use https for image %s", clip(src)))
	}
}

// checkLink validates one href.
func checkLink(href string, allowed []string) []Issue {
	href = strings.TrimSpace(href)
	if href == "" || href == "#" {
		return []Issue{warnf("empty_link", "a link has no destination")}
	}
	if m := mergeTagRe.FindStringSubmatch(href); m != nil && m[0] == href {
		t, ok := knownTag(m[1])
		switch {
		case !ok || !t.URL:
			return []Issue{errorf("bad_link", "{{%s}} is not a link; use {{unsubscribe_url}} or {{preferences_url}}", m[1])}
		case strings.Contains(m[0], "|"):
			return []Issue{errorf("bad_link", "{{%s}} always has a value; remove the fallback after |", m[1])}
		}
		return nil
	}
	u, err := url.Parse(href)
	if err != nil {
		return []Issue{errorf("bad_link", "%q is not a valid link", clip(href))}
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

// footer checks the layout's footer: there is exactly one, and it shows a
// working unsubscribe link and the postal address.
func (c *checker) footer(doc *html.Node, postalAddress string) {
	footers := FindFooters(doc)
	if len(footers) != 1 {
		c.add(errorf("missing_footer", `the layout needs exactly one footer section marked css-class="%s" holding the unsubscribe link and postal address (found %d)`, FooterClass, len(footers)))
		return
	}
	f := footers[0]
	if hiddenAt(f) {
		c.add(errorf("hidden_footer", "the layout's footer is hidden; readers must be able to see it"))
		return
	}
	var unsub, address bool
	var visit func(n *html.Node)
	visit = func(n *html.Node) {
		switch {
		case n.Type == html.ElementNode && n.Data == "a":
			href, _ := attr(n, "href")
			if m := mergeTagRe.FindStringSubmatch(strings.TrimSpace(href)); m != nil && m[1] == "unsubscribe_url" && m[0] == strings.TrimSpace(href) {
				if visibleText(n) {
					unsub = true
				}
			}
		case n.Type == html.TextNode:
			if slices.Contains(tagNames(n.Data), "postal_address") && !hiddenAt(n) {
				address = true
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			visit(ch)
		}
	}
	visit(f)
	if !address && postalAddress != "" && strings.Contains(collapse(visibleTextOf(f)), collapse(postalAddress)) {
		address = true
	}
	if !unsub {
		c.add(errorf("missing_unsubscribe", `the footer needs a visible unsubscribe link: <a href="{{unsubscribe_url}}">Unsubscribe</a>. It is required by law and by Gmail and Yahoo`))
	}
	if !address {
		c.add(errorf("missing_postal_address", "the footer needs the brand's postal address ({{postal_address}}); CAN-SPAM requires it in every marketing email"))
	}
}

// FindFooters returns every element carrying FooterClass.
func FindFooters(doc *html.Node) []*html.Node {
	var out []*html.Node
	var find func(n *html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if cls, ok := attr(n, "class"); ok && slices.Contains(strings.Fields(cls), FooterClass) {
				out = append(out, n)
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			find(ch)
		}
	}
	find(doc)
	return out
}

func visibleText(n *html.Node) bool {
	found := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" && !hiddenAt(n) {
			found = true
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return found
}

func visibleTextOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode && !hiddenAt(n) {
			b.WriteString(n.Data + " ")
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return b.String()
}

var (
	hideRe     = regexp.MustCompile(`(^|;)(display:none|visibility:hidden|opacity:0(\.0*)?|mso-hide:all)(;|$|!)`)
	fontSizeRe = regexp.MustCompile(`(^|;)font-size:([0-9.]+)(px|pt)?`)
	maxHeight0 = regexp.MustCompile(`(^|;)max-height:0`)
)

// hiddenAt reports whether n or anything around it hides it: display:none,
// visibility:hidden, opacity:0, mso-hide:all, max-height:0 with overflow
// hidden, or a font size under 2px (MJML sets font-size:0 on wrappers and
// resets it inside, so the nearest declared size is what counts).
func hiddenAt(n *html.Node) bool {
	sizeKnown := false
	for p := n; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if _, ok := attr(p, "hidden"); ok {
			return true
		}
		style, _ := attr(p, "style")
		style = strings.ToLower(strings.ReplaceAll(style, " ", ""))
		if hideRe.MatchString(style) {
			return true
		}
		if strings.Contains(style, "overflow:hidden") && maxHeight0.MatchString(style) {
			return true
		}
		if !sizeKnown {
			if m := fontSizeRe.FindStringSubmatch(style); m != nil {
				sizeKnown = true
				if v, err := strconv.ParseFloat(m[2], 64); err == nil && v < 2 {
					return true
				}
			}
		}
	}
	return false
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
		if a.Key == key && a.Namespace == "" {
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
