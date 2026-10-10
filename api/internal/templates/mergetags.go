package templates

import (
	"html"
	"regexp"
	"slices"
	"strings"
)

// Merge tags are filled in per recipient at send time:
//
//	Hi {{first_name | there}},
//
// The part after | is used when the value is empty.
var mergeTagRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*(?:\|\s*([^{}]*?)\s*)?\}\}`)

// Tag is one merge tag a template or layout may use.
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         bool   `json:"url"` // a link we generate; fine as an href
}

// Tags is every merge tag the sender knows how to fill. ContentTag is only
// valid in a layout.
var Tags = []Tag{
	{Name: "first_name", Description: "Contact's first name"},
	{Name: "last_name", Description: "Contact's last name"},
	{Name: "email", Description: "Contact's email address"},
	{Name: "brand_name", Description: "Brand name"},
	{Name: "postal_address", Description: "Brand's postal address (required in every email)"},
	{Name: "unsubscribe_url", Description: "One-click unsubscribe link (required in every email)", URL: true},
	{Name: "preferences_url", Description: "Link to the contact's email preferences", URL: true},
}

// ContentTag marks where a layout puts the template's content.
const ContentTag = "content"

func knownTag(name string) (Tag, bool) {
	i := slices.IndexFunc(Tags, func(t Tag) bool { return t.Name == name })
	if i < 0 {
		return Tag{}, false
	}
	return Tags[i], true
}

// tagNames returns the merge tags used in s, in order, with repeats.
func tagNames(s string) []string {
	var names []string
	for _, m := range mergeTagRe.FindAllStringSubmatch(s, -1) {
		names = append(names, m[1])
	}
	return names
}

// Render fills merge tags in compiled HTML. Values are HTML-escaped; unknown
// tags are left as they are (the checks already reported them).
func Render(htmlBody string, vars map[string]string) string {
	return render(htmlBody, vars, html.EscapeString)
}

// RenderText fills merge tags in the plain-text part.
func RenderText(text string, vars map[string]string) string {
	return render(text, vars, func(s string) string { return s })
}

func render(s string, vars map[string]string, escape func(string) string) string {
	return mergeTagRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mergeTagRe.FindStringSubmatch(m)
		if _, ok := knownTag(sub[1]); !ok {
			return m
		}
		v := vars[sub[1]]
		if strings.TrimSpace(v) == "" {
			// The fallback came from the template author and is already HTML.
			return sub[2]
		}
		return escape(v)
	})
}

// SampleVars fills merge tags for previews.
func SampleVars(brandName, postalAddress string) map[string]string {
	return map[string]string{
		"first_name":      "Alex",
		"last_name":       "Rivera",
		"email":           "alex@example.com",
		"brand_name":      brandName,
		"postal_address":  postalAddress,
		"unsubscribe_url": "#unsubscribe",
		"preferences_url": "#preferences",
	}
}
