package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Migrate applies numbered, single-statement SQL files in filename order.
// Each migration and its checksum record commit together.
func Migrate(ctx context.Context, db *sql.DB, dir string) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migration directory: %w", err)
	}
	count := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".sql") {
			continue
		}
		count++
		if err := applyMigration(ctx, db, filepath.Join(dir, file.Name()), file.Name()); err != nil {
			return err
		}
	}
	if count == 0 {
		return errors.New("no SQL migrations found")
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, path, name string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	checksum := sha256.Sum256(contents)
	hash := hex.EncodeToString(checksum[:])
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(937241)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	var appliedHash string
	err = tx.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE name = $1`, name).Scan(&appliedHash)
	if err == nil {
		if appliedHash != hash {
			return fmt.Errorf("migration %s changed after application", name)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`, name, hash); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}
