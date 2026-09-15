package supportbundle

import (
	"context"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/provenance"
)

func TestProvenanceCollectorEmitsOnlyBoundedMetadata(t *testing.T) {
	claim := "claim-body-MUST-NOT-LEAK"
	excerpt := "source-excerpt-MUST-NOT-LEAK"
	submitted := "submitted-payload-MUST-NOT-LEAK"
	credential := "postgres://loom:credential-MUST-NOT-LEAK@example/loom_provenance"
	now := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	age := int64(60)
	output, err := collectProvenance(context.Background(), CollectionContext{Options: Options{
		ProvenanceHealthProvider: func(context.Context) (provenance.HealthReport, error) {
			_ = claim
			_ = excerpt
			_ = submitted
			return provenance.HealthReport{
				SchemaVersion: claim,
				CapturedAt:    now,
				Database: provenance.DatabaseHealth{
					Name: credential, State: provenance.ReadinessState(excerpt), Code: provenance.ReadinessCode(submitted), AppliedHead: provenance.SchemaHead, PackagedHead: provenance.SchemaHead,
				},
				Counts: provenance.LifecycleCounts{Candidates: 2, Sources: 3, Records: provenance.HealthCountLimit + 1, Producers: -1, LifecycleEvents: 4},
				Backup: provenance.BackupFreshness{State: claim, Freshness: excerpt, AgeSeconds: &age, MaxAgeSeconds: 3600},
			}, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Files) != 1 || output.Files[0].Path != "summaries/provenance.json" {
		t.Fatalf("collector output = %#v", output)
	}
	raw := string(output.Files[0].Data)
	for _, forbidden := range []string{claim, excerpt, submitted, credential, "credential-MUST-NOT-LEAK"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("provenance support output leaked %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{provenance.HealthSchemaVersion, `"candidates": 2`, `"sources": 3`, `"records": 1000000`, `"producers": 1000000`, `"truncated": true`, `"state": "not_ready"`, `"freshness": "missing"`} {
		if !strings.Contains(raw, required) {
			t.Fatalf("provenance support output missing %q: %s", required, raw)
		}
	}
}

func TestDefaultCollectorsRegisterProvenanceAndSkipWithoutProvider(t *testing.T) {
	var collector Collector
	for _, candidate := range DefaultCollectors() {
		if candidate.Key == "provenance" {
			collector = candidate
			break
		}
	}
	if collector.Key == "" {
		t.Fatal("default support plan does not register provenance metadata")
	}
	output, err := collector.Collect(context.Background(), CollectionContext{Options: DefaultOptions()})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Files) != 0 || output.SkipReason != "provenance_health_provider_unavailable" {
		t.Fatalf("missing provider output = %#v", output)
	}
}
