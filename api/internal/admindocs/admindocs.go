// Package admindocs serves the admin API's reference (OpenAPI 3.1, rendered
// with Scalar) when DEBUG is on. The spec is written per area in spec/*.json
// and merged at startup; each area lists the routes it hasn't documented yet
// in "x-todo", and a test checks spec + todos match the registered routes.
package admindocs

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/scalar"
)

//go:embed spec/*.json
var specFiles embed.FS

// Areas in the order they appear in the reference. Each has spec/<area>.json.
var Areas = []string{"meta", "auth", "brands", "team", "templates", "campaigns",
	"contacts", "imports", "segments", "apikeys", "governance", "privacy", "agents", "health", "brakes"}

// Prefix is where the spec's paths live (its servers entry).
const Prefix = "/admin/v1"

// Roles an operation's x-role may name (see the spec's introduction).
var Roles = []string{"public", "signed_in", "company:approver", "company:owner",
	"brand:viewer", "brand:editor", "brand:sender", "brand:brand_admin"}

var methods = []string{"get", "put", "post", "delete", "patch"}

// Spec is the merged document.
type Spec struct {
	JSON []byte
	// Operations documented, as "METHOD /admin/v1/path" (path as in the mux:
	// "GET /admin/v1/brands/{slug}").
	Operations []string
	// Todo: per area, routes not documented yet, in the same form.
	Todo map[string][]string
	// ErrorCodes: per operation, the codes it lists in x-error-codes.
	ErrorCodes map[string][]string
}

type area struct {
	Tag        map[string]any            `json:"tag"`
	Todo       []string                  `json:"x-todo"`
	Paths      map[string]map[string]any `json:"paths"`
	Components map[string]map[string]any `json:"components"`
}

// Build merges spec/common.json and every area into one document, and checks
// what a reader relies on: each operation says what it does, who may call
// it and whether it needs step-up, and every $ref resolves.
func Build() (Spec, error) {
	var doc map[string]any
	if err := read("common.json", &doc); err != nil {
		return Spec{}, err
	}
	paths := doc["paths"].(map[string]any)
	comps := doc["components"].(map[string]any)
	var tags []any
	out := Spec{Todo: map[string][]string{}, ErrorCodes: map[string][]string{}}
	seen := map[string]string{} // operation → area
	for _, name := range Areas {
		var a area
		if err := read(name+".json", &a); err != nil {
			return Spec{}, err
		}
		tag, _ := a.Tag["name"].(string)
		if tag == "" {
			return Spec{}, fmt.Errorf("%s.json: tag.name is required", name)
		}
		tags = append(tags, a.Tag)
		for _, t := range a.Todo {
			if prev, dup := seen[t]; dup {
				return Spec{}, fmt.Errorf("%s.json: %s is already in %s.json", name, t, prev)
			}
			seen[t] = name
		}
		out.Todo[name] = a.Todo
		for path, item := range a.Paths {
			if !strings.HasPrefix(path, "/") {
				return Spec{}, fmt.Errorf("%s.json: path %q must start with / (relative to %s)", name, path, Prefix)
			}
			merged, _ := paths[path].(map[string]any)
			if merged == nil {
				merged = map[string]any{}
				paths[path] = merged
			}
			for method, v := range item {
				op, ok := v.(map[string]any)
				if !ok || !slices.Contains(methods, method) {
					if method == "parameters" || method == "summary" || method == "description" {
						merged[method] = v
						continue
					}
					return Spec{}, fmt.Errorf("%s.json: %s: unknown key %q", name, path, method)
				}
				key := strings.ToUpper(method) + " " + Prefix + path
				if prev, dup := seen[key]; dup {
					return Spec{}, fmt.Errorf("%s.json: %s is already in %s.json", name, key, prev)
				}
				seen[key] = name
				codes, err := operation(op, tag)
				if err != nil {
					return Spec{}, fmt.Errorf("%s.json: %s: %w", name, key, err)
				}
				merged[method] = op
				out.Operations = append(out.Operations, key)
				out.ErrorCodes[key] = codes
			}
		}
		for kind, entries := range a.Components {
			dst, _ := comps[kind].(map[string]any)
			if dst == nil {
				dst = map[string]any{}
				comps[kind] = dst
			}
			for k, v := range entries {
				if _, dup := dst[k]; dup {
					return Spec{}, fmt.Errorf("%s.json: components.%s.%s is defined twice", name, kind, k)
				}
				dst[k] = v
			}
		}
	}
	doc["tags"] = tags
	if err := refsResolve(doc, doc); err != nil {
		return Spec{}, err
	}
	sort.Strings(out.Operations)
	var err error
	out.JSON, err = json.Marshal(doc)
	return out, err
}

