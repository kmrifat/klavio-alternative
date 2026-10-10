package contacts

import (
	"encoding/hex"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	ok := map[string]string{
		"ann@example.com":             "ann@example.com",
		"  Ann.Lee@Example.COM \t":    "ann.lee@example.com",
		"ann+news@mail.example.co":    "ann+news@mail.example.co",
		"o'brien@example.ie":          "o'brien@example.ie",
		"ann@xn--bcher-kva.example":   "ann@xn--bcher-kva.example",
		"a_b-c@sub-domain.example.uk": "a_b-c@sub-domain.example.uk",
	}
	for in, want := range ok {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{
		"",
		"ann",
		"ann@",
		"@example.com",
		"ann@example", // no dot
		"ann@@example.com",
		"ann@example..com",
		"ann@-example.com",
		"ann@example.c",         // 1-letter TLD
		"ann@192.168.0.1",       // numeric TLD
		"ann@[192.168.0.1]",     // IP literal
		"Ann <ann@example.com>", // display name
		"ann lee@example.com",
		"ann@exa mple.com",
		"ann@bücher.example", // raw unicode
		"jörg@example.com",
		"ann@example.com,bob@example.com",
		string(make([]byte, 65)) + "@example.com",
	}
	for _, in := range bad {
		if got, err := NormalizeEmail(in); err == nil {
			t.Errorf("NormalizeEmail(%q) = %q, want error", in, got)
		}
	}
}

func TestSuppressionHash(t *testing.T) {
	// sha256("ann@example.com"), so the SQL side can compute the same with digest/sha256().
	want := "71d4f55f72fa128dfb468a1a3901507c804b74316488744d769d7f4b16696476"
	if got := hex.EncodeToString(SuppressionHash("ann@example.com")); got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
}

func TestIsRoleAddress(t *testing.T) {
	cases := map[string]bool{
		"info@example.com":        true,
		"support+eu@example.com":  true,
		"no-reply@example.com":    true,
		"ann@example.com":         false,
		"information@example.com": false,
		"ann.info@example.com":    false,
	}
	for in, want := range cases {
		if got := IsRoleAddress(in); got != want {
			t.Errorf("IsRoleAddress(%q) = %v", in, got)
		}
	}
}

func TestIsDisposable(t *testing.T) {
	cases := map[string]bool{
		"x@mailinator.com":              true,
		"x@eu.mailinator.com":           true,
		"x@yopmail.fr":                  true,
		"x@gmail.com":                   false,
		"x@notmailinator.com":           false,
		"x@mailinator.com.evil.example": false,
	}
	for in, want := range cases {
		if got := IsDisposable(in); got != want {
			t.Errorf("IsDisposable(%q) = %v", in, got)
		}
	}
}
