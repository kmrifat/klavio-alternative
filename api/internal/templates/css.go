package templates

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Template markup is checked against allowlists: HTML elements, their size
// attributes, MJML attribute values, and inline CSS properties. Anything
// that can put a template's content on top of the layout's footer, or push
// the footer out of reach, must not get through, and only a list of what is
// safe can be complete. (A denylist missed <dialog>, which the browser's own
// stylesheet positions absolutely.)

// allowedElements are the HTML elements a template may use inside MJML.
var allowedElements = map[string]bool{
	"p": true, "div": true, "span": true, "a": true, "strong": true, "b": true, "em": true, "i": true, "u": true,
	"s": true, "br": true, "hr": true, "img": true, "table": true, "thead": true, "tbody": true, "tfoot": true,
	"tr": true, "td": true, "th": true, "ul": true, "ol": true, "li": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "blockquote": true, "sup": true, "sub": true, "small": true,
	"center": true, "font": true,
}

// refusedOutright are reported as template_style by templateSourceIssues;
// the element check skips them so they aren't reported twice.
var refusedOutright = map[string]bool{
	"style": true, "link": true, "meta": true, "base": true, "svg": true, "math": true, "object": true, "embed": true,
	"iframe": true, "frame": true, "frameset": true, "video": true, "audio": true, "canvas": true, "form": true,
	"input": true, "button": true, "select": true, "textarea": true, "script": true, "noscript": true,
	"template": true, "portal": true, "applet": true,
}

// Size caps, in px. Bigger values can push the footer out of reach.
const (
	maxBoxPx    = 2000 // heights, margins, padding
	maxWidthPx  = 1200
	maxBorderPx = 50
	maxRadiusPx = 200
)

// lengthRule checks every length in a value: no negatives, known units
// only, and nothing over maxPx. Percentages only if pct. With unitlessPx
// (HTML and MJML attributes), "600" means 600px.
func lengthRule(maxPx float64, pct, unitlessPx bool) func(string) bool {
	return func(v string) bool { return lengthsOK(v, maxPx, pct, unitlessPx) }
}

var funcCallRe = regexp.MustCompile(`[a-z-]+\([^)]*\)`) // rgb(…), hsl(…): colours, not lengths

func lengthsOK(v string, maxPx float64, pct, unitlessPx bool) bool {
	v = funcCallRe.ReplaceAllString(strings.ToLower(v), " ")
	for _, tok := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' || r == '/' || r == '\t' || r == '\n' }) {
		if c := tok[0]; !(c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+') {
			continue // a keyword or colour: solid, auto, #fff
		}
		n, unit, ok := length(tok)
		if !ok {
			return false
		}
		px := n
		switch unit {
		case "px":
		case "pt":
			px = n * 4 / 3
		case "em", "rem":
			px = n * 16
		case "%":
			if !pct || n > 100 {
				return false
			}
			continue
		case "":
			if n != 0 && !unitlessPx {
				return false
			}
		default:
			return false // vw, vh, cm, in …
		}
		if px > maxPx {
			return false
		}
	}
	return true
}

// --- CSS ---

// ruleCtx is where a declaration appears: the element and the other
// declarations of the same style attribute.
type ruleCtx struct {
	tag   string
	decls map[string]string
}

type valueRule func(c ruleCtx, val string) bool

func lengths(maxPx float64, pct bool) valueRule {
	return func(_ ruleCtx, v string) bool { return lengthsOK(v, maxPx, pct, false) }
}

