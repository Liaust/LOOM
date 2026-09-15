package minidashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDomainSnapshotContractAndMetricAvailability(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	snapshot := healthyDomainFixture(now)
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"path", "uri", "error_json", "error_message", "raw_error", "actor_id", "correlation_id", "file_name"} {
		if strings.Contains(string(payload), `"`+forbidden+`"`) {
			t.Fatalf("domain contract exposed forbidden field %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(string(payload), `"schema_version":"`+SchemaVersion+`"`) {
		t.Fatalf("missing schema version: %s", payload)
	}

	value := 0.0
	valid := Metric{Available: true, Value: &value, SampledAt: now, StaleAfterSeconds: 6, SourceState: SourceLive, Severity: SeverityHealthy}
	if err := valid.Validate("cpu"); err != nil {
		t.Fatalf("valid zero metric rejected: %v", err)
	}
	invalid := Metric{Available: false, Value: &value, SourceState: SourceUnavailable}
	if err := invalid.Validate("temperature"); err == nil {
		t.Fatal("unavailable metric with numeric zero was accepted")
	}
	missing := Metric{Available: true, SourceState: SourceLive}
	if err := missing.Validate("memory"); err == nil {
		t.Fatal("available metric without value was accepted")
	}
}

func TestDomainSnapshotRejectsForeignVersionAndUnsafeIdentity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*DomainSnapshot)
	}{
		{"foreign version", func(value *DomainSnapshot) { value.SchemaVersion = "loom.mini_dashboard.v2" }},
		{"path label", func(value *DomainSnapshot) { value.Node.Label = "/var/lib/loom" }},
		{"bad key", func(value *DomainSnapshot) { value.Node.Key = "main node" }},
		{"invalid domain source state", func(value *DomainSnapshot) { value.Freshness.SourceState = SourceState("invented") }},
		{"invalid runtime source state", func(value *DomainSnapshot) { value.Runtime.Freshness.SourceState = SourceState("invented") }},
		{"invalid cloud cache source state", func(value *DomainSnapshot) { value.Network.Cloud.SourceState = SourceState("invented") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := healthyDomainFixture(now)
			tt.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestMetricRejectsInvalidSourceState(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	value := 18.0
	metric := Metric{Available: true, Value: &value, SampledAt: now, StaleAfterSeconds: 6, SourceState: SourceState("invented"), Severity: SeverityHealthy}
	if err := metric.Validate("cpu"); err == nil {
		t.Fatal("metric accepted an invalid source state")
	}
}

func TestMetricRejectsUnknownOrInvalidSeverity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	value := 18.0
	for _, severity := range []Severity{SeverityUnknown, SeverityActive, Severity("invented")} {
		t.Run(string(severity), func(t *testing.T) {
			metric := Metric{Available: true, Value: &value, SampledAt: now, StaleAfterSeconds: 6, SourceState: SourceLive, Severity: severity}
			if err := metric.Validate("cpu"); err == nil {
				t.Fatal("metric accepted an unknown or invalid severity")
			}
		})
	}
	unavailable := Metric{Available: false, SourceState: SourceUnavailable, Severity: Severity("invented")}
	if err := unavailable.Validate("temperature"); err == nil {
		t.Fatal("unavailable metric accepted an invalid severity")
	}
}
