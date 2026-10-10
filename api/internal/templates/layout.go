package templates

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"sync"

	nethtml "golang.org/x/net/html"
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
    <mj-section padding="16px 0 32px" css-class="%[2]s">
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
`, html.EscapeString(brandColor), FooterClass)
}

// SampleContent stands in for a template when a layout is compiled on its own.
const SampleContent = `<mj-section><mj-column><mj-text>Sample content: this is where each template goes. ` +
	`It needs enough real words to look like a normal email to the checks, so here are a few more of them.</mj-text></mj-column></mj-section>`

// layoutBuild is a layout compiled with SampleContent.
type layoutBuild struct {
	HTML   string
	Issues []Issue // compile problems; nil when it compiled
	// Footer is the footer as compiled: its ancestors and markup. A
	// template's build must reproduce it exactly.
	Footer string
}

// Compiled layouts are cached by source, so showing a layout or checking a
// template doesn't compile the same layout again.
var layoutCache = struct {
	sync.Mutex
	m     map[[32]byte]layoutBuild
	order [][32]byte
}{m: map[[32]byte]layoutBuild{}}

const layoutCacheSize = 256

func compileLayout(ctx context.Context, layout string) (layoutBuild, error) {
	key := sha256.Sum256([]byte(layout))
	layoutCache.Lock()
	lb, ok := layoutCache.m[key]
	layoutCache.Unlock()
	if ok {
		return lb, nil
	}

	c, issues, err := Compile(ctx, layout, SampleContent)
	if err != nil {
		return layoutBuild{}, err
	}
	lb = layoutBuild{HTML: c.HTML, Issues: issues}
	if len(issues) == 0 {
		lb.Footer, _ = footerPrint(c.HTML)
	}

	layoutCache.Lock()
	defer layoutCache.Unlock()
	if _, ok := layoutCache.m[key]; !ok {
		if len(layoutCache.order) >= layoutCacheSize {
			delete(layoutCache.m, layoutCache.order[0])
			layoutCache.order = layoutCache.order[1:]
		}
		layoutCache.m[key] = lb
		layoutCache.order = append(layoutCache.order, key)
	}
	return lb, nil
}

// footerPrint describes the single footer of a compiled email: the chain of
// elements around it and its own markup. ok is false without exactly one.
func footerPrint(compiled string) (string, bool) {
	doc, err := parseHTML(compiled)
	if err != nil {
		return "", false
	}
	footers := FindFooters(doc)
	if len(footers) != 1 {
		return "", false
	}
	var b strings.Builder
	for p := footers[0].Parent; p != nil; p = p.Parent {
		if p.Type != nethtml.ElementNode {
			continue
		}
		b.WriteString("<" + p.Data)
		attrs := slices.Clone(p.Attr)
		slices.SortFunc(attrs, func(x, y nethtml.Attribute) int { return strings.Compare(x.Key, y.Key) })
		for _, a := range attrs {
			b.WriteString(" " + a.Key + "=" + a.Val)
		}
		b.WriteString(">")
	}
	b.WriteString("\n")
	if err := nethtml.Render(&b, footers[0]); err != nil {
		return "", false
	}
	return b.String(), true
}

// CheckLayout compiles a layout with sample content and runs every check. A
// layout is locked brand-wide, so any error means it can't be saved.
func CheckLayout(ctx context.Context, layout string, cc CheckContext) ([]Issue, error) {
	lb, err := compileLayout(ctx, layout)
	if err != nil || len(lb.Issues) > 0 {
		return lb.Issues, err
	}
	return Check(lb.HTML, cc), nil
}

// LayoutPreview is the layout compiled with sample content ("" if it doesn't compile).
func LayoutPreview(ctx context.Context, layout string) (string, []Issue, error) {
	lb, err := compileLayout(ctx, layout)
	return lb.HTML, lb.Issues, err
}

// Build compiles a template in its layout and checks it.
func Build(ctx context.Context, layout, content string, cc CheckContext) (Compiled, []Issue, error) {
	lb, err := compileLayout(ctx, layout)
	if err != nil {
		return Compiled{}, nil, err
	}
	if len(lb.Issues) > 0 {
		return Compiled{}, []Issue{errorf("layout_broken", "the brand layout doesn't compile; a brand admin needs to fix it first")}, nil
	}
	c, issues, err := Compile(ctx, layout, content)
	if err != nil || len(issues) > 0 {
		return c, issues, err
	}
	// The footer must come through untouched: same markup, same elements
	// around it. Anything else means the template reached outside its slot
	// (an unclosed tag, a stray closing tag) and could hide or replace it.
	if fp, ok := footerPrint(c.HTML); !ok || fp != lb.Footer {
		return Compiled{}, []Issue{errorf("template_breaks_layout",
			"the template changes the brand layout around it, usually through a tag it opens and never closes; close every tag you open")}, nil
	}
	return c, Check(c.HTML, cc), nil
}
