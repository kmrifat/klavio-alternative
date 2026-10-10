package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrStale is returned when a row changed since the caller read it.
var ErrStale = errors.New("changed by someone else")

// LockBrandContent serialises template saves (shared) against layout and
// allowlist changes (exclusive), so no template is ever stored compiled
// against an old layout or checked against an old allowlist.
func (s *Store) LockBrandContent(ctx context.Context, brandID uuid.UUID, exclusive bool) error {
	mode := "FOR SHARE"
	if exclusive {
		mode = "FOR UPDATE"
	}
	var one int
	return translate(s.db.QueryRow(ctx, `SELECT 1 FROM brands WHERE id = $1 `+mode, brandID).Scan(&one))
}

// Layout is a brand's saved layout. Version 0 means none was saved yet and
// the default is in use.
type Layout struct {
	BrandID   uuid.UUID  `db:"brand_id" json:"-"`
	MJML      string     `db:"mjml" json:"mjml"`
	Version   int        `db:"version" json:"version"`
	UpdatedBy *uuid.UUID `db:"updated_by" json:"updated_by"`
	UpdatedAt *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) LayoutForBrand(ctx context.Context, brandID uuid.UUID) (Layout, error) {
	rows, err := s.db.Query(ctx, `SELECT brand_id, mjml, version, updated_by, updated_at FROM brand_layouts WHERE brand_id = $1`, brandID)
	return one[Layout](rows, err)
}

// SaveLayout stores a new layout version. It fails with ErrStale unless the
// stored version is still fromVersion (0 = no layout saved yet).
func (s *Store) SaveLayout(ctx context.Context, brandID uuid.UUID, mjml string, fromVersion int, by uuid.UUID) (Layout, error) {
	const cols = `brand_id, mjml, version, updated_by, updated_at`
	var l Layout
	var err error
	if fromVersion == 0 {
		rows, qerr := s.db.Query(ctx, `
			INSERT INTO brand_layouts (brand_id, mjml, version, updated_by) VALUES ($1, $2, 1, $3)
			ON CONFLICT (brand_id) DO NOTHING
			RETURNING `+cols, brandID, mjml, by)
		l, err = one[Layout](rows, qerr)
	} else {
		rows, qerr := s.db.Query(ctx, `
			UPDATE brand_layouts SET mjml = $2, version = version + 1, updated_by = $4, updated_at = now()
			WHERE brand_id = $1 AND version = $3
			RETURNING `+cols, brandID, mjml, fromVersion, by)
		l, err = one[Layout](rows, qerr)
	}
	if errors.Is(err, ErrNotFound) {
		return Layout{}, ErrStale
	}
	return l, err
}

// Link allowlist

type AllowedDomain struct {
	Domain    string     `db:"domain" json:"domain"`
	AddedBy   *uuid.UUID `db:"added_by" json:"added_by"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
}

func (s *Store) LinkAllowlist(ctx context.Context, brandID uuid.UUID) ([]AllowedDomain, error) {
	rows, err := s.db.Query(ctx, `SELECT domain, added_by, created_at FROM link_allowlist WHERE brand_id = $1 ORDER BY domain`, brandID)
	return all[AllowedDomain](rows, err)
}

func (s *Store) AllowDomain(ctx context.Context, brandID uuid.UUID, domain string, by uuid.UUID) (AllowedDomain, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO link_allowlist (brand_id, domain, added_by) VALUES ($1, $2, $3)
		RETURNING domain, added_by, created_at`, brandID, domain, by)
	return one[AllowedDomain](rows, err)
}

// DisallowDomain removes a domain; ErrNotFound if it wasn't there.
func (s *Store) DisallowDomain(ctx context.Context, brandID uuid.UUID, domain string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM link_allowlist WHERE brand_id = $1 AND domain = $2`, brandID, domain)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Templates

type Template struct {
	ID            uuid.UUID       `db:"id" json:"id"`
	BrandID       uuid.UUID       `db:"brand_id" json:"-"`
	Name          string          `db:"name" json:"name"`
	MJML          string          `db:"mjml" json:"mjml"`
	HTML          string          `db:"html" json:"html"`
	TextBody      string          `db:"text_body" json:"text"`
	Issues        json.RawMessage `db:"issues" json:"issues"`
	ErrorCount    int             `db:"error_count" json:"error_count"`
	WarningCount  int             `db:"warning_count" json:"warning_count"`
	LayoutVersion int             `db:"layout_version" json:"layout_version"`
	Version       int             `db:"version" json:"version"`
	CreatedBy     *uuid.UUID      `db:"created_by" json:"created_by"`
	UpdatedBy     *uuid.UUID      `db:"updated_by" json:"updated_by"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
}

