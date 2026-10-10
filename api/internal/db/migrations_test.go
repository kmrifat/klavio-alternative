package db_test

import (
	"context"
	"testing"

	"github.com/kmrifat/klavio-alternative/api/internal/db"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

// The latest migration goes down and back up, even with rows that use the
// values it added.
func TestLatestMigrationDownAndUp(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	for _, q := range []string{
		`INSERT INTO suppressions (email_hash, reason) VALUES ('\x01', 'soft_bounce')`,
		`INSERT INTO consent_records (brand_id, email_hash, kind, method) VALUES (gen_random_uuid(), '\x01', 'bounced', 'ses')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	p, closeFn, err := db.ProviderForTest(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
