package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EmailHash is how an address is identified in suppressions and consent
// records, so they survive deleting the contact.
func EmailHash(email string) []byte {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return h[:]
}

// Engagement tiers (S3), by days since the last click — or since signup for
// people who haven't clicked yet.
type Tier string

const (
	TierActive Tier = "active"  // 0–30 days
	TierWarm   Tier = "warm"    // 31–90
	TierAtRisk Tier = "at_risk" // 91–180
	TierSunset Tier = "sunset"  // 181+ or never
)

func (t Tier) Valid() bool {
	return t == TierActive || t == TierWarm || t == TierAtRisk || t == TierSunset
}

// TierSQL is the tier of contact c as SQL (for segment rules).
const TierSQL = tierExpr

// Without tracking consent there are no clicks, so nobody can prove they're
// Active: the best such a contact reaches is Warm (from the opt-in date).
const tierExpr = `CASE
	WHEN c.last_engaged_at >= now() - interval '30 days' AND c.tracking_allowed THEN 'active'
	WHEN c.last_engaged_at >= now() - interval '30 days' THEN 'warm'
	WHEN c.last_engaged_at >= now() - interval '90 days' THEN 'warm'
	WHEN c.last_engaged_at >= now() - interval '180 days' THEN 'at_risk'
	ELSE 'sunset' END`

type ContactStatus string

const (
	ContactPending      ContactStatus = "pending_confirmation"
	ContactSubscribed   ContactStatus = "subscribed"
	ContactQuarantined  ContactStatus = "quarantined"
	ContactUnsubscribed ContactStatus = "unsubscribed"
)

type Contact struct {
	ID                uuid.UUID     `db:"id" json:"id"`
	BrandID           uuid.UUID     `db:"brand_id" json:"-"`
	Email             string        `db:"email" json:"email"`
	FirstName         string        `db:"first_name" json:"first_name"`
	LastName          string        `db:"last_name" json:"last_name"`
	Status            ContactStatus `db:"status" json:"status"`
	ConsentBasis      string        `db:"consent_basis" json:"consent_basis"`
	ConsentSource     string        `db:"consent_source" json:"consent_source"`
	ConsentedAt       *time.Time    `db:"consented_at" json:"consented_at"`
	LastClickAt       *time.Time    `db:"last_click_at" json:"last_click_at"`
	LastEngagedAt     *time.Time    `db:"last_engaged_at" json:"last_engaged_at"`
	Tags              []string      `db:"tags" json:"tags"`
	TrackingAllowed   bool          `db:"tracking_allowed" json:"tracking_allowed"`
	Timezone          string        `db:"timezone" json:"timezone"`
	Country           string        `db:"country" json:"country"`
	CreatedAt         time.Time     `db:"created_at" json:"created_at"`
	Tier              Tier          `db:"tier" json:"tier"`
	SuppressionReason *string       `db:"suppression_reason" json:"suppression_reason"`
	// StatusBeforeUnsubscribe is set when someone unsubscribes; nil otherwise.
	StatusBeforeUnsubscribe *ContactStatus `db:"status_before_unsubscribe" json:"-"`
}

const contactCols = `c.id, c.brand_id, c.email, c.first_name, c.last_name, c.status, c.consent_basis,
	c.consent_source, c.consented_at, c.last_click_at, c.last_engaged_at, c.tags, c.tracking_allowed, c.timezone, c.country,
	c.created_at, ` + tierExpr + ` AS tier, s.reason AS suppression_reason, c.status_before_unsubscribe`

const contactFrom = ` FROM contacts c LEFT JOIN suppressions s ON s.email_hash = c.email_hash `

type ContactFilter struct {
	Tiers      []Tier
	Status     ContactStatus
	Tag        string
	Query      string // prefix of email, first or last name
	Suppressed *bool
	After      string // keyset: last email of the previous page
	Limit      int
}

// contactWhere builds the WHERE clause for a filter (shared by list and count).
func contactWhere(brandID uuid.UUID, f ContactFilter) ([]string, []any) {
	where := []string{"c.brand_id = $1"}
	args := []any{brandID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if len(f.Tiers) > 0 {
		tiers := make([]string, len(f.Tiers))
		for i, t := range f.Tiers {
			tiers[i] = string(t)
		}
		where = append(where, "("+tierExpr+") = ANY("+arg(tiers)+")")
	}
	if f.Status != "" {
		where = append(where, "c.status = "+arg(f.Status))
	}
	if f.Tag != "" {
		where = append(where, arg(f.Tag)+" = ANY(c.tags)")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := arg(escapeLike(strings.ToLower(q)) + "%")
		where = append(where, "(lower(c.email) LIKE "+p+" OR lower(c.first_name) LIKE "+p+" OR lower(c.last_name) LIKE "+p+")")
	}
	if f.Suppressed != nil {
		if *f.Suppressed {
			where = append(where, "s.email_hash IS NOT NULL")
		} else {
			where = append(where, "s.email_hash IS NULL")
		}
	}
	if f.After != "" {
		where = append(where, "c.email > "+arg(f.After))
	}
	return where, args
}

func (s *Store) ListContacts(ctx context.Context, brandID uuid.UUID, f ContactFilter) ([]Contact, error) {
	where, args := contactWhere(brandID, f)
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+contactFrom+
		` WHERE `+strings.Join(where, " AND ")+fmt.Sprintf(` ORDER BY c.email LIMIT $%d`, len(args)), args...)
	return all[Contact](rows, err)
}

