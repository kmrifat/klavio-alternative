package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type BrandStatus string

const (
	BrandSetup  BrandStatus = "setup"
	BrandReady  BrandStatus = "ready"
	BrandPaused BrandStatus = "paused"
)

type Brand struct {
	ID                  uuid.UUID   `db:"id" json:"id"`
	Slug                string      `db:"slug" json:"slug"`
	Name                string      `db:"name" json:"name"`
	Color               string      `db:"color" json:"color"`
	PostalAddress       string      `db:"postal_address" json:"postal_address"`
	SendingDomain       string      `db:"sending_domain" json:"sending_domain"`
	MailFromDomain      string      `db:"mail_from_domain" json:"mail_from_domain"`
	LinkDomain          string      `db:"link_domain" json:"link_domain"`
	SESConfigurationSet string      `db:"ses_configuration_set" json:"ses_configuration_set"`
	SESIdentityStatus   string      `db:"ses_identity_status" json:"ses_identity_status"`
	Status              BrandStatus `db:"status" json:"status"`
	DNSCheckedAt        *time.Time  `db:"dns_checked_at" json:"dns_checked_at"`
	WarmupStartedOn     *time.Time  `db:"warmup_started_on" json:"warmup_started_on"`
	// RepermissionOn: quarantined imports get re-permission emails (M7).
	RepermissionOn bool      `db:"repermission_on" json:"repermission_on"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
}

const brandCols = `id, slug, name, color, postal_address, sending_domain, mail_from_domain, link_domain,
	ses_configuration_set, ses_identity_status, status, dns_checked_at, warmup_started_on, repermission_on, created_at, updated_at`

func (s *Store) CreateBrand(ctx context.Context, b Brand) (Brand, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO brands (slug, name, color, postal_address, sending_domain, mail_from_domain, link_domain, ses_configuration_set)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+brandCols,
		b.Slug, b.Name, b.Color, b.PostalAddress, b.SendingDomain, b.MailFromDomain, b.LinkDomain, b.SESConfigurationSet)
	return one[Brand](rows, err)
}

func (s *Store) BrandBySlug(ctx context.Context, slug string) (Brand, error) {
	rows, err := s.db.Query(ctx, `SELECT `+brandCols+` FROM brands WHERE slug = $1`, slug)
	return one[Brand](rows, err)
}

func (s *Store) BrandByID(ctx context.Context, id uuid.UUID) (Brand, error) {
	rows, err := s.db.Query(ctx, `SELECT `+brandCols+` FROM brands WHERE id = $1`, id)
	return one[Brand](rows, err)
}

func (s *Store) ListBrands(ctx context.Context) ([]Brand, error) {
	rows, err := s.db.Query(ctx, `SELECT `+brandCols+` FROM brands ORDER BY name`)
	return all[Brand](rows, err)
}

// UpdateBrandDetails changes the editable, non-domain fields.
func (s *Store) UpdateBrandDetails(ctx context.Context, id uuid.UUID, name, color, postalAddress string) (Brand, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE brands SET name = $2, color = $3, postal_address = $4, updated_at = now()
		WHERE id = $1 RETURNING `+brandCols, id, name, color, postalAddress)
	return one[Brand](rows, err)
}

func (s *Store) SetBrandCheckResult(ctx context.Context, id uuid.UUID, sesStatus string, status BrandStatus, at time.Time) error {
	_, err := s.db.Exec(ctx, `
		UPDATE brands SET ses_identity_status = $2, status = $3, dns_checked_at = $4, updated_at = now()
		WHERE id = $1`, id, sesStatus, status, at)
	return err
}

// DNS records

type DNSRecord struct {
	ID        uuid.UUID  `db:"id" json:"id"`
	BrandID   uuid.UUID  `db:"brand_id" json:"-"`
	Purpose   string     `db:"purpose" json:"purpose"`
	Type      string     `db:"type" json:"type"`
	Name      string     `db:"name" json:"name"`
	Value     string     `db:"value" json:"value"`
	Status    string     `db:"status" json:"status"`
	Detail    string     `db:"detail" json:"detail"`
	CheckedAt *time.Time `db:"checked_at" json:"checked_at"`
}

func (s *Store) AddDNSRecords(ctx context.Context, brandID uuid.UUID, recs []DNSRecord) error {
	for _, r := range recs {
		_, err := s.db.Exec(ctx, `
			INSERT INTO brand_dns_records (brand_id, purpose, type, name, value) VALUES ($1, $2, $3, $4, $5)`,
			brandID, r.Purpose, r.Type, r.Name, r.Value)
		if err != nil {
			return translate(err)
		}
	}
	return nil
}

func (s *Store) DNSRecords(ctx context.Context, brandID uuid.UUID) ([]DNSRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, brand_id, purpose, type, name, value, status, detail, checked_at
		FROM brand_dns_records WHERE brand_id = $1 ORDER BY purpose, name`, brandID)
	return all[DNSRecord](rows, err)
}

func (s *Store) SetDNSRecordStatus(ctx context.Context, id uuid.UUID, status, detail string, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE brand_dns_records SET status = $2, detail = $3, checked_at = $4 WHERE id = $1`,
		id, status, detail, at)
	return err
}
