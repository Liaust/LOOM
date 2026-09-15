package lane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaneRepairHonorsDurableCleanupIntentAfterAcceptSideCleanupFailure(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		for _, test := range []struct {
			name      string
			keepLocal bool
			change    bool
			want      string
		}{
			{name: "keep local", keepLocal: true, want: BatchStatusCataloged},
			{name: "cleanup unchanged", want: BatchStatusLocalCleanupDone},
			{name: "changed source withheld", change: true, want: BatchStatusLocalCleanupWithheld},
		} {
			t.Run(string(mode)+"/"+test.name, func(t *testing.T) {
				root := t.TempDir()
				lanePath := filepath.Join(root, DefaultLaneRelPath)
				payloadPath := filepath.Join(lanePath, "payload.txt")
				mustLaneFile(t, payloadPath, "accepted payload")
				batchID := "lane_accept_cleanup_" + string(mode) + "_" + strings.ReplaceAll(test.name, " ", "_")
				input := bundleSendTestInput(root, batchID, false)
				input.RequestedTransport = mode
				input.KeepLocal = test.keepLocal
				input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
					command := name + " " + strings.Join(args, " ")
					if strings.Contains(command, "lane accept-received") || strings.Contains(command, "lane accept-bundle") {
						return "", "storage.lane_source_cleanup_failed", errors.New("main staging cleanup interrupted")
					}
					return "", "", nil
				})
				failed, err := Send(context.Background(), input)
				if err != nil || failed.Status != BatchStatusSourceCleanupFailed {
					t.Fatalf("accept-side cleanup failure = %#v, %v", failed, err)
				}
				wantIntent := LocalCleanupIntentQuarantine
				if test.keepLocal {
					wantIntent = LocalCleanupIntentKeep
				}
				recordPath := filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json")
				record, err := readBatchRecord(recordPath)
				if err != nil || record.LocalCleanupIntent != wantIntent {
					t.Fatalf("durable cleanup intent = %#v, %v", record, err)
				}
				if test.change {
					if err := os.WriteFile(payloadPath, []byte("changed local payload that must survive"), 0o644); err != nil {
						t.Fatal(err)
					}
				}

				repaired, err := Publish(context.Background(), PublishInput{
					RootPath: root, LaneRelPath: DefaultLaneRelPath, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
					Runner: laneTestRunner(nil),
				})
				if err != nil || repaired.Status != test.want {
					t.Fatalf("cleanup-intent repair = %#v, %v", repaired, err)
				}
				switch {
				case test.keepLocal:
					assertLaneFile(t, payloadPath, "accepted payload")
				case test.change:
					assertLaneFile(t, payloadPath, "changed local payload that must survive")
					if repaired.ErrorMessage == "" {
						t.Fatalf("withheld repair omitted reason: %#v", repaired)
					}
				default:
					if _, err := os.Stat(payloadPath); !os.IsNotExist(err) {
						t.Fatalf("unchanged cleanup-requested payload remained: %v", err)
					}
					if repaired.LocalCleanupQuarantinePath == "" || len(repaired.QuarantinedLocalItems) == 0 {
						t.Fatalf("repair omitted cleanup evidence: %#v", repaired)
					}
				}
				retried, err := Publish(context.Background(), PublishInput{
					RootPath: root, LaneRelPath: DefaultLaneRelPath, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
					Runner: laneTestRunner(nil),
				})
				if err != nil || retried.Status != test.want {
					t.Fatalf("idempotent repeated repair = %#v, %v", retried, err)
				}
			})
		}
	}
}