var allowedCSS = map[string]valueRule{
	"color": plain, "background-color": plain,
	// The shorthand only as a colour: no images, no positions.
	"background": plain,

	"font": fontShorthand, "font-family": plain, "font-size": fontSize, "font-weight": plain,
	"font-style": plain, "font-variant": plain,
	"text-align": plain, "text-decoration": plain, "text-transform": plain, "text-indent": lengths(200, true),
	"letter-spacing": smallSpacing, "word-spacing": smallSpacing, "line-height": lineHeight,
	"white-space": plain, "word-break": plain, "word-wrap": plain, "overflow-wrap": plain, "hyphens": plain,
	"vertical-align": oneOf("baseline", "sub", "super", "text-top", "text-bottom", "middle", "top", "bottom"),
	"direction":      oneOf("ltr", "rtl"),

	"padding": lengths(maxBoxPx, true), "padding-top": lengths(maxBoxPx, true), "padding-right": lengths(maxBoxPx, true),
	"padding-bottom": lengths(maxBoxPx, true), "padding-left": lengths(maxBoxPx, true),
	"margin": lengths(maxBoxPx, true), "margin-top": lengths(maxBoxPx, true), "margin-right": lengths(maxBoxPx, true),
	"margin-bottom": lengths(maxBoxPx, true), "margin-left": lengths(maxBoxPx, true),

	"border": lengths(maxBorderPx, false), "border-top": lengths(maxBorderPx, false), "border-right": lengths(maxBorderPx, false),
	"border-bottom": lengths(maxBorderPx, false), "border-left": lengths(maxBorderPx, false),
	"border-width": lengths(maxBorderPx, false), "border-style": plain, "border-color": plain,
	"border-radius": lengths(maxRadiusPx, true), "border-collapse": plain, "border-spacing": lengths(maxBorderPx, false),

	"width": lengths(maxWidthPx, true), "min-width": lengths(maxWidthPx, true), "max-width": lengths(maxWidthPx, true),
	"min-height": lengths(maxBoxPx, true), "height": boxHeight, "max-height": boxHeight,

	"display": oneOf("block", "inline", "inline-block", "table", "table-row", "table-cell", "list-item", "none"),
	"float":   oneOf("left", "right", "none"),
	// Clipping hides a box's own overflow; it can't paint over anything.
	"overflow": oneOf("hidden", "visible"), "overflow-x": oneOf("hidden", "visible"), "overflow-y": oneOf("hidden", "visible"),

	"table-layout": plain, "box-sizing": plain, "list-style": plain, "list-style-type": plain,
	"list-style-position": plain, "cursor": plain,
	// Client fixes found in most email boilerplate, and the hidden-preheader
	// pattern. Hiding template content can't hide the footer: the footer's
	// own visibility is checked separately.
	"-webkit-text-size-adjust": plain, "-ms-text-size-adjust": plain, "-webkit-font-smoothing": plain,
	"mso-line-height-rule": plain, "mso-padding-alt": lengths(maxBoxPx, false), "mso-table-lspace": lengths(maxBorderPx, false),
	"mso-table-rspace": lengths(maxBorderPx, false), "mso-hide": oneOf("all", "none"),
}

// tableTags grow to fit their content, so a height on them is a minimum.
var tableTags = map[string]bool{"table": true, "tr": true, "td": true, "th": true, "img": true}

// boxHeight: a box shorter than its content lets the content spill onto
// whatever follows, the footer included. Heights are for images and table
// cells (which grow to fit), or for boxes that clip with overflow:hidden.
func boxHeight(c ruleCtx, v string) bool {
	if !lengthsOK(v, maxBoxPx, true, false) {
		return false
	}
	if v == "auto" || v == "none" || tableTags[c.tag] {
		return true
	}
	return c.decls["overflow"] == "hidden" || c.decls["overflow-y"] == "hidden"
}