// operation checks one operation and fills in what's derived: its tag, a
// public operation's empty security, and a line on role, step-up and error
// codes at the top of its description (Scalar doesn't show x- extensions).
func operation(op map[string]any, tag string) ([]string, error) {
	if s, _ := op["summary"].(string); s == "" {
		return nil, fmt.Errorf("summary is required")
	}
	role, _ := op["x-role"].(string)
	if !slices.Contains(Roles, role) {
		return nil, fmt.Errorf("x-role %q must be one of %v", role, Roles)
	}
	stepUp, ok := op["x-step-up"].(bool)
	if !ok {
		return nil, fmt.Errorf("x-step-up (true or false) is required")
	}
	raw, ok := op["x-error-codes"].([]any)
	if !ok {
		return nil, fmt.Errorf("x-error-codes (a list, maybe empty) is required")
	}
	codes := make([]string, 0, len(raw))
	for _, c := range raw {
		s, ok := c.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("x-error-codes must be strings")
		}
		codes = append(codes, s)
	}
	if stepUp && !slices.Contains(codes, "step_up_required") {
		return nil, fmt.Errorf("x-step-up is true but x-error-codes lacks step_up_required")
	}
	if role != "public" && !slices.Contains(codes, "unauthenticated") {
		return nil, fmt.Errorf("x-role %s needs a session, so x-error-codes needs unauthenticated", role)
	}
	if _, ok := op["responses"].(map[string]any); !ok {
		return nil, fmt.Errorf("responses are required")
	}
	if _, ok := op["tags"]; !ok {
		op["tags"] = []any{tag}
	}
	if role == "public" {
		op["security"] = []any{}
	}
	line := "**Role:** `" + role + "`"
	if stepUp {
		line += " · **Step-up:** required (a passkey confirmation from the last few minutes)"
	}
	if len(codes) > 0 {
		line += " · **Errors:** `" + strings.Join(codes, "`, `") + "`"
	}
	if d, _ := op["description"].(string); d != "" {
		line += "\n\n" + d
	}
	op["description"] = line
	return codes, nil
}

// refsResolve fails on a "$ref" that points nowhere in doc.
func refsResolve(doc map[string]any, v any) error {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			var cur any = doc
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				m, _ := cur.(map[string]any)
				if cur = m[part]; cur == nil {
					return fmt.Errorf("$ref %q points nowhere", ref)
				}
			}
		}
		for _, e := range v {
			if err := refsResolve(doc, e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range v {
			if err := refsResolve(doc, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func read(name string, v any) error {
	b, err := specFiles.ReadFile("spec/" + name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Routes serves the reference and its document to signed-in members only:
// the spec maps the app's whole internal surface. Register them only when
// DEBUG is on.
func Routes(mux *http.ServeMux, s Spec, log *slog.Logger) {
	page := scalar.New("Admin API reference", Prefix+"/openapi.json", "no-store")
	signedIn := func(h http.Handler) http.HandlerFunc {
		return httpx.Handle(log, func(w http.ResponseWriter, r *http.Request) error {
			if _, err := auth.Require(r); err != nil {
				return err
			}
			h.ServeHTTP(w, r)
			return nil
		})
	}
	mux.HandleFunc("GET "+Prefix+"/docs", signedIn(page))
	mux.HandleFunc("GET "+Prefix+"/openapi.json", signedIn(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(s.JSON)
	})))
}
