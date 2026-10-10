package contactsapi

import "github.com/kmrifat/klavio-alternative/api/internal/scalar"

// The API reference (GET /api/v1/docs) for outside developers: public, like
// the spec it renders.
var docsPage = scalar.New("Contacts API reference", "/api/v1/openapi.json", "public, max-age=300")
