package store

import "context"

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
