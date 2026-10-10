package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Helpers used only by tests in other packages. They live in a regular file
// (not _test.go) so those packages can call them.

// MarkSteppedUpForTest makes every session of the user look like its passkey
// confirmation is 10 minutes old.
func (s *Store) MarkSteppedUpForTest(ctx context.Context, email string) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE sessions SET stepped_up_at = now() - interval '10 minutes'
		WHERE user_id = (SELECT id FROM users WHERE email = $1)`, email)
	return tag.RowsAffected(), err
}

// TryEditAuditForTest attempts an UPDATE that the append-only trigger must reject.
func (s *Store) TryEditAuditForTest(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE audit_log SET action = 'tampered'`)
	return tag.RowsAffected(), err
}

// BackdateImportForTest makes an import look untouched for the given time.
func (s *Store) BackdateImportForTest(ctx context.Context, id uuid.UUID, by time.Duration) error {
	_, err := s.db.Exec(ctx, `UPDATE imports SET updated_at = now() - make_interval(secs => $2) WHERE id = $1`, id, by.Seconds())
	return err
}

// ExpireApprovalForTest moves an approval's deadline into the past.
func (s *Store) ExpireApprovalForTest(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE approvals SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
	return err
}

// ExpireExportForTest ends an export's download window.
func (s *Store) ExpireExportForTest(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE exports SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
	return err
}

// BulkInsertContactsForTest adds n subscribed contacts quickly.
func (s *Store) BulkInsertContactsForTest(ctx context.Context, brandID uuid.UUID, n int) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO contacts (brand_id, email, email_hash, status, consent_basis, consented_at, last_engaged_at)
		SELECT $1, 'bulk' || g || '@example.com', sha256(convert_to('bulk' || g || '@example.com', 'UTF8')),
			'subscribed', 'single_opt_in', now(), now()
		FROM generate_series(1, $2) g`, brandID, n)
	return err
}

// ExecForTest runs a statement, for tests that need to age or reset rows.
func (s *Store) ExecForTest(ctx context.Context, sql string, args ...any) error {
	_, err := s.db.Exec(ctx, sql, args...)
	return err
}
