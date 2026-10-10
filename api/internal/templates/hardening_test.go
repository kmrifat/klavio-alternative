package templates

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Regression tests for the PR #6 re-probe.

func TestElementCapIgnoresCase(t *testing.T) {
	for _, tag := range []string{"MJ-TEXT", "Mj-Text", "mJ-tExT"} {
		section := `<MJ-SECTION><Mj-Column><` + tag + `>x</` + tag + `></Mj-Column></MJ-SECTION>`
		content := strings.Repeat(section, MaxTemplateElements/3+1)
		if n := countElements(content); n <= MaxTemplateElements {
			t.Fatalf("%s: counted %d elements", tag, n)
		}
		start := time.Now()
		if got := buildCodes(t, content); !slices.Contains(got, "too_large") {
			t.Errorf("%s: got %v, want too_large", tag, got)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: refused only after %v; it must be refused before compiling", tag, d)
		}
	}
	// Mixed-case wrapper and closing tags are caught too.
	if got := buildCodes(t, body+`</MJ-WRAPPER>`); !slices.Contains(got, "template_unbalanced") {
		t.Errorf("uppercase stray closer: %v", got)
	}
	if got := buildCodes(t, `<MJ-STYLE>.klv-footer{display:none}</MJ-STYLE>`+body); !slices.Contains(got, "template_has_wrapper") {
		t.Errorf("uppercase mj-style: %v", got)
	}
}

func TestTemplateCannotCoverTheFooter(t *testing.T) {
	text := func(style string) string {
		return body + `<mj-section><mj-column><mj-text><div style="` + style + `">cover</div></mj-text></mj-column></mj-section>`
	}
	cases := map[string]string{
		"absolute overlay (review probe)": `<mj-raw><div style="position:absolute;top:0;width:100%;height:99999px;background:#f4f4f5;z-index:999"></div></mj-raw>`,
		"relative + z-index":              text("position:relative;z-index:9"),
		"fixed":                           text("POSITION: FIXED"),
		"negative margin-bottom":          text("margin-bottom:-400px"),
		"negative margin shorthand":       text("margin: 0 0 -400px 0"),
		"transform":                       text("transform:translateY(400px)"),
		"translate":                       text("translate: 0 400px"),
		"clip-path":                       text("clip-path:inset(0)"),
		"entity-encoded property":         text("&#112;osition:absolute"),
		"css escape":                      text(`posit\69 on:absolute`),
		"css comment":                     text("posi/**/tion:absolute"),
		"single quotes":                   strings.Replace(text("x"), `style="x"`, `style='z-index:99'`, 1),
		"negative mj padding":             `<mj-section padding-bottom="-400px"><mj-column><mj-text>x</mj-text></mj-column></mj-section>`,
		// Second re-probe: a collapsed box whose content overflows the
		// footer, painted on top through a new stacking context.
		"opacity in a zero-height box": body + `<mj-raw><div style="height:0px"><div style="opacity:0.99;height:900px;background:#f4f4f5"></div></div></mj-raw>`,
		"max-height:0":                 body + `<mj-raw><div style="max-height:0;overflow:visible"><div style="height:900px">x</div></div></mj-raw>`,
		"offset-path":                  text("offset-path:path('M0 0 L0 900')"),
		"zero line height, huge font":  text("line-height:0;font-size:900px"),
		"opacity alone":                text("opacity:0.5"),
		"visibility":                   text("visibility:hidden"),
		"background image":             text("background:url(https://brightside.co/x.png)"),
		"calc to go negative":          text("margin-top:calc(0px - 900px)"),
		"display none":                 text("display:none"),
		"not a declaration":            text("color:red;opacity"),
	}
	for name, content := range cases {
		if got := buildCodes(t, content); !slices.Contains(got, "template_css") {
			t.Errorf("%s: got %v, want template_css", name, got)
		}
	}
	// Ordinary styling is fine.
	if got := buildCodes(t, text("color:#c2410c;font-weight:bold;margin:0 0 12px;padding:4px 8px;background:#fff7ed")); len(got) != 0 {
		t.Errorf("plain styles: %v", got)
	}

	// SVG, MathML and embedded media are refused outright.
	for _, el := range []string{
		`<svg width="1" height="1" overflow="visible" opacity="0.99"><rect width="600" height="900" transform="translate(0 400)" /></svg>`,
		`<math><mi>x</mi></math>`, `<video src="https://brightside.co/v.mp4"></video>`, `<canvas></canvas>`,
	} {
		if got := buildCodes(t, body+`<mj-raw>`+el+`</mj-raw>`); !slices.Contains(got, "template_style") {
			t.Errorf("%.30s: got %v, want template_style", el, got)
		}
	}
}

// A template of the kind email builders produce must pass the allowlist.
func TestHybridTemplatePasses(t *testing.T) {
	hybrid := `<mj-section><mj-column><mj-text>
  <div style="overflow:hidden;max-width:600px;margin:0 auto" data-style="ignored">
    <img src="https://brightside.co/a.png" alt="Jacket" width="120" style="float:left;height:120px;width:120px;margin:0 16px 8px 0;border-radius:6px">
    <p style="font:bold 16px/1.4 Helvetica, Arial, sans-serif;color:#18181b;margin:0 0 8px;letter-spacing:-0.2px">Waxed jacket</p>
    <p style="font-size:14px;line-height:22px;color:#52525b;text-align:left;mso-line-height-rule:exactly">Made to last, shipped in recycled
      packaging, and members get early access to the autumn range all week long.</p>
    <table style="border-collapse:collapse;width:100%"><tr><td style="padding:8px 0;border-top:1px solid #e4e4e7;vertical-align:top">£120</td></tr></table>
    <span style="display:inline-block;padding:10px 16px;background-color:#c2410c;color:#ffffff;border-radius:6px;text-decoration:none">Shop</span>
  </div>
</mj-text></mj-column></mj-section>`
	if got := buildCodes(t, hybrid); len(got) != 0 {
		t.Errorf("hybrid template: %v", got)
	}
}

func TestGmailClippingIsAnError(t *testing.T) {
	big := page(strings.Repeat("<p>long</p>", (GmailClipBytes-clipMargin)/11+200))
	issues := Check(big, cc)
	i := slices.IndexFunc(issues, func(is Issue) bool { return is.Code == "gmail_clipping" })
	if i < 0 || issues[i].Severity != SeverityError {
		t.Fatalf("want a gmail_clipping error, got %+v", issues)
	}
	if !HasErrors(issues) {
		t.Fatal("a clipped email must not be sendable")
	}
	if got := codes(Check(page(strings.Repeat("<p>long</p>", 1000)), cc)); slices.Contains(got, "gmail_clipping") {
		t.Errorf("an 11 KB email was flagged: %v", got)
	}
}

func TestPingAndSelfClosingDiv(t *testing.T) {
	if got := codes(Check(page(`<a href="https://shop.example.com/" ping="https://evil.example.net/t">Shop</a>`), cc)); !slices.Contains(got, "unsafe_html") {
		t.Errorf("ping: %v", got)
	}
	// <div/> opens a div in HTML; the slash doesn't close it.
	if got := buildCodes(t, `<mj-section><mj-column><mj-text><div style="color:red"/>x</mj-text></mj-column></mj-section>`); !slices.Contains(got, "template_unbalanced") {
		t.Errorf("self-closing div: %v", got)
	}
}
