// Package templates compiles MJML templates inside their brand's locked
// layout and checks the result before anything can be sent with it.
package templates

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// Size limits for what people paste into the editor.
const (
	MaxTemplateBytes = 200 << 10
	MaxLayoutBytes   = 100 << 10
)

// Compiled is a template ready to send: HTML and plain text, merge tags
// still in place.
type Compiled struct {
	HTML string
	Text string
}

// Compile puts content (the children of <mj-body>) into the layout at
// {{content}} and compiles the whole email. Problems the author can fix come
// back as issues; err is only for the compiler itself failing.
func Compile(ctx context.Context, layout, content string) (Compiled, []Issue, error) {
	if issues := sourceIssues(layout, content); len(issues) > 0 {
		return Compiled{}, issues, nil
	}
	before, after, _ := strings.Cut(layout, "{{"+ContentTag+"}}")
	doc := before + content + after

	out, err := mjml.ToHTML(ctx, doc, mjml.WithValidationLevel(mjml.Soft), mjml.WithMinify(false))
	var mErr mjml.Error
	switch {
	case errors.As(err, &mErr):
		return Compiled{}, mjmlIssues(mErr, before, content), nil
	case err != nil:
		return Compiled{}, nil, fmt.Errorf("compile mjml: %w", err)
	}
	return Compiled{HTML: out, Text: PlainText(out)}, nil, nil
}

// sourceIssues catches what MJML would accept but we must not.
func sourceIssues(layout, content string) []Issue {
	var issues []Issue
	switch n := strings.Count(layout, "{{"+ContentTag+"}}"); {
	case n == 0:
		issues = append(issues, errorf("layout_no_content", "the layout needs {{content}} where templates go"))
	case n > 1:
		issues = append(issues, errorf("layout_no_content", "the layout has {{content}} %d times; it must appear once", n))
	}
	lower := strings.ToLower(content)
	for _, tag := range []string{"<mjml", "<mj-body", "<mj-head"} {
		if strings.Contains(lower, tag) {
			issues = append(issues, errorf("template_has_wrapper",
				"a template holds only the body content (mj-section, mj-wrapper …); the brand layout provides %s>", tag))
		}
	}
	if strings.Contains(lower, "{{"+ContentTag) {
		issues = append(issues, errorf("template_has_content_tag", "{{content}} belongs in the layout, not in a template"))
	}
	if strings.Contains(strings.ToLower(layout), "<mj-include") || strings.Contains(lower, "<mj-include") {
		issues = append(issues, errorf("mj_include", "mj-include is not supported; paste the markup in instead"))
	}
	return issues
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
