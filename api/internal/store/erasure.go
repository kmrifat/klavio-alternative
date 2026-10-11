package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaskEmail shows enough to recognise an address without spelling it out:
// m•••@gmail.com.
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || local == "" {
		return "•••"
	}
	return string([]rune(local)[0]) + "•••@" + domain
}

// ContactRef is how the audit log names a contact: the masked address and
// the start of its hash, so entries about one person can be matched up
// (and found again from the address) without storing it.
func ContactRef(email string) string {
	return MaskEmail(strings.ToLower(email)) + " #" + hex.EncodeToString(EmailHash(email))[:10]
}

// emailPattern matches the address as a whole word, any case: not
// "xa@b.com" or "a@b.com.au" when erasing a@b.com.
func emailPattern(email string) string {
	return `(?<![[:alnum:]._%+-])` + regexp.QuoteMeta(email) + `(?![[:alnum:]-]|\.[[:alnum:]])`
}

// PersonContact is one brand's contact for an address.
type PersonContact struct {
	ID         uuid.UUID     `db:"id" json:"-"`
	BrandSlug  string        `db:"brand_slug" json:"brand_slug"`
	BrandName  string        `db:"brand_name" json:"brand_name"`
	BrandColor string        `db:"brand_color" json:"brand_color"`
	Status     ContactStatus `db:"status" json:"status"`
	CreatedAt  time.Time     `db:"created_at" json:"created_at"`
}

// PersonData is everything held about one address, before an erasure.
type PersonData struct {
	Ref            string          `json:"contact_ref"`
	Contacts       []PersonContact `json:"contacts"`
	Suppression    *string         `json:"suppression"` // reason, when the address is blocked
	Erasures       []Erasure       `json:"erasures"`    // earlier erasures of this address
	Sends          int             `json:"sends"`
	Clicks         int             `json:"clicks"`
	Opens          int             `json:"opens"`
	Events         int             `json:"events"`
	ConsentRecords int             `json:"consent_records"`
	AuditEntries   int             `json:"audit_entries"`
}

// ErasureSummary says what an erasure removed, blanked and kept.
type ErasureSummary struct {
	Brands             []string `json:"brands"` // names of the brands that had the person
	ContactsDeleted    int      `json:"contacts_deleted"`
	ClicksBlanked      int      `json:"clicks_blanked"` // IP address and browser removed; the count stays in reports
	OpensBlanked       int      `json:"opens_blanked"`
	EventsBlanked      int      `json:"events_blanked"` // Amazon's raw reports about the person
	SendErrorsBlanked  int      `json:"send_errors_blanked"`
	APIRepliesDeleted  int      `json:"api_replies_deleted"`
	ImportsScrubbed    int      `json:"imports_scrubbed"`
	ExportsDeleted     int      `json:"exports_deleted"`
	AuditRedacted      int      `json:"audit_entries_redacted"`
	ConsentRecordsKept int      `json:"consent_records_kept"`
	// PreviousSuppression: why the address was already blocked, if it was.
	PreviousSuppression string `json:"previous_suppression,omitempty"`
}

type Erasure struct {
	ID           uuid.UUID       `db:"id" json:"id"`
	ContactRef   string          `db:"contact_ref" json:"contact_ref"`
	Reference    string          `db:"reference" json:"reference"`
	Summary      json.RawMessage `db:"summary" json:"summary"`
	ErasedBy     uuid.UUID       `db:"erased_by" json:"-"`
	ErasedByName string          `db:"erased_by_name" json:"erased_by_name"`
	ErasedAt     time.Time       `db:"erased_at" json:"erased_at"`
}

const erasureCols = `e.id, e.contact_ref, e.reference, e.summary, e.erased_by, COALESCE(u.name, '') AS erased_by_name, e.erased_at`

func (s *Store) personContacts(ctx context.Context, hash []byte, lock bool) ([]PersonContact, error) {
	q := `SELECT c.id, b.slug AS brand_slug, b.name AS brand_name, b.color AS brand_color, c.status, c.created_at
		FROM contacts c JOIN brands b ON b.id = c.brand_id WHERE c.email_hash = $1 ORDER BY b.name`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	return all[PersonContact](s.db.Query(ctx, q, hash))
}

