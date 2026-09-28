package signing

import (
	"strings"
	"testing"
	"time"
)

func TestSignatureBindsMetadataBodyAndTimestamp(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	now := time.Unix(1789992000, 0)
	body := []byte(`{"order_id":"demo-1"}`)
	signature := Sign(secret, now.Unix(), "demo/a-v1", "event-1", "order.created", "delivery-1", "attempt-1", body)
	const expected = "sha256=601b8f3efe5682e88daad8d112c34b1f68e7f11d728d71231021af03cea89a27"
	if signature != expected {
		t.Fatalf("signature = %s, want %s", signature, expected)
	}
	if !Verify(secret, now, "1789992000", "demo/a-v1", "event-1", "order.created", "delivery-1", "attempt-1", body, signature) {
		t.Fatal("valid signature was rejected")
	}
	for _, tc := range []struct {
		name, timestamp, keyID, eventID, eventType, delivery, attempt string
		body                                                          []byte
		now                                                           time.Time
	}{
		{"modified body", "1789992000", "demo/a-v1", "event-1", "order.created", "delivery-1", "attempt-1", []byte(`{"order_id":"demo-2"}`), now},
		{"modified key", "1789992000", "demo/b-v1", "event-1", "order.created", "delivery-1", "attempt-1", body, now},
		{"modified event id", "1789992000", "demo/a-v1", "event-2", "order.created", "delivery-1", "attempt-1", body, now},
		{"modified event type", "1789992000", "demo/a-v1", "event-1", "order.cancelled", "delivery-1", "attempt-1", body, now},
		{"modified delivery", "1789992000", "demo/a-v1", "event-1", "order.created", "delivery-2", "attempt-1", body, now},
		{"modified attempt", "1789992000", "demo/a-v1", "event-1", "order.created", "delivery-1", "attempt-2", body, now},
		{"expired", "1789992000", "demo/a-v1", "event-1", "order.created", "delivery-1", "attempt-1", body, now.Add(Tolerance + time.Second)},
		{"future timestamp", "1789992000", "demo/a-v1", "event-1", "order.created", "delivery-1", "attempt-1", body, now.Add(-Tolerance - time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(secret, tc.now, tc.timestamp, tc.keyID, tc.eventID, tc.eventType, tc.delivery, tc.attempt, tc.body, signature) {
				t.Fatal("invalid signature was accepted")
			}
		})
	}
}
