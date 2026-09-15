package lane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLaneBundleDryRunIsReadOnlyAndAutomaticSelectionUsesCanonicalPlan(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath, "many")
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < DefaultBundleSmallFileCountAbove+1; index++ {
		pathValue := filepath.Join(lanePath, "f"+leftPadInt(index, 4))
		if err := os.WriteFile(pathValue, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Send(context.Background(), bundleSendTestInput(root, "lane_auto_dry_run", true))
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedTransport != TransportModeBundleSeed || result.RequestedTransport != TransportModeAuto || result.Transport.ReasonCode != BundleReasonManySmallFiles {
		t.Fatalf("automatic transport = %#v", result.Transport)
	}
	if result.BundleArtifactPath != "" {
		t.Fatalf("dry run created artifact evidence: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateRelPath)); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote Lane state: %v", err)
	}
	joined := bundleCommandText(result.Commands)
	if !strings.Contains(joined, "bundle create") || !strings.Contains(joined, "accept-bundle") || strings.Contains(joined, "accept-received") {
		t.Fatalf("bundle dry-run commands are not truthful:\n%s", joined)
	}
}

func TestLaneBundleSendUsesArtifactAsSafetyCopyAndTransfersOnlyBundleFiles(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, "project", "a.txt"), "alpha")
	mustLaneFile(t, filepath.Join(lanePath, "project", "b.txt"), "beta")
	input := bundleSendTestInput(root, "lane_bundle_success", false)
	input.RequestedTransport = TransportModeBundleSeed
	var commands []string
	input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, "lane staging --operation prepare") {
			return `{"source_node_key":"macbook","batch_id":"lane_bundle_success","operation":"prepare","runtime_root":"/var/lib/loom/lane","staging_path":"/var/lib/loom/lane/staging/macbook/lane_bundle_success","status":"ready","receiver_user":"loom","receiver_switch_required":true}` + "\n", "", nil
		}
		if strings.Contains(command, "accept-bundle") {
			if !strings.Contains(command, "sudo -n -u loom -- loom --json lane accept-bundle") {
				t.Fatalf("bundle accept omitted attested runtime owner: %s", command)
			}
			if _, err := os.Stat(filepath.Join(lanePath, "project", "a.txt")); err != nil {
				t.Fatalf("source removed before verified promoted/cataloged custody: %v", err)
			}
		}
		return "", "", nil
	})
	result, err := Send(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BatchStatusLocalCleanupDone || result.SelectedTransport != TransportModeBundleSeed || result.BundleResumed {
		t.Fatalf("bundle result = %#v", result)
	}
	if _, err := os.Stat(result.BundleArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("successful local bundle artifact was not retired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateRelPath, "sent", input.NewBatchID(time.Time{}))); !os.IsNotExist(err) {
		t.Fatalf("bundle mode created a full safety tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "project", "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source was not removed after published custody: %v", err)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{bundleArchiveFileName, bundleManifestFileName, "accept-bundle", "lane staging --operation cleanup --source-node macbook --batch-id lane_bundle_success"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bundle command missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"--files-from=", "accept-received", "storage export refresh", "/lane/accepted/", lanePath + string(os.PathSeparator)} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("bundle transfer used file-tree input %q:\n%s", forbidden, joined)
		}
	}
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", "lane_bundle_success.json"))
	if err != nil {
		t.Fatal(err)
	}
	if record.LocalSafetyPath != result.BundleArtifactPath || record.BundleArchiveSHA256 == "" || record.BundleCleanupState != BundleCleanupRemovedAfterSuccess || record.LocalSafetyCleanupState != SafetyArtifactRemovedAfterSuccess || record.TransferManifestPath != record.BundleManifestPath {
		t.Fatalf("bundle custody record incomplete: %#v", record)
	}
}

func TestLaneBundleTransportCleanupFailurePersistsAndRepairOnlyRetriesCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		keepLocal bool
		wantFinal string
	}{
		{name: "keep local", keepLocal: true, wantFinal: BatchStatusCataloged},
		{name: "local cleanup completed", wantFinal: BatchStatusLocalCleanupDone},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			lanePath := filepath.Join(root, DefaultLaneRelPath)
			mustLaneFile(t, filepath.Join(lanePath, "payload.txt"), "payload")
			batchID := "lane_bundle_cleanup_" + strings.ReplaceAll(test.name, " ", "_")
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = TransportModeBundleSeed
			input.KeepLocal = test.keepLocal
			input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
				cleanupCommand := remoteStagingCommand(RemoteStagingCleanup, "macbook", batchID)
				if len(args) > 0 && strings.HasPrefix(args[len(args)-1], cleanupCommand) {
					return "", "cleanup failed", errors.New("cleanup failed")
				}
				return "", "", nil
			})
			failed, err := Send(context.Background(), input)
			if err != nil {
				t.Fatalf("repairable staging cleanup returned error: %v", err)
			}
			if failed.Status != BatchStatusSourceCleanupFailed || failed.ErrorMessage == "" || failed.CompletedAt != nil {
				t.Fatalf("cleanup failure result = %#v", failed)
			}
			if test.keepLocal {
				if _, err := os.Stat(filepath.Join(lanePath, "payload.txt")); err != nil {
					t.Fatalf("keep-local payload missing: %v", err)
				}
			} else {
				if _, err := os.Stat(filepath.Join(lanePath, "payload.txt")); !os.IsNotExist(err) {
					t.Fatalf("completed local cleanup was not retained: %v", err)
				}
				if failed.LocalCleanupQuarantinePath == "" || len(failed.QuarantinedLocalItems) == 0 {
					t.Fatalf("local cleanup evidence missing: %#v", failed)
				}
			}
			status := BuildStatus(StatusInput{
				RootPath:       root,
				LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
				SSHConfigCheck: func(string) error { return nil },
			})
			if status.LastTransfer == nil || status.LastTransfer.Status != BatchStatusSourceCleanupFailed || status.LastTransfer.AttentionStatus != AttentionStatusActive || !hasLaneAction(status.LastTransfer.NextActions, "repair_main_custody") {
				t.Fatalf("cleanup failure is not visible repair attention: %#v", status.LastTransfer)
			}

			var repairCommands []string
			repairInput := PublishInput{
				RootPath: root, BatchID: batchID, MainHost: "loom-main", RemoteRoot: DefaultRemoteRoot,
				Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
					repairCommands = append(repairCommands, name+" "+strings.Join(args, " "))
					return "", "", nil
				}),
			}
			repaired, err := Publish(context.Background(), repairInput)
			if err != nil || repaired.Status != test.wantFinal {
				t.Fatalf("cleanup repair = %#v, err=%v", repaired, err)
			}
			joined := strings.Join(repairCommands, "\n")
			if !strings.Contains(joined, remoteStagingCommand(RemoteStagingCleanup, "macbook", batchID)) || strings.Contains(joined, "accept-bundle") || strings.Contains(joined, "rsync") || strings.Contains(joined, "rm -rf") {
				t.Fatalf("repair did more than transport cleanup:\n%s", joined)
			}
			record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
			if err != nil || record.Status != test.wantFinal || record.CompletedAt == nil || record.AttentionStatus != "" || record.LocalSafetyCleanupState != SafetyArtifactRemovedAfterSuccess || record.BundleCleanupState != BundleCleanupRemovedAfterSuccess {
				t.Fatalf("repaired record = %#v, err=%v", record, err)
			}
			if !test.keepLocal && (len(record.QuarantinedLocalItems) == 0 || record.LocalCleanupQuarantinePath == "" || record.LocalCleanupQuarantineExpiresAt == nil) {
				t.Fatalf("repair lost completed local cleanup evidence: %#v", record)
			}

			repairCommands = nil
			retried, err := Publish(context.Background(), repairInput)
			if err != nil || retried.Status != test.wantFinal {
				t.Fatalf("idempotent cleanup retry = %#v, err=%v", retried, err)
			}
			if strings.Contains(strings.Join(repairCommands, "\n"), "accept-bundle") {
				t.Fatalf("idempotent retry reran acceptance: %v", repairCommands)
			}
		})
	}
}