// suppressionReason: why the address is blocked, or "" if it isn't.
func (s *Store) suppressionReason(ctx context.Context, hash []byte) (string, error) {
	var reason string
	err := s.db.QueryRow(ctx, `SELECT reason FROM suppressions WHERE email_hash = $1`, hash).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return reason, err
}

func contactIDs(cs []PersonContact) []uuid.UUID {
	ids := make([]uuid.UUID, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return ids
}

// PersonData looks up everything held about an address, for the erasure
// screen. It changes nothing.
func (s *Store) PersonData(ctx context.Context, email string) (PersonData, error) {
	hash := EmailHash(email)
	out := PersonData{Ref: ContactRef(email)}
	var err error
	if out.Contacts, err = s.personContacts(ctx, hash, false); err != nil {
		return out, err
	}
	if out.Contacts == nil {
		out.Contacts = []PersonContact{}
	}
	if out.Erasures, err = all[Erasure](s.db.Query(ctx, `SELECT `+erasureCols+`
		FROM erasures e LEFT JOIN users u ON u.id = e.erased_by WHERE e.email_hash = $1 ORDER BY e.erased_at DESC`, hash)); err != nil {
		return out, err
	}
	if out.Erasures == nil {
		out.Erasures = []Erasure{}
	}
	if reason, err := s.suppressionReason(ctx, hash); err != nil {
		return out, err
	} else if reason != "" {
		out.Suppression = &reason
	}
	ids := contactIDs(out.Contacts)
	err = s.db.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM sends WHERE contact_id = ANY($2)),
			(SELECT count(*) FROM clicks WHERE contact_id = ANY($2)),
			(SELECT count(*) FROM opens WHERE contact_id = ANY($2)),
			(SELECT count(*) FROM email_events WHERE email_hash = $1),
			(SELECT count(*) FROM consent_records WHERE email_hash = $1),
			(SELECT count(*) FROM audit_log WHERE target_id ~* $3 OR before::text ~* $3 OR after::text ~* $3)`,
		hash, ids, emailPattern(email)).Scan(&out.Sends, &out.Clicks, &out.Opens, &out.Events, &out.ConsentRecords, &out.AuditEntries)
	return out, err
}

// Erase carries out a GDPR erasure of an address, across every brand, in
// one transaction:
//
//   - its contacts are deleted (with their topics and topic requests);
//   - the address is suppressed with reason "erasure", so it is never
//     emailed or imported again; only its hash is kept;
//   - clicks and opens lose their IP address and browser, sends their error
//     text, and Amazon's raw reports about the person are emptied; the rows
//     stay, so campaign reports keep their totals;
//   - stored Contacts API replies that mention the person are deleted, the
//     person's rows are taken out of import previews and samples, and
//     export files that contain the address are deleted;
//   - the audit log's mentions of the address become its ContactRef.
//
// Consent records are kept: they're filed under the hash and are the proof
// of consent if it's ever disputed.
func (s *Store) Erase(ctx context.Context, email, reference string, by uuid.UUID) (Erasure, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	hash, ref, pattern := EmailHash(email), ContactRef(email), emailPattern(email)
	var out Erasure
	err := s.InTx(ctx, func(tx *Store) error {
		var sum ErasureSummary
		contacts, err := tx.personContacts(ctx, hash, true)
		if err != nil {
			return err
		}
		ids := contactIDs(contacts)
		for _, c := range contacts {
			sum.Brands = append(sum.Brands, c.BrandName)
		}
		if sum.Brands == nil {
			sum.Brands = []string{}
		}
		sum.ContactsDeleted = len(contacts)

		exec := func(dst *int, sql string, args ...any) error {
			tag, err := tx.db.Exec(ctx, sql, args...)
			if dst != nil {
				*dst = int(tag.RowsAffected())
			}
			return err
		}
		steps := []struct {
			dst  *int
			sql  string
			args []any
		}{
			{&sum.ClicksBlanked, `UPDATE clicks SET ip = '', user_agent = '' WHERE contact_id = ANY($1)`, []any{ids}},
			{&sum.OpensBlanked, `UPDATE opens SET ip = '', user_agent = '' WHERE contact_id = ANY($1)`, []any{ids}},
			{&sum.EventsBlanked, `UPDATE email_events SET detail = '{}' WHERE detail <> '{}' AND (email_hash = $1
				OR ses_message_id IN (SELECT ses_message_id FROM sends WHERE contact_id = ANY($2) AND ses_message_id IS NOT NULL))`,
				[]any{hash, ids}},
			{&sum.SendErrorsBlanked, `UPDATE sends SET error = '' WHERE contact_id = ANY($1) AND error <> ''`, []any{ids}},
			{&sum.APIRepliesDeleted, `DELETE FROM api_idempotency WHERE response::text ~* $1
				OR EXISTS (SELECT 1 FROM unnest($2::uuid[]) i WHERE position(i::text IN response::text) > 0)`, []any{pattern, ids}},
			// Import previews are the file's first rows; samples are a few
			// addresses per check outcome. Drop the person's from both.
			{&sum.ImportsScrubbed, `UPDATE imports SET
				preview = (SELECT COALESCE(jsonb_agg(r ORDER BY n), '[]') FROM jsonb_array_elements(preview) WITH ORDINALITY p(r, n)
					WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(r) v WHERE lower(trim(v)) = $1)),
				check_result = CASE WHEN jsonb_typeof(check_result->'samples') = 'object' THEN jsonb_set(check_result, '{samples}',
					(SELECT COALESCE(jsonb_object_agg(k, (SELECT COALESCE(jsonb_agg(a), '[]') FROM jsonb_array_elements_text(v) a
						WHERE lower(trim(a)) <> $1)), '{}') FROM jsonb_each(check_result->'samples') x(k, v)))
					ELSE check_result END
				WHERE preview::text ~* $2 OR check_result::text ~* $2`, []any{email, pattern}},
			// Export files are UTF-8 CSV.
			{&sum.ExportsDeleted, `UPDATE exports SET file = NULL WHERE file IS NOT NULL AND convert_from(file, 'UTF8') ~* $1`, []any{pattern}},
			// The one change the audit log allows, and only inside this transaction.
			{nil, `SELECT set_config('app.erasure', 'on', true)`, nil},
			{&sum.AuditRedacted, `UPDATE audit_log SET
				target_id = regexp_replace(target_id, $1, $2, 'gi'),
				before = regexp_replace(before::text, $1, $2, 'gi')::jsonb,
				after = regexp_replace(after::text, $1, $2, 'gi')::jsonb
				WHERE target_id ~* $1 OR before::text ~* $1 OR after::text ~* $1`, []any{pattern, ref}},
			{nil, `SELECT set_config('app.erasure', 'off', true)`, nil},
		}
		for _, st := range steps {
			if err := exec(st.dst, st.sql, st.args...); err != nil {
				return fmt.Errorf("erase: %w", err)
			}
		}

		if sum.PreviousSuppression, err = tx.suppressionReason(ctx, hash); err != nil {
			return err
		}
		// The note was free text and may name the person; the brand says
		// where they were, which no longer matters.
		if err := exec(nil, `INSERT INTO suppressions (email_hash, reason, created_by) VALUES ($1, 'erasure', $2)
			ON CONFLICT (email_hash) DO UPDATE SET reason = 'erasure', note = '', brand_id = NULL, created_by = $2`, hash, by); err != nil {
			return err
		}
		if err := exec(nil, `DELETE FROM contacts WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		if err := tx.db.QueryRow(ctx, `SELECT count(*) FROM consent_records WHERE email_hash = $1`, hash).Scan(&sum.ConsentRecordsKept); err != nil {
			return err
		}

		summary, err := json.Marshal(sum)
		if err != nil {
			return err
		}
		out, err = one[Erasure](tx.db.Query(ctx, `WITH e AS (
				INSERT INTO erasures (email_hash, contact_ref, reference, summary, erased_by) VALUES ($1, $2, $3, $4, $5) RETURNING *)
			SELECT `+erasureCols+` FROM e LEFT JOIN users u ON u.id = e.erased_by`, hash, ref, reference, summary, by))
		return err
	})
	return out, err
}

// ListErasures returns the most recent erasures, newest first.
func (s *Store) ListErasures(ctx context.Context, limit int) ([]Erasure, error) {
	return all[Erasure](s.db.Query(ctx, `SELECT `+erasureCols+`
		FROM erasures e LEFT JOIN users u ON u.id = e.erased_by ORDER BY e.erased_at DESC LIMIT $1`, limit))
}
