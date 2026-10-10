package templates

import (
	"slices"
	"strings"
	"testing"
)

// Fourth re-probe: the checks must read markup the way browsers do.

func TestQuotesInAttributeNames(t *testing.T) {
	dialog := `<dialog open style="width:600px;height:2000px;overflow:hidden;background-color:#f4f4f5;border:0;padding:0;margin:0"></dialog>`
	wrap := func(inner string) string {
		return body + `<mj-section><mj-column><mj-text>` + inner + `</mj-text></mj-column></mj-section>`
	}
	cases := map[string]string{
		"double quote in a span (review probe)": wrap(`<span x">` + dialog + `<span y">z</span></span>`),
		"single quote in a span":                wrap(`<span x'>` + dialog + `<span y'>z</span></span>`),
		"backtick in a span":                    wrap("<span x`>" + dialog + "<span y`>z</span></span>"),
		"quote in an mj-* tag":                  `<mj-section x"><mj-column><mj-text>` + dialog + `</mj-text></mj-column></mj-section>`,
		"quote in an mj-* tag, single":          `<mj-section x'><mj-column><mj-text>x</mj-text></mj-column></mj-section>`,
	}
	for name, content := range cases {
		got := buildCodes(t, content)
		if !slices.Contains(got, "template_attr_name") {
			t.Errorf("%s: got %v, want template_attr_name", name, got)
		}
	}
	// The dialog itself is now seen and refused.
	if got := buildCodes(t, cases["double quote in a span (review probe)"]); !slices.Contains(got, "template_element") {
		t.Errorf("hidden dialog: %v", got)
	}
	// Other odd names are refused too.
	for _, name := range []string{"x<y", "on=click", "a!b"} {
		if !validAttrNameRefuses(name) {
			t.Errorf("%q should be refused", name)
		}
	}
}

func validAttrNameRefuses(n string) bool { return !validAttrName(n) }

func TestStackedBoxesAreBudgeted(t *testing.T) {
	boxes := strings.Repeat(`<div style="height:2000px;overflow:hidden">x</div>`, 6)
	if got := buildCodes(t, `<mj-section><mj-column><mj-text>`+boxes+`</mj-text></mj-column></mj-section>`); !slices.Contains(got, "too_large") {
		t.Errorf("6 × 2000px clipped boxes: got %v, want too_large", got)
	}
	images := strings.Repeat(`<img src="https://brightside.co/a.png" alt="a" height="2000">`, 6)
	if got := buildCodes(t, `<mj-section><mj-column><mj-text>`+images+`</mj-text></mj-column></mj-section>`); !slices.Contains(got, "too_large") {
		t.Errorf("6 × 2000px images: got %v, want too_large", got)
	}
	spacing := strings.Repeat(`<p style="margin:0 0 1900px">x</p>`, 11)
	if got := buildCodes(t, `<mj-section><mj-column><mj-text>`+spacing+`</mj-text></mj-column></mj-section>`); !slices.Contains(got, "too_large") {
		t.Errorf("stacked margins: got %v, want too_large", got)
	}
	many := strings.Repeat(`<span>x</span>`, MaxHTMLElements+1)
	if got := buildCodes(t, `<mj-section><mj-column><mj-text>`+many+`</mj-text></mj-column></mj-section>`); !slices.Contains(got, "too_large") {
		t.Errorf("too many elements: got %v, want too_large", got)
	}
	// A normal long newsletter stays well inside the budgets.
	ok := strings.Repeat(`<mj-section padding="20px 0"><mj-column><mj-image src="https://brightside.co/a.png" alt="a" height="300px" /><mj-text padding="10px 25px">Story text that is long enough to read like a real paragraph in an email.</mj-text></mj-column></mj-section>`, 12)
	if got := buildCodes(t, ok); len(got) != 0 {
		t.Errorf("12-story newsletter: %v", got)
	}
}

func TestOnlySafeMJMLComponents(t *testing.T) {
	for _, comp := range []string{
		`<mj-hero background-url="https://brightside.co/h.png" height="600px"><mj-text>x</mj-text></mj-hero>`,
		`<mj-section><mj-column><mj-carousel><mj-carousel-image src="https://brightside.co/a.png" /></mj-carousel></mj-column></mj-section>`,
		`<mj-section><mj-column><mj-navbar><mj-navbar-link href="https://brightside.co">x</mj-navbar-link></mj-navbar></mj-column></mj-section>`,
	} {
		if got := buildCodes(t, body+comp); !slices.Contains(got, "template_element") {
			t.Errorf("%.30s: got %v, want template_element", comp, got)
		}
	}
}

func TestCompiledSlotIsChecked(t *testing.T) {
	page := func(slot string) string {
		return `<html><body><div class="klv-footer"></div><!--` + slotStart + `-->` + slot + `<!--` + slotEnd + `--></body></html>`
	}
	if got := codes(compiledSlotIssues(page(`<div><p>fine</p></div>`))); len(got) != 0 {
		t.Errorf("clean slot: %v", got)
	}
	if got := codes(compiledSlotIssues(page(`<div><dialog open>x</dialog></div>`))); !slices.Contains(got, "template_element") {
		t.Errorf("dialog in the compiled slot: %v", got)
	}
	if got := codes(compiledSlotIssues(`<html><body><p>no markers</p></body></html>`)); !slices.Contains(got, "template_breaks_layout") {
		t.Errorf("missing markers: %v", got)
	}
	// An element outside the slot (the layout's own) isn't the template's business.
	if got := codes(compiledSlotIssues(`<html><body><dialog></dialog>` + page(`<p>x</p>`)[len(`<html><body>`):])); len(got) != 0 {
		t.Errorf("layout element outside the slot: %v", got)
	}
}

