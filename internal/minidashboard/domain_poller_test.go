package minidashboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loom.local/loom/internal/response"
)

type pollClient struct {
	snapshots []DomainSnapshot
	errors    []error
	index     int
}

type hungThenHealthyClient struct {
	calls         atomic.Int32
	snapshot      DomainSnapshot
	firstTimeout  chan struct{}
	retried       chan struct{}
	allowRecovery chan struct{}
	timeoutOnce   sync.Once
	retryOnce     sync.Once
}

func (c *hungThenHealthyClient) MiniDashboardStatus(ctx context.Context, _ string) (response.Envelope[DomainSnapshot], error) {
	if c.calls.Add(1) == 1 {
		<-ctx.Done()
		c.timeoutOnce.Do(func() { close(c.firstTimeout) })
		return response.Envelope[DomainSnapshot]{}, ctx.Err()
	}
	c.retryOnce.Do(func() { close(c.retried) })
	<-c.allowRecovery
	return response.Envelope[DomainSnapshot]{OK: true, Data: c.snapshot}, nil
}

func (c *pollClient) MiniDashboardStatus(context.Context, string) (response.Envelope[DomainSnapshot], error) {
	index := c.index
	c.index++
	if index < len(c.errors) && c.errors[index] != nil {
		return response.Envelope[DomainSnapshot]{}, c.errors[index]
	}
	return response.Envelope[DomainSnapshot]{OK: true, Data: c.snapshots[index]}, nil
}

func TestPollerPreservesLastGoodAndMarksDaemonOffline(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	client := &pollClient{snapshots: []DomainSnapshot{healthyDomainFixture(now)}, errors: []error{nil, errors.New("offline")}}
	poller := DomainPoller{Client: client, Cache: DomainCache{Path: filepath.Join(t.TempDir(), "cache.json")}, Now: func() time.Time { return now }}
	if !poller.Poll(context.Background()) {
		t.Fatal("healthy poll failed")
	}
	now = now.Add(10 * time.Second)
	if poller.Poll(context.Background()) {
		t.Fatal("offline poll succeeded")
	}
	got := poller.Snapshot()
	if got == nil || got.Runtime.Queued != healthyDomainFixture(now).Runtime.Queued || got.Freshness.SourceState != SourceOffline {
		t.Fatalf("offline snapshot = %+v", got)
	}
}

func TestPollerIgnoresCorruptCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := osWriteFile(path, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	poller := DomainPoller{Cache: DomainCache{Path: path}}
	poller.LoadCache()
	if poller.Snapshot() != nil {
		t.Fatal("corrupt cache loaded")
	}
}

func TestPollerTimesOutHungRequestAndTickerRetries(t *testing.T) {
	now := time.Now().UTC()
	client := &hungThenHealthyClient{snapshot: healthyDomainFixture(now), firstTimeout: make(chan struct{}), retried: make(chan struct{}), allowRecovery: make(chan struct{})}
	poller := DomainPoller{
		Client: client, Cache: DomainCache{Path: filepath.Join(t.TempDir(), "cache.json")},
		RequestTimeout: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(client.allowRecovery) }) }
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("poller fixture did not stop")
		}
	})
	go func() { defer close(done); poller.Run(ctx, 15*time.Millisecond) }()
	select {
	case <-client.firstTimeout:
	case <-time.After(time.Second):
		t.Fatal("hung request did not reach its deadline")
	}
	select {
	case <-client.retried:
	case <-time.After(time.Second):
		t.Fatal("poll ticker did not retry after timeout")
	}
	// The next request proves the previous Poll returned. Hold its response so
	// recovery cannot race the timeout-state assertion.
	if got := poller.Diagnostic().SourceState; got != SourceOffline {
		t.Fatalf("source after timeout = %q", got)
	}
	release()
	deadline := time.Now().Add(time.Second)
	for poller.Diagnostic().SourceState != SourceLive {
		if time.Now().After(deadline) {
			t.Fatal("poller did not publish recovery")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
	if client.calls.Load() < 2 {
		t.Fatalf("calls = %d", client.calls.Load())
	}
	if got := poller.Diagnostic().SourceState; got != SourceLive {
		t.Fatalf("source after recovery = %q", got)
	}
}

func TestPollerSurfacesCacheSaveFailureWithoutMarkingLoomdOffline(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &pollClient{snapshots: []DomainSnapshot{healthyDomainFixture(now)}}
	poller := DomainPoller{Client: client, Cache: DomainCache{Path: filepath.Join(parent, "cache.json")}, Now: func() time.Time { return now }, RequestTimeout: time.Second}
	if !poller.Poll(context.Background()) {
		t.Fatal("valid loomd response was rejected because cache save failed")
	}
	diagnostic := poller.Diagnostic()
	if diagnostic.SourceState != SourceLive || diagnostic.CacheState != SourceFailed || diagnostic.LastCacheFailureAt.IsZero() {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
	if got := poller.Snapshot(); got == nil || got.Freshness.SourceState != SourceLive {
		t.Fatalf("in-memory snapshot = %+v", got)
	}
}

func osWriteFile(path string, payload []byte) error { return os.WriteFile(path, payload, 0o640) }
