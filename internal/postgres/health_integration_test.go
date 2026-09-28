package postgres

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"hookrelay/internal/delivery"
	"hookrelay/internal/events"
)

type testDBProxy struct {
	listener net.Listener
	upstream string
	mu       sync.Mutex
	down     bool
	peers    map[net.Conn]net.Conn
}

func TestReadinessRequiresCurrentMigration(t *testing.T) {
	ctx, db, _ := testDatabaseUpTo(t, "017")
	if err := (EventStore{DB: db}).Ready(ctx); err == nil {
		t.Fatal("old database schema was reported ready")
	}
}

func startTestDBProxy(t *testing.T, upstream string) *testDBProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &testDBProxy{listener: listener, upstream: upstream, peers: make(map[net.Conn]net.Conn)}
	t.Cleanup(func() {
		p.setDown(true)
		listener.Close()
	})
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go p.forward(client)
		}
	}()
	return p
}

func (p *testDBProxy) forward(client net.Conn) {
	p.mu.Lock()
	if p.down {
		p.mu.Unlock()
		client.Close()
		return
	}
	p.mu.Unlock()
	upstream, err := net.DialTimeout("tcp", p.upstream, time.Second)
	if err != nil {
		client.Close()
		return
	}
	p.mu.Lock()
	if p.down {
		p.mu.Unlock()
		client.Close()
		upstream.Close()
		return
	}
	p.peers[client] = upstream
	p.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	client.Close()
	upstream.Close()
	p.mu.Lock()
	delete(p.peers, client)
	p.mu.Unlock()
}

func (p *testDBProxy) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
	if down {
		for client, upstream := range p.peers {
			client.Close()
			upstream.Close()
		}
	}
}

func TestDatabaseOutageRejectsAcceptanceAndWorkerRecoversWithoutRestart(t *testing.T) {
	ctx, baseDB, testURL := testDatabase(t)
	parsed, err := url.Parse(testURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := startTestDBProxy(t, parsed.Host)
	parsed.Host = proxy.listener.Addr().String()
	proxy.setDown(true)
	appDB, err := NewPool(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { appDB.Close() })
	appStore := EventStore{DB: appDB}
	baseStore := EventStore{DB: baseDB}
	queued, _, err := baseStore.Create(ctx, "test-producer", "outage-queued", events.Input{
		Type: "order.created", Payload: []byte(`{"outage":true}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := appStore.Ready(ctx); err == nil {
		t.Fatal("API readiness stayed healthy during database outage")
	}
	if _, _, err := appStore.Create(ctx, "test-producer", "outage-rejected", events.Input{
		Type: "order.created", Payload: []byte(`{"outage":true}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	}); err == nil {
		t.Fatal("API accepted an event during database outage")
	}
	progress := &delivery.Progress{}
	service := delivery.Service{Store: appStore, Sender: immediateDatabaseSender{}, Progress: progress,
		Concurrency: 1, PollInterval: 20 * time.Millisecond, ShutdownGrace: time.Second}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- service.Run(runCtx) }()
	deadline := time.Now().Add(3 * time.Second)
	for progress.Snapshot(time.Now()).StorageErrors == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not observe database outage")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if progress.Snapshot(time.Now()).Ready {
		t.Fatal("worker remained ready during outage")
	}
	select {
	case err := <-done:
		t.Fatalf("worker exited instead of waiting for database recovery: %v", err)
	default:
	}
	proxy.setDown(false)
	deadline = time.Now().Add(8 * time.Second)
	for {
		item, err := baseStore.GetDelivery(ctx, queued.Deliveries[0].ID)
		if err == nil && item.Status == "succeeded" && progress.Snapshot(time.Now()).Ready && appStore.Ready(ctx) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("database/worker did not recover: delivery=%+v err=%v progress=%+v", item, err, progress.Snapshot(time.Now()))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, _, err := appStore.Create(ctx, "test-producer", "outage-recovered", events.Input{
		Type: "order.created", Payload: []byte(`{"after":true}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	}); err != nil {
		t.Fatalf("API acceptance did not recover: %v", err)
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after test")
	}
}
