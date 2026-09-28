package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hookrelay/internal/events"
)

func testDatabase(t *testing.T) (context.Context, *sql.DB, string) {
	return testDatabaseWithMigrations(t, filepath.Join("..", "..", "migrations"))
}

func testDatabaseWithMigrations(t *testing.T, migrationDir string) (context.Context, *sql.DB, string) {
	t.Helper()
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := Open(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	id, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "hrtest_" + strings.ReplaceAll(id, "-", "")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	testURL := parsed.String()
	db, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(ctx, db, migrationDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, migrationDir); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	return ctx, db, testURL
}

func testDatabaseBeforeRetry(t *testing.T) (context.Context, *sql.DB, string) {
	return testDatabaseUpTo(t, "008")
}

func testDatabaseUpTo(t *testing.T, lastMigration string) (context.Context, *sql.DB, string) {
	t.Helper()
	sourceDir := filepath.Join("..", "..", "migrations")
	destinationDir := t.TempDir()
	files, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".sql") || file.Name()[:3] > lastMigration {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(sourceDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destinationDir, file.Name()), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return testDatabaseWithMigrations(t, destinationDir)
}

func TestEventAndDeliveriesCommitTogetherAndSurviveReconnect(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	store := EventStore{DB: db}
	input := events.Input{
		Type:        "order.created",
		Payload:     []byte(`{"order_id":"demo-1"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"},
	}
	created, _, err := store.Create(ctx, "test-producer", "first-event", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Deliveries) != 2 {
		t.Fatalf("created %d deliveries, want 2", len(created.Deliveries))
	}
	reopened, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	found, err := (EventStore{DB: reopened}).Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Deliveries) != 2 || string(found.Payload) != string(input.Payload) {
		t.Fatalf("reopened event differs: %+v", found)
	}
	for _, delivery := range found.Deliveries {
		if delivery.Status != "pending" {
			t.Fatalf("unexpected status %q", delivery.Status)
		}
	}
}

func TestInvalidSecondEndpointAndDeliveryFailureRollBackEvent(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	input := events.Input{
		Type:        "order.created",
		Payload:     []byte(`{"order_id":"demo-2"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"},
	}
	_, _, err := store.Create(ctx, "test-producer", "invalid-target", input)
	if !errors.Is(err, ErrUnknownEndpoint) {
		t.Fatalf("got %v, want invalid endpoint", err)
	}
	assertCounts(t, ctx, db, 0, 0)

	// Fail the second INSERT after the event and first delivery have been written.
	functionSQL := `CREATE FUNCTION reject_demo_b() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.endpoint_id = '22222222-2222-4222-8222-222222222222' THEN RAISE EXCEPTION 'injected delivery failure'; END IF;
		RETURN NEW; END; $$`
	if _, err := db.ExecContext(ctx, functionSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_demo_b BEFORE INSERT ON deliveries FOR EACH ROW EXECUTE FUNCTION reject_demo_b()`); err != nil {
		t.Fatal(err)
	}
	input.EndpointIDs[1] = "22222222-2222-4222-8222-222222222222"
	if _, _, err := store.Create(ctx, "test-producer", "injected-delivery-failure", input); err == nil {
		t.Fatal("injected delivery failure was not returned")
	}
	assertCounts(t, ctx, db, 0, 0)
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_demo_b ON deliveries`); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := store.Create(ctx, "test-producer", "injected-delivery-failure", input); err != nil || replayed {
		t.Fatalf("retry after rolled-back transaction: replayed=%t err=%v", replayed, err)
	}
	assertCounts(t, ctx, db, 1, 2)
}

func TestIdempotencySerializesConcurrentAcceptsAndRejectsDifferentContent(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	input := events.Input{
		Type:        "order.created",
		Payload:     []byte(`{"order_id":"same"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"},
	}
	const requests = 20
	start := make(chan struct{})
	results := make(chan struct {
		id       string
		replayed bool
		err      error
	}, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			event, replayed, err := store.Create(requestCtx, "producer-a", "same-key", input)
			results <- struct {
				id       string
				replayed bool
				err      error
			}{event.ID, replayed, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var id string
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id == "" {
			id = result.id
		} else if result.id != id {
			t.Fatalf("multiple event IDs: %s and %s", id, result.id)
		}
		if !result.replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("new events = %d, want 1", created)
	}
	assertCounts(t, ctx, db, 1, 2)

	// A replay uses the stored event even when an endpoint is later disabled.
	if _, err := db.ExecContext(ctx, `UPDATE endpoints SET enabled = FALSE WHERE id = $1`, input.EndpointIDs[0]); err != nil {
		t.Fatal(err)
	}
	reordered := input
	reordered.EndpointIDs = []string{input.EndpointIDs[1], input.EndpointIDs[0]}
	replayedEvent, replayed, err := store.Create(ctx, "producer-a", "same-key", reordered)
	if err != nil || !replayed || replayedEvent.ID != id {
		t.Fatalf("replay after endpoint disable: id=%s replayed=%t err=%v", replayedEvent.ID, replayed, err)
	}

	for name, changed := range map[string]events.Input{
		"payload": {Type: input.Type, Payload: []byte(`{"order_id":"different"}`), EndpointIDs: input.EndpointIDs},
		"bytes":   {Type: input.Type, Payload: []byte(`{ "order_id":"same"}`), EndpointIDs: input.EndpointIDs},
		"type":    {Type: "order.cancelled", Payload: input.Payload, EndpointIDs: input.EndpointIDs},
		"targets": {Type: input.Type, Payload: input.Payload, EndpointIDs: input.EndpointIDs[:1]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.Create(ctx, "producer-a", "same-key", changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("got %v, want idempotency conflict", err)
			}
		})
	}
	assertCounts(t, ctx, db, 1, 2)

	// A distinct producer may use the same key independently.
	if _, err := db.ExecContext(ctx, `UPDATE endpoints SET enabled = TRUE WHERE id = $1`, input.EndpointIDs[0]); err != nil {
		t.Fatal(err)
	}
	otherEvent, otherReplay, err := store.Create(ctx, "producer-b", "same-key", input)
	if err != nil || otherReplay || otherEvent.ID == id {
		t.Fatalf("other producer: id=%s replayed=%t err=%v", otherEvent.ID, otherReplay, err)
	}
	assertCounts(t, ctx, db, 2, 4)
}

func assertCounts(t *testing.T, ctx context.Context, db *sql.DB, wantEvents, wantDeliveries int) {
	t.Helper()
	for _, item := range []struct {
		table string
		want  int
	}{{"events", wantEvents}, {"deliveries", wantDeliveries}} {
		var got int
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", item.table)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != item.want {
			t.Fatalf("%s count = %d, want %d", item.table, got, item.want)
		}
	}
}
