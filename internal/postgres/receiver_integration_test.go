package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"hookrelay/internal/delivery"
	"hookrelay/internal/events"
	"hookrelay/internal/receiver"
	"hookrelay/internal/signing"
	"hookrelay/internal/targetpolicy"
)

func signedReceiverRequest(t *testing.T, url string, secret []byte, eventID, deliveryID, attemptID string, body []byte) *http.Request {
	return signedReceiverRequestWithKey(t, url, secret, "demo/a-v1", eventID, deliveryID, attemptID, body)
}

func signedReceiverRequestWithKey(t *testing.T, url string, secret []byte, keyID, eventID, deliveryID, attemptID string, body []byte) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url+"/hook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	request.Header.Set("X-HookRelay-Key-Id", keyID)
	request.Header.Set("X-HookRelay-Event-Id", eventID)
	request.Header.Set("X-HookRelay-Event-Type", "order.created")
	request.Header.Set("X-HookRelay-Delivery-Id", deliveryID)
	request.Header.Set("X-HookRelay-Attempt-Id", attemptID)
	request.Header.Set("X-HookRelay-Timestamp", fmt.Sprint(timestamp))
	request.Header.Set("X-HookRelay-Signature", signing.Sign(secret, timestamp, keyID, eventID, "order.created", deliveryID, attemptID, body))
	return request
}

func TestReceiverAcceptsBothRotationKeysThenRetiresOldKey(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	oldKey := bytes.Repeat([]byte("o"), 32)
	newKey := bytes.Repeat([]byte("n"), 32)
	keys := map[string][]byte{"external-shop-v1": oldKey, "external-shop-v2": newKey}
	server := httptest.NewServer(&receiver.Handler{Store: ReceiverStore{DB: db}, Keys: keys})
	client := &http.Client{Timeout: 3 * time.Second}
	eventID, _ := events.NewID()
	oldDelivery, _ := events.NewID()
	newDelivery, _ := events.NewID()
	body := []byte(`{"order_id":"rotation"}`)
	for _, tc := range []struct {
		keyID, deliveryID string
		key               []byte
	}{
		{"external-shop-v1", oldDelivery, oldKey},
		{"external-shop-v2", newDelivery, newKey},
	} {
		attemptID, _ := events.NewID()
		request := signedReceiverRequestWithKey(t, server.URL, tc.key, tc.keyID, eventID, tc.deliveryID, attemptID, body)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("overlap key %s returned %d", tc.keyID, response.StatusCode)
		}
	}
	server.Close()
	server = httptest.NewServer(&receiver.Handler{Store: ReceiverStore{DB: db}, Keys: map[string][]byte{"external-shop-v2": newKey}})
	defer server.Close()
	oldRetryID, _ := events.NewID()
	oldRetry := signedReceiverRequestWithKey(t, server.URL, oldKey, "external-shop-v1", eventID, oldDelivery, oldRetryID, body)
	response, err := client.Do(oldRetry)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("retired key returned %d", response.StatusCode)
	}
	newRetryID, _ := events.NewID()
	newRetry := signedReceiverRequestWithKey(t, server.URL, newKey, "external-shop-v2", eventID, newDelivery, newRetryID, body)
	response, err = client.Do(newRetry)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("current key retry returned %d", response.StatusCode)
	}
	var effects int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects`).Scan(&effects); err != nil || effects != 2 {
		t.Fatalf("rotation effects=%d err=%v", effects, err)
	}
}

func TestDemoReceiverEffectIsDurableAcrossConcurrentRetriesAndRestart(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	secret := bytes.Repeat([]byte("s"), 32)
	eventID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"order_id":"dedup-1"}`)
	server := httptest.NewServer(&receiver.Handler{Store: ReceiverStore{DB: db}, Secret: secret, KeyID: "demo/a-v1"})
	client := &http.Client{Timeout: 3 * time.Second}
	const requests = 20
	errs := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		attemptID, err := events.NewID()
		if err != nil {
			t.Fatal(err)
		}
		request := signedReceiverRequest(t, server.URL, secret, eventID, deliveryID, attemptID, body)
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := client.Do(request)
			if err != nil {
				errs <- err
				return
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				errs <- fmt.Errorf("concurrent retry returned %d", response.StatusCode)
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	server.Close()
	var effects int
	var storedHash []byte
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload_sha256 FROM demo_receiver_effects WHERE delivery_id = $1`, deliveryID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(body)
	if effects != 1 || !bytes.Equal(storedHash, wantHash[:]) {
		t.Fatalf("durable side effect count=%d hash=%x", effects, storedHash)
	}
	reopened, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := httptest.NewServer(&receiver.Handler{Store: ReceiverStore{DB: reopened}, Secret: secret, KeyID: "demo/a-v1"})
	defer restarted.Close()
	newAttempt, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	request := signedReceiverRequest(t, restarted.URL, secret, eventID, deliveryID, newAttempt, body)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("retry after receiver restart returned %d", response.StatusCode)
	}
	conflict := signedReceiverRequest(t, restarted.URL, secret, eventID, deliveryID, newAttempt, []byte(`{"order_id":"changed"}`))
	response, err = client.Do(conflict)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting signed replay returned %d", response.StatusCode)
	}
	invalid := signedReceiverRequest(t, restarted.URL, secret, eventID, deliveryID, newAttempt, body)
	invalid.Header.Set("X-HookRelay-Signature", "sha256=invalid")
	response, err = client.Do(invalid)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid signature returned %d", response.StatusCode)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects`).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("duplicate created another effect: count=%d err=%v", effects, err)
	}
}

