// Package store is the Postgres data-access layer. Every query lives here.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a single-row lookup finds nothing.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint is violated.
var ErrConflict = errors.New("already exists")

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db   DBTX
	pool *pgxpool.Pool // nil inside a transaction
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{db: pool, pool: pool}
}

// InTx runs fn in a transaction. fn gets a Store bound to that transaction.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	if s.pool == nil {
		return fn(s) // already in a transaction
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&Store{db: tx})
	})
}

// InTransaction reports whether the store is bound to a transaction.
func (s *Store) InTransaction() bool { return s != nil && s.pool == nil }

// translate maps driver errors to store errors.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
	}
	return err
}

func one[T any](rows pgx.Rows, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, translate(err)
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	return v, translate(err)
}

func all[T any](rows pgx.Rows, err error) ([]T, error) {
	if err != nil {
		return nil, translate(err)
	}
	v, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	return v, translate(err)
}
