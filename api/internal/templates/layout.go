package templates

import (
	"context"
	"fmt"
	"html"
	"regexp"
)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// DefaultLayout is a brand's layout until a brand admin changes it: the
// brand name in its colour on top, and the footer every email needs.
func DefaultLayout(brandColor string) string {
	if !hexColor.MatchString(brandColor) {
		brandColor = "#4f46e5"
	}
	return fmt.Sprintf(`<mjml>
  <mj-head>
    <mj-attributes>
      <mj-all font-family="Helvetica, Arial, sans-serif" />
      <mj-text font-size="15px" line-height="1.6" color="#27272a" />
      <mj-button background-color="%[1]s" color="#ffffff" border-radius="6px" />
    </mj-attributes>
    <mj-preview>{{brand_name}}</mj-preview>
  </mj-head>
  <mj-body background-color="#f4f4f5">
    <mj-section padding="24px 0 8px">
      <mj-column>
        <mj-text align="center" font-size="20px" font-weight="bold" color="%[1]s">{{brand_name}}</mj-text>
      </mj-column>
    </mj-section>
    <mj-wrapper background-color="#ffffff" border-radius="8px" padding="8px 0">
{{content}}
    </mj-wrapper>
    <mj-section padding="16px 0 32px">
      <mj-column>
        <mj-text align="center" font-size="12px" color="#71717a">
          You're getting this because you signed up at {{brand_name}}.<br />
          <a href="{{preferences_url}}" style="color:#71717a">Email preferences</a> ·
          <a href="{{unsubscribe_url}}" style="color:#71717a">Unsubscribe</a><br />
          {{postal_address}}
        </mj-text>
      </mj-column>
    </mj-section>
  </mj-body>
</mjml>
`, html.EscapeString(brandColor))
}

// SampleContent stands in for a template when a layout is checked on its own.
const SampleContent = `<mj-section><mj-column><mj-text>Sample content: this is where each template goes. ` +
	`It needs enough real words to look like a normal email to the checks, so here are a few more of them.</mj-text></mj-column></mj-section>`

// CheckLayout compiles a layout with sample content and runs every check. A
// layout is locked brand-wide, so any error means it can't be saved.
func CheckLayout(ctx context.Context, layout string, cc CheckContext) ([]Issue, error) {
	if len(layout) > MaxLayoutBytes {
		return []Issue{errorf("too_large", "the layout is over %d KB", MaxLayoutBytes>>10)}, nil
	}
	c, issues, err := Compile(ctx, layout, SampleContent)
	if err != nil || len(issues) > 0 {
		return issues, err
	}
	return Check(c.HTML, cc), nil
}

// Build compiles a template in its layout and checks it.
func Build(ctx context.Context, layout, content string, cc CheckContext) (Compiled, []Issue, error) {
	if len(content) > MaxTemplateBytes {
		return Compiled{}, []Issue{errorf("too_large", "the template is over %d KB", MaxTemplateBytes>>10)}, nil
	}
	c, issues, err := Compile(ctx, layout, content)
	if err != nil || len(issues) > 0 {
		return c, issues, err
	}
	return c, Check(c.HTML, cc), nil
}
