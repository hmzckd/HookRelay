package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hookrelay/internal/delivery"
	"hookrelay/internal/events"
)

func createDemoEvent(t *testing.T, ctx context.Context, store EventStore) events.Event {
	t.Helper()
	event, _, err := store.Create(ctx, "test-producer", "delivery-test", events.Input{
		Type: "order.created", Payload: []byte(`{"order_id":"demo-1"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestClaimAndCompletePersistAttemptAndDeliveryTogether(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	event := createDemoEvent(t, ctx, store)
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%+v error=%v", job, err)
	}
	if job.ID != event.Deliveries[0].ID || job.SecretRef != "demo/a-v1" || string(job.Payload) != string(event.Payload) {
		t.Fatalf("wrong job: %+v", job)
	}
	if job.ClaimToken == "" {
		t.Fatal("claim token was not assigned")
	}
	var storedToken string
	var leaseUntil time.Time
	if err := db.QueryRowContext(ctx, `SELECT claim_token, lease_until FROM deliveries WHERE id = $1`, job.ID).
		Scan(&storedToken, &leaseUntil); err != nil || storedToken != job.ClaimToken || !leaseUntil.After(time.Now()) {
		t.Fatalf("stored ownership: token=%q lease=%v err=%v", storedToken, leaseUntil, err)
	}
	processing, err := store.GetDelivery(ctx, job.ID)
	if err != nil || processing.Status != "processing" {
		t.Fatalf("processing state: %+v, %v", processing, err)
	}
	status := 204
	wrongOwner := *job
	wrongOwner.ClaimToken = "00000000-0000-4000-8000-000000000000"
	if err := store.Complete(ctx, wrongOwner, delivery.Result{Status: "succeeded", HTTPStatus: &status}); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("unowned completion: %v", err)
	}
	if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
		t.Fatal(err)
	}
	finished, err := store.GetDelivery(ctx, job.ID)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("finished state: %+v, %v", finished, err)
	}
	var clearedToken sql.NullString
	var clearedLease sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT claim_token, lease_until FROM deliveries WHERE id = $1`, job.ID).
		Scan(&clearedToken, &clearedLease); err != nil || clearedToken.Valid || clearedLease.Valid {
		t.Fatalf("ownership not cleared: token=%+v lease=%+v err=%v", clearedToken, clearedLease, err)
	}
	page, err := store.ListAttempts(ctx, job.ID, 1, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].HTTPStatus == nil || *page.Items[0].HTTPStatus != 204 || page.Items[0].FinishedAt == nil {
		t.Fatalf("attempt page: %+v, %v", page, err)
	}
	if another, err := store.Claim(ctx); err != nil || another != nil {
		t.Fatalf("completed job claimed again: %+v, %v", another, err)
	}
}

func TestTwoWorkersClaimOneHundredJobsWithoutOverlap(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store := EventStore{DB: db}
	const total = 100
	for i := range total {
		_, _, err := store.Create(ctx, "test-producer", fmt.Sprintf("parallel-%03d", i), events.Input{
			Type: "order.created", Payload: []byte(`{"batch":true}`),
			EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	otherDB, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	jobs := make(chan delivery.Job, 2*total)
	errs := make(chan error, 2)
	firstClaims := make(chan *delivery.Job, 2)
	start := make(chan struct{})
	resume := make(chan struct{})
	var workers sync.WaitGroup
	for _, workerStore := range []EventStore{store, {DB: otherDB}} {
		workers.Add(1)
		go func(s EventStore) {
			defer workers.Done()
			<-start
			job, err := s.Claim(ctx)
			if err != nil {
				errs <- err
			}
			firstClaims <- job
			<-resume
			if err != nil || job == nil {
				return
			}
			jobs <- *job
			for {
				job, err := s.Claim(ctx)
				if err != nil {
					errs <- err
					return
				}
				if job == nil {
					return
				}
				jobs <- *job
			}
		}(workerStore)
	}
	close(start)
	first, second := <-firstClaims, <-firstClaims
	close(resume)
	workers.Wait()
	close(jobs)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if first == nil || second == nil || first.ID == second.ID {
		t.Fatalf("workers did not claim distinct first jobs: %+v %+v", first, second)
	}
	seen := make(map[string]bool, total)
	for job := range jobs {
		if seen[job.ID] || job.ClaimToken == "" || job.AttemptID == "" {
			t.Fatalf("duplicate or incomplete claim: %+v", job)
		}
		seen[job.ID] = true
	}
	if len(seen) != total {
		t.Fatalf("claimed %d jobs, want %d", len(seen), total)
	}
	var processing, attempts, tokens, leases int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(claim_token), count(lease_until)
		FROM deliveries WHERE status = 'processing'`).Scan(&processing, &tokens, &leases); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_attempts WHERE status = 'processing'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if processing != total || attempts != total || tokens != total || leases != total {
		t.Fatalf("claim rows: processing=%d attempts=%d tokens=%d leases=%d", processing, attempts, tokens, leases)
	}
}

func TestClaimSkipsLockedFirstJob(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	store := EventStore{DB: db}
	first := createDemoEvent(t, ctx, store)
	second, _, err := store.Create(ctx, "test-producer", "second-job", events.Input{
		Type: "order.created", Payload: []byte(`{"order_id":"demo-2"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lockDB, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	tx, err := lockDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var lockedID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM deliveries WHERE id = $1 FOR UPDATE`, first.Deliveries[0].ID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	claimCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	job, err := store.Claim(claimCtx)
	if err != nil || job == nil || job.ID != second.Deliveries[0].ID {
		t.Fatalf("locked first job blocked second: job=%+v err=%v", job, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	job, err = store.Claim(ctx)
	if err != nil || job == nil || job.ID != first.Deliveries[0].ID {
		t.Fatalf("released first job was not claimed: job=%+v err=%v", job, err)
	}
}

func TestClaimSkipsLockedExpiredJob(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	store := EventStore{DB: db}
	first := createDemoEvent(t, ctx, store)
	second, _, err := store.Create(ctx, "test-producer", "ready-after-expired", events.Input{
		Type: "order.created", Payload: []byte(`{"order_id":"ready"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET created_at = clock_timestamp() - interval '16 minutes' WHERE id = $1`, first.Deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	lockDB, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	tx, err := lockDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var lockedID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM deliveries WHERE id = $1 FOR UPDATE`, first.Deliveries[0].ID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	claimCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	job, err := store.Claim(claimCtx)
	if err != nil || job == nil || job.ID != second.Deliveries[0].ID {
		t.Fatalf("locked expired job blocked ready job: job=%+v err=%v", job, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if job, err := store.Claim(ctx); err != nil || job != nil {
		t.Fatalf("expired job was claimed: job=%+v err=%v", job, err)
	}
	item, err := store.GetDelivery(ctx, first.Deliveries[0].ID)
	if err != nil || item.Status != "dead" || item.TerminalReason != "age_exhausted" {
		t.Fatalf("expired job state: item=%+v err=%v", item, err)
	}
}

func TestRecoveryMigrationReclaimsLegacyProcessingJob(t *testing.T) {
	ctx, db, _ := testDatabaseUpTo(t, "011")
	store := EventStore{DB: db}
	// This row predates the delivery secret snapshot; current Create requires
	// the new column and cannot write into an old schema during upgrade tests.
	eventID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (id, event_type, payload) VALUES ($1, 'order.created', $2)`, eventID, []byte(`{"order_id":"legacy"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO deliveries (id, event_id, endpoint_id, endpoint_url, endpoint_version)
		VALUES ($1, $2, '11111111-1111-4111-8111-111111111111', 'http://127.0.0.1:18080/hook', 1)`, deliveryID, eventID); err != nil {
		t.Fatal(err)
	}
	attemptID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET status = 'processing', attempt_count = 1 WHERE id = $1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO delivery_attempts (id, delivery_id, status) VALUES ($1, $2, 'processing')`,
		attemptID, deliveryID); err != nil {
		t.Fatal(err)
	}
	fullDir := filepath.Join("..", "..", "migrations")
	if err := Migrate(ctx, db, fullDir); err != nil {
		t.Fatalf("ownership migration over processing row: %v", err)
	}
	if err := Migrate(ctx, db, fullDir); err != nil {
		t.Fatalf("repeat ownership migration: %v", err)
	}
	var token sql.NullString
	var lease sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT claim_token, lease_until FROM deliveries WHERE id = $1`, deliveryID).
		Scan(&token, &lease); err != nil || token.Valid || lease.Valid {
		t.Fatalf("legacy ownership changed: token=%+v lease=%+v err=%v", token, lease, err)
	}
	job, err := store.Claim(ctx)
	if err != nil || job == nil || job.ID != deliveryID || job.ClaimToken == "" {
		t.Fatalf("legacy processing job was not recovered: job=%+v err=%v", job, err)
	}
	page, err := store.ListAttempts(ctx, job.ID, 10, 0)
	if err != nil || len(page.Items) != 2 || page.Items[0].Status != "processing" || page.Items[1].Status != "unknown" {
		t.Fatalf("legacy attempt history: page=%+v err=%v", page, err)
	}
}

func TestExpiredLeaseRecoveryRejectsOldOwnerAndPreservesUnknownAttempt(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	store := EventStore{DB: db}
	event := createDemoEvent(t, ctx, store)
	first, err := store.Claim(ctx)
	if err != nil || first == nil {
		t.Fatalf("first claim: job=%+v err=%v", first, err)
	}
	if another, err := store.Claim(ctx); err != nil || another != nil {
		t.Fatalf("active lease was reclaimed: job=%+v err=%v", another, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	status := 204
	if err := store.Complete(ctx, *first, delivery.Result{Status: "succeeded", HTTPStatus: &status}); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("expired owner completed before recovery: %v", err)
	}
	reopened, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	otherStore := EventStore{DB: reopened}
	second, err := otherStore.Claim(ctx)
	if err != nil || second == nil || second.ID != event.Deliveries[0].ID || second.AttemptID == first.AttemptID || second.ClaimToken == first.ClaimToken {
		t.Fatalf("recovery claim: job=%+v err=%v", second, err)
	}
	if err := store.Complete(ctx, *first, delivery.Result{Status: "succeeded", HTTPStatus: &status}); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("old owner overwrote new claim: %v", err)
	}
	page, err := store.ListAttempts(ctx, first.ID, 10, 0)
	if err != nil || len(page.Items) != 2 || page.Items[0].Status != "processing" || page.Items[1].Status != "unknown" ||
		page.Items[1].FailureClass != "ownership_lost" || page.Items[1].FinishedAt == nil || page.Items[1].HTTPStatus != nil {
		t.Fatalf("recovered attempt history: page=%+v err=%v", page, err)
	}
	if err := otherStore.Complete(ctx, *second, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
		t.Fatal(err)
	}
	item, err := store.GetDelivery(ctx, first.ID)
	if err != nil || item.Status != "succeeded" || item.AttemptCount != 2 {
		t.Fatalf("recovered final state: item=%+v err=%v", item, err)
	}
}

func TestExpiredLeaseClosesExhaustedAttemptsWithoutSendingAgain(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		prepare      func(context.Context, *sql.DB, string) error
	}{
		{"attempts", "max_attempts", func(ctx context.Context, db *sql.DB, id string) error {
			_, err := db.ExecContext(ctx, `UPDATE deliveries SET attempt_count = $2 WHERE id = $1`, id, delivery.MaxAttempts)
			return err
		}},
		{"age", "age_exhausted", func(ctx context.Context, db *sql.DB, id string) error {
			_, err := db.ExecContext(ctx, `UPDATE deliveries SET created_at = clock_timestamp() - interval '16 minutes' WHERE id = $1`, id)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db, _ := testDatabase(t)
			store := EventStore{DB: db}
			createDemoEvent(t, ctx, store)
			first, err := store.Claim(ctx)
			if err != nil || first == nil {
				t.Fatalf("first claim: job=%+v err=%v", first, err)
			}
			if err := tc.prepare(ctx, db, first.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE deliveries SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, first.ID); err != nil {
				t.Fatal(err)
			}
			if next, err := store.Claim(ctx); err != nil || next != nil {
				t.Fatalf("exhausted job was claimed: job=%+v err=%v", next, err)
			}
			item, err := store.GetDelivery(ctx, first.ID)
			if err != nil || item.Status != "dead" || item.TerminalReason != tc.reason {
				t.Fatalf("terminal state: item=%+v err=%v", item, err)
			}
			page, err := store.ListAttempts(ctx, first.ID, 10, 0)
			if err != nil || len(page.Items) != 1 || page.Items[0].Status != "unknown" {
				t.Fatalf("terminal history: page=%+v err=%v", page, err)
			}
		})
	}
}

func TestRecoveryClaimRollsBackWhenNewAttemptCannotBeInserted(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	createDemoEvent(t, ctx, store)
	first, err := store.Claim(ctx)
	if err != nil || first == nil {
		t.Fatalf("first claim: job=%+v err=%v", first, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION reject_recovery_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected attempt insert failure'; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_recovery_attempt BEFORE INSERT ON delivery_attempts
		FOR EACH ROW EXECUTE FUNCTION reject_recovery_attempt()`); err != nil {
		t.Fatal(err)
	}
	if job, err := store.Claim(ctx); err == nil || job != nil {
		t.Fatalf("injected failure did not abort recovery: job=%+v err=%v", job, err)
	}
	var token, attemptStatus string
	var count int
	if err := db.QueryRowContext(ctx, `SELECT claim_token, attempt_count FROM deliveries WHERE id = $1`, first.ID).
		Scan(&token, &count); err != nil || token != first.ClaimToken || count != 1 {
		t.Fatalf("recovery was partly committed: token=%q count=%d err=%v", token, count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM delivery_attempts WHERE id = $1`, first.AttemptID).
		Scan(&attemptStatus); err != nil || attemptStatus != "processing" {
		t.Fatalf("old attempt was partly closed: status=%q err=%v", attemptStatus, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_recovery_attempt ON delivery_attempts`); err != nil {
		t.Fatal(err)
	}
	if next, err := store.Claim(ctx); err != nil || next == nil || next.ID != first.ID {
		t.Fatalf("recovery after repair: job=%+v err=%v", next, err)
	}
}

func TestWorkerProcessCrashPhases(t *testing.T) {
	if phase := os.Getenv("HOOKRELAY_CRASH_TEST_CHILD"); phase != "" {
		ctx := context.Background()
		db, err := Open(ctx, os.Getenv("HOOKRELAY_CRASH_TEST_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if phase != "before_claim" {
			job, err := (EventStore{DB: db}).Claim(ctx)
			if err != nil || job == nil {
				t.Fatalf("child claim: job=%+v err=%v", job, err)
			}
			if phase == "after_receiver" {
				response, err := http.Post(os.Getenv("HOOKRELAY_CRASH_TEST_RECEIVER"), "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					t.Fatalf("receiver returned %d", response.StatusCode)
				}
			}
		}
		if err := os.WriteFile(os.Getenv("HOOKRELAY_CRASH_TEST_MARKER"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		return
	}
	for _, phase := range []string{"before_claim", "after_claim", "after_receiver"} {
		t.Run(phase, func(t *testing.T) {
			ctx, db, testURL := testDatabase(t)
			store := EventStore{DB: db}
			event := createDemoEvent(t, ctx, store)
			var received atomic.Int32
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer receiver.Close()
			marker := filepath.Join(t.TempDir(), "child-ready")
			child := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessCrashPhases$")
			child.Env = append(os.Environ(),
				"HOOKRELAY_CRASH_TEST_CHILD="+phase,
				"HOOKRELAY_CRASH_TEST_URL="+testURL,
				"HOOKRELAY_CRASH_TEST_RECEIVER="+receiver.URL,
				"HOOKRELAY_CRASH_TEST_MARKER="+marker)
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					child.Process.Kill()
					child.Wait()
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child process did not reach crash point")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			child.Wait()
			if phase != "before_claim" {
				if _, err := db.ExecContext(ctx, `UPDATE deliveries SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, event.Deliveries[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			job, err := store.Claim(ctx)
			if err != nil || job == nil || job.ID != event.Deliveries[0].ID {
				t.Fatalf("claim after child crash: job=%+v err=%v", job, err)
			}
			wantAttempts := 1
			wantReceives := int32(0)
			if phase != "before_claim" {
				wantAttempts = 2
			}
			if phase == "after_receiver" {
				wantReceives = 1
			}
			page, err := store.ListAttempts(ctx, job.ID, 10, 0)
			if err != nil || len(page.Items) != wantAttempts || phase != "before_claim" && page.Items[1].Status != "unknown" || received.Load() != wantReceives {
				t.Fatalf("crash recovery history: page=%+v received=%d err=%v", page, received.Load(), err)
			}
			if phase == "after_receiver" {
				response, err := http.Post(receiver.URL, "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent || received.Load() != 2 {
					t.Fatalf("receiver did not observe duplicate send: status=%d received=%d", response.StatusCode, received.Load())
				}
			}
			status := 204
			if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type gatedDatabaseSender struct {
	started chan string
	release <-chan struct{}
}

func (s gatedDatabaseSender) Send(ctx context.Context, job delivery.Job) delivery.Result {
	s.started <- job.ID
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return delivery.Result{Status: "succeeded"}
}

type immediateDatabaseSender struct{}

func (immediateDatabaseSender) Send(context.Context, delivery.Job) delivery.Result {
	return delivery.Result{Status: "succeeded"}
}

func TestWorkerPoolClaimsOnlyCapacityAndDrainsBeforeRestart(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	const total = 12
	for i := range total {
		_, _, err := store.Create(ctx, "test-producer", fmt.Sprintf("pool-%03d", i), events.Input{
			Type: "order.created", Payload: []byte(`{"pool":true}`),
			EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	started := make(chan string, total)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	firstRun := make(chan error, 1)
	service := delivery.Service{
		Store: store, Sender: gatedDatabaseSender{started: started, release: release},
		Concurrency: 3, PollInterval: 10 * time.Millisecond, ShutdownGrace: 2 * time.Second,
	}
	go func() { firstRun <- service.Run(runCtx) }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("pool did not fill its initial capacity")
		}
	}
	var processing, pending int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status = 'processing'),
		count(*) FILTER (WHERE status = 'pending') FROM deliveries`).Scan(&processing, &pending); err != nil {
		t.Fatal(err)
	}
	if processing != 3 || pending != total-3 {
		t.Fatalf("pool overclaimed: processing=%d pending=%d", processing, pending)
	}
	stop()
	close(release)
	select {
	case err := <-firstRun:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pool did not drain on shutdown")
	}
	var succeeded int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE status = 'succeeded'`).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if succeeded != 3 {
		t.Fatalf("graceful shutdown completed %d jobs, want 3", succeeded)
	}
	restartCtx, stopRestart := context.WithCancel(ctx)
	defer stopRestart()
	secondRun := make(chan error, 1)
	service.Sender = immediateDatabaseSender{}
	go func() { secondRun <- service.Run(restartCtx) }()
	deadline := time.Now().Add(4 * time.Second)
	for {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE status = 'succeeded'`).Scan(&succeeded); err != nil {
			t.Fatal(err)
		}
		if succeeded == total {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart completed %d of %d jobs", succeeded, total)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopRestart()
	select {
	case err := <-secondRun:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restarted pool did not stop")
	}
	var attempts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != total {
		t.Fatalf("attempt count = %d, want %d", attempts, total)
	}
}

func TestClaimWithoutCompletionRemainsVisibleAndIsNotRetried(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	createDemoEvent(t, ctx, store)
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v, %v", job, err)
	}
	if another, err := store.Claim(ctx); err != nil || another != nil {
		t.Fatalf("processing job claimed twice: %+v, %v", another, err)
	}
	item, err := store.GetDelivery(ctx, job.ID)
	if err != nil || item.Status != "processing" {
		t.Fatalf("uncompleted delivery: %+v, %v", item, err)
	}
	page, err := store.ListAttempts(ctx, job.ID, 20, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "processing" || page.Items[0].FinishedAt != nil {
		t.Fatalf("uncompleted attempt: %+v, %v", page, err)
	}
}

type resultSequence struct {
	results []delivery.Result
	calls   int
}

func (s *resultSequence) Send(context.Context, delivery.Job) delivery.Result {
	result := s.results[s.calls]
	s.calls++
	return result
}

func TestWorkerRetriesTwoServerErrorsThenSucceeds(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db, Jitter: func(time.Duration) time.Duration { return 0 }}
	event := createDemoEvent(t, ctx, store)
	serverError, success := 500, 204
	sender := &resultSequence{results: []delivery.Result{
		{Status: "failed", HTTPStatus: &serverError, FailureClass: "http_500"},
		{Status: "failed", HTTPStatus: &serverError, FailureClass: "http_500"},
		{Status: "succeeded", HTTPStatus: &success},
	}}
	service := delivery.Service{Store: store, Sender: sender}
	for i, want := range []string{"retry_wait", "retry_wait", "succeeded"} {
		found, err := service.ProcessOne(ctx)
		if err != nil || !found {
			t.Fatalf("process %d: found=%t err=%v", i+1, found, err)
		}
		item, err := store.GetDelivery(ctx, event.Deliveries[0].ID)
		if err != nil || item.Status != want || item.AttemptCount != i+1 {
			t.Fatalf("after process %d: item=%+v err=%v", i+1, item, err)
		}
		if (want == "retry_wait") != (item.NextAttemptAt != nil) {
			t.Fatalf("unexpected next attempt after process %d: %+v", i+1, item)
		}
	}
	page, err := store.ListAttempts(ctx, event.Deliveries[0].ID, 10, 0)
	if err != nil || len(page.Items) != 3 || sender.calls != 3 {
		t.Fatalf("attempt history: page=%+v calls=%d err=%v", page, sender.calls, err)
	}
	failed, succeeded := 0, 0
	for _, attempt := range page.Items {
		switch attempt.Status {
		case "failed":
			failed++
		case "succeeded":
			succeeded++
		}
	}
	if failed != 2 || succeeded != 1 {
		t.Fatalf("attempt statuses: failed=%d succeeded=%d", failed, succeeded)
	}
	if another, err := store.Claim(ctx); err != nil || another != nil {
		t.Fatalf("completed job claimed again: job=%+v err=%v", another, err)
	}
}

func TestRetryScheduleSurvivesReconnectAndPermanentFailureStops(t *testing.T) {
	ctx, db, testURL := testDatabase(t)
	store := EventStore{DB: db, Jitter: func(time.Duration) time.Duration { return 0 }}
	event := createDemoEvent(t, ctx, store)
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("first claim: job=%+v err=%v", job, err)
	}
	rateLimited := 429
	if err := store.Complete(ctx, *job, delivery.Result{Status: "failed", HTTPStatus: &rateLimited, FailureClass: "http_429", RetryAfter: "30"}); err != nil {
		t.Fatal(err)
	}
	item, err := store.GetDelivery(ctx, event.Deliveries[0].ID)
	if err != nil || item.Status != "retry_wait" || item.NextAttemptAt == nil || item.AttemptCount != 1 {
		t.Fatalf("scheduled state: item=%+v err=%v", item, err)
	}
	reopened, err := Open(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedStore := EventStore{DB: reopened, Jitter: store.Jitter}
	if early, err := restartedStore.Claim(ctx); err != nil || early != nil {
		t.Fatalf("future job was claimed after reconnect: job=%+v err=%v", early, err)
	}
	// Advance the persisted due time without waiting 30 real seconds.
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET next_attempt_at = clock_timestamp() - interval '1 second' WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	second, err := restartedStore.Claim(ctx)
	if err != nil || second == nil || second.ID != job.ID || second.AttemptID == job.AttemptID {
		t.Fatalf("due retry claim: job=%+v err=%v", second, err)
	}
	badRequest := 400
	if err := restartedStore.Complete(ctx, *second, delivery.Result{Status: "failed", HTTPStatus: &badRequest, FailureClass: "http_400"}); err != nil {
		t.Fatal(err)
	}
	item, err = restartedStore.GetDelivery(ctx, job.ID)
	if err != nil || item.Status != "dead" || item.TerminalReason != "permanent_failure" || item.NextAttemptAt != nil || item.AttemptCount != 2 {
		t.Fatalf("terminal state: item=%+v err=%v", item, err)
	}
	if next, err := restartedStore.Claim(ctx); err != nil || next != nil {
		t.Fatalf("terminal job claimed: job=%+v err=%v", next, err)
	}
}

func TestRetryBudgetAndExpiredPendingJob(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db, Jitter: func(time.Duration) time.Duration { return 0 }}
	event := createDemoEvent(t, ctx, store)
	status := 500
	for i := 1; i <= delivery.MaxAttempts; i++ {
		job, err := store.Claim(ctx)
		if err != nil || job == nil {
			t.Fatalf("claim %d: job=%+v err=%v", i, job, err)
		}
		if err := store.Complete(ctx, *job, delivery.Result{Status: "failed", HTTPStatus: &status, FailureClass: "http_500"}); err != nil {
			t.Fatal(err)
		}
	}
	item, err := store.GetDelivery(ctx, event.Deliveries[0].ID)
	if err != nil || item.Status != "dead" || item.TerminalReason != "max_attempts" || item.AttemptCount != delivery.MaxAttempts {
		t.Fatalf("budget result: item=%+v err=%v", item, err)
	}
	if more, err := store.Claim(ctx); err != nil || more != nil {
		t.Fatalf("exhausted job claimed: job=%+v err=%v", more, err)
	}

	second, _, err := store.Create(ctx, "test-producer", "expired-pending", events.Input{
		Type: "order.created", Payload: []byte(`{"order_id":"expired"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET created_at = clock_timestamp() - interval '16 minutes' WHERE id = $1`, second.Deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	if expired, err := store.Claim(ctx); err != nil || expired != nil {
		t.Fatalf("expired pending job claimed: job=%+v err=%v", expired, err)
	}
	item, err = store.GetDelivery(ctx, second.Deliveries[0].ID)
	if err != nil || item.Status != "dead" || item.TerminalReason != "age_exhausted" || item.AttemptCount != 0 {
		t.Fatalf("expired pending state: item=%+v err=%v", item, err)
	}
}

func TestUpgradeBackfillsExistingAttemptWithoutRetryingLegacyFailure(t *testing.T) {
	ctx, db, _ := testDatabaseBeforeRetry(t)
	store := EventStore{DB: db}
	eventID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (id, event_type, payload) VALUES ($1, 'order.created', $2)`, eventID, []byte(`{"order_id":"legacy"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO deliveries (id, event_id, endpoint_id, endpoint_url, endpoint_version)
		VALUES ($1, $2, '11111111-1111-4111-8111-111111111111', 'http://127.0.0.1:18080/hook', 1)`, deliveryID, eventID); err != nil {
		t.Fatal(err)
	}
	attemptID, err := events.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO delivery_attempts
		(id, delivery_id, status, finished_at, http_status, failure_class)
		VALUES ($1, $2, 'failed', now(), 500, 'http_500')`, attemptID, deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET status = 'failed' WHERE id = $1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	fullDir := filepath.Join("..", "..", "migrations")
	if err := Migrate(ctx, db, fullDir); err != nil {
		t.Fatalf("upgrade from pre-retry schema: %v", err)
	}
	if err := Migrate(ctx, db, fullDir); err != nil {
		t.Fatalf("repeat upgrade: %v", err)
	}
	item, err := store.GetDelivery(ctx, deliveryID)
	if err != nil || item.Status != "failed" || item.AttemptCount != 1 || item.NextAttemptAt != nil {
		t.Fatalf("upgraded legacy failure: item=%+v err=%v", item, err)
	}
	if job, err := store.Claim(ctx); err != nil || job != nil {
		t.Fatalf("legacy failed job retried: job=%+v err=%v", job, err)
	}
}
