// Package templates compiles MJML templates inside their brand's locked
// layout and checks the result before anything can be sent with it.
package templates

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Boostport/mjml-go"
)

// Severity: errors block sending, warnings are advice.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one problem found while compiling or checking.
type Issue struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	// Where: "template" or "layout" for MJML errors, with Line; empty otherwise.
	Where string `json:"where,omitempty"`
	Line  int    `json:"line,omitempty"`
}

func errorf(code, format string, args ...any) Issue {
	return Issue{Code: code, Severity: SeverityError, Message: fmt.Sprintf(format, args...)}
}

func warnf(code, format string, args ...any) Issue {
	return Issue{Code: code, Severity: SeverityWarning, Message: fmt.Sprintf(format, args...)}
}

// Limits. MJML costs about 6 ms per element and can't be interrupted once
// started, so the element count is what keeps a compile short. A normal
// email has 30–80 elements.
const (
	MaxTemplateBytes    = 64 << 10
	MaxTemplateElements = 150
	MaxLayoutBytes      = 32 << 10
	MaxLayoutElements   = 80
	MaxCompiledBytes    = 512 << 10
)

// Compiles run at most this many at a time, process-wide, so one busy editor
// can't hold up everyone else's.
const compileSlots = 4

var (
	compileQueue   = 5 * time.Second  // wait for a free slot at most this long
	compileTimeout = 15 * time.Second // give up waiting for a result after this
)

var slots = make(chan struct{}, compileSlots)

// ErrBusy means every compile slot stayed taken; try again shortly.
var ErrBusy = errors.New("the template compiler is busy")

// ErrTimeout means a compile took too long. It keeps running in the
// background (MJML can't be stopped) but its slot is still counted.
var ErrTimeout = errors.New("compiling took too long")

// Compiled is a template ready to send: HTML and plain text, merge tags
// still in place.
type Compiled struct {
	HTML string
	Text string
}

// Compile puts content (the children of <mj-body>) into the layout at
// {{content}} and compiles the whole email. Problems the author can fix come
// back as issues; err is for the compiler being busy, slow or broken.
func Compile(ctx context.Context, layout, content string) (Compiled, []Issue, error) {
	issues := layoutSourceIssues(layout)
	if content != SampleContent {
		issues = append(issues, templateSourceIssues(content)...)
	}
	if len(issues) > 0 {
		return Compiled{}, issues, nil
	}
	before, after, _ := strings.Cut(layout, "{{"+ContentTag+"}}")

	out, err := runMJML(ctx, before+content+after)
	var mErr mjml.Error
	switch {
	case errors.As(err, &mErr):
		return Compiled{}, mjmlIssues(mErr, before, content), nil
	case err != nil:
		return Compiled{}, nil, err
	case len(out) > MaxCompiledBytes:
		return Compiled{}, []Issue{errorf("too_large", "the compiled email is %d KB; keep it under %d KB", len(out)>>10, MaxCompiledBytes>>10)}, nil
	}
	return Compiled{HTML: out, Text: PlainText(out)}, nil, nil
}

// runMJML compiles one document in a free slot.
func runMJML(ctx context.Context, doc string) (string, error) {
	wait := time.NewTimer(compileQueue)
	defer wait.Stop()
	select {
	case slots <- struct{}{}:
	case <-wait.C:
		return "", ErrBusy
	case <-ctx.Done():
		return "", ctx.Err()
	}

	type result struct {
		html string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-slots }() // freed when MJML really finishes
		// Detached: MJML ignores cancellation, and the slot must stay
		// taken for as long as it runs.
		html, err := mjml.ToHTML(context.WithoutCancel(ctx), doc, mjml.WithValidationLevel(mjml.Soft), mjml.WithMinify(false))
		done <- result{html, err}
	}()

	limit := time.NewTimer(compileTimeout)
	defer limit.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			var mErr mjml.Error
			if !errors.As(r.err, &mErr) {
				return "", fmt.Errorf("compile mjml: %w", r.err)
			}
		}
		return r.html, r.err
	case <-limit.C:
		return "", ErrTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

var (
	mjTagRe   = regexp.MustCompile(`<(/?)(mj-[a-z-]+|mjml)\b[^>]*?(/?)>`)
	htmlTagRe = regexp.MustCompile(`(?i)<\s*(style|link|meta|base)\b`)
)

func layoutSourceIssues(layout string) []Issue {
	var issues []Issue
	switch {
	case len(layout) > MaxLayoutBytes:
		return []Issue{errorf("too_large", "the layout is %d KB; keep it under %d KB", len(layout)>>10, MaxLayoutBytes>>10)}
	case countElements(layout) > MaxLayoutElements:
		return []Issue{errorf("too_large", "the layout has %d MJML elements; keep it under %d", countElements(layout), MaxLayoutElements)}
	}
	switch n := strings.Count(layout, "{{"+ContentTag+"}}"); {
	case n == 0:
		issues = append(issues, errorf("layout_no_content", "the layout needs {{content}} where templates go"))
	case n > 1:
		issues = append(issues, errorf("layout_no_content", "the layout has {{content}} %d times; it must appear once", n))
	}
	if strings.Contains(strings.ToLower(layout), "<mj-include") {
		issues = append(issues, errorf("mj_include", "mj-include is not supported; paste the markup in instead"))
	}
	return issues
}

