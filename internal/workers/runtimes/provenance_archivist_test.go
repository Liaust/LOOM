package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/workers"
)

type provenanceArchivistRuntimeTransport struct {
	provenance.FoundationTransport
	provenance.SearchTransport
}

type scheduledProvenanceArchivistRuntime struct {
	ProvenanceArchivistRuntime
}

func (runtime scheduledProvenanceArchivistRuntime) Describe() workers.KindDescriptor {
	descriptor := runtime.ProvenanceArchivistRuntime.Describe()
	descriptor.DefaultTickPolicyJSON = json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60}`)
	return descriptor
}

func (provenanceArchivistRuntimeTransport) Readiness() provenance.RuntimeReadiness {
	return provenance.RuntimeReadiness{State: provenance.ReadinessReady, Code: provenance.ReadinessCodeReady}
}

func (provenanceArchivistRuntimeTransport) ListCandidates(context.Context, provenance.PageRequest) (provenance.Page[provenance.CandidateSummary], error) {
	return provenance.Page[provenance.CandidateSummary]{}, nil
}

func TestProvenanceArchivistRuntimeDescriptorIsManualOnlyAndBounded(t *testing.T) {
	runtime := NewProvenanceArchivistRuntime(provenanceArchivistRuntimeTransport{})
	descriptor := runtime.Describe()

	if descriptor.WorkerKind != workers.KindProvenanceArchivist {
		t.Fatalf("unexpected worker kind %q", descriptor.WorkerKind)
	}
	if descriptor.MayCreateJobs || descriptor.MayCallCapabilities || descriptor.MayTouchFilesystem || descriptor.MayStoreRawPayloads {
		t.Fatalf("archivist descriptor exceeds its resource boundary: %+v", descriptor)
	}
	policy, err := workers.ParseTickPolicy(descriptor.DefaultTickPolicyJSON)
	if err != nil {
		t.Fatalf("parse tick policy: %v", err)
	}
	if policy.Mode != workers.TickModeManual || policy.RunOnStartup || policy.NextAfter(time.Now()) != nil {
		t.Fatalf("unexpected archivist tick policy: %+v", policy)
	}
	if len(runtime.DefaultInstances()) != 1 || runtime.DefaultInstances()[0].WorkerKey != "main.provenance_archivist" {
		t.Fatalf("unexpected default instances: %+v", runtime.DefaultInstances())
	}
	if !strings.Contains(string(descriptor.Metadata), `"scheduling_enabled":false`) {
		t.Fatalf("descriptor does not freeze scheduling disabled: %s", descriptor.Metadata)
	}

	registry := workers.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatalf("register archivist runtime: %v", err)
	}
	if err := workers.NewRegistry().Register(scheduledProvenanceArchivistRuntime{ProvenanceArchivistRuntime: runtime}); !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("scheduled archivist descriptor should fail closed, got %v", err)
	}
}

func TestProvenanceArchivistRuntimeRunsManualEmptyCycleAndCheckpoints(t *testing.T) {
	runtime := NewProvenanceArchivistRuntime(provenanceArchivistRuntimeTransport{})
	runtime.LeaseGuard = func(context.Context, workers.RunContext) error { return nil }
	run := provenanceArchivistRunContext(t, workers.TriggerManual, provenanceArchivistTestMetadata(json.RawMessage(`{
		"schema_version":"provenance_archivist.request.v1",
		"candidate_limit":2,
		"evidence_limit":2
	}`)))

	result, err := runtime.RunOnce(context.Background(), run)
	if err != nil {
		t.Fatalf("run archivist: %v", err)
	}
	if result.Status != workers.RunStatusSucceeded {
		t.Fatalf("unexpected status %q", result.Status)
	}
	if result.NextRunAfter != nil {
		t.Fatalf("manual archivist must not schedule a next run: %v", result.NextRunAfter)
	}
	if len(result.CheckpointUpdates) != 1 || result.CheckpointUpdates[0].Key != provenanceArchivistCheckpointKey {
		t.Fatalf("unexpected checkpoint updates: %+v", result.CheckpointUpdates)
	}
	var summary provenance.ArchivistRunResult
	if err := json.Unmarshal(result.ResultSummary, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.SchemaVersion != provenance.ArchivistResultSchemaVersion || !summary.CycleComplete || summary.Selected != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestProvenanceArchivistRuntimeRejectsSchedulingBoundExpansionAndLeaseLoss(t *testing.T) {
	runtime := NewProvenanceArchivistRuntime(provenanceArchivistRuntimeTransport{})
	runtime.LeaseGuard = func(context.Context, workers.RunContext) error { return nil }

	_, err := runtime.RunOnce(context.Background(), provenanceArchivistRunContext(t, workers.TriggerSchedule, nil))
	if err == nil || !strings.Contains(err.Error(), "manual or explicit retry") {
		t.Fatalf("expected schedule rejection, got %v", err)
	}

	_, err = runtime.RunOnce(context.Background(), provenanceArchivistRunContext(t, workers.TriggerManual, provenanceArchivistTestMetadata(json.RawMessage(`{
		"schema_version":"provenance_archivist.request.v1",
		"candidate_limit":25
	}`))))
	if err == nil || !strings.Contains(err.Error(), "cannot expand configured") {
		t.Fatalf("expected bound expansion rejection, got %v", err)
	}

	runtime.LeaseGuard = func(context.Context, workers.RunContext) error { return errors.New("lease lost") }
	_, err = runtime.RunOnce(context.Background(), provenanceArchivistRunContext(t, workers.TriggerManual, nil))
	if err == nil || !strings.Contains(err.Error(), "lease lost") {
		t.Fatalf("expected lease loss, got %v", err)
	}
}

func TestProvenanceArchivistConfigAndRequestFailClosed(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"policy":     json.RawMessage(`{"schema_version":"provenance_archivist.config.v1","policy_version":"other","candidate_limit":5,"evidence_limit":8}`),
		"limit":      json.RawMessage(`{"schema_version":"provenance_archivist.config.v1","policy_version":"loom.provenance.archivist.policy.v1","candidate_limit":26,"evidence_limit":8}`),
		"scheduling": json.RawMessage(`{"schema_version":"provenance_archivist.config.v1","policy_version":"loom.provenance.archivist.policy.v1","candidate_limit":5,"evidence_limit":8,"scheduling_enabled":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProvenanceArchivistConfig(raw); err == nil {
				t.Fatalf("expected config rejection for %s", raw)
			}
		})
	}

	if _, err := parseProvenanceArchivistRequest(json.RawMessage(`{
		"schema_version":"worker_run.metadata.v0.2",
		"request_metadata":{"schema_version":"provenance_archivist.request.v1","scan_notes":true}
	}`)); err == nil {
		t.Fatal("expected unknown request field to fail closed")
	}
}

