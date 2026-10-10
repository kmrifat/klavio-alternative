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

// Template generations: a counter per brand, bumped by every layout or
// allowlist change. Compiling happens outside any transaction; a result is
// stored only if the generation it was built against is still current.

// TemplateGeneration returns the brand's current generation (0 if none yet).
func (s *Store) TemplateGeneration(ctx context.Context, brandID uuid.UUID) (int64, error) {
	var g int64
	err := s.db.QueryRow(ctx, `SELECT generation FROM template_generations WHERE brand_id = $1`, brandID).Scan(&g)
	if errors.Is(translate(err), ErrNotFound) {
		return 0, nil
	}
	return g, err
}

// LockTemplateGeneration returns the current generation and holds it until
// the transaction ends: a layout or allowlist change waits until then.
func (s *Store) LockTemplateGeneration(ctx context.Context, brandID uuid.UUID) (int64, error) {
	if _, err := s.db.Exec(ctx, `INSERT INTO template_generations (brand_id) VALUES ($1) ON CONFLICT DO NOTHING`, brandID); err != nil {
		return 0, err
	}
	var g int64
	err := s.db.QueryRow(ctx, `SELECT generation FROM template_generations WHERE brand_id = $1 FOR SHARE`, brandID).Scan(&g)
	return g, err
}

// BumpTemplateGeneration starts a new generation; every template becomes stale.
func (s *Store) BumpTemplateGeneration(ctx context.Context, brandID uuid.UUID) (int64, error) {
	var g int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO template_generations (brand_id, generation) VALUES ($1, 1)
		ON CONFLICT (brand_id) DO UPDATE SET generation = template_generations.generation + 1
		RETURNING generation`, brandID).Scan(&g)
	return g, err
}

// BrandsWithStaleTemplates lists brands that have a template built against
// an old generation (a rebuild job was lost or failed).
func (s *Store) BrandsWithStaleTemplates(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT t.brand_id FROM templates t JOIN template_generations g USING (brand_id)
		WHERE t.archived_at IS NULL AND t.built_generation < g.generation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
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
	ID              uuid.UUID       `db:"id" json:"id"`
	BrandID         uuid.UUID       `db:"brand_id" json:"-"`
	Name            string          `db:"name" json:"name"`
	MJML            string          `db:"mjml" json:"mjml"`
	HTML            string          `db:"html" json:"html"`
	TextBody        string          `db:"text_body" json:"text"`
	Issues          json.RawMessage `db:"issues" json:"issues"`
	ErrorCount      int             `db:"error_count" json:"error_count"`
	WarningCount    int             `db:"warning_count" json:"warning_count"`
	LayoutVersion   int             `db:"layout_version" json:"layout_version"`
	BuiltGeneration int64           `db:"built_generation" json:"-"`
	// Stale: the layout or allowlist changed and the rebuild hasn't reached
	// this template yet. Its checks may be out of date; don't send it.
	Stale     bool       `db:"stale" json:"stale"`
	Version   int        `db:"version" json:"version"`
	CreatedBy *uuid.UUID `db:"created_by" json:"created_by"`
	UpdatedBy *uuid.UUID `db:"updated_by" json:"updated_by"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
}

// TemplateSummary is a row of the templates list.
type TemplateSummary struct {
	ID            uuid.UUID `db:"id" json:"id"`
	Name          string    `db:"name" json:"name"`
	ErrorCount    int       `db:"error_count" json:"error_count"`
	WarningCount  int       `db:"warning_count" json:"warning_count"`
	Stale         bool      `db:"stale" json:"stale"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedByName *string   `db:"updated_by_name" json:"updated_by_name"`
}

// templateSelect reads templates (t) with their staleness.
const templateSelect = `SELECT t.id, t.brand_id, t.name, t.mjml, t.html, t.text_body, t.issues, t.error_count, t.warning_count,
	t.layout_version, t.built_generation, t.built_generation < coalesce(g.generation, 0) AS stale,
	t.version, t.created_by, t.updated_by, t.created_at, t.updated_at
	FROM templates t LEFT JOIN template_generations g ON g.brand_id = t.brand_id`

// TemplateBuild is the compiled result saved with a template.
type TemplateBuild struct {
	HTML          string
	Text          string
	Issues        json.RawMessage
	ErrorCount    int
	WarningCount  int
	LayoutVersion int
	Generation    int64
}

func (s *Store) CreateTemplate(ctx context.Context, brandID uuid.UUID, name, mjml string, b TemplateBuild, by uuid.UUID) (Template, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO templates (brand_id, name, mjml, html, text_body, issues, error_count, warning_count, layout_version, built_generation, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		RETURNING id`,
		brandID, name, mjml, b.HTML, b.Text, b.Issues, b.ErrorCount, b.WarningCount, b.LayoutVersion, b.Generation, by).Scan(&id)
	if err != nil {
		return Template{}, translate(err)
	}
	return s.TemplateByID(ctx, brandID, id)
}

