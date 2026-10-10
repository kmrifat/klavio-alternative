package templates

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Inline CSS in templates is an allowlist. Anything that can paint a
// template's content on top of the layout's footer (a new stacking context
// from opacity, transforms, filters or positioning; offset paths; collapsed
// boxes whose content overflows) must not get through, and listing what is
// safe is the only list that can be complete.
//
// Each allowed property has a value rule; a value that fails it is refused.

type valueRule func(tag, val string) bool

var allowedCSS = map[string]valueRule{
	"color": plain, "background-color": plain,
	// The shorthand only as a colour: no images, no positions.
	"background": plain,

	"font": fontShorthand, "font-family": plain, "font-size": fontSize, "font-weight": plain,
	"font-style": plain, "font-variant": plain,
	"text-align": plain, "text-decoration": plain, "text-transform": plain, "text-indent": nonNegative,
	"letter-spacing": smallSpacing, "word-spacing": smallSpacing, "line-height": lineHeight,
	"white-space": plain, "word-break": plain, "word-wrap": plain, "overflow-wrap": plain, "hyphens": plain,
	"vertical-align": plain, "direction": plain,

	"padding": nonNegative, "padding-top": nonNegative, "padding-right": nonNegative,
	"padding-bottom": nonNegative, "padding-left": nonNegative,
	"margin": nonNegative, "margin-top": nonNegative, "margin-right": nonNegative,
	"margin-bottom": nonNegative, "margin-left": nonNegative,

	"border": plain, "border-top": plain, "border-right": plain, "border-bottom": plain, "border-left": plain,
	"border-width": nonNegative, "border-style": plain, "border-color": plain,
	"border-radius": nonNegative, "border-collapse": plain, "border-spacing": nonNegative,

	"width": nonNegative, "min-width": nonNegative, "max-width": nonNegative,
	// A box that is shorter than its content lets the content overflow
	// onto whatever comes next, so heights are for images, or not tiny.
	"height": boxHeight, "min-height": boxHeight, "max-height": boxHeight,

	"display": oneOf("block", "inline", "inline-block", "table", "table-row", "table-cell", "list-item"),
	"float":   oneOf("left", "right", "none"),
	// Clipping hides a box's own overflow; it can't paint over anything.
	"overflow": oneOf("hidden", "visible"), "overflow-x": oneOf("hidden", "visible"), "overflow-y": oneOf("hidden", "visible"),

	"table-layout": plain, "box-sizing": plain, "list-style": plain, "list-style-type": plain,
	"list-style-position": plain, "cursor": plain,
	// Client fixes found in most email boilerplate.
	"-webkit-text-size-adjust": plain, "-ms-text-size-adjust": plain, "-webkit-font-smoothing": plain,
	"mso-line-height-rule": plain, "mso-padding-alt": nonNegative, "mso-table-lspace": nonNegative,
	"mso-table-rspace": nonNegative,
}

var (
	// A tag and its attributes; quoted values may contain ">".
	tagRe = regexp.MustCompile(`(?s)<([a-zA-Z][a-zA-Z0-9:-]*)((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	// The style attribute exactly: not data-style or mystyle.
	styleAttrRe = regexp.MustCompile(`(?is)(?:^|\s)style\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))`)
	// Any MJML attribute with a negative value (padding="-40px").
	negativeAttrRe = regexp.MustCompile(`(?is)\s([a-z-]+)\s*=\s*["']?\s*-\s*[0-9.]`)
	negativeLenRe  = regexp.MustCompile(`(^|[\s,(])-\s*[0-9.]`)
	numberRe       = regexp.MustCompile(`^([0-9]*\.?[0-9]+)([a-z%]*)$`)
)

// styleIssues checks every style attribute in a template, and the values of
// MJML attributes.
func styleIssues(content string) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	add := func(format string, args ...any) {
		is := errorf("template_css", format, args...)
		if !seen[is.Message] {
			seen[is.Message] = true
			issues = append(issues, is)
		}
	}
	for _, t := range tagRe.FindAllStringSubmatch(content, -1) {
		tag, attrs := strings.ToLower(t[1]), t[2]
		if strings.HasPrefix(tag, "mj-") {
			for _, m := range negativeAttrRe.FindAllStringSubmatch(attrs, -1) {
				add("templates can't use negative values (%s): they can pull content over the brand layout", strings.ToLower(m[1]))
			}
		}
		for _, m := range styleAttrRe.FindAllStringSubmatch(attrs, -1) {
			// Browsers decode entities before reading CSS: &#112;osition is position.
			css := strings.ToLower(html.UnescapeString(m[1] + m[2] + m[3]))
			if strings.ContainsAny(css, `\`) || strings.Contains(css, "/*") {
				add("a style uses CSS escapes or comments; write plain CSS")
				continue
			}
			for _, decl := range strings.Split(css, ";") {
				prop, val, ok := strings.Cut(decl, ":")
				prop = strings.TrimSpace(prop)
				if !ok {
					if prop != "" {
						add("a style contains %q, which isn't a CSS declaration", clip(prop))
					}
					continue
				}
				val = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(val), "!important"))
				rule, allowed := allowedCSS[prop]
				switch {
				case !allowed:
					add("templates can't use the CSS property %s; allowed are colours, fonts, text, spacing, borders and sizes", prop)
				case unsafeValue(val):
					add("%s: %s uses url(), var(), calc() or expression(), which templates can't use", prop, clip(val))
				case !rule(tag, val):
					add("%s: %s isn't allowed in a template (%s)", prop, clip(val), why(prop))
				}
			}
		}
	}
	return issues
}

func unsafeValue(v string) bool {
	for _, f := range []string{"url(", "var(", "calc(", "expression(", "attr(", "env(", "image(", "element("} {
		if strings.Contains(v, f) {
			return true
		}
	}
	return false
}

func why(prop string) string {
	switch {
	case strings.Contains(prop, "height"):
		return "boxes shorter than 10px let content spill over what follows; images may use any height"
	case prop == "font-size" || prop == "font":
		return "keep text between 6px and 96px"
	case prop == "line-height":
		return "use 0.8–3, 8px–150px or 80%–300%"
	case strings.HasSuffix(prop, "spacing"):
		return "keep it within ±5px"
	case prop == "display":
		return "use block, inline, inline-block or a table display"
	}
	return "negative values can pull content over the brand layout"
}

func plain(_, _ string) bool { return true }

func oneOf(vals ...string) valueRule {
	return func(_, v string) bool {
		for _, ok := range vals {
			if v == ok {
				return true
			}
		}
		return false
	}
}

func nonNegative(_, v string) bool { return !negativeLenRe.MatchString(v) }

// length parses "12px" → (12, "px"). Unitless numbers have unit "".
func length(v string) (float64, string, bool) {
	m := numberRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	return n, m[2], err == nil
}

func boxHeight(tag, v string) bool {
	if !nonNegative(tag, v) {
		return false
	}
	if tag == "img" || v == "auto" || v == "none" {
		return true
	}
	n, unit, ok := length(v)
	return ok && unit == "px" && n >= 10
}

func fontSize(_, v string) bool {
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

func lineHeight(_, v string) bool {
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

func smallSpacing(_, v string) bool {
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
func fontShorthand(tag, v string) bool {
	for _, part := range strings.Fields(v) {
		size, lh, hasLH := strings.Cut(part, "/")
		if _, _, ok := length(size); !ok {
			continue
		}
		if !fontSize(tag, size) {
			return false
		}
		if hasLH && !lineHeight(tag, lh) {
			return false
		}
	}
	return true
}