func TestLaneRepairRetriesRemoteCleanupBeforeApplyingLocalCleanupIntent(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			payloadPath := filepath.Join(root, DefaultLaneRelPath, "payload.txt")
			mustLaneFile(t, payloadPath, "accepted payload")
			batchID := "lane_cleanup_retry_" + string(mode)
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = mode
			input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
				command := name + " " + strings.Join(args, " ")
				if strings.Contains(command, "lane accept-received") || strings.Contains(command, "lane accept-bundle") {
					return "", "storage.lane_source_cleanup_failed", errors.New("main staging cleanup interrupted")
				}
				return "", "", nil
			})
			if failed, err := Send(context.Background(), input); err != nil || failed.Status != BatchStatusSourceCleanupFailed {
				t.Fatalf("seed source-cleanup failure = %#v, %v", failed, err)
			}
			cleanupAttempts := 0
			repairInput := PublishInput{
				RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
				Runner: laneTestRunner(func(_ context.Context, _ string, args ...string) (string, string, error) {
					if len(args) > 0 && strings.Contains(args[len(args)-1], "lane staging --operation cleanup") {
						cleanupAttempts++
						if cleanupAttempts == 1 {
							return "", "interrupted", errors.New("connection lost after main cleanup crash window")
						}
					}
					return "", "", nil
				}),
			}
			if first, err := Publish(context.Background(), repairInput); err == nil || first.Status != BatchStatusSourceCleanupFailed {
				t.Fatalf("first repair should retain retry phase: %#v, %v", first, err)
			}
			assertLaneFile(t, payloadPath, "accepted payload")
			second, err := Publish(context.Background(), repairInput)
			if err != nil || second.Status != BatchStatusLocalCleanupDone {
				t.Fatalf("retry cleanup repair = %#v, %v", second, err)
			}
			if _, err := os.Stat(payloadPath); !os.IsNotExist(err) {
				t.Fatalf("successful retry did not quarantine local payload: %v", err)
			}
		})
	}
}

func TestLaneRepairPreservesPreexistingCompletedCleanupEvidence(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			batchID := "lane_cleanup_evidence_" + string(mode)
			statePath := filepath.Join(root, DefaultStateRelPath)
			quarantinePath := filepath.Join(statePath, "cleanup", batchID)
			mustLaneFile(t, filepath.Join(quarantinePath, "payload.txt"), "accepted payload")
			started := time.Now().UTC().Add(-time.Minute)
			if err := writeBatchRecord(statePath, BatchRecord{
				SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusSourceCleanupFailed,
				SourceNodeKey: "macbook", RemoteStagingPath: remotePathJoin(DefaultRemoteRoot, "staging", "macbook", batchID),
				SelectedTransport: mode, LocalCleanupIntent: LocalCleanupIntentQuarantine, StartedAt: &started,
				LocalCleanupQuarantinePath: quarantinePath, LocalCleanupQuarantineState: CleanupQuarantineRetainedForRecovery,
				QuarantinedLocalItems: []string{"payload.txt"}, RemovedLocalItems: []string{"payload.txt"},
			}); err != nil {
				t.Fatal(err)
			}
			result, err := Publish(context.Background(), PublishInput{
				RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
				Runner: laneTestRunner(nil),
			})
			if err != nil || result.Status != BatchStatusLocalCleanupDone || result.LocalCleanupQuarantinePath != quarantinePath || len(result.QuarantinedLocalItems) != 1 {
				t.Fatalf("pre-existing cleanup evidence repair = %#v, %v", result, err)
			}
			assertLaneFile(t, filepath.Join(quarantinePath, "payload.txt"), "accepted payload")
		})
	}
}