// CountContacts counts the contacts a filter matches (After and Limit are ignored).
func (s *Store) CountContacts(ctx context.Context, brandID uuid.UUID, f ContactFilter) (int, error) {
	f.After = ""
	where, args := contactWhere(brandID, f)
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*)`+contactFrom+` WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	return n, err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Store) ContactByID(ctx context.Context, brandID, id uuid.UUID) (Contact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+contactFrom+` WHERE c.brand_id = $1 AND c.id = $2`, brandID, id)
	return one[Contact](rows, err)
}

func (s *Store) ContactByEmail(ctx context.Context, brandID uuid.UUID, email string) (Contact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+contactFrom+` WHERE c.brand_id = $1 AND c.email = $2`, brandID, email)
	return one[Contact](rows, err)
}

// ContactStats are the S3 header cards. Tier counts only include people who
// can be emailed: subscribed and not suppressed.
type ContactStats struct {
	Total            int          `json:"total"`
	Tiers            map[Tier]int `json:"tiers"`
	Quarantined      int          `json:"quarantined"`
	Pending          int          `json:"pending_confirmation"`
	Unsubscribed     int          `json:"unsubscribed"`
	SuppressedHere   int          `json:"suppressed_in_brand"`
	SuppressedGlobal *int         `json:"suppressed_company_wide,omitempty"` // nil for viewers

}

func (s *Store) ContactStats(ctx context.Context, brandID uuid.UUID) (ContactStats, error) {
	st := ContactStats{Tiers: map[Tier]int{TierActive: 0, TierWarm: 0, TierAtRisk: 0, TierSunset: 0}}
	rows, err := s.db.Query(ctx, `
		SELECT c.status, `+tierExpr+` AS tier, s.email_hash IS NOT NULL AS suppressed, count(*)
		`+contactFrom+` WHERE c.brand_id = $1 GROUP BY 1, 2, 3`, brandID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var status ContactStatus
		var tier Tier
		var suppressed bool
		var n int
		if err := rows.Scan(&status, &tier, &suppressed, &n); err != nil {
			return st, err
		}
		st.Total += n
		switch {
		case suppressed:
			st.SuppressedHere += n
		case status == ContactSubscribed:
			st.Tiers[tier] += n
		case status == ContactQuarantined:
			st.Quarantined += n
		case status == ContactPending:
			st.Pending += n
		case status == ContactUnsubscribed:
			st.Unsubscribed += n
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	var global int
	err = s.db.QueryRow(ctx, `SELECT count(*) FROM suppressions`).Scan(&global)
	st.SuppressedGlobal = &global
	return st, err
}

// NewContact is everything needed to insert one.
type NewContact struct {
	BrandID       uuid.UUID
	Email         string
	FirstName     string
	LastName      string
	Status        ContactStatus
	ConsentBasis  string
	ConsentSource string
	ConsentedAt   *time.Time
	LastClickAt   *time.Time
	// EngagedAt is a sign of life from elsewhere that counts like a click
	// (a 4–5 star rating in the old tool, stamped with the import time).
	EngagedAt       *time.Time
	TrackingAllowed bool
	Tags            []string
	Timezone        string
	Country         string
	ImportID        *uuid.UUID
}

// lastEngaged picks what the tier is measured from. Without tracking consent
// only the opt-in counts.
func (c NewContact) lastEngaged() *time.Time {
	if !c.TrackingAllowed {
		return c.ConsentedAt
	}
	return latest(latest(c.LastClickAt, c.EngagedAt), c.ConsentedAt)
}

func latest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil || a.After(*b):
		return a
	}
	return b
}