// Fifth re-probe: MJML copies attribute values into the styles it writes.
func TestMJMLAttributesCantCarryCSS(t *testing.T) {
	overlay := "position:absolute;top:0;left:0;width:700px;height:3000px;background-color:#f4f4f5;z-index:9"
	text := func(attr string) string {
		return `<mj-section><mj-column><mj-text ` + attr + `>Hello there, a few words of real text for the checks to read.</mj-text></mj-column></mj-section>`
	}
	cases := map[string]string{
		"font-family (review probe)": text(`font-family="Arial;` + overlay + `"`),
		"font-size":                  text(`font-size="14px;` + overlay + `"`),
		"line-height":                text(`line-height="1.5;` + overlay + `"`),
		"letter-spacing":             text(`letter-spacing="1px;` + overlay + `"`),
		"align":                      text(`align="left;` + overlay + `"`),
		"padding":                    text(`padding="10px;` + overlay + `"`),
		"color":                      text(`color="red;` + overlay + `"`),
		"container colour":           text(`container-background-color="#fff;` + overlay + `"`),
		"entity-encoded semicolon":   text(`font-family="Arial&#59;position:absolute"`),
		"quote in a value":           text(`font-family='Arial" style="position:absolute'`),
		"braces":                     text(`font-family="Arial} .klv-footer{display:none"`),
		"css-class Outlook breakout": `<mj-section css-class='a" style="position:absolute;top:0;z-index:9'><mj-column><mj-text>x</mj-text></mj-column></mj-section>`,
		"button background url":      `<mj-section background-url="https://brightside.co/a.png');position:absolute;('"><mj-column><mj-text>x</mj-text></mj-column></mj-section>`,
		"image src with a quote":     `<mj-section><mj-column><mj-image src='https://brightside.co/a.png" style="position:absolute' alt="A" /></mj-column></mj-section>`,
	}
	for name, content := range cases {
		if got := buildCodes(t, body+content); !slices.Contains(got, "template_css") {
			t.Errorf("%s: got %v, want template_css", name, got)
		}
	}
	// Ordinary MJML attributes pass.
	fine := `<mj-section padding="20px 0" background-color="#fff7ed" css-class="hero-band"><mj-column width="50%" vertical-align="middle">` +
		`<mj-text font-family="Helvetica, Arial, sans-serif" font-size="16px" line-height="1.5" letter-spacing="0.2px" color="rgb(24, 24, 27)" align="center">Hello</mj-text>` +
		`<mj-image src="https://brightside.co/a.png" alt="Ann's jacket, in olive" href="https://shop.example.com/jacket?c=olive&amp;s=m" width="300px" />` +
		`<mj-button href="https://shop.example.com/autumn" background-color="#c2410c" border-radius="6px" inner-padding="12px 24px">Shop</mj-button>` +
		`</mj-column></mj-section>`
	if got := buildCodes(t, body+fine); len(got) != 0 {
		t.Errorf("ordinary MJML attributes: %v", got)
	}
}

func TestCompiledSlotStylesAreChecked(t *testing.T) {
	page := func(slot string) string {
		return `<html><body><div class="klv-footer"></div><!--` + slotStart + `-->` + slot + `<!--` + slotEnd + `--></body></html>`
	}
	cases := map[string]string{
		"position":                `<div style="font-family:Arial;position:absolute;top:0">x</div>`,
		"opacity":                 `<div style="opacity:0.99">x</div>`,
		"negative margin":         `<div style="margin-top:-900px">x</div>`,
		"huge padding":            `<table><tr><td style="padding-bottom:99999px">x</td></tr></table>`,
		"inside an Outlook block": `<!--[if mso | IE]><table class="a" style="position:absolute;top:0"><tr><td>x</td></tr></table><![endif]-->`,
		"transform-origin trick":  `<div style="transform-origin:0 0">x</div>`,
	}
	for name, slot := range cases {
		if got := codes(compiledSlotIssues(page(slot))); !slices.Contains(got, "template_css") {
			t.Errorf("%s: got %v, want template_css", name, got)
		}
	}
	// What MJML itself writes is fine.
	mjml := `<div style="margin:0px auto;max-width:600px;"><table align="center" border="0" cellpadding="0" cellspacing="0" role="presentation" style="width:100%;"><tbody><tr>` +
		`<td style="direction:ltr;font-size:0px;padding:20px 0;text-align:center;"><!--[if mso | IE]><table role="presentation" border="0" cellpadding="0" cellspacing="0"><tr><td class="" style="vertical-align:top;width:600px;" ><![endif]-->` +
		`<div class="mj-column-per-100 mj-outlook-group-fix" style="font-size:0px;text-align:left;direction:ltr;display:inline-block;vertical-align:top;width:100%;">x</div>` +
		`<!--[if mso | IE]></td></tr></table><![endif]--></td></tr></tbody></table></div>`
	if got := codes(compiledSlotIssues(page(mjml))); len(got) != 0 {
		t.Errorf("MJML's own output: %v", got)
	}
}
