package logsafe

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Kind is safe to log even when an underlying error includes a DSN, URL,
// request body, or key material. Never log the original error beside it.
func Kind(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres_" + pgErr.Code
	}
	return "internal"
}
