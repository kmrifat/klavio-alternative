package imports

import (
	"errors"
	"strings"
)

// ListKind is what an exported file contains. Mailchimp exports a zip with
// one CSV of each kind.
type ListKind string

const (
	// KindSubscribed rows become contacts, after every check and the gate.
	KindSubscribed ListKind = "subscribed"
	// KindUnsubscribed rows become unsubscribed contacts, so a later import
	// or signup form cannot quietly re-subscribe them.
	KindUnsubscribed ListKind = "unsubscribed"
	// KindCleaned rows (hard bounces at the old provider) go straight onto
	// the company-wide suppression list.
	KindCleaned ListKind = "cleaned"
)

func (k ListKind) Valid() bool {
	return k == KindSubscribed || k == KindUnsubscribed || k == KindCleaned
}

// GuessKind reads the list kind from a Mailchimp-style file name
// ("cleaned_members_export_3f2a.csv"), defaulting to subscribed.
func GuessKind(fileName string) ListKind {
	n := strings.ToLower(fileName)
	switch {
	case strings.Contains(n, "unsubscribed"):
		return KindUnsubscribed
	case strings.Contains(n, "cleaned"), strings.Contains(n, "bounced"):
		return KindCleaned
	default:
		return KindSubscribed
	}
}

// AttestationText is what the importer signs for a subscribed list. It is
// stored verbatim with the import, so change it by adding a new version, not
// by editing it.
const AttestationText = "I confirm that every person in this file asked to receive email from this brand, " +
	"that I can show how and when each of them signed up, and that the list was not bought, rented or scraped."

// Attestation is the importer's signed promise about where a subscribed
// list came from. Without it nothing is imported.
type Attestation struct {
	Source   string // where the list came from, in the importer's words ("Mailchimp, Brightside main list")
	Text     string // the exact statement they agreed to
	SignedBy string // user id
}

var (
	ErrNoSource       = errors.New("say where this list came from")
	ErrNotAttested    = errors.New("the consent statement must be signed before importing")
	ErrWrongStatement = errors.New("the signed statement is not the current one")
)

func (a Attestation) Validate() error {
	if strings.TrimSpace(a.Source) == "" {
		return ErrNoSource
	}
	if a.SignedBy == "" || a.Text == "" {
		return ErrNotAttested
	}
	if a.Text != AttestationText {
		return ErrWrongStatement
	}
	return nil
}