// TemplateSummary is a row of the templates list.
type TemplateSummary struct {
	ID            uuid.UUID `db:"id" json:"id"`
	Name          string    `db:"name" json:"name"`
	ErrorCount    int       `db:"error_count" json:"error_count"`
	WarningCount  int       `db:"warning_count" json:"warning_count"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedByName *string   `db:"updated_by_name" json:"updated_by_name"`
}

const templateCols = `id, brand_id, name, mjml, html, text_body, issues, error_count, warning_count,
	layout_version, version, created_by, updated_by, created_at, updated_at`

// TemplateBuild is the compiled result saved with a template.
type TemplateBuild struct {
	HTML          string
	Text          string
	Issues        json.RawMessage
	ErrorCount    int
	WarningCount  int
	LayoutVersion int
}

func (s *Store) CreateTemplate(ctx context.Context, brandID uuid.UUID, name, mjml string, b TemplateBuild, by uuid.UUID) (Template, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO templates (brand_id, name, mjml, html, text_body, issues, error_count, warning_count, layout_version, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
		RETURNING `+templateCols,
		brandID, name, mjml, b.HTML, b.Text, b.Issues, b.ErrorCount, b.WarningCount, b.LayoutVersion, by)
	return one[Template](rows, err)
}

// TemplateByID returns a live (not archived) template of the brand.
func (s *Store) TemplateByID(ctx context.Context, brandID, id uuid.UUID) (Template, error) {
	rows, err := s.db.Query(ctx, `SELECT `+templateCols+` FROM templates WHERE brand_id = $1 AND id = $2 AND archived_at IS NULL`, brandID, id)
	return one[Template](rows, err)
}

func (s *Store) ListTemplates(ctx context.Context, brandID uuid.UUID) ([]TemplateSummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.name, t.error_count, t.warning_count, t.updated_at, u.name AS updated_by_name
		FROM templates t LEFT JOIN users u ON u.id = t.updated_by
		WHERE t.brand_id = $1 AND t.archived_at IS NULL
		ORDER BY t.updated_at DESC`, brandID)
	return all[TemplateSummary](rows, err)
}

// LiveTemplates returns every non-archived template of a brand, for recompiling.
func (s *Store) LiveTemplates(ctx context.Context, brandID uuid.UUID) ([]Template, error) {
	rows, err := s.db.Query(ctx, `SELECT `+templateCols+` FROM templates WHERE brand_id = $1 AND archived_at IS NULL ORDER BY id FOR UPDATE`, brandID)
	return all[Template](rows, err)
}

// UpdateTemplate saves an edit. It fails with ErrStale unless the stored
// version is still fromVersion.
func (s *Store) UpdateTemplate(ctx context.Context, brandID, id uuid.UUID, name, mjml string, b TemplateBuild, fromVersion int, by uuid.UUID) (Template, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE templates SET name = $3, mjml = $4, html = $5, text_body = $6, issues = $7, error_count = $8,
			warning_count = $9, layout_version = $10, version = version + 1, updated_by = $12, updated_at = now()
		WHERE brand_id = $1 AND id = $2 AND archived_at IS NULL AND version = $11
		RETURNING `+templateCols,
		brandID, id, name, mjml, b.HTML, b.Text, b.Issues, b.ErrorCount, b.WarningCount, b.LayoutVersion, fromVersion, by)
	t, err := one[Template](rows, err)
	if errors.Is(err, ErrNotFound) {
		if _, gerr := s.TemplateByID(ctx, brandID, id); gerr == nil {
			return Template{}, ErrStale
		}
	}
	return t, err
}

// RebuildTemplate stores a recompile after a layout change. It keeps the
// author's version number and updated_by: nobody edited the template.
func (s *Store) RebuildTemplate(ctx context.Context, id uuid.UUID, b TemplateBuild) error {
	_, err := s.db.Exec(ctx, `
		UPDATE templates SET html = $2, text_body = $3, issues = $4, error_count = $5, warning_count = $6, layout_version = $7
		WHERE id = $1`, id, b.HTML, b.Text, b.Issues, b.ErrorCount, b.WarningCount, b.LayoutVersion)
	return err
}

func (s *Store) ArchiveTemplate(ctx context.Context, brandID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE templates SET archived_at = now() WHERE brand_id = $1 AND id = $2 AND archived_at IS NULL`, brandID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
