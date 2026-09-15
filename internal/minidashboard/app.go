package minidashboard

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

type App struct {
	Config    Config
	Collector *HostCollector
	Poller    *DomainPoller

	mu   sync.RWMutex
	host HostSnapshot
}

func (a *App) Snapshot() ViewSnapshot {
	a.mu.RLock()
	host := a.host
	a.mu.RUnlock()
	return Project(time.Now().UTC(), host, a.Poller.Snapshot(), a.Config)
}

func (a *App) Run(ctx context.Context) error {
	if err := a.Config.Validate(); err != nil {
		return err
	}
	if a.Collector == nil {
		a.Collector = NewHostCollector("/")
	}
	if a.Poller == nil {
		a.Poller = &DomainPoller{Cache: DomainCache{Path: a.Config.StatePath}}
	}
	if a.Poller.RequestTimeout <= 0 {
		a.Poller.RequestTimeout = a.Config.RequestTimeout
	}
	a.Poller.LoadCache()
	listener, err := net.Listen("tcp", a.Config.ListenAddress)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.sampleHost()
	go a.runHostSampler(runCtx)
	if a.Poller.Client != nil {
		go a.Poller.Run(runCtx, a.Config.PollInterval)
	}
	server := &http.Server{Handler: NewDisplayHandler(a.Snapshot, a.Poller.Diagnostic), ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-runCtx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (a *App) runHostSampler(ctx context.Context) {
	ticker := time.NewTicker(a.Config.SamplingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sampleHost()
		}
	}
}

func (a *App) sampleHost() {
	snapshot := a.Collector.Collect(a.Config)
	a.mu.Lock()
	a.host = snapshot
	a.mu.Unlock()
}
