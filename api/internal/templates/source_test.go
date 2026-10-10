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
