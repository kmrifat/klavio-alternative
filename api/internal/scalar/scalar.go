// Package scalar serves an API reference page: Scalar, pinned to one version
// and checked by Subresource Integrity, rendering one OpenAPI document. It
// only reads: no request client (keys must never pass through a browser or
// someone else's proxy), no Scalar services, no telemetry, no web fonts. The
// page's CSP is what holds that: it can fetch nothing but this server.
package scalar

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
)

const (
	Version = "1.72.1"
	URL     = "https://cdn.jsdelivr.net/npm/@scalar/api-reference@" + Version + "/dist/browser/standalone.js"
	// SRI is the sha256 of that file, as jsDelivr lists it. A different file won't run.
	SRI = "sha256-vwkQBr3Dr2Bnr3fC9TSBLj5jPpSpTocTgGn1cPM7diw="
)

// Page is one rendered reference page and its headers.
type Page struct {
	html  []byte
	csp   string
	cache string
}

// New builds the page for the spec at specURL (same origin). cache is its
// Cache-Control header.
func New(title, specURL, cache string) Page {
	init := initScript(specURL)
	sum := sha256.Sum256([]byte(init))
	// The pinned script, the init script by hash, and Scalar's own injected
	// styles (it builds them at runtime, so no nonce or hash can cover them;
	// styles can't run code).
	csp := "default-src 'none'; script-src " + URL + " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; " +
		"style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	t, s := html.EscapeString(title), html.EscapeString(specURL)
	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>` + t + `</title>
</head>
<body>
<noscript><p>The API reference needs JavaScript. The specification itself is at <a href="` + s + `">` + s + `</a>.</p></noscript>
<div id="app"></div>
<script src="` + URL + `" integrity="` + SRI + `" crossorigin="anonymous"></script>
<script>` + init + `</script>
</body>
</html>
`
	return Page{html: []byte(page), csp: csp, cache: cache}
}

// initScript configures Scalar (options as of @scalar/types 0.22.1). The AI
// agent ("Ask AI") and "Generate MCP" talk to Scalar's services and turn on
// by themselves on localhost, so both are switched off explicitly. The spec
// is fetched same-origin, so a session cookie goes with it.
func initScript(specURL string) string {
	u, _ := json.Marshal(specURL)
	return `Scalar.createApiReference("#app", {
  url: ` + string(u) + `,
  hideTestRequestButton: true,
  hideClientButton: true,
  persistAuth: false,
  isEditable: false,
  telemetry: false,
  withDefaultFonts: false,
  showDeveloperTools: "never",
  documentDownloadType: "json",
  agent: { disabled: true },
  mcp: { disabled: true }
});`
}

func (p Page) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", p.csp)
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", p.cache)
	hd.Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(p.html)
}
