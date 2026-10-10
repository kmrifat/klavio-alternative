package templates

import (
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// Template source is read with the HTML tokenizer from golang.org/x/net/html,
// which splits tags and attributes exactly as browsers (and MJML's parser) do.
// Earlier versions used regular expressions, and `<span x">` hid a <dialog>
// from them: a quote only starts a value after "=", so that x" is an
// attribute name, not the start of a string.

// mjAllowed are the MJML components a template may use. Others render
// form controls (carousel, accordion, navbar) or fixed-height backgrounds
// (hero) that the checks can't keep inside the template's slot.
var mjAllowed = map[string]bool{
	"mj-section": true, "mj-column": true, "mj-group": true, "mj-wrapper": true, "mj-text": true,
	"mj-image": true, "mj-button": true, "mj-divider": true, "mj-spacer": true, "mj-table": true,
	"mj-raw": true, "mj-social": true, "mj-social-element": true,
}

// headOnly are MJML elements that belong to the layout's <mj-head>.
var headOnly = map[string]bool{
	"mjml": true, "mj-body": true, "mj-head": true, "mj-attributes": true, "mj-style": true, "mj-font": true,
	"mj-breakpoint": true, "mj-html-attributes": true, "mj-title": true, "mj-preview": true, "mj-include": true,
}

// Tags that must be closed as often as they're opened. Browsers confine an
// unclosed one to its table cell, but not every email client does, and an
// unclosed <div style="display:none"> could then swallow the footer.
// Tags whose closing is commonly left out (<p>, <li>) aren't counted.
var balancedTags = map[string]bool{"a": true, "b": true, "blockquote": true, "center": true, "div": true, "em": true,
	"font": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "i": true, "ol": true, "s": true,
	"small": true, "span": true, "strong": true, "sub": true, "sup": true, "table": true, "tbody": true, "td": true,
	"th": true, "thead": true, "tr": true, "u": true, "ul": true}

// Budgets for the whole template. Each box is capped on its own, but a
// thousand capped boxes would still push the footer out of reach.
const (
	MaxHTMLElements  = 1500
	MaxTotalHeightPx = 10000 // fixed heights: CSS height/min-height and height attributes
	MaxTotalSpacePx  = 20000 // margins and padding
)

// validAttrName: what a sane attribute name looks like. Anything else (a
// quote, "<", "=") is a parsing trick or a typo, and refused.
func validAttrName(n string) bool {
	if n == "" {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// token is one start or self-closing tag as the tokenizer saw it.
type token struct {
	name        string
	attrs       []htmlAttr
	selfClosing bool
}

// scan tokenizes s and calls fn for each token type it cares about.
func scan(s string, start func(token), end func(name string), comment func()) error {
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return nil
			}
			return z.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			tok := token{name: t.Data, selfClosing: t.Type == html.SelfClosingTagToken}
			for _, a := range t.Attr {
				name := a.Key
				if a.Namespace != "" {
					name = a.Namespace + ":" + name
				}
				tok.attrs = append(tok.attrs, htmlAttr{name: strings.ToLower(name), val: a.Val})
			}
			start(tok)
		case html.EndTagToken:
			end(z.Token().Data)
		case html.CommentToken, html.DoctypeToken:
			comment()
		}
	}
}

// countElements counts opening MJML tags (any letter case: the tokenizer
// lower-cases names, as MJML does).
func countElements(s string) int {
	n := 0
	_ = scan(s, func(t token) {
		if strings.HasPrefix(t.name, "mj-") || t.name == "mjml" {
			n++
		}
	}, func(string) {}, func() {})
	return n
}