func TestLaneSendWithholdsCleanupWhenAcceptedSourceChanges(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		for _, timing := range []string{"after_acceptance", "after_final_replan"} {
			t.Run(string(mode)+"/"+timing, func(t *testing.T) {
				root := t.TempDir()
				lanePath := filepath.Join(root, DefaultLaneRelPath)
				payloadPath := filepath.Join(lanePath, "project", "payload.txt")
				mustLaneFile(t, payloadPath, "accepted payload")
				batchID := "lane_cleanup_withheld_" + string(mode) + "_" + timing
				input := bundleSendTestInput(root, batchID, false)
				input.RequestedTransport = mode

				var accepted, mutated bool
				mutate := func() error {
					if mutated {
						return nil
					}
					mutated = true
					if err := os.WriteFile(payloadPath, []byte("newer local payload that must survive"), 0o644); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(lanePath, "new-after-acceptance.txt"), []byte("new local file"), 0o644)
				}
				input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
					command := name + " " + strings.Join(args, " ")
					if strings.Contains(command, "accept-bundle") || strings.Contains(command, "accept-received") {
						accepted = true
						if timing == "after_acceptance" {
							return "", "", mutate()
						}
					}
					return "", "", nil
				})
				if timing == "after_final_replan" {
					input.BeforeCleanupEntry = func(_ int, entry TransferEntry) error {
						if entry.RelativePath == "project/payload.txt" {
							return mutate()
						}
						return nil
					}
				}

				result, err := Send(context.Background(), input)
				if err != nil {
					t.Fatalf("accepted cleanup withholding returned error: %v", err)
				}
				if !accepted || !mutated {
					t.Fatalf("test did not reach promoted/cataloged mutation boundary: accepted=%t mutated=%t", accepted, mutated)
				}
				if result.Status != BatchStatusLocalCleanupWithheld || result.ErrorMessage == "" {
					t.Fatalf("cleanup withholding result = %#v", result)
				}
				assertLaneFile(t, payloadPath, "newer local payload that must survive")
				assertLaneFile(t, filepath.Join(lanePath, "new-after-acceptance.txt"), "new local file")
				if len(result.RemovedLocalItems) != 0 || len(result.QuarantinedLocalItems) != 0 {
					t.Fatalf("pre-stage cleanup withholding reported removal: removed=%#v quarantined=%#v", result.RemovedLocalItems, result.QuarantinedLocalItems)
				}
				assertCleanupBatchEvidence(t, root, batchID, result)
				assertCleanupSafetyArtifact(t, mode, result, filepath.Join("project", "payload.txt"), "accepted payload")
				assertCleanupAttention(t, root)
				assertNoVisibleCleanupInternals(t, lanePath)
			})
		}
	}
}

func TestLaneCleanupQuarantinePreservesLateOpenDescriptorWrites(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			lanePath := filepath.Join(root, DefaultLaneRelPath)
			payloadPath := filepath.Join(lanePath, "project", "payload.txt")
			mustLaneFile(t, payloadPath, "accepted payload")
			handle, err := os.OpenFile(payloadPath, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()

			batchID := "lane_cleanup_late_descriptor_" + string(mode)
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = mode
			var stagedPath string
			input.AfterCleanupStage = func(_ int, entry TransferEntry, quarantinePath string) error {
				if entry.RelativePath != "project/payload.txt" {
					return nil
				}
				stagedPath = quarantinePath
				if _, err := os.Lstat(payloadPath); !os.IsNotExist(err) {
					return errUnexpectedVisiblePath(err)
				}
				if err := handle.Truncate(0); err != nil {
					return err
				}
				if _, err := handle.Seek(0, 0); err != nil {
					return err
				}
				if _, err := handle.WriteString("late descriptor bytes"); err != nil {
					return err
				}
				return handle.Sync()
			}

			result, err := Send(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != BatchStatusLocalCleanupDone || stagedPath == "" {
				t.Fatalf("late-descriptor cleanup result = %#v staged=%q", result, stagedPath)
			}
			if result.LocalCleanupQuarantinePath == "" || result.LocalCleanupQuarantineState != CleanupQuarantineRetainedForRecovery {
				t.Fatalf("durable cleanup quarantine missing: %#v", result)
			}
			if !containsString(result.QuarantinedLocalItems, "project/payload.txt") || !containsString(result.RemovedLocalItems, "project/payload.txt") {
				t.Fatalf("exact cleanup evidence missing: quarantined=%#v removed=%#v", result.QuarantinedLocalItems, result.RemovedLocalItems)
			}
			assertLaneFile(t, stagedPath, "late descriptor bytes")
			assertLaneFile(t, filepath.Join(result.LocalCleanupQuarantinePath, "project", "payload.txt"), "late descriptor bytes")
			if _, err := os.Lstat(payloadPath); !os.IsNotExist(err) {
				t.Fatalf("visible accepted source should be finalized into quarantine: %v", err)
			}
			assertSuccessfulSafetyArtifactRetired(t, result)
			assertCleanupBatchEvidence(t, root, batchID, result)
			plan, err := BuildTransferPlan(lanePath, root, result.Profile)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range plan.Entries {
				if strings.Contains(entry.RelativePath, "cleanup") || strings.Contains(entry.RelativePath, ".loom") {
					t.Fatalf("internal quarantine leaked into Lane plan: %#v", plan.Entries)
				}
			}
			assertNoVisibleCleanupInternals(t, lanePath)
		})
	}
}