func provenanceArchivistRunContext(t *testing.T, trigger string, metadata json.RawMessage) workers.RunContext {
	t.Helper()
	startedAt := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	return workers.RunContext{
		Instance: workers.WorkerInstance{
			WorkerInstanceID: "instance-archivist-test",
			WorkerKey:        "main.provenance_archivist",
			WorkerKind:       workers.KindProvenanceArchivist,
			ConfigJSON:       runtimeDefaultProvenanceArchivistConfig(t),
			TickPolicyJSON:   json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		},
		Run: workers.WorkerRun{
			WorkerRunID: "run-archivist-test",
			TriggerKind: trigger,
			Metadata:    metadata,
		},
		Lease: workers.WorkerLease{
			WorkerLeaseID: "lease-archivist-test",
			Generation:    1,
		},
		Checkpoints:    map[string]workers.WorkerCheckpoint{},
		StartedAt:      startedAt,
		IdempotencyKey: "archivist-runtime-test-key",
	}
}

func provenanceArchivistTestMetadata(request json.RawMessage) json.RawMessage {
	var requestValue any
	if err := json.Unmarshal(request, &requestValue); err != nil {
		panic(err)
	}
	value, err := json.Marshal(map[string]any{
		"schema_version":   "worker_run.metadata.v0.2",
		"request_metadata": requestValue,
	})
	if err != nil {
		panic(err)
	}
	return value
}

func runtimeDefaultProvenanceArchivistConfig(t *testing.T) json.RawMessage {
	t.Helper()
	runtime := NewProvenanceArchivistRuntime(provenanceArchivistRuntimeTransport{})
	return runtime.DefaultConfig()
}
