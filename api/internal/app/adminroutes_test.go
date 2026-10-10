package app_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kmrifat/klavio-alternative/api/internal/admindocs"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
)

// adminRoute is one "METHOD /admin/v1/…" registration found in the source.
type adminRoute struct {
	Pattern string // "GET /admin/v1/brands/{slug}"
	File    string // relative to internal/
}

var methodPattern = regexp.MustCompile(`^[A-Z]+ /`)

// sourceAdminRoutes finds every mux.Handle/HandleFunc call under internal/
// (tests excluded) whose pattern is an /admin/v1 route. Patterns are string
// literals, constants, or a local `base := "…"` joined with +; anything it
// can't resolve fails the test, so no registration slips past unseen.
func sourceAdminRoutes(t *testing.T) []adminRoute {
	t.Helper()
	root, err := filepath.Abs("..") // internal/
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	pkgs := map[string][]*ast.File{} // dir → files
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		pkgs[filepath.Dir(path)] = append(pkgs[filepath.Dir(path)], f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Package-level string constants and vars with a literal value, per dir.
	consts := map[string]map[string]string{}
	for dir, files := range pkgs {
		consts[dir] = map[string]string{}
		for _, f := range files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								consts[dir][name.Name], _ = strconv.Unquote(lit.Value)
							}
						}
					}
				}
			}
		}
	}

	var out []adminRoute
	var problems []string
	for dir, files := range pkgs {
		for _, f := range files {
			imports := map[string]string{} // local name → dir
			for _, im := range f.Imports {
				p, _ := strconv.Unquote(im.Path.Value)
				if i := strings.Index(p, "/internal/"); i >= 0 {
					d := filepath.Join(root, filepath.FromSlash(p[i+len("/internal/"):]))
					name := filepath.Base(d)
					if im.Name != nil {
						name = im.Name.Name
					}
					imports[name] = d
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				// Local string variables assigned once from a literal.
				locals := map[string]string{}
				assigned := map[string]int{}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if as, ok := n.(*ast.AssignStmt); ok {
						for i, lhs := range as.Lhs {
							if id, ok := lhs.(*ast.Ident); ok {
								assigned[id.Name]++
								if i < len(as.Rhs) && len(as.Lhs) == len(as.Rhs) {
									if lit, ok := as.Rhs[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
										locals[id.Name], _ = strconv.Unquote(lit.Value)
									}
								}
							}
						}
					}
					return true
				})
				var eval func(e ast.Expr) (string, bool)
				eval = func(e ast.Expr) (string, bool) {
					switch e := e.(type) {
					case *ast.BasicLit:
						if e.Kind != token.STRING {
							return "", false
						}
						s, err := strconv.Unquote(e.Value)
						return s, err == nil
					case *ast.ParenExpr:
						return eval(e.X)
					case *ast.BinaryExpr:
						if e.Op != token.ADD {
							return "", false
						}
						a, ok1 := eval(e.X)
						b, ok2 := eval(e.Y)
						return a + b, ok1 && ok2
					case *ast.Ident:
						if v, ok := locals[e.Name]; ok && assigned[e.Name] == 1 {
							return v, true
						}
						v, ok := consts[dir][e.Name]
						return v, ok
					case *ast.SelectorExpr:
						if pkg, ok := e.X.(*ast.Ident); ok {
							if d, ok := imports[pkg.Name]; ok {
								v, ok := consts[d][e.Sel.Name]
								return v, ok
							}
						}
					}
					return "", false
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) != 2 {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
						return true
					}
					if x, ok := sel.X.(*ast.Ident); ok && (imports[x.Name] != "" || x.Name == "http") {
						return true // a package function (httpx.Handle), not a mux
					}
					rel, _ := filepath.Rel(root, fset.Position(call.Pos()).Filename)
					pat, ok := eval(call.Args[0])
					if !ok {
						problems = append(problems, fmt.Sprintf("%s: can't read the route pattern of %s(…)", fset.Position(call.Pos()), sel.Sel.Name))
						return true
					}
					switch {
					case methodPattern.MatchString(pat) && strings.HasPrefix(pat[strings.IndexByte(pat, ' ')+1:], "/admin/v1/"):
						out = append(out, adminRoute{Pattern: pat, File: rel})
					case strings.Contains(pat, "/admin/v1/"):
						problems = append(problems, fmt.Sprintf("%s: admin route %q has no method", fset.Position(call.Pos()), pat))
					}
					return true
				})
				return false
			})
		}
	}
	if len(problems) > 0 {
		t.Fatalf("route registrations the docs check can't follow (use a literal, a constant or a local base := \"…\"):\n%s", strings.Join(problems, "\n"))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// sourceErrorCodes: every error code the handlers can return.
func sourceErrorCodes(t *testing.T) map[string]bool {
	t.Helper()
	codes := map[string]bool{"internal": true} // httpx.Handle's 500
	re := regexp.MustCompile(`(?:Errorf\(\s*http\.Status\w+,\s*|\b(?:bad|errBad)\(\s*|Code:\s*)"([a-z][a-z0-9_]*)"`)
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			codes[m[1]] = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return codes
}

// served is the pattern the mux routes pattern's own path to ("" if none).
func served(mux *http.ServeMux, pattern string) string {
	method, path, _ := strings.Cut(pattern, " ")
	r := httptest.NewRequest(method, regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x"), nil)
	_, got := mux.Handler(r)
	return got
}

// The admin API reference covers every /admin/v1 route: each one is either
// documented or in its area's x-todo list, and nothing else is. Areas
// remove their todos as they document them.
func TestAdminAPIReferenceCoversEveryRoute(t *testing.T) {
	routes := sourceAdminRoutes(t)
	a, _, _ := setupWith(t, func(c *config.Config) { c.Debug = true })
	mux := a.AdminMux()
	registered := map[string]string{}
	for _, r := range routes {
		if got := served(mux, r.Pattern); got != r.Pattern {
			t.Errorf("%s (%s) isn't served by the admin mux (it routes to %q): dead code, or registered elsewhere?", r.Pattern, r.File, got)
		}
		registered[r.Pattern] = r.File
	}
	if len(registered) < 90 {
		t.Fatalf("found only %d admin routes; the source scan is broken", len(registered))
	}

	spec, err := admindocs.Build()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, op := range spec.Operations {
		listed[op] = "documented"
	}
	todos := 0
	for area, list := range spec.Todo {
		for _, op := range list {
			listed[op] = "todo in " + area + ".json"
			todos++
		}
	}
	for p, file := range registered {
		if listed[p] == "" {
			t.Errorf("%s (%s) is neither documented nor in an area's x-todo", p, file)
		}
	}
	for p, where := range listed {
		if registered[p] == "" {
			t.Errorf("%s (%s) isn't a registered route", p, where)
		}
	}
	codes := sourceErrorCodes(t)
	for op, list := range spec.ErrorCodes {
		for _, c := range list {
			if !codes[c] {
				t.Errorf("%s lists error code %q, which no handler returns", op, c)
			}
		}
	}
	t.Logf("%d routes: %d documented, %d to do", len(registered), len(spec.Operations), todos)
}

// The reference only exists with DEBUG on, and then only for signed-in
// members; meta tells the app which.
func TestAdminAPIReferenceNeedsDebugAndASession(t *testing.T) {
	off, srv, _ := setup(t)
	for _, p := range []string{"GET /admin/v1/docs", "GET /admin/v1/openapi.json"} {
		if got := served(off.AdminMux(), p); got != "" {
			t.Errorf("DEBUG off, yet %s is registered", p)
		}
	}
	anon := newBrowser(t, srv.URL)
	if m := anon.must(200, anon.do("GET", "/admin/v1/meta", nil)); m.body["debug"] != false {
		t.Fatalf("meta with DEBUG off: %s", m.raw)
	}
	anon.must(404, anon.do("GET", "/admin/v1/docs", nil))

	on, srv, _ := setupWith(t, func(c *config.Config) { c.Debug = true })
	anon = newBrowser(t, srv.URL)
	if m := anon.must(200, anon.do("GET", "/admin/v1/meta", nil)); m.body["debug"] != true {
		t.Fatalf("meta with DEBUG on: %s", m.raw)
	}
	anon.must(401, anon.do("GET", "/admin/v1/docs", nil))
	anon.must(401, anon.do("GET", "/admin/v1/openapi.json", nil))

	owner := newBrowser(t, srv.URL)
	link, _ := on.Bootstrap(context.Background(), "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	res, err := owner.http.Get(srv.URL + "/admin/v1/docs")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if page := string(raw); res.StatusCode != 200 || !strings.Contains(page, `"/admin/v1/openapi.json"`) || !strings.Contains(page, "hideTestRequestButton: true") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "connect-src 'self'") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("docs page: %d %v\n%s", res.StatusCode, res.Header, page)
	}
	spec := owner.must(200, owner.do("GET", "/admin/v1/openapi.json", nil))
	if spec.body["openapi"] != "3.1.0" {
		t.Fatalf("spec: %.200s", spec.raw)
	}
	if servers := spec.body["servers"].([]any); servers[0].(map[string]any)["url"] != "/admin/v1" {
		t.Fatalf("servers: %v", servers)
	}
}