func TestLaneCleanupStageFailureRestoresOrRecordsPartialWork(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		for _, replacement := range []bool{false, true} {
			name := "restore"
			if replacement {
				name = "replacement"
			}
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				lanePath := filepath.Join(root, DefaultLaneRelPath)
				aPath := filepath.Join(lanePath, "a.txt")
				zPath := filepath.Join(lanePath, "z.txt")
				mustLaneFile(t, aPath, "accepted a")
				mustLaneFile(t, zPath, "accepted z")

				batchID := "lane_cleanup_partial_" + string(mode) + "_" + name
				input := bundleSendTestInput(root, batchID, false)
				input.RequestedTransport = mode
				input.BeforeCleanupEntry = func(_ int, entry TransferEntry) error {
					if entry.RelativePath != "z.txt" {
						return nil
					}
					if replacement {
						if err := os.WriteFile(aPath, []byte("replacement a"), 0o644); err != nil {
							return err
						}
					}
					return os.WriteFile(zPath, []byte("changed z that must survive"), 0o644)
				}

				result, err := Send(context.Background(), input)
				if err != nil {
					t.Fatalf("accepted partial cleanup should return attention result: %v", err)
				}
				if result.Status != BatchStatusLocalCleanupWithheld {
					t.Fatalf("partial cleanup status = %#v", result)
				}
				assertLaneFile(t, zPath, "changed z that must survive")
				if replacement {
					assertLaneFile(t, aPath, "replacement a")
					assertLaneFile(t, filepath.Join(result.LocalCleanupQuarantinePath, "a.txt"), "accepted a")
					if !equalStringSlices(result.QuarantinedLocalItems, []string{"a.txt"}) || !equalStringSlices(result.RemovedLocalItems, []string{"a.txt"}) || len(result.RestoredLocalItems) != 0 {
						t.Fatalf("replacement evidence is not truthful: removed=%#v quarantined=%#v restored=%#v", result.RemovedLocalItems, result.QuarantinedLocalItems, result.RestoredLocalItems)
					}
				} else {
					assertLaneFile(t, aPath, "accepted a")
					if result.LocalCleanupQuarantinePath != "" || len(result.QuarantinedLocalItems) != 0 || len(result.RemovedLocalItems) != 0 || !equalStringSlices(result.RestoredLocalItems, []string{"a.txt"}) {
						t.Fatalf("rollback evidence is not truthful: path=%q removed=%#v quarantined=%#v restored=%#v", result.LocalCleanupQuarantinePath, result.RemovedLocalItems, result.QuarantinedLocalItems, result.RestoredLocalItems)
					}
				}
				assertCleanupBatchEvidence(t, root, batchID, result)
				assertCleanupSafetyArtifact(t, mode, result, "a.txt", "accepted a")
				assertNoVisibleCleanupInternals(t, lanePath)
			})
		}
	}
}

func TestLaneSendUnchangedCleanupStillSucceedsForBothTransports(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			lanePath := filepath.Join(root, DefaultLaneRelPath)
			mustLaneFile(t, filepath.Join(lanePath, "project", "payload.txt"), "unchanged payload")
			batchID := "lane_cleanup_unchanged_" + string(mode)
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = mode

			result, err := Send(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != BatchStatusLocalCleanupDone || result.ErrorMessage != "" {
				t.Fatalf("unchanged cleanup result = %#v", result)
			}
			if _, statErr := os.Stat(filepath.Join(lanePath, "project", "payload.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("unchanged payload was not removed from visible Lane: %v", statErr)
			}
			assertLaneFile(t, filepath.Join(result.LocalCleanupQuarantinePath, "project", "payload.txt"), "unchanged payload")
			if !equalStringSlices(result.QuarantinedLocalItems, []string{"project/payload.txt"}) || !containsString(result.RemovedLocalItems, "project/payload.txt") || !containsString(result.RemovedLocalItems, "project") {
				t.Fatalf("unchanged cleanup evidence = removed=%#v quarantined=%#v", result.RemovedLocalItems, result.QuarantinedLocalItems)
			}
			assertSuccessfulSafetyArtifactRetired(t, result)
			assertCleanupBatchEvidence(t, root, batchID, result)
			assertNoVisibleCleanupInternals(t, lanePath)
		})
	}
}