func senderRoutedToTestReceiver(server *httptest.Server, secret []byte) delivery.HTTPSender {
	sender := delivery.NewHTTPSender(map[string][]byte{"demo/a-v1": secret}, targetpolicy.ProfileDemo)
	sender.Client.Transport = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	return sender
}

func TestLostResponseRetriesSignedWebhookWithoutRepeatingReceiverEffect(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db, Jitter: func(time.Duration) time.Duration { return 0 }}
	event := createDemoEvent(t, ctx, store)
	secret := bytes.Repeat([]byte("s"), 32)
	firstReceiver := httptest.NewServer(&receiver.Handler{
		Store: ReceiverStore{DB: db}, Secret: secret, KeyID: "demo/a-v1", Mode: "drop-after-commit",
	})
	firstJob, err := store.Claim(ctx)
	if err != nil || firstJob == nil {
		t.Fatalf("first claim: job=%+v err=%v", firstJob, err)
	}
	firstResult := senderRoutedToTestReceiver(firstReceiver, secret).Send(ctx, *firstJob)
	firstReceiver.Close()
	if firstResult.Status != "failed" || firstResult.FailureClass != "transport_error" && firstResult.FailureClass != "response_read_error" {
		t.Fatalf("lost response classified incorrectly: %+v", firstResult)
	}
	if err := store.Complete(ctx, *firstJob, firstResult); err != nil {
		t.Fatal(err)
	}
	item, err := store.GetDelivery(ctx, event.Deliveries[0].ID)
	if err != nil || item.Status != "retry_wait" || item.AttemptCount != 1 {
		t.Fatalf("retry after lost response: item=%+v err=%v", item, err)
	}
	var effects int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects WHERE delivery_id = $1`, firstJob.ID).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("first receiver effect: count=%d err=%v", effects, err)
	}
	restartedReceiver := httptest.NewServer(&receiver.Handler{
		Store: ReceiverStore{DB: db}, Secret: secret, KeyID: "demo/a-v1", Mode: "drop-after-commit",
	})
	defer restartedReceiver.Close()
	secondJob, err := store.Claim(ctx)
	if err != nil || secondJob == nil || secondJob.ID != firstJob.ID || secondJob.AttemptID == firstJob.AttemptID {
		t.Fatalf("second claim: job=%+v err=%v", secondJob, err)
	}
	secondResult := senderRoutedToTestReceiver(restartedReceiver, secret).Send(ctx, *secondJob)
	if secondResult.Status != "succeeded" || secondResult.HTTPStatus == nil || *secondResult.HTTPStatus != http.StatusNoContent {
		t.Fatalf("duplicate delivery was not acknowledged: %+v", secondResult)
	}
	if err := store.Complete(ctx, *secondJob, secondResult); err != nil {
		t.Fatal(err)
	}
	item, err = store.GetDelivery(ctx, firstJob.ID)
	if err != nil || item.Status != "succeeded" || item.AttemptCount != 2 {
		t.Fatalf("final delivery state: item=%+v err=%v", item, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects WHERE delivery_id = $1`, firstJob.ID).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("retry repeated receiver side effect: count=%d err=%v", effects, err)
	}
}

func TestDemoReceiverRecordsNoEffectForFailedOrUnstoredRequests(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	secret := bytes.Repeat([]byte("s"), 32)
	eventID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"order_id":"failure-before-effect"}`)
	client := &http.Client{Timeout: 3 * time.Second}
	flaky := httptest.NewServer(&receiver.Handler{Store: ReceiverStore{DB: db}, Secret: secret, KeyID: "demo/a-v1", Mode: "flaky"})
	for i, want := range []int{500, 500, 204} {
		attemptID, err := events.NewID()
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(signedReceiverRequest(t, flaky.URL, secret, eventID, deliveryID, attemptID, body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("flaky attempt %d returned %d, want %d", i+1, response.StatusCode, want)
		}
		var effects int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects`).Scan(&effects); err != nil || effects != i/2 {
			t.Fatalf("flaky effect count after attempt %d: count=%d err=%v", i+1, effects, err)
		}
	}
	flaky.Close()
	brokenDB, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	brokenDB.Close()
	unavailable := httptest.NewServer(&receiver.Handler{Store: ReceiverStore{DB: brokenDB}, Secret: secret, KeyID: "demo/a-v1"})
	defer unavailable.Close()
	newDeliveryID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	attemptID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(signedReceiverRequest(t, unavailable.URL, secret, eventID, newDeliveryID, attemptID, body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable storage returned %d", response.StatusCode)
	}
	var effects int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM demo_receiver_effects`).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("unavailable storage created effect: count=%d err=%v", effects, err)
	}
}
