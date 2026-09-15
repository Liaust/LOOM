package minidashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	DomainRequestTimeout = 2 * time.Second
	MaxDomainPayloadSize = 32 * 1024
)

type ProtectionSnapshot struct {
	Local    ProtectionState
	Cloud    ProtectionState
	Coverage CoverageState
	Findings FindingSummary
}

type DomainDependencies struct {
	Runtime    func(context.Context) (RuntimeState, error)
	Protection func(context.Context) (ProtectionSnapshot, error)
	Network    func(context.Context) (NetworkState, error)
	Activity   func(context.Context) (ActivityState, error)
}

type DomainService struct {
	Node NodeIdentity
	Deps DomainDependencies
	Now  func() time.Time
}

func (s DomainService) Snapshot(ctx context.Context) (DomainSnapshot, error) {
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	ctx, cancel := context.WithTimeout(ctx, DomainRequestTimeout)
	defer cancel()
	snapshot := DomainSnapshot{
		SchemaVersion: SchemaVersion, GeneratedAt: now,
		Freshness:   Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive},
		Node:        NodeIdentity{Key: SafeToken(s.Node.Key, "main", MaxNodeKeyLength), Label: SafeLabel(s.Node.Label, "MAIN", MaxNodeLabelLength)},
		Runtime:     unavailableRuntime(now),
		LocalBackup: unavailableProtection(now), CloudSnapshot: unavailableProtection(now), Coverage: unavailableCoverage(now),
		Network: unavailableNetwork(now), Activity: unavailableActivity(now),
	}
	type result struct {
		kind  string
		value any
		err   error
	}
	results := make(chan result, 4)
	run := func(kind string, fn func(context.Context) (any, error)) {
		go func() { value, err := fn(ctx); results <- result{kind: kind, value: value, err: err} }()
	}
	if s.Deps.Runtime != nil {
		run("runtime", func(ctx context.Context) (any, error) { return s.Deps.Runtime(ctx) })
	} else {
		results <- result{kind: "runtime", err: fmt.Errorf("unavailable")}
	}
	if s.Deps.Protection != nil {
		run("protection", func(ctx context.Context) (any, error) { return s.Deps.Protection(ctx) })
	} else {
		results <- result{kind: "protection", err: fmt.Errorf("unavailable")}
	}
	if s.Deps.Network != nil {
		run("network", func(ctx context.Context) (any, error) { return s.Deps.Network(ctx) })
	} else {
		results <- result{kind: "network", err: fmt.Errorf("unavailable")}
	}
	if s.Deps.Activity != nil {
		run("activity", func(ctx context.Context) (any, error) { return s.Deps.Activity(ctx) })
	} else {
		results <- result{kind: "activity", err: fmt.Errorf("unavailable")}
	}
	for i := 0; i < 4; i++ {
		select {
		case item := <-results:
			if item.err != nil {
				continue
			}
			switch item.kind {
			case "runtime":
				snapshot.Runtime = item.value.(RuntimeState)
			case "protection":
				value := item.value.(ProtectionSnapshot)
				snapshot.LocalBackup, snapshot.CloudSnapshot, snapshot.Coverage, snapshot.Findings = value.Local, value.Cloud, value.Coverage, value.Findings
			case "network":
				snapshot.Network = item.value.(NetworkState)
			case "activity":
				snapshot.Activity = item.value.(ActivityState)
			}
		case <-ctx.Done():
			i = 4
		}
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return DomainSnapshot{}, err
	}
	if len(payload) > MaxDomainPayloadSize {
		return DomainSnapshot{}, fmt.Errorf("mini-dashboard snapshot exceeds %d bytes", MaxDomainPayloadSize)
	}
	return snapshot, nil
}

func unavailableRuntime(now time.Time) RuntimeState {
	return RuntimeState{Freshness: unavailableFreshness(now), State: SourceUnavailable, DatabaseState: "unknown"}
}
func unavailableProtection(now time.Time) ProtectionState {
	return ProtectionState{State: "unknown", SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceUnavailable}
}
func unavailableCoverage(now time.Time) CoverageState {
	return CoverageState{State: "unknown", CapturedAt: now, StaleAfterSeconds: 30, SourceState: SourceUnavailable}
}
func unavailableNetwork(now time.Time) NetworkState {
	return NetworkState{Freshness: unavailableFreshness(now), Cloud: CloudReachability{State: "unknown", SourceState: SourceUnavailable}}
}
func unavailableActivity(now time.Time) ActivityState {
	return ActivityState{Freshness: unavailableFreshness(now)}
}
func unavailableFreshness(now time.Time) Freshness {
	return Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceUnavailable}
}