// templateSourceIssues keeps a template inside its slot in the layout: it
// may not comment out, close, restyle or replace anything around it.
func templateSourceIssues(content string) []Issue {
	if len(content) > MaxTemplateBytes {
		return []Issue{errorf("too_large", "the template is %d KB; keep it under %d KB", len(content)>>10, MaxTemplateBytes>>10)}
	}
	if n := countElements(content); n > MaxTemplateElements {
		return []Issue{errorf("too_large", "the template has %d MJML elements; keep it under %d (split long emails, or combine text blocks)", n, MaxTemplateElements)}
	}

	var issues []Issue
	seen := map[string]bool{}
	add := func(code, format string, args ...any) {
		is := errorf(code, format, args...)
		if !seen[is.Message] {
			seen[is.Message] = true
			issues = append(issues, is)
		}
	}
	if strings.Contains(strings.ToLower(content), "{{"+ContentTag) {
		add("template_has_content_tag", "{{content}} belongs in the layout, not in a template")
	}

	var mjStack []string
	open := map[string]int{}
	htmlElements := 0
	var heights, space float64
	unbalanced := false

	err := scan(content,
		func(t token) {
			for _, a := range t.attrs {
				if !validAttrName(a.name) {
					add("template_attr_name", "<%s> has an attribute named %q; attribute names may use letters, digits, dashes, underscores and colons", t.name, clip(a.name))
				}
			}
			heights += heightOf(t)
			space += spacingOf(t)
			switch {
			case headOnly[t.name]:
				add("template_has_wrapper", "<%s> belongs to the brand layout; a template holds only body content (mj-section, mj-wrapper …)", t.name)
			case strings.HasPrefix(t.name, "mj-"):
				if !mjAllowed[t.name] {
					add("template_element", "<%s> isn't available in templates; use sections, columns, text, images, buttons, dividers, spacers, tables and social links", t.name)
				}
				mjAttrIssues(t.name, t.attrs, add)
				if !t.selfClosing {
					mjStack = append(mjStack, t.name)
				}
			case refusedOutright[t.name]:
				add("template_style", "templates can't contain <%s> elements", t.name)
			case !allowedElements[t.name]:
				add("template_element", "templates can't contain <%s>; use p, div, span, a, img, tables, lists, headings and text formatting", t.name)
			default:
				htmlElements++
				// Every counted tag is a normal element, so "<div/>" still
				// opens a div: HTML ignores the slash there.
				if balancedTags[t.name] {
					open[t.name]++
				}
				htmlAttrIssues(t.name, t.attrs, add)
				for _, a := range t.attrs {
					if a.name == "style" {
						cssIssues(t.name, a.val, add)
					}
				}
			}
		},
		func(name string) {
			switch {
			case headOnly[name]:
				add("template_has_wrapper", "</%s> belongs to the brand layout", name)
			case strings.HasPrefix(name, "mj-"):
				if len(mjStack) == 0 || mjStack[len(mjStack)-1] != name {
					if !unbalanced {
						unbalanced = true
						add("template_unbalanced", "</%s> closes something this template didn't open", name)
					}
					return
				}
				mjStack = mjStack[:len(mjStack)-1]
			case balancedTags[name]:
				open[name]--
			}
		},
		func() {
			add("template_comment", "templates can't contain HTML comments, <!…> or <![…]> blocks; remove them")
		})
	if err != nil {
		add("bad_html", "the template could not be read: %v", err)
	}
	if len(mjStack) > 0 && !unbalanced {
		add("template_unbalanced", "<%s> is never closed", mjStack[len(mjStack)-1])
	}
	for tag, n := range open {
		switch {
		case n > 0:
			add("template_unbalanced", "<%s> is opened %d more time(s) than it is closed; close every tag you open", tag, n)
		case n < 0:
			add("template_unbalanced", "</%s> appears %d more time(s) than <%s>; it would close part of the layout", tag, -n, tag)
		}
	}
	if htmlElements > MaxHTMLElements {
		add("too_large", "the template has %d HTML elements; keep it under %d", htmlElements, MaxHTMLElements)
	}
	if heights > MaxTotalHeightPx {
		add("too_large", "the template's fixed heights add up to %.0fpx; keep them under %dpx in total", heights, MaxTotalHeightPx)
	}
	if space > MaxTotalSpacePx {
		add("too_large", "the template's margins and padding add up to %.0fpx; keep them under %dpx in total", space, MaxTotalSpacePx)
	}
	return issues
}

