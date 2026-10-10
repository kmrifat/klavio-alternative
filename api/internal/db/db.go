// Package db owns the Postgres connection pool and schema migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Connect opens a pool and verifies the database is reachable.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies the app's SQL migrations, then the river job-queue migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()

	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, files)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("app migrations: %w", err)
	}
	for _, r := range results {
		log.Info("applied migration", "source", "app", "version", r.Source.Version, "file", r.Source.Path)
	}

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	for _, v := range res.Versions {
		log.Info("applied migration", "source", "river", "version", v.Version)
	}
	return nil
}

// ProviderForTest returns the app's goose provider over pool, so tests can
// step migrations down and up. close releases it.
func ProviderForTest(pool *pgxpool.Pool) (provider *goose.Provider, close func(), err error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		sqlDB.Close()
		return nil, nil, err
	}
	provider, err = goose.NewProvider(goose.DialectPostgres, sqlDB, files)
	if err != nil {
		sqlDB.Close()
		return nil, nil, err
	}
	return provider, func() { sqlDB.Close() }, nil
}