var (
	// A tag and its attributes; quoted values may contain ">".
	tagRe = regexp.MustCompile(`(?s)<([a-zA-Z][a-zA-Z0-9:-]*)((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	// One attribute: name="v", name='v', name=v or a bare name.
	attrRe = regexp.MustCompile(`(?s)([^\s=/"'<>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+)))?`)
	// Any MJML attribute with a negative value (padding="-40px").
	negativeLenRe = regexp.MustCompile(`(^|[\s,(])-\s*[0-9.]`)
	numberRe      = regexp.MustCompile(`^[+]?([0-9]*\.?[0-9]+)([a-z%]*)$`)
)

type htmlAttr struct{ name, val string }

func parseAttrs(s string) []htmlAttr {
	var out []htmlAttr
	for _, m := range attrRe.FindAllStringSubmatch(s, -1) {
		out = append(out, htmlAttr{strings.ToLower(m[1]), html.UnescapeString(m[2] + m[3] + m[4])})
	}
	return out
}

// styleIssues checks every element, attribute and style in a template.
func styleIssues(content string) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	add := func(code, format string, args ...any) {
		is := errorf(code, format, args...)
		if !seen[is.Message] {
			seen[is.Message] = true
			issues = append(issues, is)
		}
	}
	for _, t := range tagRe.FindAllStringSubmatch(content, -1) {
		tag := strings.ToLower(t[1])
		as := parseAttrs(t[2])
		switch {
		case strings.HasPrefix(tag, "mj-") || tag == "mjml":
			mjAttrIssues(tag, as, add)
		case refusedOutright[tag]:
			continue // already reported
		case !allowedElements[tag]:
			add("template_element", "templates can't contain <%s>; use p, div, span, a, img, tables, lists, headings and text formatting", tag)
			continue
		default:
			htmlAttrIssues(tag, as, add)
		}
		for _, a := range as {
			if a.name == "style" {
				cssIssues(tag, a.val, add)
			}
		}
	}
	return issues
}

// MJML attribute rules by name. Every value is also checked for negatives.
func mjAttrIssues(tag string, as []htmlAttr, add func(string, string, ...any)) {
	for _, a := range as {
		v := strings.ToLower(strings.TrimSpace(a.val))
		bad := func(why string) {
			add("template_css", "<%s %s=%q> isn't allowed in a template: %s", tag, a.name, clip(a.val), why)
		}
		switch {
		case negativeLenRe.MatchString(v):
			bad("negative values can pull content over the brand layout")
		case unsafeValue(v):
			bad("functions like calc() aren't allowed")
		case a.name == "height" || strings.HasSuffix(a.name, "-height") && a.name != "line-height":
			// Only images and spacers have a fixed height; on anything
			// with text, content would spill over what follows.
			if tag != "mj-image" && tag != "mj-spacer" && v != "auto" && v != "" {
				bad("only mj-image and mj-spacer take a height")
			} else if !lengthsOK(v, maxBoxPx, true, true) {
				bad("keep heights under 2000px")
			}
		case strings.Contains(a.name, "border-radius"):
			if !lengthsOK(v, maxRadiusPx, true, true) {
				bad("keep radii under 200px")
			}
		case strings.HasPrefix(a.name, "border"):
			if !lengthsOK(v, maxBorderPx, false, true) {
				bad("keep borders under 50px")
			}
		case strings.Contains(a.name, "width"):
			if !lengthsOK(v, maxWidthPx, true, true) {
				bad("keep widths under 1200px")
			}
		case strings.HasPrefix(a.name, "padding") || strings.HasPrefix(a.name, "margin") || a.name == "inner-padding":
			if !lengthsOK(v, maxBoxPx, true, true) {
				bad("keep spacing under 2000px")
			}
		case a.name == "font-size":
			if !fontSize(ruleCtx{}, v) {
				bad("keep text between 6px and 96px")
			}
		case a.name == "line-height":
			if !lineHeight(ruleCtx{}, v) {
				bad("use 0.8–3, 8px–150px or 80%–300%")
			}
		case a.name == "letter-spacing":
			if !smallSpacing(ruleCtx{}, v) {
				bad("keep it within ±5px")
			}
		}
	}
}

// HTML size attributes, by element.
func htmlAttrIssues(tag string, as []htmlAttr, add func(string, string, ...any)) {
	for _, a := range as {
		v := strings.ToLower(strings.TrimSpace(a.val))
		bad := func(why string) {
			add("template_css", "<%s %s=%q> isn't allowed in a template: %s", tag, a.name, clip(a.val), why)
		}
		switch a.name {
		case "width":
			if !lengthsOK(v, maxWidthPx, true, true) {
				bad("keep widths under 1200px")
			}
		case "height":
			if !tableTags[tag] {
				bad("only images and table cells take a height attribute")
			} else if !lengthsOK(v, maxBoxPx, true, true) {
				bad("keep heights under 2000px")
			}
		case "border", "cellpadding", "cellspacing":
			if !lengthsOK(v, maxBorderPx, false, true) {
				bad("keep it under 50px")
			}
		case "hspace", "vspace":
			if !lengthsOK(v, 100, false, true) {
				bad("keep it under 100px")
			}
		case "size":
			if tag == "font" && !fontSizeAttr(v) {
				bad("use a size from 1 to 7")
			}
		}
	}
}

func fontSizeAttr(v string) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(v, "+"), "-"))
	return err == nil && n >= 1 && n <= 7
}