// TemplateByID returns a live (not archived) template of the brand.
func (s *Store) TemplateByID(ctx context.Context, brandID, id uuid.UUID) (Template, error) {
	rows, err := s.db.Query(ctx, templateSelect+` WHERE t.brand_id = $1 AND t.id = $2 AND t.archived_at IS NULL`, brandID, id)
	return one[Template](rows, err)
}

func (s *Store) ListTemplates(ctx context.Context, brandID uuid.UUID) ([]TemplateSummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.name, t.error_count, t.warning_count, t.updated_at, u.name AS updated_by_name,
			t.built_generation < coalesce(g.generation, 0) AS stale
		FROM templates t
		LEFT JOIN users u ON u.id = t.updated_by
		LEFT JOIN template_generations g ON g.brand_id = t.brand_id
		WHERE t.brand_id = $1 AND t.archived_at IS NULL
		ORDER BY t.updated_at DESC`, brandID)
	return all[TemplateSummary](rows, err)
}

// StaleTemplates returns the brand's live templates built before generation.
func (s *Store) StaleTemplates(ctx context.Context, brandID uuid.UUID, generation int64) ([]Template, error) {
	rows, err := s.db.Query(ctx, templateSelect+`
		WHERE t.brand_id = $1 AND t.archived_at IS NULL AND t.built_generation < $2 ORDER BY t.id`, brandID, generation)
	return all[Template](rows, err)
}

func (s *Store) CountLiveTemplates(ctx context.Context, brandID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM templates WHERE brand_id = $1 AND archived_at IS NULL`, brandID).Scan(&n)
	return n, err
}

// UpdateTemplate saves an edit. It fails with ErrStale unless the stored
// version is still fromVersion.
func (s *Store) UpdateTemplate(ctx context.Context, brandID, id uuid.UUID, name, mjml string, b TemplateBuild, fromVersion int, by uuid.UUID) (Template, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE templates SET name = $3, mjml = $4, html = $5, text_body = $6, issues = $7, error_count = $8,
			warning_count = $9, layout_version = $10, built_generation = $11, version = version + 1, updated_by = $13, updated_at = now()
		WHERE brand_id = $1 AND id = $2 AND archived_at IS NULL AND version = $12`,
		brandID, id, name, mjml, b.HTML, b.Text, b.Issues, b.ErrorCount, b.WarningCount, b.LayoutVersion, b.Generation, fromVersion, by)
	if err != nil {
		return Template{}, translate(err)
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.TemplateByID(ctx, brandID, id); gerr == nil {
			return Template{}, ErrStale
		}
		return Template{}, ErrNotFound
	}
	return s.TemplateByID(ctx, brandID, id)
}

// RebuildTemplate stores a background recompile. It keeps the author's
// version and updated_by (nobody edited the template), and writes nothing
// if the template was edited since, or the generation moved on: a later
// rebuild will handle it. Reports whether it wrote.
func (s *Store) RebuildTemplate(ctx context.Context, brandID, id uuid.UUID, version int, b TemplateBuild) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE templates SET html = $4, text_body = $5, issues = $6, error_count = $7, warning_count = $8,
			layout_version = $9, built_generation = $10
		WHERE brand_id = $1 AND id = $2 AND version = $3 AND archived_at IS NULL AND built_generation < $10
		  AND $10 = (SELECT generation FROM template_generations WHERE brand_id = $1)`,
		brandID, id, version, b.HTML, b.Text, b.Issues, b.ErrorCount, b.WarningCount, b.LayoutVersion, b.Generation)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