// headOnly are MJML elements that belong to the layout's <mj-head>.
var headOnly = map[string]bool{
	"mjml": true, "mj-body": true, "mj-head": true, "mj-attributes": true, "mj-style": true, "mj-font": true,
	"mj-breakpoint": true, "mj-html-attributes": true, "mj-title": true, "mj-preview": true, "mj-include": true,
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
	lower := strings.ToLower(content)
	if strings.Contains(lower, "<!--") || strings.Contains(lower, "<![") {
		issues = append(issues, errorf("template_comment", "templates can't contain HTML comments or <![…]> blocks; remove them"))
	}
	if strings.Contains(lower, "{{"+ContentTag) {
		issues = append(issues, errorf("template_has_content_tag", "{{content}} belongs in the layout, not in a template"))
	}
	if m := htmlTagRe.FindStringSubmatch(content); m != nil {
		issues = append(issues, errorf("template_style", "templates can't add <%s> elements; the brand layout sets styles", strings.ToLower(m[1])))
	}

	var stack []string
	reported := map[string]bool{}
	for _, m := range mjTagRe.FindAllStringSubmatch(lower, -1) {
		closing, name, selfClosing := m[1] == "/", m[2], m[3] == "/"
		if headOnly[name] {
			if !reported[name] {
				reported[name] = true
				issues = append(issues, errorf("template_has_wrapper",
					"<%s> belongs to the brand layout; a template holds only body content (mj-section, mj-wrapper …)", name))
			}
			continue
		}
		switch {
		case selfClosing:
		case !closing:
			stack = append(stack, name)
		case len(stack) == 0 || stack[len(stack)-1] != name:
			return append(issues, errorf("template_unbalanced", "</%s> closes something this template didn't open", name))
		default:
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		issues = append(issues, errorf("template_unbalanced", "<%s> is never closed", stack[len(stack)-1]))
	}
	return append(issues, htmlBalance(lower)...)
}

// Tags that must be closed as often as they're opened. Browsers confine an
// unclosed one to its table cell, but not every email client does, and an
// unclosed <div style="display:none"> could then swallow the footer.
// Tags whose closing is commonly left out (<p>, <li>) aren't counted.
var balancedTags = []string{"a", "b", "blockquote", "center", "div", "em", "font", "h1", "h2", "h3", "h4", "h5", "h6",
	"i", "ol", "s", "small", "span", "strong", "sub", "sup", "table", "tbody", "td", "th", "thead", "tr", "u", "ul"}

var htmlOpenRe = regexp.MustCompile(`<([a-z][a-z0-9]*)\b[^>]*?(/?)>`)
var htmlCloseRe = regexp.MustCompile(`</([a-z][a-z0-9]*)\s*>`)

func htmlBalance(lower string) []Issue {
	open := map[string]int{}
	for _, m := range htmlOpenRe.FindAllStringSubmatch(lower, -1) {
		if m[2] != "/" {
			open[m[1]]++
		}
	}
	for _, m := range htmlCloseRe.FindAllStringSubmatch(lower, -1) {
		open[m[1]]--
	}
	var issues []Issue
	for _, tag := range balancedTags {
		switch n := open[tag]; {
		case n > 0:
			issues = append(issues, errorf("template_unbalanced", "<%s> is opened %d more time(s) than it is closed; close every tag you open", tag, n))
		case n < 0:
			issues = append(issues, errorf("template_unbalanced", "</%s> appears %d more time(s) than <%s>; it would close part of the layout", tag, -n, tag))
		}
	}
	return issues
}

func countElements(s string) int {
	n := 0
	for _, m := range mjTagRe.FindAllStringSubmatch(s, -1) {
		if m[1] != "/" {
			n++
		}
	}
	return n
}

// mjmlIssues turns MJML validation errors into issues, with line numbers
// pointing at the template or the layout rather than the combined document.
func mjmlIssues(e mjml.Error, layoutBefore, content string) []Issue {
	if len(e.Details) == 0 {
		return []Issue{errorf("mjml", "%s", e.Message)}
	}
	contentStart := strings.Count(layoutBefore, "\n") + 1
	contentLines := strings.Count(content, "\n")
	issues := make([]Issue, 0, len(e.Details))
	for _, d := range e.Details {
		is := errorf("mjml", "%s", d.Message)
		switch {
		case d.Line >= contentStart && d.Line <= contentStart+contentLines:
			is.Where, is.Line = "template", d.Line-contentStart+1
		case d.Line > contentStart+contentLines:
			is.Where, is.Line = "layout", d.Line-contentLines
		default:
			is.Where, is.Line = "layout", d.Line
		}
		issues = append(issues, is)
	}
	return issues
}

// HasErrors reports whether any issue blocks sending.
func HasErrors(issues []Issue) bool {
	for _, is := range issues {
		if is.Severity == SeverityError {
			return true
		}
	}
	return false
}
