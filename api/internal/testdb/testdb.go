// Package testdb gives each test its own freshly migrated Postgres schema.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kmrifat/klavio-alternative/api/internal/db"
)

// New skips the test unless TEST_DATABASE_URL is set.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "test_" + hex.EncodeToString(b)
	if err := ensureExtensions(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		admin.Close()
	})

	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return pool
}

// extensionLockKey is an arbitrary constant shared by every test process.
const extensionLockKey = 0x6b6c6176696f // "klavio"

// ensureExtensions creates the database-wide extensions the migrations need,
// in public so every test schema sees them. `go test ./...` runs packages in
// parallel processes, and CREATE EXTENSION IF NOT EXISTS is not safe against
// concurrent sessions on a fresh database (duplicate key on
// pg_extension_name_index), so creation is serialized with an advisory lock.
func ensureExtensions(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(extensionLockKey)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS citext SCHEMA public`)
		return err
	})
}
