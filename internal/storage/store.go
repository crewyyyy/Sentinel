// Package storage implements the PostgreSQL persistence layer: durable
// update ingest with atomic dedup+cursor, message journal with edit
// versioning, trust aggregates, the leased AI job queue and related records.
package storage

import (
	"context"
	"fmt"
	"time"

	"sentinel/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store owns the pgx connection pool. The exported Pool lets the app compose
// its own transactions across repos; all queries remain parameterized.
type Store struct {
	Pool *pgxpool.Pool
}

const migrationTable = "schema_migrations"

// New connects and pings the database. Migrations are applied separately via
// Migrate so callers control schema lifecycle.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("storage: parse database url: %w", err)
	}
	// Bounded pool: the service is single-process with a handful of workers.
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("storage: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// Migrate applies the embedded migrations inside one transaction. The
// schema_migrations table guards re-application, so this is safe to run on
// every start.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin migrate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(824913)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}

	for _, migration := range migrations.Ordered() {
		var applied bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, migration.Version).Scan(&applied); err != nil {
			return fmt.Errorf("storage: check migration state: %w", err)
		}
		if applied {
			continue
		}
		if _, err = tx.Exec(ctx, migration.SQL); err != nil {
			return fmt.Errorf("storage: apply %s: %w", migration.Version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, migration.Version); err != nil {
			return fmt.Errorf("storage: record migration: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage: commit migrate: %w", err)
	}
	return nil
}

// execTx runs fn in a transaction, rolling back on error.
func execTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage: commit tx: %w", err)
	}
	return nil
}
