package templates

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Regression tests for the PR #6 review: ways a template could escape its
// slot in the layout, or a link could slip past the allowlist.

func buildCodes(t *testing.T, content string) []string {
	t.Helper()
	_, issues, err := Build(context.Background(), DefaultLayout("#f97316"), content, cc)
	if err != nil {
		t.Fatal(err)
	}
	return codes(issues)
}

const body = `<mj-section><mj-column><mj-text>Hi {{first_name | there}}, the autumn range is in. Every piece is made to last,
  shipped in recycled packaging, and members get early access all week long.</mj-text></mj-column></mj-section>`

func TestTemplateCannotRemoveTheLayoutFooter(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{
			// The review's probe: comment out everything after the
			// template, then supply a hidden unsubscribe link and address.
			"comment swallows the footer",
			body + `<mj-section><mj-column><mj-text><a href="{{unsubscribe_url}}" style="display:none">u</a>` +
				`<span style="display:none">{{postal_address}}</span></mj-text></mj-column></mj-section><mj-raw><!--</mj-raw>`,
			"template_comment",
		},
		{"conditional comment", body + `<mj-raw><!--[if mso]><a href="https://bit.ly/evil">x</a><![endif]--></mj-raw>`, "template_comment"},
		{"CDATA", body + `<mj-raw><![CDATA[ x ]]></mj-raw>`, "template_comment"},
		{"unclosed hiding div", body + `<mj-raw><div style="display:none"></mj-raw>`, "template_unbalanced"},
		{"unclosed tiny-text span", `<mj-section><mj-column><mj-text><span style="font-size:0">` + body[len(`<mj-section><mj-column><mj-text>`):], "template_unbalanced"},
		{"raw-text element swallows the rest", body + `<mj-raw><xmp></mj-raw>`, "template_breaks_layout"},
		{"stray closing wrapper", body + `</mj-wrapper><mj-section><mj-column><mj-text>x</mj-text></mj-column></mj-section>`, "template_unbalanced"},
		{"stray closing body", body + `</mj-body>`, "template_has_wrapper"},
		{"never closed", `<mj-section><mj-column><mj-text>x</mj-text>`, "template_unbalanced"},
		{"stylesheet aimed at the footer", body + `<mj-raw><style>.klv-footer{display:none}</style></mj-raw>`, "template_style"},
		{"mj-style", `<mj-style>.klv-footer{display:none}</mj-style>` + body, "template_has_wrapper"},
		{"external stylesheet", body + `<mj-raw><link rel="stylesheet" href="https://evil.example/x.css"></mj-raw>`, "template_style"},
	}
	for _, c := range cases {
		if got := buildCodes(t, c.content); !slices.Contains(got, c.want) {
			t.Errorf("%s: got %v, want %s", c.name, got, c.want)
		}
	}
	// A template that closes everything it opens is fine.
	if got := buildCodes(t, body+`<mj-section><mj-column><mj-text><b>bold</b> and <span style="color:red">red</span></mj-text></mj-column></mj-section>`); len(got) != 0 {
		t.Errorf("balanced template: %v", got)
	}
}

