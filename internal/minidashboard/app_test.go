package minidashboard

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loom.local/loom/internal/response"
)

func TestAppHostSamplingContinuesWithOfflineDomain(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	collector := NewHostCollector("/data")
	cpu := []string{"cpu 100 0 100 800\n", "cpu 200 0 200 800\n"}
	collector.Now = func() time.Time { return now }
	collector.ReadFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "/stat") {
			value := cpu[0]
			cpu = cpu[1:]
			return []byte(value), nil
		}
		if strings.HasSuffix(path, "/meminfo") {
			return []byte("MemTotal: 1000 kB\nMemAvailable: 500 kB\n"), nil
		}
		return nil, errors.New("missing")
	}
	collector.Glob = func(string) ([]string, error) { return nil, nil }
	collector.FilesystemUsage = func(string) (float64, error) { return 20, nil }
	domain := healthyDomainFixture(now)
	poller := &DomainPoller{lastGood: &domain, lastFailure: now.Add(time.Second)}
	app := App{Config: DefaultConfig(), Collector: collector, Poller: poller}
	app.sampleHost()
	now = now.Add(2 * time.Second)
	app.sampleHost()
	view := app.Snapshot()
	if view.System[0].Value == "Unavailable" || view.Header.FreshnessState != SourceOffline {
		t.Fatalf("view = %+v", view)
	}
}

func TestAppRejectsNonLoopbackListener(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ListenAddress = "0.0.0.0:8090"
	app := App{Config: cfg}
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("non-loopback listener accepted")
	}
}

type countingDomainClient struct{ calls atomic.Int32 }

func (c *countingDomainClient) MiniDashboardStatus(context.Context, string) (response.Envelope[DomainSnapshot], error) {
	c.calls.Add(1)
	return response.Envelope[DomainSnapshot]{}, errors.New("unexpected poll")
}

func TestAppListenerFailureStartsNoSamplerOrPoller(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var reads atomic.Int32
	collector := NewHostCollector(t.TempDir())
	collector.ReadFile = func(string) ([]byte, error) { reads.Add(1); return nil, errors.New("unexpected sample") }
	collector.Glob = func(string) ([]string, error) { reads.Add(1); return nil, nil }
	collector.FilesystemUsage = func(string) (float64, error) { reads.Add(1); return 0, errors.New("unexpected sample") }
	client := &countingDomainClient{}
	cfg := DefaultConfig()
	cfg.ListenAddress = occupied.Addr().String()
	cfg.StatePath = filepath.Join(t.TempDir(), "cache.json")
	cfg.SamplingInterval = 5 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond
	cfg.RequestTimeout = 5 * time.Millisecond
	app := App{Config: cfg, Collector: collector, Poller: &DomainPoller{Client: client, Cache: DomainCache{Path: cfg.StatePath}}}
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("occupied listener unexpectedly succeeded")
	}
	time.Sleep(50 * time.Millisecond)
	if reads.Load() != 0 || client.calls.Load() != 0 {
		t.Fatalf("background work started after listen failure: reads=%d polls=%d", reads.Load(), client.calls.Load())
	}
}
