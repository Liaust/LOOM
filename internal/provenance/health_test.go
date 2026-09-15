package provenance

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecoveryRelationsAreVersionedExactlyForHistoricalAndCurrentHeads(t *testing.T) {
	head6, err := RecoveryRelationsForSchemaHead(6)
	if err != nil {
		t.Fatal(err)
	}
	head7, err := RecoveryRelationsForSchemaHead(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(head6) != 19 || len(head7) != 21 {
		t.Fatalf("recovery relation sizes: head6=%d head7=%d", len(head6), len(head7))
	}
	for _, relation := range []string{"project_projection_snapshots", "repository_projection_snapshots"} {
		if containsRecoveryRelation(head6, relation) || !containsRecoveryRelation(head7, relation) {
			t.Fatalf("relation %q historical/current coverage mismatch: head6=%v head7=%v", relation, head6, head7)
		}
	}
	for _, unsupported := range []int{0, 1, 5, 8, 99} {
		if _, err := RecoveryRelationsForSchemaHead(unsupported); !errors.Is(err, ErrUnsupportedRecoverySchemaHead) {
			t.Fatalf("schema head %d error = %v", unsupported, err)
		}
	}
	head6[0] = "mutated"
	reloaded, err := RecoveryRelationsForSchemaHead(6)
	if err != nil || reloaded[0] == "mutated" {
		t.Fatalf("recovery relation contract was mutable: %v err=%v", reloaded, err)
	}

	counts6 := make(map[string]int64, len(reloaded))
	for _, relation := range reloaded {
		counts6[relation] = 0
	}
	valid6 := RecoverySnapshot{SchemaHead: 6, RelationCounts: counts6, GraphDigest: "sha256:historical"}
	if err := CompareRecoverySnapshots(valid6, valid6); err != nil {
		t.Fatalf("exact head-6 snapshot rejected: %v", err)
	}
	missing := cloneRecoverySnapshotForTest(valid6)
	delete(missing.RelationCounts, "source_references")
	if err := CompareRecoverySnapshots(valid6, missing); err == nil {
		t.Fatal("missing head-6 relation was accepted")
	}
	extra := cloneRecoverySnapshotForTest(valid6)
	extra.RelationCounts["project_projection_snapshots"] = 0
	if err := CompareRecoverySnapshots(valid6, extra); err == nil {
		t.Fatal("cross-head relation was accepted at head 6")
	}
	crossHead := cloneRecoverySnapshotForTest(valid6)
	crossHead.SchemaHead = 7
	if err := CompareRecoverySnapshots(valid6, crossHead); err == nil {
		t.Fatal("cross-head snapshots were accepted")
	}

	current := RecoveryRelations()
	if !reflect.DeepEqual(current, head7) {
		t.Fatalf("current recovery relations = %v, want head 7 %v", current, head7)
	}
}

func containsRecoveryRelation(relations []string, want string) bool {
	for _, relation := range relations {
		if relation == want {
			return true
		}
	}
	return false
}

func cloneRecoverySnapshotForTest(input RecoverySnapshot) RecoverySnapshot {
	counts := make(map[string]int64, len(input.RelationCounts))
	for relation, count := range input.RelationCounts {
		counts[relation] = count
	}
	input.RelationCounts = counts
	return input
}

func TestHealthReportIsBoundedMetadataOnlyAndClassifiesBackupFreshness(t *testing.T) {
	now := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Hour)
	verified := now.Add(-30 * time.Minute)
	report, err := BuildHealthReport(nil, nil, RuntimeReadiness{
		State: ReadinessNotReady, Code: ReadinessCodeInvalidConfiguration,
		Database: "postgres://loom:credential-MUST-NOT-LEAK@example/loom_provenance",
		Role:     "role-secret-MUST-NOT-LEAK", PackagedHead: SchemaHead,
		PendingVersions: []int{1, 2, 3},
	}, BackupObservation{
		State: BackupStateComplete, CompletedAt: &completed, VerifiedAt: &verified, MaxAge: 2 * time.Hour,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Database.Name != "" || report.Backup.Freshness != "current" || !report.Backup.RecoveryReady || report.Backup.AgeSeconds == nil || *report.Backup.AgeSeconds != 3600 {
		t.Fatalf("health report = %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"credential-MUST-NOT-LEAK", "role-secret-MUST-NOT-LEAK", "pending_versions", "postgres://"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("health output leaked %q: %s", forbidden, raw)
		}
	}

	counts := map[string]int64{"candidates": HealthCountLimit + 1, "source_references": HealthCountLimit, "evidence_registrations": 1}
	bounded := lifecycleCounts(counts)
	if bounded.Candidates != HealthCountLimit || bounded.Sources != HealthCountLimit || !bounded.Truncated {
		t.Fatalf("bounded counts = %#v", bounded)
	}
}

func TestHealthReportFailsBackupFreshnessClosed(t *testing.T) {
	now := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	completed := now.Add(-48 * time.Hour)
	verified := completed
	invalidCompleted := now.Add(-time.Hour)
	invalidVerified := invalidCompleted.Add(-time.Minute)
	tests := []struct {
		name        string
		observation BackupObservation
		freshness   string
	}{
		{"missing", BackupObservation{State: BackupStateMissing}, "missing"},
		{"interrupted", BackupObservation{State: BackupStateInterrupted}, "interrupted"},
		{"unverified", BackupObservation{State: BackupStateComplete, CompletedAt: &completed}, "unverified"},
		{"stale", BackupObservation{State: BackupStateComplete, CompletedAt: &completed, VerifiedAt: &verified, MaxAge: 24 * time.Hour}, "stale"},
		{"invalid verification order", BackupObservation{State: BackupStateComplete, CompletedAt: &invalidCompleted, VerifiedAt: &invalidVerified}, "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := backupFreshness(test.observation, now)
			if result.Freshness != test.freshness || result.RecoveryReady {
				t.Fatalf("freshness = %#v", result)
			}
		})
	}
}
