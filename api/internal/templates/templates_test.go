package templates

import (
	"context"
	"slices"
	"strings"
	"testing"
)

var cc = CheckContext{
	Allowlist:     []string{"shop.example.com"},
	BrandDomains:  []string{"news.brightside.co", "go.brightside.co"},
	PostalAddress: "221B Market St, San Francisco",
}

const goodContent = `<mj-section><mj-column>
  <mj-image src="https://brightside.co/hero.png" alt="Autumn collection" />
  <mj-text>Hi {{first_name | there}}, our autumn range is in. Every piece is made to last and
    shipped in recycled packaging, and members get early access all week.</mj-text>
  <mj-button href="https://shop.example.com/autumn">Shop the range</mj-button>
</mj-column></mj-section>`

func codes(issues []Issue) []string {
	var out []string
	for _, is := range issues {
		out = append(out, is.Code)
	}
	return out
}

func TestBuildCleanTemplate(t *testing.T) {
	c, issues, err := Build(context.Background(), DefaultLayout("#f97316"), goodContent, cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("want no issues, got %+v", issues)
	}
	for _, want := range []string{`href="{{unsubscribe_url}}"`, "{{postal_address}}", "Shop the range", "#f97316"} {
		if !strings.Contains(c.HTML, want) {
			t.Errorf("compiled HTML missing %q", want)
		}
	}
	for _, want := range []string{"Hi {{first_name | there}}", "Shop the range (https://shop.example.com/autumn)", "Unsubscribe ({{unsubscribe_url}})", "Autumn collection"} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("plain text missing %q:\n%s", want, c.Text)
		}
	}
	if strings.Contains(c.Text, "font-family") {
		t.Errorf("plain text has CSS in it:\n%s", c.Text)
	}
}

func TestCheckLayout(t *testing.T) {
	if issues, err := CheckLayout(context.Background(), DefaultLayout("#f97316"), cc); err != nil || len(issues) != 0 {
		t.Fatalf("default layout: %v %+v", err, issues)
	}
	noFooter := strings.Replace(DefaultLayout("#f97316"), `<a href="{{unsubscribe_url}}" style="color:#71717a">Unsubscribe</a><br />`, "", 1)
	noFooter = strings.Replace(noFooter, "{{postal_address}}", "", 1)
	issues, _ := CheckLayout(context.Background(), noFooter, cc)
	if got := codes(issues); !slices.Contains(got, "missing_unsubscribe") || !slices.Contains(got, "missing_postal_address") {
		t.Fatalf("got %v", got)
	}
	issues, _ = CheckLayout(context.Background(), `<mjml><mj-body></mj-body></mjml>`, cc)
	if got := codes(issues); !slices.Equal(got, []string{"layout_no_content"}) {
		t.Fatalf("got %v", got)
	}
	// The literal brand address is as good as the merge tag.
	literal := strings.Replace(DefaultLayout("#f97316"), "{{postal_address}}", "221B Market St,\n San Francisco", 1)
	if issues, _ := CheckLayout(context.Background(), literal, cc); len(issues) != 0 {
		t.Fatalf("literal address: %+v", issues)
	}
}

func TestCompileErrorsPointAtTheRightFile(t *testing.T) {
	content := "<mj-section><mj-column>\n<mj-text>ok</mj-text>\n<mj-text foo=\"bar\">x</mj-text>\n</mj-column></mj-section>"
	_, issues, err := Compile(context.Background(), DefaultLayout("#f97316"), content)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Code != "mjml" || issues[0].Where != "template" || issues[0].Line != 3 {
		t.Fatalf("got %+v", issues)
	}

	layout := strings.Replace(DefaultLayout("#f97316"), `<mj-text align="center" font-size="12px"`, `<mj-text bogus="1" align="center" font-size="12px"`, 1)
	_, issues, _ = Compile(context.Background(), layout, content[:strings.Index(content, "\n<mj-text foo")]+"\n</mj-column></mj-section>")
	wantLine := 0
	for i, l := range strings.Split(layout, "\n") {
		if strings.Contains(l, "bogus") {
			wantLine = i + 1
		}
	}
	if len(issues) != 1 || issues[0].Where != "layout" || issues[0].Line != wantLine {
		t.Fatalf("got %+v, want layout line %d", issues, wantLine)
	}
}

func TestSourceIssues(t *testing.T) {
	cases := map[string]string{
		"<mjml><mj-body></mj-body></mjml>":      "template_has_wrapper",
		"<mj-section>{{content}}</mj-section>":  "template_has_content_tag",
		`<mj-include path="./footer.mjml" />`:   "template_has_wrapper",
		strings.Repeat("x", MaxTemplateBytes+1): "too_large",
	}
	for content, want := range cases {
		_, issues, err := Build(context.Background(), DefaultLayout("#000000"), content, cc)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(codes(issues), want) {
			t.Errorf("%.40q: got %v, want %s", content, codes(issues), want)
		}
	}
}

// page wraps body HTML with the parts every email needs, so each case only
// shows the problem it tests.
func page(body string) string {
	return `<html><head><style>p{color:red}</style></head><body>` + body +
		`<p>Plenty of real words here so that the email does not look image only to the checks at all, honestly.</p>` +
		footerHTML + `</body></html>`
}

const footerHTML = `<div class="klv-footer"><a href="{{unsubscribe_url}}">Unsubscribe</a> {{postal_address}}</div>`

