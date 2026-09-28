package postgres

import (
	"context"
	"errors"
	"fmt"
)

// Advance this marker when a new required schema migration is introduced.
const requiredMigration = "018_attempt_delivery_index.sql"

// Ready verifies both a working database connection and the event table used
// by acceptance. A successful Ping alone would miss unapplied migrations.
func (s EventStore) Ready(ctx context.Context) error {
	var migrated, hasEvents bool
	if err := s.DB.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1),
		EXISTS (SELECT 1 FROM events)`, requiredMigration).Scan(&migrated, &hasEvents); err != nil {
		return fmt.Errorf("read application schema: %w", err)
	}
	if !migrated {
		return errors.New("required migration is missing")
	}
	return nil
}