func TestLayoutFooterRules(t *testing.T) {
	def := DefaultLayout("#f97316")
	cases := []struct {
		name, layout, want string
	}{
		{"no footer marker", strings.Replace(def, ` css-class="klv-footer"`, "", 1), "missing_footer"},
		{"two footers", strings.Replace(def, `<mj-section padding="24px 0 8px">`, `<mj-section padding="24px 0 8px" css-class="klv-footer">`, 1), "missing_footer"},
		{"tiny footer text", strings.Replace(def, `<mj-text align="center" font-size="12px"`, `<mj-text align="center" font-size="1px"`, 1), "missing_unsubscribe"},
		{"invisible unsubscribe link", strings.Replace(def, `style="color:#71717a">Unsubscribe`, `style="display:none">Unsubscribe`, 1), "missing_unsubscribe"},
		{"empty unsubscribe link", strings.Replace(def, `style="color:#71717a">Unsubscribe</a>`, `style="color:#71717a"></a>`, 1), "missing_unsubscribe"},
		{"unsubscribe outside the footer", strings.Replace(strings.Replace(def, `<a href="{{unsubscribe_url}}" style="color:#71717a">Unsubscribe</a>`, "", 1),
			`{{brand_name}}</mj-text>`, `{{brand_name}} <a href="{{unsubscribe_url}}">Unsubscribe</a></mj-text>`, 1), "missing_unsubscribe"},
		{"fallback on the unsubscribe link", strings.Replace(def, `href="{{unsubscribe_url}}"`, `href="{{unsubscribe_url | https://bit.ly/x}}"`, 1), "bad_link"},
	}
	for _, c := range cases {
		issues, err := CheckLayout(context.Background(), c.layout, cc)
		if err != nil {
			t.Fatal(err)
		}
		if got := codes(issues); !slices.Contains(got, c.want) {
			t.Errorf("%s: got %v, want %s", c.name, got, c.want)
		}
	}
	// Spaces inside merge tags are fine.
	spaced := strings.Replace(strings.Replace(def, "{{unsubscribe_url}}", "{{ unsubscribe_url }}", 1), "{{postal_address}}", "{{ postal_address }}", 1)
	if issues, _ := CheckLayout(context.Background(), spaced, cc); len(issues) != 0 {
		t.Errorf("spaced merge tags: %+v", issues)
	}
}

func TestLinksAreCheckedEverywhere(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		// The layout may keep conditional comments; their markup is checked.
		{"link in an Outlook-only block", `<!--[if mso]><a href="https://bit.ly/evil">x</a><![endif]-->`, "link_shortener"},
		{"VML image in an Outlook-only block", `<!--[if mso]><v:fill type="frame" src="javascript:alert(1)" /><![endif]-->`, "bad_image"},
		{"link in noscript", `<noscript><a href="https://evil.example.net/">x</a></noscript>`, "link_not_allowed"},
		{"image map area", `<map name="m"><area href="https://bit.ly/x" alt="x"></map>`, "link_shortener"},
		{"td background", `<table><tr><td background="javascript:alert(1)">x</td></tr></table>`, "bad_image"},
		{"css background", `<div style="background-image:url('http://brightside.co/bg.png')">x</div>`, "insecure_image"},
		{"image on a shortener", `<img src="https://bit.ly/pic" alt="x">`, "link_shortener"},
		{"relative image", `<img src="/hero.png" alt="x">`, "bad_image"},
		{"srcset", `<img src="https://brightside.co/a.png" srcset="http://brightside.co/a2.png 2x" alt="x">`, "insecure_image"},
	}
	for _, c := range cases {
		if got := codes(Check(page(c.body), cc)); !slices.Contains(got, c.want) {
			t.Errorf("%s: got %v, want %s", c.name, got, c.want)
		}
	}
}

func TestLimits(t *testing.T) {
	many := strings.Repeat(`<mj-section><mj-column><mj-text>x</mj-text></mj-column></mj-section>`, MaxTemplateElements/3+1)
	start := time.Now()
	if got := buildCodes(t, many); !slices.Contains(got, "too_large") {
		t.Errorf("element cap: %v", got)
	}
	if time.Since(start) > time.Second {
		t.Errorf("an over-limit template should be refused before compiling (took %v)", time.Since(start))
	}
	if got := buildCodes(t, strings.Repeat("x", MaxTemplateBytes+1)); !slices.Contains(got, "too_large") {
		t.Errorf("byte cap: %v", got)
	}
}

func TestCompilerBusy(t *testing.T) {
	old := compileQueue
	compileQueue = 50 * time.Millisecond
	defer func() { compileQueue = old }()
	for range compileSlots {
		slots <- struct{}{}
	}
	defer func() {
		for range compileSlots {
			<-slots
		}
	}()
	// A layout nobody compiled yet, so the cache can't answer.
	_, _, err := Build(context.Background(), DefaultLayout("#123456"), body, cc)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy with every slot taken, got %v", err)
	}
}