func TestCheck(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string // issue codes; nil = clean
	}{
		{"clean", `<a href="https://shop.example.com/x">Shop</a>`, nil},
		{"subdomain of allowlisted", `<a href="https://eu.shop.example.com/x">Shop</a>`, nil},
		{"brand's own domain", `<a href="https://www.brightside.co/about">About</a>`, nil},
		{"mailto and tel", `<a href="mailto:help@brightside.co">Mail</a><a href="tel:+15550100">Call</a>`, nil},
		{"preferences tag", `<a href="{{ preferences_url }}">Prefs</a>`, nil},
		{"not allowlisted", `<a href="https://evil.example.net/">x</a>`, []string{"link_not_allowed"}},
		{"lookalike domain", `<a href="https://shop.example.com.evil.example.net/">x</a>`, []string{"link_not_allowed"}},
		{"shortener", `<a href="https://bit.ly/abc">x</a>`, []string{"link_shortener"}},
		{"http", `<a href="http://shop.example.com/">x</a>`, []string{"insecure_link"}},
		{"javascript", `<a href="javascript:alert(1)">x</a>`, []string{"unsafe_link"}},
		{"empty href", `<a href="">x</a>`, []string{"empty_link"}},
		{"non-url tag as href", `<a href="{{email}}">x</a>`, []string{"bad_link"}},
		{"missing alt", `<img src="https://brightside.co/a.png">`, []string{"missing_alt"}},
		{"empty alt", `<img src="https://brightside.co/a.png" alt=" ">`, []string{"missing_alt"}},
		{"spacer is fine", `<img src="https://brightside.co/s.gif" width="1" height="1">`, nil},
		{"http image", `<img src="http://brightside.co/a.png" alt="A">`, []string{"insecure_image"}},
		{"data image", `<img src="data:image/png;base64,AAAA" alt="A">`, []string{"inline_image"}},
		{"script", `<script>alert(1)</script>`, []string{"unsafe_html"}},
		{"event handler", `<p onclick="x()">hi</p>`, []string{"unsafe_html"}},
		{"form", `<form action="https://brightside.co/s">Search</form>`, []string{"unsafe_html"}},
		{"unknown tag", `<p>{{shoe_size}}</p>`, []string{"unknown_merge_tag"}},
	}
	for _, c := range cases {
		got := codes(Check(page(c.body), cc))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	if bare := codes(Check(`<p>hello</p><a href="{{unsubscribe_url}}">Unsubscribe</a> {{postal_address}}`, cc)); !slices.Equal(bare, []string{"missing_footer"}) {
		t.Errorf("unsubscribe and address outside a footer don't count: %v", bare)
	}
	// An unsubscribe link with other words in its href doesn't count.
	if got := codes(Check(`<div class="klv-footer"><a href="https://brightside.co/?u={{unsubscribe_url}}">Unsubscribe</a> {{postal_address}}</div>`, cc)); !slices.Contains(got, "missing_unsubscribe") {
		t.Errorf("wrapped unsubscribe: %v", got)
	}
	big := page(strings.Repeat("<p>long</p>", GmailClipBytes/10))
	if !slices.Contains(codes(Check(big, cc)), "gmail_clipping") {
		t.Error("no gmail_clipping warning")
	}
	imageOnly := `<img src="https://brightside.co/a.png" alt="Sale">` + footerHTML
	if got := codes(Check(imageOnly, cc)); !slices.Equal(got, []string{"image_only"}) {
		t.Errorf("image only: %v", got)
	}
	if HasErrors([]Issue{warnf("x", "y")}) || !HasErrors([]Issue{errorf("x", "y")}) {
		t.Error("HasErrors")
	}
}

func TestIsShortener(t *testing.T) {
	for host, want := range map[string]bool{"bit.ly": true, "BIT.LY": true, "go.bit.ly": true, "bitly.example": false, "t.com": false} {
		if IsShortener(host) != want {
			t.Errorf("%s: %v", host, !want)
		}
	}
}

func TestRender(t *testing.T) {
	in := `<p>Hi {{first_name | friend}}, {{ last_name }}!</p><a href="{{unsubscribe_url}}">u</a> {{shoe_size}}`
	got := Render(in, map[string]string{"first_name": "<Ann>", "unsubscribe_url": "https://go.brightside.co/u/abc?x=1&y=2"})
	want := `<p>Hi &lt;Ann&gt;, !</p><a href="https://go.brightside.co/u/abc?x=1&amp;y=2">u</a> {{shoe_size}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := Render("Hi {{first_name | friend}}", map[string]string{"first_name": "  "}); got != "Hi friend" {
		t.Errorf("fallback: %q", got)
	}
	if got := RenderText("Hi {{first_name}} & co", map[string]string{"first_name": "A&B"}); got != "Hi A&B & co" {
		t.Errorf("text: %q", got)
	}
}

func TestPlainText(t *testing.T) {
	got := PlainText(`<html><head><title>T</title><style>.x{}</style></head><body>
		<!-- a comment --><h1>Big   news</h1><p>Line one<br>line two</p>
		<div style="display: none">Preview text</div>
		<p><a href="https://shop.example.com/a"><img src="x.png" alt="Shop now"></a></p>
		<p><a href="https://shop.example.com/b">https://shop.example.com/b</a> and
		<a href="https://shop.example.com/c">
		  More
		</a></p>
		<table><tr><td>Cell</td></tr></table></body></html>`)
	want := "Big news\nLine one\nline two\nShop now (https://shop.example.com/a)\nhttps://shop.example.com/b and More (https://shop.example.com/c)\nCell\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