func TestLaneBundleFailureRetainsSourceAndVerifiedArtifactThenResumeReusesIt(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, "retry.txt"), "retry")
	input := bundleSendTestInput(root, "lane_bundle_retry", false)
	currentTime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	input.Now = func() time.Time { return currentTime }
	input.NewBatchID = nil
	input.RequestedTransport = TransportModeBundleSeed
	input.Runner = laneTestRunner(func(_ context.Context, name string, _ ...string) (string, string, error) {
		if strings.Contains(name, "rsync") {
			return "", "interrupted", errors.New("transfer interrupted")
		}
		return "", "", nil
	})
	failed, err := Send(context.Background(), input)
	if err == nil || failed.Status != BatchStatusFailed {
		t.Fatalf("failed bundle result=%#v err=%v", failed, err)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "retry.txt")); err != nil {
		t.Fatalf("failed transfer removed source: %v", err)
	}
	if _, err := VerifyBundle(failed.BundleManifestPath, failed.BundleArchivePath); err != nil {
		t.Fatalf("failed transfer did not retain verified artifact: %v", err)
	}
	archiveInfo, err := os.Stat(failed.BundleArchivePath)
	if err != nil {
		t.Fatal(err)
	}

	currentTime = time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
	input.Resume = true
	input.BeforeBundleEntry = func(int, TransferEntry) error { return errors.New("bundle must not be recreated") }
	input.Runner = laneTestRunner(nil)
	resumed, err := Send(context.Background(), input)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.BundleResumed || resumed.Status != BatchStatusLocalCleanupDone || resumed.BundleArchiveSHA256 != failed.BundleArchiveSHA256 || resumed.BatchID != failed.BatchID || resumed.VisibleStoragePath != failed.VisibleStoragePath {
		t.Fatalf("resume did not reuse artifact: failed=%#v resumed=%#v", failed, resumed)
	}
	if archiveInfo.Size() == 0 {
		t.Fatal("failed bundle artifact was unexpectedly empty")
	}
	if _, err := os.Stat(resumed.BundleArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("successful resumed bundle artifact was not retired: %v", err)
	}
}

