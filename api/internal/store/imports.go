package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ImportStatus string

const (
	ImportUploaded  ImportStatus = "uploaded"
	ImportChecking  ImportStatus = "checking"
	ImportChecked   ImportStatus = "checked"
	ImportImporting ImportStatus = "importing"
	ImportDone      ImportStatus = "done"
	ImportFailed    ImportStatus = "failed"
	ImportCancelled ImportStatus = "cancelled"
)

// Import is an S4 import. Mapping, Consent and the results are JSON owned by
// the imports package.
type Import struct {
	ID           uuid.UUID       `db:"id" json:"id"`
	BrandID      uuid.UUID       `db:"brand_id" json:"-"`
	CreatedBy    uuid.UUID       `db:"created_by" json:"created_by"`
	Filename     string          `db:"filename" json:"filename"`
	RowCount     int             `db:"row_count" json:"row_count"`
	Headers      []string        `db:"headers" json:"headers"`
	Mapping      json.RawMessage `db:"mapping" json:"mapping"`
	Consent      json.RawMessage `db:"consent" json:"consent"`
	TopicIDs     []uuid.UUID     `db:"topic_ids" json:"topic_ids"`
	Status       ImportStatus    `db:"status" json:"status"`
	CheckResult  json.RawMessage `db:"check_result" json:"check_result"`
	ImportResult json.RawMessage `db:"import_result" json:"import_result"`
	Error        string          `db:"error" json:"error"`
	CreatedAt    time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time       `db:"updated_at" json:"updated_at"`
}

const importCols = `id, brand_id, created_by, filename, row_count, headers, mapping, consent, topic_ids,
	status, check_result, import_result, error, created_at, updated_at`

func (s *Store) CreateImport(ctx context.Context, brandID, by uuid.UUID, filename string, file []byte, rowCount int, headers []string, mapping any) (Import, error) {
	m, err := json.Marshal(mapping)
	if err != nil {
		return Import{}, err
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO imports (brand_id, created_by, filename, file, row_count, headers, mapping)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+importCols,
		brandID, by, filename, file, rowCount, headers, m)
	return one[Import](rows, err)
}

func (s *Store) ImportByID(ctx context.Context, brandID, id uuid.UUID) (Import, error) {
	rows, err := s.db.Query(ctx, `SELECT `+importCols+` FROM imports WHERE brand_id = $1 AND id = $2`, brandID, id)
	return one[Import](rows, err)
}

// ImportFile returns the uploaded CSV (nil once the import is finished).
func (s *Store) ImportFile(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var f []byte
	err := s.db.QueryRow(ctx, `SELECT file FROM imports WHERE id = $1`, id).Scan(&f)
	return f, translate(err)
}

func (s *Store) ListImports(ctx context.Context, brandID uuid.UUID, limit int) ([]Import, error) {
	rows, err := s.db.Query(ctx, `SELECT `+importCols+` FROM imports WHERE brand_id = $1 ORDER BY created_at DESC LIMIT $2`,
		brandID, limit)
	return all[Import](rows, err)
}

// UpdateImportSetup saves the column mapping, consent answers and topics, and
// resets any earlier check (answers changed, so the check must run again).
func (s *Store) UpdateImportSetup(ctx context.Context, id uuid.UUID, mapping, consent any, topicIDs []uuid.UUID) error {
	m, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	c, err := json.Marshal(consent)
	if err != nil {
		return err
	}
	if topicIDs == nil {
		topicIDs = []uuid.UUID{}
	}
	_, err = s.db.Exec(ctx, `
		UPDATE imports SET mapping = $2, consent = $3, topic_ids = $4, status = 'uploaded', check_result = NULL, updated_at = now()
		WHERE id = $1`, id, m, c, topicIDs)
	return err
}

// TransitionImport moves an import from one status to another; it returns
// ErrNotFound if the import wasn't in `from` (someone else moved it first).
func (s *Store) TransitionImport(ctx context.Context, id uuid.UUID, from []ImportStatus, to ImportStatus) error {
	fromS := make([]string, len(from))
	for i, f := range from {
		fromS[i] = string(f)
	}
	tag, err := s.db.Exec(ctx, `UPDATE imports SET status = $3, error = '', updated_at = now() WHERE id = $1 AND status = ANY($2)`,
		id, fromS, to)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SetImportCheckResult(ctx context.Context, id uuid.UUID, result any) error {
	r, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE imports SET status = 'checked', check_result = $2, updated_at = now() WHERE id = $1`, id, r)
	return err
}

// FinishImport stores the result and drops the uploaded file (it holds personal data).
func (s *Store) FinishImport(ctx context.Context, id uuid.UUID, result any) error {
	r, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE imports SET status = 'done', import_result = $2, file = NULL, updated_at = now() WHERE id = $1`, id, r)
	return err
}

func (s *Store) FailImport(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE imports SET status = 'failed', error = $2, updated_at = now() WHERE id = $1`, id, msg)
	return err
}

func (s *Store) CancelImport(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE imports SET status = 'cancelled', file = NULL, updated_at = now()
		WHERE id = $1 AND status IN ('uploaded', 'checked', 'failed')`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
