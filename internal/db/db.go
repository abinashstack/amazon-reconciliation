// Package db owns the pgx connection pool and the tiny .sql migration runner.
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DSN returns the connection string, from $RECON_DSN or a local default.
func DSN() string {
	if v := os.Getenv("RECON_DSN"); v != "" {
		return v
	}
	return "postgres://postgres:postgres@localhost:5432/recon?sslmode=disable"
}

// Connect opens a pool against DSN().
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, DSN())
}

// Migrate runs every *.sql file in dir, in filename order, each in its own tx.
// The schema file is idempotent (drop ... if exists), so re-running is safe.
func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", f, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		fmt.Printf("  applied %s\n", f)
	}
	return nil
}