// heightOf is the fixed height a tag asks for: height attributes and the
// CSS height and min-height of its style.
func heightOf(t token) float64 {
	var sum float64
	for _, a := range t.attrs {
		switch a.name {
		case "height", "min-height":
			sum += pxTotal(a.val, true)
		case "style":
			for prop, val := range declarations(a.val) {
				if prop == "height" || prop == "min-height" {
					sum += pxTotal(val, false)
				}
			}
		}
	}
	return sum
}

// spacingOf adds up a tag's margins and padding (attributes and CSS).
func spacingOf(t token) float64 {
	var sum float64
	for _, a := range t.attrs {
		switch {
		case strings.HasPrefix(a.name, "padding") || strings.HasPrefix(a.name, "margin") || a.name == "inner-padding":
			sum += pxTotal(a.val, true)
		case a.name == "style":
			for prop, val := range declarations(a.val) {
				if strings.HasPrefix(prop, "padding") || strings.HasPrefix(prop, "margin") {
					sum += pxTotal(val, false)
				}
			}
		}
	}
	return sum
}

// declarations splits a style attribute into property → value.
func declarations(style string) map[string]string {
	out := map[string]string{}
	for _, decl := range strings.Split(strings.ToLower(style), ";") {
		if prop, val, ok := strings.Cut(decl, ":"); ok {
			out[strings.TrimSpace(prop)] = strings.TrimSpace(val)
		}
	}
	return out
}

// pxTotal adds up the lengths in a value, in px (percentages don't count).
func pxTotal(v string, unitlessPx bool) float64 {
	var sum float64
	for _, tok := range strings.Fields(funcCallRe.ReplaceAllString(strings.ToLower(v), " ")) {
		n, unit, ok := length(tok)
		if !ok {
			continue
		}
		switch unit {
		case "px":
			sum += n
		case "pt":
			sum += n * 4 / 3
		case "em", "rem":
			sum += n * 16
		case "":
			if unitlessPx {
				sum += n
			}
		}
	}
	return sum
}

// Slot markers go around the template's content when it is compiled, so the
// compiled HTML of the template alone can be found and checked.
const (
	slotStart = "klv-slot-start"
	slotEnd   = "klv-slot-end"
)

// compiledAllowed: what MJML itself makes of the allowed components, on top
// of the elements templates may write.
var compiledAllowed = map[string]bool{"tbody": true, "tr": true, "td": true, "table": true, "div": true, "p": true,
	"img": true, "a": true, "span": true, "br": true}

// compiledSlotIssues checks the compiled template exactly as email clients
// will parse it: every element between the slot markers must be one a
// template may produce, with sane attribute names.
func compiledSlotIssues(compiled string) []Issue {
	doc, err := parseHTML(compiled)
	if err != nil {
		return []Issue{errorf("bad_html", "the compiled email could not be read: %v", err)}
	}
	var issues []Issue
	seen := map[string]bool{}
	starts, ends := 0, 0
	in := false
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.CommentNode:
			switch strings.TrimSpace(n.Data) {
			case slotStart:
				starts++
				in = true
			case slotEnd:
				ends++
				in = false
			}
		case html.ElementNode:
			if in {
				if !allowedElements[n.Data] && !compiledAllowed[n.Data] && !seen[n.Data] {
					seen[n.Data] = true
					issues = append(issues, errorf("template_element", "the compiled template contains <%s>, which templates can't produce", n.Data))
				}
				for _, a := range n.Attr {
					name := a.Key
					if a.Namespace != "" {
						name = a.Namespace + ":" + name
					}
					if !validAttrName(name) && !seen["attr:"+name] {
						seen["attr:"+name] = true
						issues = append(issues, errorf("template_attr_name", "the compiled template has an attribute named %q", clip(name)))
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if starts != 1 || ends != 1 {
		issues = append(issues, errorf("template_breaks_layout",
			"the template changes the brand layout around it, usually through a tag it opens and never closes; close every tag you open"))
	}
	return issues
}
