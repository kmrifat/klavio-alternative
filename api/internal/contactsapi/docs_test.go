package contactsapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDocsPage(t *testing.T) {
	mux := http.NewServeMux()
	(&API{}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/api/v1/docs")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	page := string(body)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("docs: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	for h, want := range map[string]string{"Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff"} {
		if got := res.Header.Get(h); got != want {
			t.Errorf("%s: %q", h, got)
		}
	}

	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'", "img-src 'self' data:", "frame-ancestors 'none'",
		"script-src https://cdn.jsdelivr.net/npm/@scalar/api-reference@" + scalarVersion + "/dist/browser/standalone.js "} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") && strings.Contains(d, "'unsafe-inline'") {
			t.Errorf("inline scripts allowed: %s", d)
		}
	}
	// The inline init script is allowed by its hash, and only it.
	m := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no init script")
	}
	sum := sha256.Sum256([]byte(m[1]))
	if !strings.Contains(csp, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
		t.Fatalf("CSP doesn't allow the init script it serves: %s", csp)
	}

	// Pinned, integrity-checked Scalar; read-only, nothing phoning home.
	if !regexp.MustCompile(`@scalar/api-reference@\d+\.\d+\.\d+/`).MatchString(page) || strings.Contains(page, "@latest") ||
		!strings.Contains(page, `integrity="sha256-`) {
		t.Errorf("Scalar not pinned with SRI:\n%s", page)
	}
	for _, want := range []string{`url: "/api/v1/openapi.json"`, "hideTestRequestButton: true", "hideClientButton: true",
		"telemetry: false", "withDefaultFonts: false", "agent: { disabled: true }", "mcp: { disabled: true }"} {
		if !strings.Contains(m[1], want) {
			t.Errorf("init lacks %q", want)
		}
	}
	if strings.Contains(page, "proxy") {
		t.Error("the page mentions a proxy")
	}

	// Other routes keep their own headers: the spec has no docs CSP.
	res, err = http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Servers []struct{ URL string } `json:"servers"`
	}
	_ = json.NewDecoder(res.Body).Decode(&spec)
	res.Body.Close()
	if res.Header.Get("Content-Security-Policy") != "" {
		t.Error("the docs CSP leaked onto the spec route")
	}
	// Examples show this server's API base.
	if len(spec.Servers) != 1 || spec.Servers[0].URL != "/api/v1" {
		t.Errorf("servers: %+v", spec.Servers)
	}
}
