package update

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func failedUpdateForReconcile(t *testing.T) (string, ApplyResult) {
	t.Helper()
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)
	state := filepath.Join(root, "state")
	result, err := Apply(context.Background(), ApplyInput{
		Spec:    UpdateSpec{ReleasePath: target, ActivePath: current, ActiveMigrationsDir: filepath.Join(active, "migrations"), CurrentMigrationVersion: int64Ptr(36), StateDir: state},
		Runtime: devRuntime(), Yes: true, AllowNonProduction: true, SkipBackup: true, SkipRebuild: true,
		MaintenancePolicy: MaintenancePausePolicy{PauseSchedules: true}, Maintenance: &fakeMaintenanceCoordinator{}, Now: fixedNow,
		Runner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "loom" {
				return []byte(`{"ok":true,"data":{"service":"loomd","status":"unhealthy"}}`), nil
			}
			return readinessFixtureRunner(ctx, name, args...)
		},
	})
	if err == nil || result.Status != UpdateStatusFailed || result.Manifest.ReadinessTarget == nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	return state, result
}

func TestReconcilePreservesInitialFailureAndPerformsOnlyReads(t *testing.T) {
	state, failed := failedUpdateForReconcile(t)
	paths := []string{failed.ManifestPath, failed.HistoryPath, failed.PendingPath}
	original := make([][]byte, len(paths))
	for i, path := range paths {
		original[i], _ = os.ReadFile(path)
	}
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "readlink", "loom", "systemctl", "cat":
		default:
			t.Fatalf("reconcile performed command %s %v", name, args)
		}
		return readinessFixtureRunner(ctx, name, args...)
	}
	result, err := Reconcile(context.Background(), ReconcileInput{StateDir: state, Runner: runner})
	if err != nil || result.Status != "failed_then_observed_ready" || !result.Appended {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	second, err := Reconcile(context.Background(), ReconcileInput{StateDir: state, ManifestPath: failed.HistoryPath, Runner: runner})
	if err != nil || second.Appended || result.EvidencePath != second.EvidencePath || !result.Evidence.Readiness.ObservedAt.Equal(second.Evidence.Readiness.ObservedAt) {
		t.Fatalf("replay=%+v error=%v", second, err)
	}
	for i, path := range paths {
		current, _ := os.ReadFile(path)
		if !bytes.Equal(current, original[i]) {
			t.Fatalf("reconcile changed original receipt %s", path)
		}
	}
	status := Status(StatusInput{StateDir: state})
	if status.Active.Status != UpdateStatusFailed || len(status.Reconciliations) != 1 || status.Active.Maintenance.Status != MaintenanceWindowStatusResumeRequired {
		t.Fatalf("status=%+v", status)
	}
	var output bytes.Buffer
	RenderStatus(&output, status)
	if !strings.Contains(output.String(), "failed_then_observed_ready") || !strings.Contains(output.String(), "initial status failed") {
		t.Fatal(output.String())
	}
}

func TestReconcileRejectsUnknownIdentityWrongTargetAndChangedReceipt(t *testing.T) {
	for _, scenario := range []string{"legacy identity", "wrong target", "receipt changed", "unhealthy", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			state, failed := failedUpdateForReconcile(t)
			manifest := failed.Manifest
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "legacy identity":
				manifest.ReadinessTarget = nil
			case "wrong target":
				manifest.ReadinessTarget.ReleasePath = "/another/release"
			case "cancelled":
				cancel()
			}
			if err := WriteUpdateManifest(failed.ManifestPath, manifest); err != nil {
				t.Fatal(err)
			}
			runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "loom" {
					if scenario == "unhealthy" {
						return nil, errors.New("unhealthy")
					}
					if scenario == "receipt changed" {
						manifest.Status = UpdateStatusRunning
						if err := WriteUpdateManifest(failed.ManifestPath, manifest); err != nil {
							t.Fatal(err)
						}
					}
				}
				return readinessFixtureRunner(ctx, name, args...)
			}
			_, err := Reconcile(ctx, ReconcileInput{StateDir: state, Runner: runner})
			if err == nil {
				t.Fatal("invalid observation appended readiness")
			}
			if _, err := os.Stat(filepath.Join(state, "reconciliations")); !os.IsNotExist(err) {
				t.Fatal("failed inspection wrote evidence")
			}
		})
	}
}

func TestReconcileConcurrentEquivalentEvidenceIsAppendOnce(t *testing.T) {
	state, _ := failedUpdateForReconcile(t)
	var wg sync.WaitGroup
	results := make(chan ReconcileResult, 2)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := Reconcile(context.Background(), ReconcileInput{StateDir: state, Runner: readinessFixtureRunner})
			results <- result
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	appended := 0
	for result := range results {
		if result.Appended {
			appended++
		}
	}
	if appended != 1 {
		t.Fatalf("appended=%d", appended)
	}
}

func TestRollbackReadinessRejectsOldDaemonAndRetainsFailureReceipt(t *testing.T) {
	state, failed := failedUpdateForReconcile(t)
	manifest := failed.Manifest
	manifest.Rollback.RestoreRequired = false
	if err := WriteUpdateManifest(failed.ManifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	result, err := Rollback(context.Background(), RollbackInput{StateDir: state, Yes: true, AllowNonProduction: true, Runtime: devRuntime(), ServiceOnly: true, SkipRebuild: true, Now: fixedNow,
		Runner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "readlink" && len(args) == 1 {
				return []byte("/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-old/bin/loomd"), nil
			}
			return readinessFixtureRunner(ctx, name, args...)
		},
	})
	if err == nil || result.Status != UpdateStatusFailed || result.Manifest.Readiness != nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	history, err := ReadUpdateManifest(result.HistoryPath)
	if err != nil || history.Status != UpdateStatusFailed || metadataString(history.Metadata, "failed_step") != "health_check" {
		t.Fatalf("history=%+v error=%v", history, err)
	}
}
