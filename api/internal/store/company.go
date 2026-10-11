package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CompanyName is the name set in Settings → General, or "" if none is.
func (s *Store) CompanyName(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM company_state WHERE id = 1`).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// SetCompanyName changes the name and returns the one it replaced.
func (s *Store) SetCompanyName(ctx context.Context, name string) (string, error) {
	var old string
	err := s.db.QueryRow(ctx, `
		WITH old AS (SELECT name FROM company_state WHERE id = 1 FOR UPDATE)
		INSERT INTO company_state (id, name) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name
		RETURNING COALESCE((SELECT name FROM old), '')`, name).Scan(&old)
	return old, err
}