// cssIssues checks one style attribute.
func cssIssues(tag, style string, add func(string, string, ...any)) {
	// Browsers decode entities before reading CSS (done by attrs): &#112;osition is position.
	css := strings.ToLower(style)
	if strings.ContainsAny(css, `\`) || strings.Contains(css, "/*") {
		add("template_css", "a style uses CSS escapes or comments; write plain CSS")
		return
	}
	decls := map[string]string{}
	var order []string
	for _, decl := range strings.Split(css, ";") {
		prop, val, ok := strings.Cut(decl, ":")
		prop = strings.TrimSpace(prop)
		if !ok {
			if prop != "" {
				add("template_css", "a style contains %q, which isn't a CSS declaration", clip(prop))
			}
			continue
		}
		decls[prop] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(val), "!important"))
		order = append(order, prop)
	}
	c := ruleCtx{tag: tag, decls: decls}
	for _, prop := range order {
		val := decls[prop]
		rule, allowed := allowedCSS[prop]
		switch {
		case !allowed:
			add("template_css", "templates can't use the CSS property %s; allowed are colours, fonts, text, spacing, borders and sizes", prop)
		case unsafeValue(val):
			add("template_css", "%s: %s uses a CSS function (url(), calc(), min() …), which templates can't use", prop, clip(val))
		case !rule(c, val):
			add("template_css", "%s: %s isn't allowed in a template (%s)", prop, clip(val), why(prop))
		}
	}
}

func unsafeValue(v string) bool {
	for _, f := range []string{"url(", "var(", "calc(", "min(", "max(", "clamp(", "expression(", "attr(", "env(",
		"image(", "image-set(", "cross-fade(", "element(", "paint("} {
		if strings.Contains(v, f) {
			return true
		}
	}
	return false
}

func why(prop string) string {
	switch {
	case strings.Contains(prop, "height") && prop != "line-height":
		return "on boxes other than images and table cells a height needs overflow:hidden, or content spills over what follows; heights stay under 2000px"
	case prop == "font-size" || prop == "font":
		return "keep text between 6px and 96px"
	case prop == "line-height":
		return "use 0.8–3, 8px–150px or 80%–300%"
	case strings.HasSuffix(prop, "spacing") && !strings.HasPrefix(prop, "border"):
		return "keep it within ±5px"
	case strings.HasPrefix(prop, "border"):
		return "no negatives; borders under 50px, radii under 200px"
	case strings.Contains(prop, "width"):
		return "no negatives; widths under 1200px or 100%"
	case prop == "display":
		return "use block, inline, inline-block, none or a table display"
	case prop == "vertical-align":
		return "use a keyword such as top, middle or bottom"
	}
	return "no negative values, and spacing under 2000px"
}

func plain(_ ruleCtx, _ string) bool { return true }

func oneOf(vals ...string) valueRule {
	return func(_ ruleCtx, v string) bool {
		for _, ok := range vals {
			if v == ok {
				return true
			}
		}
		return false
	}
}

// length parses "12px" → (12, "px"). Unitless numbers have unit "".
func length(v string) (float64, string, bool) {
	m := numberRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	return n, m[2], err == nil
}

func fontSize(_ ruleCtx, v string) bool {
	switch v {
	case "xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large", "smaller", "larger":
		return true
	}
	n, unit, ok := length(v)
	switch {
	case !ok:
		return false
	case unit == "px" || unit == "pt":
		return n >= 6 && n <= 96
	case unit == "em" || unit == "rem":
		return n >= 0.4 && n <= 6
	case unit == "%":
		return n >= 40 && n <= 600
	}
	return false
}

func lineHeight(_ ruleCtx, v string) bool {
	if v == "normal" {
		return true
	}
	n, unit, ok := length(v)
	switch {
	case !ok:
		return false
	case unit == "":
		return n >= 0.8 && n <= 3
	case unit == "px":
		return n >= 8 && n <= 150
	case unit == "em" || unit == "rem":
		return n >= 0.8 && n <= 3
	case unit == "%":
		return n >= 80 && n <= 300
	}
	return false
}

func smallSpacing(_ ruleCtx, v string) bool {
	if v == "normal" {
		return true
	}
	n, unit, ok := length(strings.TrimPrefix(v, "-"))
	switch {
	case !ok:
		return false
	case unit == "px":
		return n <= 5
	case unit == "em" || unit == "rem":
		return n <= 0.3
	}
	return n == 0
}

// fontShorthand checks the size and line height inside "bold 14px/1.4 Arial".
func fontShorthand(c ruleCtx, v string) bool {
	for _, part := range strings.Fields(v) {
		size, lh, hasLH := strings.Cut(part, "/")
		if _, _, ok := length(size); !ok {
			continue
		}
		if !fontSize(c, size) {
			return false
		}
		if hasLH && !lineHeight(c, lh) {
			return false
		}
	}
	return true
}