func TestLaneBundleResumeReusesTransferredArtifactAfterProcessInterruption(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, "retry.txt"), "retry")
	input := bundleSendTestInput(root, "lane_bundle_interrupted", false)
	input.RequestedTransport = TransportModeBundleSeed
	input.Runner = laneTestRunner(func(_ context.Context, name string, _ ...string) (string, string, error) {
		if strings.Contains(name, "rsync") {
			return "", "interrupted", errors.New("transfer interrupted")
		}
		return "", "", nil
	})
	failed, err := Send(context.Background(), input)
	if err == nil || failed.Status != BatchStatusFailed {
		t.Fatalf("failed bundle result=%#v err=%v", failed, err)
	}
	recordPath := filepath.Join(root, DefaultStateRelPath, "batches", failed.BatchID+".json")
	record, err := readBatchRecord(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	record.Status = BatchStatusTransferred
	record.ErrorMessage = ""
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), record); err != nil {
		t.Fatal(err)
	}

	input.Resume = true
	input.BeforeBundleEntry = func(int, TransferEntry) error { return errors.New("bundle must not be recreated") }
	input.Runner = laneTestRunner(nil)
	resumed, err := Send(context.Background(), input)
	if err != nil {
		t.Fatalf("resume transferred bundle: %v", err)
	}
	if !resumed.BundleResumed || resumed.Status != BatchStatusLocalCleanupDone || resumed.BatchID != failed.BatchID || resumed.BundleArchiveSHA256 != failed.BundleArchiveSHA256 {
		t.Fatalf("transferred bundle was not resumed in place: failed=%#v resumed=%#v", failed, resumed)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "retry.txt")); !os.IsNotExist(err) {
		t.Fatalf("resumed transfer did not clean the visible source: %v", err)
	}
}

