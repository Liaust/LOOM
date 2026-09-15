package minidashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDomainServiceReturnsBoundedPartialSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	live := Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive}
	service := DomainService{Node: NodeIdentity{Key: "main", Label: "MAIN"}, Now: func() time.Time { return now }, Deps: DomainDependencies{
		Runtime: func(context.Context) (RuntimeState, error) {
			return RuntimeState{Freshness: live, Available: true, State: SourceLive, DatabaseState: "ok", MigrationsCurrent: true}, nil
		},
		Protection: func(context.Context) (ProtectionSnapshot, error) {
			return ProtectionSnapshot{Local: ProtectionState{Available: true, State: "ok", SourceUpdatedAt: now, StaleAfterSeconds: 60, SourceState: SourceLive}, Cloud: ProtectionState{State: "disabled", SourceUpdatedAt: now, StaleAfterSeconds: 60, SourceState: SourceDisabled}, Coverage: CoverageState{Available: true, State: "ok", CapturedAt: now, StaleAfterSeconds: 60, SourceState: SourceCached}, Findings: FindingSummary{Available: true}}, nil
		},
		Network:  func(context.Context) (NetworkState, error) { return NetworkState{}, context.DeadlineExceeded },
		Activity: func(context.Context) (ActivityState, error) { return ActivityState{Freshness: live}, nil },
	}}
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("partial snapshot contract invalid: %v", err)
	}
	if snapshot.Network.Freshness.SourceState != SourceUnavailable {
		t.Fatalf("network = %+v", snapshot.Network)
	}
	if snapshot.Runtime.State != SourceLive || snapshot.LocalBackup.State != "ok" {
		t.Fatalf("successful sections lost: %+v", snapshot)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= MaxDomainPayloadSize {
		t.Fatalf("payload size = %d", len(payload))
	}
	for _, forbidden := range []string{"/var/lib", "error_message", "uri", "path"} {
		if stringContains(string(payload), forbidden) {
			t.Fatalf("payload contains %q: %s", forbidden, payload)
		}
	}
}

func TestDomainServiceSanitizesNodeIdentity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service := DomainService{Node: NodeIdentity{Key: "not a token", Label: "/private/path"}, Now: func() time.Time { return now }}
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Node.Key != "main" || snapshot.Node.Label != "MAIN" {
		t.Fatalf("node = %+v", snapshot.Node)
	}
}

func stringContains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
