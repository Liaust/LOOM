package minidashboard

import (
	"context"
	"sync"
	"time"

	"loom.local/loom/internal/response"
)

type DomainClient interface {
	MiniDashboardStatus(context.Context, string) (response.Envelope[DomainSnapshot], error)
}

type DomainPoller struct {
	Client         DomainClient
	Cache          DomainCache
	Now            func() time.Time
	RequestTimeout time.Duration

	mu               sync.RWMutex
	lastGood         *DomainSnapshot
	lastSuccess      time.Time
	lastFailure      time.Time
	lastCacheFailure time.Time
	latency          time.Duration
	sourceState      SourceState
	cacheState       SourceState
}

type PollerDiagnostic struct {
	SourceState        SourceState `json:"source_state"`
	CacheState         SourceState `json:"cache_state"`
	LastSuccessAt      time.Time   `json:"last_success_at,omitempty"`
	LastFailureAt      time.Time   `json:"last_failure_at,omitempty"`
	LastCacheFailureAt time.Time   `json:"last_cache_failure_at,omitempty"`
	LatencyMS          int64       `json:"latency_ms"`
}

func (p *DomainPoller) LoadCache() {
	snapshot, err := p.Cache.Load()
	now := p.now().UTC()
	if err != nil {
		p.mu.Lock()
		p.cacheState, p.lastCacheFailure = SourceFailed, now
		p.mu.Unlock()
		return
	}
	if snapshot == nil {
		p.mu.Lock()
		p.cacheState = SourceUnavailable
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	p.lastGood, p.cacheState = snapshot, SourceCached
	p.mu.Unlock()
}

func (p *DomainPoller) Poll(ctx context.Context) bool {
	started := p.now()
	requestCtx, cancel := context.WithTimeout(ctx, p.requestTimeout())
	defer cancel()
	envelope, err := p.Client.MiniDashboardStatus(requestCtx, "mini_dashboard_poll")
	now := p.now().UTC()
	p.mu.Lock()
	p.latency = now.Sub(started)
	if err != nil || !envelope.OK || envelope.Data.SchemaVersion != SchemaVersion || envelope.Data.Validate() != nil {
		p.lastFailure, p.sourceState = now, SourceOffline
		p.mu.Unlock()
		return false
	}
	snapshot := envelope.Data
	p.lastGood, p.lastSuccess, p.sourceState = &snapshot, now, SourceLive
	p.mu.Unlock()
	cacheErr := p.Cache.Save(snapshot)
	p.mu.Lock()
	if cacheErr != nil {
		p.cacheState, p.lastCacheFailure = SourceFailed, now
	} else {
		p.cacheState, p.lastCacheFailure = SourceCached, time.Time{}
	}
	p.mu.Unlock()
	return true
}

func (p *DomainPoller) Snapshot() *DomainSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.lastGood == nil {
		return nil
	}
	value := *p.lastGood
	if !p.lastFailure.IsZero() && p.lastFailure.After(p.lastSuccess) {
		value.Freshness.SourceState = SourceOffline
	}
	return &value
}

func (p *DomainPoller) Diagnostic() PollerDiagnostic {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sourceState := p.sourceState
	if sourceState == "" {
		sourceState = SourceUnknown
	}
	cacheState := p.cacheState
	if cacheState == "" {
		cacheState = SourceUnknown
	}
	return PollerDiagnostic{
		SourceState: sourceState, CacheState: cacheState,
		LastSuccessAt: p.lastSuccess, LastFailureAt: p.lastFailure,
		LastCacheFailureAt: p.lastCacheFailure, LatencyMS: p.latency.Milliseconds(),
	}
}

func (p *DomainPoller) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	p.Poll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Poll(ctx)
		}
	}
}

func (p *DomainPoller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *DomainPoller) requestTimeout() time.Duration {
	if p.RequestTimeout > 0 {
		return p.RequestTimeout
	}
	return 2 * time.Second
}
