package exports

import "testing"

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{
		"Maya":                 "Maya",
		"=HYPERLINK(\"x\")":    "'=HYPERLINK(\"x\")",
		"-2":                   "'-2",
		"vip;=1+1":             "vip;'=1+1", // Excel splits on ; in many locales
		"x,@SUM(A1)":           "x,'@SUM(A1)",
		"a|+b":                 "a|'+b",
		"O'Brien; Smith-Jones": "O'Brien; Smith-Jones",
		"ann@example.com":      "ann@example.com",
		"":                     "",
		"\t=cmd":               "'\t'=cmd",
	} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
