package admindocs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	s, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Tags    []struct{ Name string }
		Paths   map[string]map[string]struct {
			Tags        []string
			Description string
			Security    *[]any
		}
	}
	if err := json.Unmarshal(s.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || len(doc.Tags) != len(Areas) {
		t.Fatalf("openapi %q, %d tags", doc.OpenAPI, len(doc.Tags))
	}
	me := doc.Paths["/me"]["get"]
	if len(me.Tags) != 1 || me.Tags[0] != "Meta" || !strings.HasPrefix(me.Description, "**Role:** `signed_in` · **Errors:** `unauthenticated`") {
		t.Fatalf("/me: %+v", me)
	}
	if meta := doc.Paths["/meta"]["get"]; meta.Security == nil || len(*meta.Security) != 0 {
		t.Fatal("a public operation must have empty security")
	}
}

// The rules every area's operations follow, with the error an author sees.
func TestOperationRules(t *testing.T) {
	ok := func() map[string]any {
		return map[string]any{"summary": "x", "x-role": "brand:editor", "x-step-up": true,
			"x-error-codes": []any{"unauthenticated", "step_up_required"}, "responses": map[string]any{}}
	}
	if _, err := operation(ok(), "T"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"summary is required":       func(o map[string]any) { delete(o, "summary") },
		"x-role":                    func(o map[string]any) { o["x-role"] = "admin" },
		"x-step-up (true or false)": func(o map[string]any) { delete(o, "x-step-up") },
		"x-error-codes (a list":     func(o map[string]any) { delete(o, "x-error-codes") },
		"lacks step_up_required":    func(o map[string]any) { o["x-error-codes"] = []any{"unauthenticated"} },
		"needs unauthenticated":     func(o map[string]any) { o["x-step-up"] = false; o["x-error-codes"] = []any{} },
		"responses are required":    func(o map[string]any) { delete(o, "responses") },
	}
	for want, edit := range cases {
		o := ok()
		edit(o)
		if _, err := operation(o, "T"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	op := ok()
	_, _ = operation(op, "T")
	if d := op["description"].(string); !strings.Contains(d, "**Step-up:** required") {
		t.Errorf("step-up note missing: %q", d)
	}
}
