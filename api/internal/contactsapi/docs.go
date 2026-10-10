package contactsapi

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

// The API reference (GET /api/v1/docs): Scalar, pinned to one version and
// checked by Subresource Integrity, rendering openapi.json. It only reads:
// no request client (keys must never pass through a browser or someone
// else's proxy), no Scalar services, no telemetry, no web fonts. The CSP
// below is what holds that: the page can fetch nothing but this server.
const (
	scalarVersion = "1.72.1"
	scalarURL     = "https://cdn.jsdelivr.net/npm/@scalar/api-reference@" + scalarVersion + "/dist/browser/standalone.js"
	// sha256 of that file, as jsDelivr lists it. A different file won't run.
	scalarSRI = "sha256-vwkQBr3Dr2Bnr3fC9TSBLj5jPpSpTocTgGn1cPM7diw="
)

// docsInit configures Scalar (options as of @scalar/types 0.22.1). The AI
// agent ("Ask AI") and "Generate MCP" talk to Scalar's services and turn on
// by themselves on localhost, so both are switched off explicitly.
const docsInit = `Scalar.createApiReference("#app", {
  url: "/api/v1/openapi.json",
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

const docsPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Contacts API reference</title>
</head>
<body>
<noscript><p>The API reference needs JavaScript. The specification itself is at <a href="/api/v1/openapi.json">/api/v1/openapi.json</a>.</p></noscript>
<div id="app"></div>
<script src="` + scalarURL + `" integrity="` + scalarSRI + `" crossorigin="anonymous"></script>
<script>` + docsInit + `</script>
</body>
</html>
`

// docsCSP allows the one pinned script, the init script by hash, and
// Scalar's own injected styles (it builds them at runtime, so no nonce or
// hash can cover them; styles can't run code).
var docsCSP = func() string {
	sum := sha256.Sum256([]byte(docsInit))
	return "default-src 'none'; script-src " + scalarURL + " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; " +
		"style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

func serveDocs(w http.ResponseWriter, _ *http.Request) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", docsCSP)
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "public, max-age=300")
	hd.Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}
