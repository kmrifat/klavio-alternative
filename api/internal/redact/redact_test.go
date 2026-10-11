package redact

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestEmails(t *testing.T) {
	cases := map[string]string{
		"jane.doe+news@example.co.uk":                  "j•••@example.co.uk",
		"to Jane@Example.com and bob@mail.example.org": "to J•••@Example.com and b•••@mail.example.org",
		"/api/v1/contacts/jane%40example.com":          "/api/v1/contacts/jane%40example.com", // encoded: not an address
		"no address @ here, nor@localhost":             "no address @ here, nor@localhost",
		"":                                             "",
	}
	for in, want := range cases {
		if got := Emails(in); got != want {
			t.Errorf("Emails(%q) = %q, want %q", in, got, want)
		}
	}
}

type addr string

func (a addr) String() string { return string(a) }

// Every place an address can reach a log line is masked: the message,
// string attributes (also ones added with With), groups, errors and
// Stringers, string lists. Other values pass through untouched.
func TestHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewHandler(slog.NewJSONHandler(&buf, nil))).With("actor", "owner@acme.test")
	log.Info("sent to jane@example.com",
		"to", "jane@example.com",
		"err", fmt.Errorf("ses: %w", errors.New("address bob@example.com rejected")),
		"who", addr("carol@example.com"),
		"list", []string{"dan@example.com", "plain"},
		slog.Group("req", "path", "/u/x?e=erin@example.com"),
		"count", 3,
	)
	out := buf.String()
	for _, leak := range []string{"jane@", "bob@", "carol@", "dan@", "erin@", "owner@"} {
		if strings.Contains(out, leak) {
			t.Errorf("%s leaked: %s", leak, out)
		}
	}
	for _, want := range []string{`"msg":"sent to j•••@example.com"`, `"actor":"o•••@acme.test"`, `"err":"ses: address b•••@example.com rejected"`,
		`"who":"c•••@example.com"`, `"list":["d•••@example.com","plain"]`, `"path":"/u/x?e=e•••@example.com"`, `"count":3`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}
