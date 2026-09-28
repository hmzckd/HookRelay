package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"hookrelay/internal/endpoints"
	"hookrelay/internal/events"
	"hookrelay/internal/keyring"
	"hookrelay/internal/targetpolicy"
)

var ErrEndpointNameConflict = errors.New("endpoint name already exists")
var ErrInvalidEndpointURL = errors.New("endpoint URL is not allowed by the target profile")
var ErrUnknownKeyID = errors.New("key ID is not configured for this target")
var ErrEndpointVersionConflict = errors.New("endpoint version changed")

type EndpointStore struct {
	DB      *sql.DB
	Profile targetpolicy.Profile
	KeyIDs  map[string]struct{}
}

func (s EndpointStore) validateTarget(targetURL, keyID string) (string, error) {
	target, err := targetpolicy.ParseForProfile(targetURL, s.Profile)
	if err != nil {
		return "", ErrInvalidEndpointURL
	}
	if target.Demo {
		ref, _ := endpoints.DemoSecretRef(targetURL)
		if keyID != "" && keyID != ref {
			return "", ErrUnknownKeyID
		}
		return ref, nil
	}
	if !keyring.ValidID(keyID) {
		return "", ErrUnknownKeyID
	}
	if _, ok := s.KeyIDs[keyID]; !ok {
		return "", ErrUnknownKeyID
	}
	return keyID, nil
}

func (s EndpointStore) Create(ctx context.Context, name, targetURL, keyID string) (endpoints.Endpoint, error) {
	secretRef, err := s.validateTarget(targetURL, keyID)
	if err != nil {
		return endpoints.Endpoint{}, err
	}
	id, err := events.NewID()
	if err != nil {
		return endpoints.Endpoint{}, err
	}
	var result endpoints.Endpoint
	err = s.DB.QueryRowContext(ctx, `INSERT INTO endpoints (id, name, url, secret_ref)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, url, secret_ref, version, enabled, created_at`, id, name, targetURL, secretRef).
		Scan(&result.ID, &result.Name, &result.URL, &result.KeyID, &result.Version, &result.Enabled, &result.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return endpoints.Endpoint{}, ErrEndpointNameConflict
		}
		return endpoints.Endpoint{}, fmt.Errorf("create endpoint: %w", err)
	}
	return result, nil
}

func (s EndpointStore) Get(ctx context.Context, id string) (endpoints.Endpoint, error) {
	var result endpoints.Endpoint
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, url, secret_ref, version, enabled, created_at FROM endpoints WHERE id = $1`, id).
		Scan(&result.ID, &result.Name, &result.URL, &result.KeyID, &result.Version, &result.Enabled, &result.CreatedAt)
	if err != nil {
		return endpoints.Endpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	return result, nil
}

func (s EndpointStore) List(ctx context.Context, limit, offset int) (endpoints.Page, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, url, secret_ref, version, enabled, created_at
		FROM endpoints ORDER BY created_at, id LIMIT $1 OFFSET $2`, limit+1, offset)
	if err != nil {
		return endpoints.Page{}, fmt.Errorf("list endpoints: %w", err)
	}
	defer rows.Close()
	page := endpoints.Page{Items: []endpoints.Endpoint{}, Limit: limit, Offset: offset}
	for rows.Next() {
		var item endpoints.Endpoint
		if err := rows.Scan(&item.ID, &item.Name, &item.URL, &item.KeyID, &item.Version, &item.Enabled, &item.CreatedAt); err != nil {
			return endpoints.Page{}, fmt.Errorf("scan endpoint: %w", err)
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return endpoints.Page{}, fmt.Errorf("read endpoints: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := offset + limit
		page.NextOffset = &next
	}
	return page, nil
}

func (s EndpointStore) Update(ctx context.Context, id string, update endpoints.Update) (endpoints.Endpoint, error) {
	if update.ExpectedVersion < 1 || update.URL == nil && update.KeyID == nil && update.Enabled == nil {
		return endpoints.Endpoint{}, errors.New("expected_version and a change are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return endpoints.Endpoint{}, fmt.Errorf("begin endpoint update: %w", err)
	}
	defer tx.Rollback()
	var current endpoints.Endpoint
	err = tx.QueryRowContext(ctx, `SELECT id, name, url, secret_ref, version, enabled, created_at
		FROM endpoints WHERE id = $1 FOR UPDATE`, id).
		Scan(&current.ID, &current.Name, &current.URL, &current.KeyID, &current.Version, &current.Enabled, &current.CreatedAt)
	if err != nil {
		return endpoints.Endpoint{}, fmt.Errorf("read endpoint for update: %w", err)
	}
	if current.Version != update.ExpectedVersion {
		return endpoints.Endpoint{}, ErrEndpointVersionConflict
	}
	newURL, newKeyID, newEnabled := current.URL, current.KeyID, current.Enabled
	if update.URL != nil {
		newURL = *update.URL
		if _, isDemo := endpoints.DemoSecretRef(newURL); isDemo && update.KeyID == nil {
			newKeyID = ""
		}
	}
	if update.KeyID != nil {
		newKeyID = *update.KeyID
	}
	if update.Enabled != nil {
		newEnabled = *update.Enabled
	}
	if update.URL != nil || update.KeyID != nil || newEnabled && !current.Enabled {
		newKeyID, err = s.validateTarget(newURL, newKeyID)
		if err != nil {
			return endpoints.Endpoint{}, err
		}
	}
	version := current.Version
	if newURL != current.URL || newKeyID != current.KeyID || newEnabled != current.Enabled {
		version++
	}
	var result endpoints.Endpoint
	err = tx.QueryRowContext(ctx, `UPDATE endpoints SET url = $2, secret_ref = $3, enabled = $4, version = $5
		WHERE id = $1 RETURNING id, name, url, secret_ref, version, enabled, created_at`,
		id, newURL, newKeyID, newEnabled, version).
		Scan(&result.ID, &result.Name, &result.URL, &result.KeyID, &result.Version, &result.Enabled, &result.CreatedAt)
	if err != nil {
		return endpoints.Endpoint{}, fmt.Errorf("update endpoint: %w", err)
	}
	if !newEnabled && current.Enabled {
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries
			SET status = 'dead', next_attempt_at = NULL, terminal_reason = 'endpoint_disabled'
			WHERE endpoint_id = $1 AND status IN ('pending', 'retry_wait')`, id); err != nil {
			return endpoints.Endpoint{}, fmt.Errorf("stop waiting deliveries: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return endpoints.Endpoint{}, fmt.Errorf("commit endpoint update: %w", err)
	}
	return result, nil
}