func TestLaneBundleResumeWithoutFailedArtifactRefusesToCreateReplacement(t *testing.T) {
	root := t.TempDir()
	mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
	input := bundleSendTestInput(root, "ignored_new_batch", false)
	input.RequestedTransport = TransportModeBundleSeed
	input.Resume = true
	if _, err := Send(context.Background(), input); err == nil || !strings.Contains(err.Error(), "no interrupted or failed Lane bundle") {
		t.Fatalf("resume without retained artifact = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateRelPath, "bundles")); !os.IsNotExist(err) {
		t.Fatalf("resume without artifact created bundle state: %v", err)
	}
}

func TestLaneBundlePreflightAndMutationFailuresNeverReachNetwork(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		root := t.TempDir()
		mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
		input := bundleSendTestInput(root, "lane_bundle_preflight", false)
		input.RequestedTransport = TransportModeBundleSeed
		input.LookupPath = func(string) (string, error) { return "", errors.New("missing") }
		if _, err := Send(context.Background(), input); err == nil || !strings.Contains(err.Error(), "preflight") {
			t.Fatalf("expected preflight error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, DefaultStateRelPath, "bundles")); !os.IsNotExist(err) {
			t.Fatalf("preflight wrote bundle state: %v", err)
		}
	})

	t.Run("mutation", func(t *testing.T) {
		root := t.TempDir()
		lanePath := filepath.Join(root, DefaultLaneRelPath)
		mustLaneFile(t, filepath.Join(lanePath, "a.txt"), "a")
		mustLaneFile(t, filepath.Join(lanePath, "z.txt"), "z")
		input := bundleSendTestInput(root, "lane_bundle_mutation", false)
		input.RequestedTransport = TransportModeBundleSeed
		input.BeforeBundleEntry = func(_ int, entry TransferEntry) error {
			if entry.RelativePath == "z.txt" {
				return os.WriteFile(filepath.Join(lanePath, "a.txt"), []byte("changed"), 0o644)
			}
			return nil
		}
		networkCalled := false
		input.Runner = func(context.Context, string, ...string) (string, string, error) {
			networkCalled = true
			return "", "", nil
		}
		if _, err := Send(context.Background(), input); err == nil {
			t.Fatal("source mutation did not abort send")
		}
		if networkCalled {
			t.Fatal("network called after source mutation")
		}
		if _, err := os.Stat(filepath.Join(lanePath, "a.txt")); err != nil {
			t.Fatalf("mutation failure removed source: %v", err)
		}
	})

	t.Run("mutation during transfer", func(t *testing.T) {
		root := t.TempDir()
		lanePath := filepath.Join(root, DefaultLaneRelPath)
		mustLaneFile(t, filepath.Join(lanePath, "payload.txt"), "payload")
		input := bundleSendTestInput(root, "lane_bundle_transfer_mutation", false)
		input.RequestedTransport = TransportModeBundleSeed
		accepted := false
		input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			command := name + " " + strings.Join(args, " ")
			if strings.Contains(name, "rsync") {
				if err := os.WriteFile(filepath.Join(lanePath, "payload.txt"), []byte("changed during transfer"), 0o644); err != nil {
					return "", "", err
				}
			}
			if strings.Contains(command, "accept-bundle") {
				accepted = true
			}
			return "", "", nil
		})
		if _, err := Send(context.Background(), input); err == nil || !strings.Contains(err.Error(), "changed during bundle transfer") {
			t.Fatalf("expected post-transfer mutation failure, got %v", err)
		}
		if accepted {
			t.Fatal("main acceptance ran after source mutation")
		}
		if _, err := os.Stat(filepath.Join(lanePath, "payload.txt")); err != nil {
			t.Fatalf("mutated source was removed: %v", err)
		}
	})
}

func bundleSendTestInput(root, batchID string, dryRun bool) SendInput {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	return SendInput{
		RootPath: root, SourceNodeKey: "macbook", SourceBoxID: "box-test", MainHost: "loom-main",
		DryRun: dryRun, Now: func() time.Time { return now }, NewBatchID: func(time.Time) string { return batchID },
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Runner:         laneTestRunner(nil),
		AvailableBytes: maxBundleTestSpace,
	}
}

func bundleCommandText(commands []CommandSummary) string {
	var rows []string
	for _, command := range commands {
		rows = append(rows, command.Name+" "+strings.Join(command.Args, " "))
	}
	return strings.Join(rows, "\n")
}

func hasLaneAction(actions []SafeAction, key string) bool {
	for _, action := range actions {
		if action.Key == key {
			return true
		}
	}
	return false
}

func leftPadInt(value, width int) string {
	text := "0000000000" + strconv.Itoa(value)
	return text[len(text)-width:]
}