func (s *Store) InsertContact(ctx context.Context, c NewContact) (uuid.UUID, error) {
	if c.Tags == nil {
		c.Tags = []string{}
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO contacts (brand_id, email, email_hash, first_name, last_name, status, consent_basis,
			consent_source, consented_at, last_click_at, last_engaged_at, tracking_allowed, tags, timezone, country, import_id,
			imported_click_at, imported_engaged_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $10, $17)
		RETURNING id`,
		c.BrandID, c.Email, EmailHash(c.Email), c.FirstName, c.LastName, c.Status, c.ConsentBasis,
		c.ConsentSource, c.ConsentedAt, c.LastClickAt, c.lastEngaged(), c.TrackingAllowed, c.Tags,
		c.Timezone, c.Country, c.ImportID, c.EngagedAt).Scan(&id)
	return id, translate(err)
}

// MergeImportedFields fills in blanks and adds tags/activity for an existing
// contact. It never changes status or consent: an import can't re-subscribe anyone.
func (s *Store) MergeImportedFields(ctx context.Context, id uuid.UUID, c NewContact) error {
	if c.Tags == nil {
		c.Tags = []string{}
	}
	_, err := s.db.Exec(ctx, `
		UPDATE contacts SET
			first_name = CASE WHEN first_name = '' THEN $2 ELSE first_name END,
			last_name  = CASE WHEN last_name = '' THEN $3 ELSE last_name END,
			timezone   = CASE WHEN timezone = '' THEN $4 ELSE timezone END,
			country    = CASE WHEN country = '' THEN $5 ELSE country END,
			tags       = ARRAY(SELECT DISTINCT unnest(tags || $6::text[])),
			last_click_at   = CASE WHEN tracking_allowed THEN GREATEST(last_click_at, $7) ELSE last_click_at END,
			last_engaged_at = CASE WHEN tracking_allowed THEN GREATEST(last_engaged_at, $7, $8) ELSE last_engaged_at END,
			imported_click_at   = GREATEST(imported_click_at, $7),
			imported_engaged_at = GREATEST(imported_engaged_at, $8),
			updated_at = now()
		WHERE id = $1`, id, c.FirstName, c.LastName, c.Timezone, c.Country, c.Tags, c.LastClickAt, c.EngagedAt)
	return err
}

// LockContact loads a contact and locks its row until the transaction ends,
// so two unsubscribe clicks can't interleave.
func (s *Store) LockContact(ctx context.Context, brandID, id uuid.UUID) (Contact, error) {
	if _, err := s.db.Exec(ctx, `SELECT 1 FROM contacts WHERE brand_id = $1 AND id = $2 FOR UPDATE`, brandID, id); err != nil {
		return Contact{}, err
	}
	return s.ContactByID(ctx, brandID, id)
}

// Resubscribe: the person opted back in themselves (preference center).
// Their new consent counts as fresh engagement. Only someone who was properly
// subscribed before unsubscribing qualifies (ErrNotFound otherwise).
func (s *Store) Resubscribe(ctx context.Context, id uuid.UUID, at time.Time) error {
	return expectOne(s.db.Exec(ctx, `
		UPDATE contacts SET status = 'subscribed', status_before_unsubscribe = NULL, consented_at = $2,
			last_engaged_at = GREATEST(last_engaged_at, $2), updated_at = now()
		WHERE id = $1 AND status = 'unsubscribed' AND status_before_unsubscribe = 'subscribed'`, id, at))
}

// CampaignUnsubscribes counts unsubscribes that came from a campaign (M4 brake input).
func (s *Store) CampaignUnsubscribes(ctx context.Context, campaignID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(DISTINCT contact_id) FROM consent_records WHERE campaign_id = $1 AND kind = 'unsubscribed'`, campaignID).Scan(&n)
	return n, err
}

// SetContactStatus also remembers the status someone had when they unsubscribe.
func (s *Store) SetContactStatus(ctx context.Context, id uuid.UUID, status ContactStatus) error {
	_, err := s.db.Exec(ctx, `
		UPDATE contacts SET
			status_before_unsubscribe = CASE
				WHEN $2 = 'unsubscribed' AND status <> 'unsubscribed' THEN status
				WHEN $2 <> 'unsubscribed' THEN NULL
				ELSE status_before_unsubscribe END,
			status = $2, updated_at = now()
		WHERE id = $1`, id, status)
	return err
}

// ExistingContacts returns email → (id, status) for the given addresses in a brand.
func (s *Store) ExistingContacts(ctx context.Context, brandID uuid.UUID, emails []string) (map[string]Contact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+contactFrom+` WHERE c.brand_id = $1 AND c.email = ANY($2::citext[])`,
		brandID, emails)
	cs, err := all[Contact](rows, err)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Contact, len(cs))
	for _, c := range cs {
		out[strings.ToLower(c.Email)] = c
	}
	return out, nil
}

// --- consent records ---

