package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"hookrelay/internal/receiver"
)

type ReceiverStore struct{ DB *sql.DB }

func (s ReceiverStore) Record(ctx context.Context, effect receiver.Effect) (bool, error) {
	var insertedID string
	err := s.DB.QueryRowContext(ctx, `INSERT INTO demo_receiver_effects
		(delivery_id, key_id, event_id, event_type, payload_sha256)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (delivery_id) DO NOTHING RETURNING delivery_id`,
		effect.DeliveryID, effect.KeyID, effect.EventID, effect.EventType, effect.PayloadHash[:]).Scan(&insertedID)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("record demo receiver effect: %w", err)
	}
	var keyID, eventID, eventType string
	var payloadHash []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT key_id, event_id, event_type, payload_sha256
		FROM demo_receiver_effects WHERE delivery_id = $1`, effect.DeliveryID).
		Scan(&keyID, &eventID, &eventType, &payloadHash); err != nil {
		return false, fmt.Errorf("read prior demo receiver effect: %w", err)
	}
	if keyID != effect.KeyID || eventID != effect.EventID || eventType != effect.EventType || !bytes.Equal(payloadHash, effect.PayloadHash[:]) {
		return false, receiver.ErrConflictingReplay
	}
	return false, nil
}