func assertCleanupSafetyArtifact(t *testing.T, mode TransportMode, result SendResult, relativePath, want string) {
	t.Helper()
	if mode == TransportModeBundleSeed {
		if _, err := VerifyBundle(result.BundleManifestPath, result.BundleArchivePath); err != nil {
			t.Fatalf("retained bundle artifact is invalid: %v", err)
		}
		return
	}
	assertLaneFile(t, filepath.Join(result.LocalSafetyPath, relativePath), want)
}

func assertSuccessfulSafetyArtifactRetired(t *testing.T, result SendResult) {
	t.Helper()
	if result.LocalSafetyCleanupState != SafetyArtifactRemovedAfterSuccess || result.LocalSafetyRemovedAt == nil || result.LocalSafetyRemovalReason != SafetyRemovalReasonMainCataloged {
		t.Fatalf("successful safety artifact audit is incomplete: %#v", result)
	}
	if _, err := os.Lstat(result.LocalSafetyPath); !os.IsNotExist(err) {
		t.Fatalf("successful safety artifact still exists at %s: %v", result.LocalSafetyPath, err)
	}
}

func assertCleanupBatchEvidence(t *testing.T, root, batchID string, result SendResult) {
	t.Helper()
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != result.Status || record.LocalCleanupQuarantinePath != result.LocalCleanupQuarantinePath || record.LocalCleanupQuarantineState != result.LocalCleanupQuarantineState || !equalStringSlices(record.QuarantinedLocalItems, result.QuarantinedLocalItems) || !equalStringSlices(record.RestoredLocalItems, result.RestoredLocalItems) || !equalStringSlices(record.RemovedLocalItems, result.RemovedLocalItems) {
		t.Fatalf("batch cleanup evidence differs: record=%#v result=%#v", record, result)
	}
	if result.Status == BatchStatusLocalCleanupWithheld && (record.AttentionStatus != AttentionStatusActive || record.ErrorMessage == "") {
		t.Fatalf("cleanup-withheld attention missing: %#v", record)
	}
}

func assertCleanupAttention(t *testing.T, root string) {
	t.Helper()
	status := BuildStatus(StatusInput{
		RootPath: root, MaxItems: 100,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if status.LastTransfer == nil || status.LastTransfer.Status != BatchStatusLocalCleanupWithheld || status.LastTransfer.AttentionStatus != AttentionStatusActive {
		t.Fatalf("cleanup attention missing from status: %#v", status.LastTransfer)
	}
	if !hasLaneDiagnostic(status.Diagnostics, "lane.local_cleanup_withheld") {
		t.Fatalf("cleanup diagnostic missing: %#v", status.Diagnostics)
	}
}

func assertLaneFile(t *testing.T, pathValue, want string) {
	t.Helper()
	payload, err := os.ReadFile(pathValue)
	if err != nil || string(payload) != want {
		t.Fatalf("file %s = %q err=%v, want %q", pathValue, payload, err, want)
	}
}

func assertNoVisibleCleanupInternals(t *testing.T, lanePath string) {
	t.Helper()
	guards, err := filepath.Glob(filepath.Join(lanePath, ".loom-cleanup-*"))
	if err != nil || len(guards) != 0 {
		t.Fatalf("cleanup internals leaked into visible Lane: guards=%#v err=%v", guards, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func errUnexpectedVisiblePath(statErr error) error {
	if statErr == nil {
		return os.ErrExist
	}
	return statErr
}