type ConsentRecord struct {
	ID         int64      `db:"id" json:"id"`
	Kind       string     `db:"kind" json:"kind"`
	Method     string     `db:"method" json:"method"`
	Source     string     `db:"source" json:"source"`
	Wording    string     `db:"wording" json:"wording"`
	PageURL    string     `db:"page_url" json:"page_url"`
	IP         string     `db:"ip" json:"ip"`
	OccurredAt time.Time  `db:"occurred_at" json:"occurred_at"`
	RecordedBy *uuid.UUID `db:"recorded_by" json:"recorded_by"`
	ImportID   *uuid.UUID `db:"import_id" json:"import_id"`
}

type NewConsentRecord struct {
	BrandID    uuid.UUID
	ContactID  uuid.UUID
	Email      string
	Kind       string
	Method     string
	Source     string
	Wording    string
	PageURL    string
	IP         string
	OccurredAt time.Time
	RecordedBy *uuid.UUID
	ImportID   *uuid.UUID
	CampaignID *uuid.UUID // the email an unsubscribe came from
	WordingID  *uuid.UUID // the consent wording agreed to (Contacts API)
}

func (s *Store) AddConsentRecord(ctx context.Context, r NewConsentRecord) error {
	if r.OccurredAt.IsZero() {
		r.OccurredAt = time.Now()
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO consent_records (brand_id, contact_id, email_hash, kind, method, source, wording, page_url, ip, occurred_at, recorded_by, import_id, campaign_id, wording_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		r.BrandID, r.ContactID, EmailHash(r.Email), r.Kind, r.Method, r.Source, r.Wording, r.PageURL, r.IP,
		r.OccurredAt, r.RecordedBy, r.ImportID, r.CampaignID, r.WordingID)
	return err
}

func (s *Store) ConsentHistory(ctx context.Context, contactID uuid.UUID) ([]ConsentRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, kind, method, source, wording, page_url, ip, occurred_at, recorded_by, import_id
		FROM consent_records WHERE contact_id = $1 ORDER BY occurred_at DESC, id DESC`, contactID)
	return all[ConsentRecord](rows, err)
}

// --- topics ---

type Topic struct {
	ID          uuid.UUID `db:"id" json:"id"`
	Name        string    `db:"name" json:"name"`
	Subscribers int       `db:"subscribers" json:"subscribers"`
}

func (s *Store) CreateTopic(ctx context.Context, brandID uuid.UUID, name string) (Topic, error) {
	rows, err := s.db.Query(ctx, `INSERT INTO topics (brand_id, name) VALUES ($1, $2) RETURNING id, name, 0 AS subscribers`,
		brandID, name)
	return one[Topic](rows, err)
}

func (s *Store) ListTopics(ctx context.Context, brandID uuid.UUID) ([]Topic, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.name, count(ct.contact_id)::int AS subscribers
		FROM topics t LEFT JOIN contact_topics ct ON ct.topic_id = t.id
		WHERE t.brand_id = $1 GROUP BY t.id ORDER BY t.created_at, t.name`, brandID)
	return all[Topic](rows, err)
}

// AddContactTopics links topics; the same-brand foreign keys reject another brand's topic.
func (s *Store) AddContactTopics(ctx context.Context, contactID uuid.UUID, topicIDs []uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO contact_topics (brand_id, contact_id, topic_id)
		SELECT c.brand_id, c.id, t FROM contacts c, unnest($2::uuid[]) AS t WHERE c.id = $1
		ON CONFLICT DO NOTHING`, contactID, topicIDs)
	return translate(err)
}

func (s *Store) ContactTopicIDs(ctx context.Context, contactID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `SELECT topic_id FROM contact_topics WHERE contact_id = $1`, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) RemoveContactTopics(ctx context.Context, contactID uuid.UUID, topicIDs []uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM contact_topics WHERE contact_id = $1 AND topic_id = ANY($2::uuid[])`, contactID, topicIDs)
	return err
}

// --- suppressions ---

type SuppressionReason string

const (
	SuppressHardBounce SuppressionReason = "hard_bounce"
	SuppressSoftBounce SuppressionReason = "soft_bounce" // 3 in 30 days
	SuppressComplaint  SuppressionReason = "complaint"
	SuppressManual     SuppressionReason = "manual"
	SuppressErasure    SuppressionReason = "erasure"
)

// Suppress blocks an address for every brand. Re-suppressing keeps the first reason.
func (s *Store) Suppress(ctx context.Context, email string, reason SuppressionReason, brandID *uuid.UUID, note string, by *uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO suppressions (email_hash, reason, brand_id, note, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (email_hash) DO NOTHING`, EmailHash(email), reason, brandID, note, by)
	return err
}

// SuppressedHashes returns which of the given hashes are suppressed.
func (s *Store) SuppressedHashes(ctx context.Context, hashes [][]byte) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT email_hash FROM suppressions WHERE email_hash = ANY($1::bytea[])`, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[string(h)] = true
	}
	return out, rows.Err()
}
