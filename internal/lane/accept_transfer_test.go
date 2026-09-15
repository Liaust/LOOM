package lane

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"

	"golang.org/x/sys/unix"
)

func laneTestRunner(custom CommandRunner) CommandRunner {
	return func(ctx context.Context, name string, args ...string) (string, string, error) {
		if custom != nil {
			stdout, stderr, err := custom(ctx, name, args...)
			if err != nil || stdout != "" || stderr != "" {
				return stdout, stderr, err
			}
		}
		response, ok := laneTestStagingResponse(name, args...)
		if ok {
			return response, "", nil
		}
		return "", "", nil
	}
}

func laneTestStagingResponse(name string, args ...string) (string, bool) {
	if name != "ssh" || len(args) == 0 {
		return "", false
	}
	fields := strings.Fields(args[len(args)-1])
	valueAfter := func(flag string) string {
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] == flag {
				return strings.Trim(fields[index+1], "'")
			}
		}
		return ""
	}
	operation := RemoteStagingOperation(valueAfter("--operation"))
	if operation == "" {
		return "", false
	}
	sourceNodeKey := valueAfter("--source-node")
	batchID := valueAfter("--batch-id")
	runtimeRoot := valueAfter("--expected-runtime-root")
	if runtimeRoot == "" {
		runtimeRoot = DefaultRemoteRoot
	}
	status := "ready"
	removed := ""
	if operation == RemoteStagingCleanup {
		status = "removed"
		removed = `,"removed":true`
	}
	receiver := ""
	expectedReceiver := valueAfter("--expected-receiver-user")
	if operation != RemoteStagingCleanup || expectedReceiver != "" {
		if expectedReceiver == "" {
			expectedReceiver = "loom"
		}
		receiver = fmt.Sprintf(`,"receiver_user":%q`, expectedReceiver)
	}
	return fmt.Sprintf(`{"source_node_key":%q,"batch_id":%q,"operation":%q,"runtime_root":%q,"staging_path":%q,"status":%q%s%s}`+"\n",
		sourceNodeKey, batchID, operation, runtimeRoot, remotePathJoin(runtimeRoot, "staging", sourceNodeKey, batchID), status, removed, receiver), true
}

func TestAcceptCatalogsPayloadAndSkipsAppleSidecars(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	root := filepath.Join(remoteRoot, "staging", "macbook", "lane_20260614T120000Z")
	importsRoot := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "folder", "report.md"), []byte("lane payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "._report.md"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := &fakeLaneCatalog{}
	result, err := Accept(context.Background(), catalog, AcceptInput{
		AcceptedPath:      root,
		RemoteRoot:        remoteRoot,
		TrustedRemoteRoot: true,
		ImportsRoot:       importsRoot,
		SourceNodeKey:     "macbook",
		SourceBoxID:       "box_local",
		BatchID:           "lane_20260614T120000Z",
		AcceptedDate:      "2026-06-14",
		SkipAppleDouble:   true,
	})
	if err != nil {
		t.Fatalf("Accept returned error: %v", err)
	}
	if result.FilesCataloged != 3 {
		t.Fatalf("FilesCataloged = %d, want 3", result.FilesCataloged)
	}
	if result.VisibleStoragePath != "imports/macbook/2026-06-14/lane_20260614T120000Z" {
		t.Fatalf("VisibleStoragePath = %q", result.VisibleStoragePath)
	}
	if len(catalog.inputs) != 3 {
		t.Fatalf("catalog calls = %d, want 3", len(catalog.inputs))
	}
	wantPaths := map[string]bool{".DS_Store": true, "folder/report.md": true, "node_modules/pkg/index.js": true}
	for _, got := range catalog.inputs {
		if !wantPaths[got.RelativeLanePath] {
			t.Fatalf("unexpected RelativeLanePath = %q", got.RelativeLanePath)
		}
		if got.ChecksumAlgorithm != "sha256" || got.ChecksumValue == "" {
			t.Fatalf("checksum not populated: %s %s", got.ChecksumAlgorithm, got.ChecksumValue)
		}
	}
}

func TestManageRemoteStagingConfinesPrepareResumeAndCleanup(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "lane")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	input := RemoteStagingInput{
		RuntimeRoot: runtimeRoot, SourceNodeKey: "macbook", BatchID: "lane_confined", Operation: RemoteStagingPrepare,
	}
	prepared, err := ManageRemoteStaging(input)
	if err != nil || !prepared.Created || prepared.Status != "ready" {
		t.Fatalf("prepare = %#v, %v", prepared, err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ReceiverUser != currentUser.Username || prepared.ReceiverSwitchRequired {
		t.Fatalf("prepare receiver identity = %#v, want current user %q without switch", prepared, currentUser.Username)
	}
	batchPath := filepath.Join(runtimeRoot, "staging", "macbook", "lane_confined")
	for _, path := range []string{filepath.Join(runtimeRoot, "staging"), filepath.Join(runtimeRoot, "staging", "macbook"), batchPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat shared staging path %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o770 || info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("shared staging mode for %s = %v, want setgid 0770", path, info.Mode())
		}
	}
	if err := os.WriteFile(filepath.Join(batchPath, "partial"), []byte("resume evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.Operation = RemoteStagingResume
	input.ExpectedRuntimeRoot = runtimeRoot
	resumed, err := ManageRemoteStaging(input)
	if err != nil || resumed.Created || resumed.Status != "ready" {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	if payload, err := os.ReadFile(filepath.Join(batchPath, "partial")); err != nil || string(payload) != "resume evidence" {
		t.Fatalf("resume did not preserve staged evidence: %q, %v", payload, err)
	}
	input.Operation = RemoteStagingPrepare
	reset, err := ManageRemoteStaging(input)
	if err != nil || !reset.Created {
		t.Fatalf("reset prepare = %#v, %v", reset, err)
	}
	if _, err := os.Stat(filepath.Join(batchPath, "partial")); !os.IsNotExist(err) {
		t.Fatalf("new prepare retained superseded partial payload: %v", err)
	}
	input.Operation = RemoteStagingCleanup
	removed, err := ManageRemoteStaging(input)
	if err != nil || !removed.Removed || removed.Status != "removed" {
		t.Fatalf("cleanup = %#v, %v", removed, err)
	}
	absent, err := ManageRemoteStaging(input)
	if err != nil || absent.Removed || absent.Status != "absent" {
		t.Fatalf("idempotent missing cleanup = %#v, %v", absent, err)
	}
}

func TestManageRemoteStagingRejectsConfiguredRootDriftBeforeFilesystemAccess(t *testing.T) {
	configuredRoot := filepath.Join(t.TempDir(), "configured", "lane")
	expectedRoot := filepath.Join(t.TempDir(), "persisted", "lane")
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ManageRemoteStaging(RemoteStagingInput{
		RuntimeRoot: configuredRoot, ExpectedRuntimeRoot: expectedRoot,
		SourceNodeKey: "macbook", BatchID: "lane_root_drift", Operation: RemoteStagingCleanup,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match persisted expected root") {
		t.Fatalf("configured root drift was not rejected: %v", err)
	}
	if _, statErr := os.Stat(configuredRoot); !os.IsNotExist(statErr) {
		t.Fatalf("root drift reached filesystem setup: %v", statErr)
	}
	if payload, readErr := os.ReadFile(sentinel); readErr != nil || string(payload) != "must survive" {
		t.Fatalf("root drift changed external sentinel: %q, %v", payload, readErr)
	}
}

func TestManageRemoteStagingRejectsReceiverDriftBeforeCleanup(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "lane")
	batchPath := filepath.Join(runtimeRoot, "staging", "macbook", "lane_receiver_drift")
	if err := os.MkdirAll(batchPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchPath, "sentinel"), []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ManageRemoteStaging(RemoteStagingInput{
		RuntimeRoot: runtimeRoot, ExpectedRuntimeRoot: runtimeRoot,
		ExpectedReceiverUser: "definitely-not-the-runtime-owner",
		SourceNodeKey:        "macbook", BatchID: "lane_receiver_drift", Operation: RemoteStagingCleanup,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match persisted expected receiver") {
		t.Fatalf("receiver drift was not rejected: %v", err)
	}
	if payload, readErr := os.ReadFile(filepath.Join(batchPath, "sentinel")); readErr != nil || string(payload) != "must remain" {
		t.Fatalf("receiver drift changed staged sentinel: %q, %v", payload, readErr)
	}
}

func TestInvokeRemoteStagingRequiresExactStructuredAttestation(t *testing.T) {
	root := "/var/custom/loom/lane"
	source := "macbook"
	batchID := "lane_attested"
	response := func(operation RemoteStagingOperation, status string, removed bool) string {
		removedJSON := ""
		if removed {
			removedJSON = `,"removed":true`
		}
		receiverJSON := ""
		if operation != RemoteStagingCleanup {
			receiverJSON = `,"receiver_user":"loom"`
		}
		return fmt.Sprintf(`{"source_node_key":%q,"batch_id":%q,"operation":%q,"runtime_root":%q,"staging_path":%q,"status":%q%s%s}`,
			source, batchID, operation, root, remotePathJoin(root, "staging", source, batchID), status, removedJSON, receiverJSON)
	}

	validPrepare := response(RemoteStagingPrepare, "ready", false)
	for _, test := range []struct {
		name     string
		response string
	}{
		{name: "missing receiver", response: strings.Replace(validPrepare, `,"receiver_user":"loom"`, "", 1)},
		{name: "unsafe receiver", response: strings.Replace(validPrepare, `"receiver_user":"loom"`, `"receiver_user":"loom;touch"`, 1)},
		{name: "root switch", response: strings.Replace(validPrepare, `"receiver_user":"loom"`, `"receiver_user":"root","receiver_switch_required":true`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := invokeRemoteStaging(context.Background(), func(context.Context, string, ...string) (string, string, error) {
				return test.response, "", nil
			}, DefaultMainHost, RemoteStagingPrepare, source, batchID, "")
			if err == nil || !strings.Contains(err.Error(), "receiver") {
				t.Fatalf("accepted invalid receiver response %q: %v", test.response, err)
			}
		})
	}
	for _, test := range []struct {
		name      string
		operation RemoteStagingOperation
		status    string
		removed   bool
	}{
		{name: "prepare", operation: RemoteStagingPrepare, status: "ready"},
		{name: "resume", operation: RemoteStagingResume, status: "ready"},
		{name: "cleanup removed", operation: RemoteStagingCleanup, status: "removed", removed: true},
		{name: "cleanup already absent", operation: RemoteStagingCleanup, status: "absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := invokeRemoteStaging(context.Background(), func(context.Context, string, ...string) (string, string, error) {
				return response(test.operation, test.status, test.removed), "", nil
			}, DefaultMainHost, test.operation, source, batchID, root)
			if err != nil || got.RuntimeRoot != root || got.StagingPath != remotePathJoin(root, "staging", source, batchID) || got.Status != test.status {
				t.Fatalf("attested %s = %#v, %v", test.operation, got, err)
			}
		})
	}

	validCleanup := response(RemoteStagingCleanup, "removed", true)
	for _, test := range []struct {
		name     string
		response string
	}{
		{name: "empty"},
		{name: "malformed", response: "{"},
		{name: "wrong source", response: strings.Replace(validCleanup, `"source_node_key":"macbook"`, `"source_node_key":"other"`, 1)},
		{name: "wrong batch", response: strings.Replace(validCleanup, `"batch_id":"lane_attested"`, `"batch_id":"other"`, 1)},
		{name: "wrong operation", response: strings.Replace(validCleanup, `"operation":"cleanup"`, `"operation":"resume"`, 1)},
		{name: "wrong root", response: strings.Replace(validCleanup, root, "/var/custom/loom/other", 1)},
		{name: "wrong path", response: strings.Replace(validCleanup, remotePathJoin(root, "staging", source, batchID), remotePathJoin(root, "staging", source, "other"), 1)},
		{name: "nonterminal status", response: strings.Replace(validCleanup, `"status":"removed"`, `"status":"ready"`, 1)},
		{name: "removed without evidence", response: strings.Replace(validCleanup, `,"removed":true`, "", 1)},
		{name: "absent with removal evidence", response: strings.Replace(validCleanup, `"status":"removed"`, `"status":"absent"`, 1)},
		{name: "cleanup with creation evidence", response: strings.Replace(validCleanup, `,"removed":true`, `,"removed":true,"created":true`, 1)},
		{name: "oversized", response: validCleanup + strings.Repeat(" ", DefaultCommandOutputLimit)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, output, err := invokeRemoteStaging(context.Background(), func(context.Context, string, ...string) (string, string, error) {
				return test.response, "", nil
			}, DefaultMainHost, RemoteStagingCleanup, source, batchID, root)
			if err == nil {
				t.Fatalf("accepted invalid staging response: %s", test.response)
			}
			if test.name == "oversized" && (!output.StdoutTruncated || output.StdoutBytes != int64(len(test.response))) {
				t.Fatalf("oversized staging response lost bounded evidence: %#v", output)
			}
		})
	}
}

func TestPublishRootAttestationFailurePreservesLocalPayloadAndAttention(t *testing.T) {
	root := t.TempDir()
	payloadPath := filepath.Join(root, DefaultLaneRelPath, "payload.txt")
	mustLaneFile(t, payloadPath, "must remain local")
	batchID := "lane_cleanup_root_drift"
	persistedRoot := "/var/custom/loom/lane-old"
	currentRoot := "/var/custom/loom/lane-new"
	started := time.Now().UTC().Add(-time.Minute)
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusSourceCleanupFailed,
		SourceNodeKey: "macbook", RemoteRuntimeRoot: persistedRoot,
		RemoteStagingPath: remotePathJoin(persistedRoot, "staging", "macbook", batchID),
		SelectedTransport: TransportModeFileTree, LocalCleanupIntent: LocalCleanupIntentQuarantine,
		Profile: filepolicy.ProfileFaithful, StartedAt: &started,
	}); err != nil {
		t.Fatal(err)
	}
	var command string
	result, err := Publish(context.Background(), PublishInput{
		RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
		Runner: func(_ context.Context, name string, args ...string) (string, string, error) {
			command = name + " " + strings.Join(args, " ")
			return fmt.Sprintf(`{"source_node_key":"macbook","batch_id":%q,"operation":"cleanup","runtime_root":%q,"staging_path":%q,"status":"absent"}`,
				batchID, currentRoot, remotePathJoin(currentRoot, "staging", "macbook", batchID)), "", nil
		},
	})
	if err == nil || result.Status != BatchStatusSourceCleanupFailed || !strings.Contains(err.Error(), "does not match persisted expected root") {
		t.Fatalf("root drift repair = %#v, %v", result, err)
	}
	if !strings.Contains(command, "--expected-runtime-root "+persistedRoot) {
		t.Fatalf("cleanup command omitted persisted root attestation: %s", command)
	}
	assertLaneFile(t, payloadPath, "must remain local")
	record, readErr := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
	if readErr != nil || record.Status != BatchStatusSourceCleanupFailed || record.AttentionStatus != AttentionStatusActive || laneRecordHasCompletedLocalCleanup(record) {
		t.Fatalf("root drift cleared attention or fabricated cleanup: %#v, %v", record, readErr)
	}
}

func TestManageRemoteStagingRejectsSymlinksAndNonDirectoriesWithoutChangingExternalData(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, runtimeRoot, sourceParent, batchPath, external string)
	}{
		{
			name: "final symlink",
			setup: func(t *testing.T, _, sourceParent, batchPath, external string) {
				if err := os.MkdirAll(sourceParent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, batchPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "parent symlink",
			setup: func(t *testing.T, runtimeRoot, sourceParent, _, external string) {
				if err := os.MkdirAll(filepath.Join(runtimeRoot, "staging"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, sourceParent); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink collision",
			setup: func(t *testing.T, _, sourceParent, batchPath, external string) {
				if err := os.MkdirAll(sourceParent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(external, "sentinel"), batchPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "non-directory parent",
			setup: func(t *testing.T, runtimeRoot, sourceParent, _, _ string) {
				if err := os.MkdirAll(filepath.Join(runtimeRoot, "staging"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sourceParent, []byte("collision"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeRoot := filepath.Join(t.TempDir(), "lane")
			external := filepath.Join(t.TempDir(), "external")
			if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(external, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(external, "sentinel")
			if err := os.WriteFile(sentinel, []byte("must survive"), 0o600); err != nil {
				t.Fatal(err)
			}
			sourceParent := filepath.Join(runtimeRoot, "staging", "macbook")
			batchPath := filepath.Join(sourceParent, "lane_hostile")
			test.setup(t, runtimeRoot, sourceParent, batchPath, external)

			for _, operation := range []RemoteStagingOperation{RemoteStagingResume, RemoteStagingPrepare, RemoteStagingCleanup} {
				_, err := ManageRemoteStaging(RemoteStagingInput{
					RuntimeRoot: runtimeRoot, ExpectedRuntimeRoot: runtimeRoot, SourceNodeKey: "macbook", BatchID: "lane_hostile", Operation: operation,
				})
				if err == nil {
					t.Fatalf("%s accepted hostile staging layout", operation)
				}
				payload, readErr := os.ReadFile(sentinel)
				if readErr != nil || string(payload) != "must survive" {
					t.Fatalf("%s changed external sentinel: %q, %v", operation, payload, readErr)
				}
			}
		})
	}
}

func TestSendConsumesMainResolvedCustomRuntimeStagingRoot(t *testing.T) {
	root := t.TempDir()
	mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
	customRuntimeRoot := "/var/custom/loom/lane"
	batchID := "lane_custom_runtime_root"
	customStagingPath := remotePathJoin(customRuntimeRoot, "staging", "macbook", batchID)
	var commands []string
	result, err := Send(context.Background(), SendInput{
		RootPath: root, SourceNodeKey: "macbook", KeepLocal: true, MainHost: DefaultMainHost,
		NewBatchID:     func(time.Time) string { return batchID },
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			command := name + " " + strings.Join(args, " ")
			commands = append(commands, command)
			if strings.Contains(command, "lane staging --operation prepare") {
				return fmt.Sprintf(`{"source_node_key":"macbook","batch_id":%q,"operation":"prepare","runtime_root":%q,"staging_path":%q,"status":"ready","receiver_user":"loom"}`+"\n", batchID, customRuntimeRoot, customStagingPath), "", nil
			}
			return "", "", nil
		}),
	})
	if err != nil || result.Status != BatchStatusCataloged || result.RemoteRuntimeRoot != customRuntimeRoot || result.RemoteStagingPath != customStagingPath {
		t.Fatalf("custom runtime staging send = %#v, %v", result, err)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, customStagingPath) || strings.Contains(joined, DefaultRemoteRoot+"/staging/macbook/"+batchID) {
		t.Fatalf("send did not use main-resolved staging path:\n%s", joined)
	}
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
	if err != nil || record.RemoteRuntimeRoot != customRuntimeRoot || record.RemoteStagingPath != customStagingPath {
		t.Fatalf("custom runtime staging record = %#v, %v", record, err)
	}
	record.Status = BatchStatusSourceCleanupFailed
	record.CompletedAt = nil
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), record); err != nil {
		t.Fatal(err)
	}
	var repairCommands []string
	repaired, err := Publish(context.Background(), PublishInput{
		RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			repairCommands = append(repairCommands, name+" "+strings.Join(args, " "))
			return "", "", nil
		}),
	})
	if err != nil || repaired.Status != BatchStatusCataloged {
		t.Fatalf("custom runtime staging repair = %#v, %v", repaired, err)
	}
	if joined := strings.Join(repairCommands, "\n"); !strings.Contains(joined, "--expected-runtime-root "+customRuntimeRoot) {
		t.Fatalf("custom-root repair did not attest exact cleanup identity:\n%s", joined)
	}
}

func TestCrossDevicePromotionAuthorizationPersistsAcrossRepairForBothTransports(t *testing.T) {
	for _, transport := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(transport), func(t *testing.T) {
			root := t.TempDir()
			batchID := "lane_cross_device_repair_" + string(transport)
			mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = transport
			input.KeepLocal = true
			input.AllowCrossDevicePromotion = true
			input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
				command := name + " " + strings.Join(args, " ")
				if strings.Contains(command, "lane accept-received") || strings.Contains(command, "lane accept-bundle") {
					if !strings.Contains(command, "--allow-cross-device-promotion") {
						t.Fatalf("initial acceptance lost explicit authorization: %s", command)
					}
					return "", `{"error":{"code":"storage.lane_promotion_failed"}}`, errors.New("copy promotion interrupted")
				}
				return "", "", nil
			})
			failed, err := Send(context.Background(), input)
			if err == nil || failed.Status != BatchStatusPromotionFailed {
				t.Fatalf("interrupted cross-device promotion = %#v, %v", failed, err)
			}
			recordPath := filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json")
			record, err := readBatchRecord(recordPath)
			if err != nil || !record.AllowCrossDevicePromotion {
				t.Fatalf("durable cross-device decision = %#v, %v", record, err)
			}
			status := BuildStatus(StatusInput{
				RootPath: root, LookupPath: func(name string) (string, error) { return "/usr/bin/" + name, nil }, SSHConfigCheck: func(string) error { return nil },
			})
			if status.LastTransfer == nil {
				t.Fatal("missing interrupted transfer status")
			}
			repairAction, ok := laneActionByKey(status.LastTransfer.NextActions, "repair_main_custody")
			if !ok || repairAction.Command != "loom lane repair "+batchID || !strings.Contains(repairAction.Description, "durably recorded explicit cross-filesystem") {
				t.Fatalf("repair action does not carry the durable same-batch contract: %#v", status.LastTransfer.NextActions)
			}

			var repairCommands []string
			repairRunner := laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
				repairCommands = append(repairCommands, name+" "+strings.Join(args, " "))
				return "", "", nil
			})
			repaired, err := Publish(context.Background(), PublishInput{
				RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot, Runner: repairRunner,
			})
			if err != nil || repaired.Status != BatchStatusCataloged {
				t.Fatalf("authorized repair = %#v, %v", repaired, err)
			}
			joined := strings.Join(repairCommands, "\n")
			if !strings.Contains(joined, "--allow-cross-device-promotion") {
				t.Fatalf("repair acceptance lost durable authorization:\n%s", joined)
			}
			if !strings.Contains(joined, remoteStagingCommand(RemoteStagingCleanup, "macbook", batchID)) {
				t.Fatalf("repair did not complete transport-owner staging cleanup:\n%s", joined)
			}
			repairCommands = nil
			retried, err := Publish(context.Background(), PublishInput{
				RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot, Runner: repairRunner,
			})
			if err != nil || retried.Status != BatchStatusCataloged || strings.Contains(strings.Join(repairCommands, "\n"), "accept-") {
				t.Fatalf("idempotent repeated repair = %#v, %v, commands=%v", retried, err, repairCommands)
			}
		})
	}
}

func TestCrossDevicePromotionAuthorizationPersistsAcrossResumeAndLegacyRecordsFailSafe(t *testing.T) {
	for _, transport := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(transport), func(t *testing.T) {
			root := t.TempDir()
			batchID := "lane_cross_device_resume_" + string(transport)
			mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = transport
			input.KeepLocal = true
			input.AllowCrossDevicePromotion = true
			input.Runner = laneTestRunner(func(_ context.Context, name string, _ ...string) (string, string, error) {
				if strings.Contains(name, "rsync") {
					return "", "interrupted", errors.New("transfer interrupted")
				}
				return "", "", nil
			})
			failed, err := Send(context.Background(), input)
			if err == nil || failed.Status != BatchStatusFailed {
				t.Fatalf("interrupted transfer = %#v, %v", failed, err)
			}
			status := BuildStatus(StatusInput{RootPath: root, LookupPath: func(name string) (string, error) { return "/usr/bin/" + name, nil }, SSHConfigCheck: func(string) error { return nil }})
			resumeKey := "resume_file_tree"
			if transport == TransportModeBundleSeed {
				resumeKey = "resume_bundle"
			}
			resumeAction, ok := laneActionByKey(status.LastTransfer.NextActions, resumeKey)
			if !ok || !strings.Contains(resumeAction.Description, "durably recorded explicit cross-filesystem") {
				t.Fatalf("resume action does not carry durable authorization: %#v", status.LastTransfer)
			}

			input.Resume = true
			input.AllowCrossDevicePromotion = false
			var commands []string
			input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
				commands = append(commands, name+" "+strings.Join(args, " "))
				return "", "", nil
			})
			resumed, err := Send(context.Background(), input)
			joined := strings.Join(commands, "\n")
			if err != nil || resumed.Status != BatchStatusCataloged || !strings.Contains(joined, "--allow-cross-device-promotion") || !strings.Contains(joined, remoteStagingCommand(RemoteStagingCleanup, "macbook", batchID)) {
				t.Fatalf("authorized resume = %#v, %v, commands=%v", resumed, err, commands)
			}

			unauthorizedRoot := t.TempDir()
			unauthorizedBatchID := "lane_unauthorized_resume_" + string(transport)
			mustLaneFile(t, filepath.Join(unauthorizedRoot, DefaultLaneRelPath, "payload.txt"), "payload")
			unauthorized := bundleSendTestInput(unauthorizedRoot, unauthorizedBatchID, false)
			unauthorized.RequestedTransport = transport
			unauthorized.KeepLocal = true
			unauthorized.Runner = laneTestRunner(func(_ context.Context, name string, _ ...string) (string, string, error) {
				if strings.Contains(name, "rsync") {
					return "", "interrupted", errors.New("transfer interrupted")
				}
				return "", "", nil
			})
			if result, err := Send(context.Background(), unauthorized); err == nil || result.Status != BatchStatusFailed {
				t.Fatalf("seed unauthorized transfer = %#v, %v", result, err)
			}
			unauthorized.Resume = true
			unauthorized.AllowCrossDevicePromotion = true
			if result, err := Send(context.Background(), unauthorized); err == nil || !strings.Contains(err.Error(), "was not originally authorized") || result.BatchID != "" {
				t.Fatalf("same-batch resume silently elevated authorization: %#v, %v", result, err)
			}

			legacyRoot := t.TempDir()
			legacyBatchID := "lane_legacy_cross_device_" + string(transport)
			started := time.Now().UTC().Add(-time.Minute)
			if err := writeBatchRecord(filepath.Join(legacyRoot, DefaultStateRelPath), BatchRecord{
				SchemaVersion: BatchSchemaVersion, BatchID: legacyBatchID, Status: BatchStatusPromotionFailed,
				SourceNodeKey: "macbook", RemoteRuntimeRoot: DefaultRemoteRoot,
				RemoteStagingPath: remotePathJoin(DefaultRemoteRoot, "staging", "macbook", legacyBatchID),
				SelectedTransport: transport, LocalCleanupIntent: LocalCleanupIntentKeep, StartedAt: &started,
			}); err != nil {
				t.Fatal(err)
			}
			legacyRecordPath := filepath.Join(legacyRoot, DefaultStateRelPath, "batches", legacyBatchID+".json")
			if payload, err := os.ReadFile(legacyRecordPath); err != nil || strings.Contains(string(payload), "allow_cross_device_promotion") {
				t.Fatalf("legacy fixture unexpectedly carried new authorization field: %s, %v", payload, err)
			}
			var legacyCommand string
			legacyResult, legacyErr := Publish(context.Background(), PublishInput{
				RootPath: legacyRoot, BatchID: legacyBatchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
				Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
					command := name + " " + strings.Join(args, " ")
					if strings.Contains(command, "accept-") {
						legacyCommand = command
						return "", `{"error":{"code":"storage.lane_promotion_failed"}}`, errors.New("cross-device promotion remains unauthorized")
					}
					return "", "", nil
				}),
			})
			if legacyErr == nil || legacyResult.Status != BatchStatusPromotionFailed || strings.Contains(legacyCommand, "--allow-cross-device-promotion") {
				t.Fatalf("legacy missing-field record did not fail safe: %#v, %v, command=%s", legacyResult, legacyErr, legacyCommand)
			}
		})
	}
}

func laneActionByKey(actions []SafeAction, key string) (SafeAction, bool) {
	for _, action := range actions {
		if action.Key == key {
			return action, true
		}
	}
	return SafeAction{}, false
}

func TestAcceptPreservesSourceMtimeWithoutDestinationBirthtime(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	root := filepath.Join(remoteRoot, "staging", "macbook", "lane_old_source_time")
	importsRoot := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(root, "old.md")
	if err := os.WriteFile(payloadPath, []byte("old source"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceMtime := time.Date(2018, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(payloadPath, sourceMtime, sourceMtime); err != nil {
		t.Fatal(err)
	}

	destinationObservation, err := filesystemmeta.DetectPath(payloadPath, filesystemmeta.DetectOptions{RootPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if destinationObservation.SourceModifiedAt == nil || !destinationObservation.SourceModifiedAt.Equal(sourceMtime) {
		t.Fatalf("destination source mtime = %v, want %s", destinationObservation.SourceModifiedAt, sourceMtime)
	}
	catalog := &fakeLaneCatalog{}
	_, err = Accept(context.Background(), catalog, AcceptInput{
		AcceptedPath: root, RemoteRoot: remoteRoot, ImportsRoot: importsRoot, SourceNodeKey: "macbook",
		SourceBoxID: "box_local", BatchID: "lane_old_source_time", AcceptedDate: "2026-06-14",
	})
	if err != nil {
		t.Fatalf("Accept returned error: %v", err)
	}
	if len(catalog.inputs) != 1 || catalog.inputs[0].FilesystemObservation == nil {
		t.Fatalf("catalog inputs = %#v", catalog.inputs)
	}
	observation := catalog.inputs[0].FilesystemObservation
	if observation.SourceModifiedAt == nil || !observation.SourceModifiedAt.Equal(sourceMtime) ||
		observation.SourceModifiedBasis != filesystemmeta.SourceTimeBasisFilesystemMtime {
		t.Fatalf("catalog source mtime = %#v, want %s", observation, sourceMtime)
	}
	if observation.SourceCreatedAt != nil || observation.SourceCreatedBasis != "" {
		t.Fatalf("catalog source creation = %v/%q, want unset destination birth time", observation.SourceCreatedAt, observation.SourceCreatedBasis)
	}
}

func TestSanitizeAcceptedLaneObservationDropsDestinationBirthtime(t *testing.T) {
	sourceMtime := time.Date(2018, 1, 2, 3, 4, 5, 0, time.UTC)
	destinationBirthtime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	got := sanitizeAcceptedLaneObservation(filesystemmeta.Observation{
		SourceModifiedAt:    &sourceMtime,
		SourceModifiedBasis: filesystemmeta.SourceTimeBasisFilesystemMtime,
		SourceCreatedAt:     &destinationBirthtime,
		SourceCreatedBasis:  filesystemmeta.SourceTimeBasisFilesystemBirthtime,
	})
	if got.SourceModifiedAt == nil || !got.SourceModifiedAt.Equal(sourceMtime) ||
		got.SourceModifiedBasis != filesystemmeta.SourceTimeBasisFilesystemMtime {
		t.Fatalf("source mtime changed during sanitization: %#v", got)
	}
	if got.SourceCreatedAt != nil || got.SourceCreatedBasis != "" {
		t.Fatalf("destination birth time survived sanitization: %#v", got)
	}
}

func TestAcceptRecordsEmptyDirectoryObservation(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	root := filepath.Join(remoteRoot, "staging", "macbook", "lane_20260614T120000Z")
	importsRoot := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(filepath.Join(root, "dataset", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	catalog := &fakeLaneCatalog{}
	result, err := Accept(context.Background(), catalog, AcceptInput{
		AcceptedPath:  root,
		RemoteRoot:    remoteRoot,
		ImportsRoot:   importsRoot,
		SourceNodeKey: "macbook",
		SourceBoxID:   "box_local",
		BatchID:       "lane_20260614T120000Z",
		AcceptedDate:  "2026-06-14",
	})
	if err != nil {
		t.Fatalf("Accept returned error: %v", err)
	}
	if result.FilesCataloged != 0 || result.DirectoriesObserved == 0 || result.ObservationsRecorded == 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !fakeLaneObservationExists(catalog.observations, "dataset/empty", filesystemmeta.ObjectKindDirectory) {
		t.Fatalf("empty directory observation missing: %#v", catalog.observations)
	}
}

func TestAcceptObservesSymlinkWithoutCatalogingPayload(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	root := filepath.Join(remoteRoot, "staging", "macbook", "lane_20260614T120000Z")
	importsRoot := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	catalog := &fakeLaneCatalog{}
	result, err := Accept(context.Background(), catalog, AcceptInput{
		AcceptedPath:  root,
		RemoteRoot:    remoteRoot,
		ImportsRoot:   importsRoot,
		SourceNodeKey: "macbook",
		SourceBoxID:   "box_local",
		BatchID:       "lane_20260614T120000Z",
		AcceptedDate:  "2026-06-14",
	})
	if err != nil {
		t.Fatalf("Accept returned error: %v", err)
	}
	if result.FilesCataloged != 0 || len(catalog.inputs) != 0 {
		t.Fatalf("symlink should not be cataloged as payload: result=%#v inputs=%#v", result, catalog.inputs)
	}
	if !fakeLaneObservationExists(catalog.observations, "link", filesystemmeta.ObjectKindSymlink) {
		t.Fatalf("symlink observation missing: %#v", catalog.observations)
	}
}

func TestLaneCustodyHashCannotFollowSymlinkOutsideCanonicalRoot(t *testing.T) {
	rootPath := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "payload.txt")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := fileSHA256Root(root, "payload.txt"); err == nil {
		t.Fatal("custody hashing followed a symlink outside the canonical batch")
	}
}

func TestAcceptRejectsPathOutsideLaneAcceptedRoot(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath:  outside,
		RemoteRoot:    remoteRoot,
		ImportsRoot:   filepath.Join(t.TempDir(), "imports"),
		SourceNodeKey: "macbook",
		BatchID:       "lane_20260614T120000Z",
		AcceptedDate:  "2026-06-14",
	})
	if err == nil || !strings.Contains(err.Error(), "accepted_path must be the current Lane staging batch") {
		t.Fatalf("expected accepted root containment error, got %v", err)
	}
}

func TestAcceptPromotesStagingIntoCustomImportsAndRetriesCommittedBatch(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_custom_imports"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(filepath.Join(staging, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "folder", "payload.txt"), []byte("canonical payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	importsRoot := filepath.Join(t.TempDir(), "custom-storage", "imports")
	input := AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
	}
	catalog := &fakeLaneCatalog{}
	first, err := Accept(context.Background(), catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PromotionMethod != "atomic_rename" || first.PromotionIdempotent || first.StagingCleanupState != "moved_atomically" {
		t.Fatalf("first promotion = %#v", first)
	}
	if !strings.HasSuffix(first.AcceptedPath, filepath.Join("custom-storage", "imports", "macbook", "2026-08-27", batchID)) {
		t.Fatalf("canonical imports path = %q", first.AcceptedPath)
	}
	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatalf("same-device staging survived atomic promotion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.AcceptedPath, "folder", "payload.txt")); err != nil {
		t.Fatalf("canonical payload missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.AcceptedPath, laneCustodyManifestName)); err != nil {
		t.Fatalf("committed custody manifest missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(remoteRoot, "accepted")); !os.IsNotExist(err) {
		t.Fatalf("new acceptance recreated legacy accepted tree: %v", err)
	}

	second, err := Accept(context.Background(), &fakeLaneCatalog{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.PromotionIdempotent || second.PromotionMethod != "existing_committed" || second.AcceptedPath != first.AcceptedPath {
		t.Fatalf("committed retry = %#v", second)
	}
	if err := os.WriteFile(filepath.Join(first.AcceptedPath, "unexpected.txt"), []byte("changed evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Accept(context.Background(), &fakeLaneCatalog{}, input); err == nil || !strings.Contains(err.Error(), "no longer matches committed manifest") {
		t.Fatalf("mutated committed custody was accepted: %v", err)
	}
}

func TestAcceptCrossDevicePromotionIsExplicitSpaceCheckedAndDefersTransportCleanup(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_cross_device"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload.txt"), []byte("cross device"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: filepath.Join(t.TempDir(), "imports"),
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
		DeviceID: func(pathValue string) (uint64, error) {
			if strings.Contains(filepath.ToSlash(pathValue), "/staging/") {
				return 1, nil
			}
			return 2, nil
		},
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
	}
	if _, err := Accept(context.Background(), &fakeLaneCatalog{}, input); err == nil || !strings.Contains(err.Error(), "explicit cross-device promotion") {
		t.Fatalf("implicit cross-device promotion was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "payload.txt")); err != nil {
		t.Fatalf("rejected promotion mutated staging: %v", err)
	}

	input.AllowCrossDevicePromotion = true
	input.AvailableBytes = func(string) (int64, error) { return 1, nil }
	if _, err := Accept(context.Background(), &fakeLaneCatalog{}, input); err == nil || !strings.Contains(err.Error(), "insufficient imports free space") {
		t.Fatalf("insufficient-space promotion was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "payload.txt")); err != nil {
		t.Fatalf("space failure mutated staging: %v", err)
	}

	input.AvailableBytes = func(string) (int64, error) { return math.MaxInt64, nil }
	failingCatalog := &fakeLaneCatalog{failAt: 1, failErr: errors.New("catalog unavailable")}
	partial, err := Accept(context.Background(), failingCatalog, input)
	var phaseErr *AcceptPhaseError
	if err == nil || !errors.As(err, &phaseErr) || phaseErr.Phase != "catalog" {
		t.Fatalf("catalog failure phase = %#v err=%v", partial, err)
	}
	if partial.PromotionMethod != "copy_verify_rename" || partial.StagingCleanupState != "retained_until_catalog_success" {
		t.Fatalf("cross-device partial result = %#v", partial)
	}
	if _, err := os.Stat(filepath.Join(staging, "payload.txt")); err != nil {
		t.Fatalf("catalog failure removed rollback staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(partial.AcceptedPath, "payload.txt")); err != nil {
		t.Fatalf("catalog failure lost promoted custody: %v", err)
	}

	retried, err := Accept(context.Background(), &fakeLaneCatalog{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !retried.PromotionIdempotent || retried.PromotionMethod != "existing_committed" || retried.StagingCleanupState != "retained_for_transport_cleanup" {
		t.Fatalf("cross-device retry = %#v", retried)
	}
	if _, err := os.Lstat(staging); err != nil {
		t.Fatalf("transport-owner staging was not retained after catalog success: %v", err)
	}
	cleanup, err := ManageRemoteStaging(RemoteStagingInput{
		RuntimeRoot: remoteRoot, ExpectedRuntimeRoot: remoteRoot,
		SourceNodeKey: "macbook", BatchID: batchID, Operation: RemoteStagingCleanup,
	})
	if err != nil || !cleanup.Removed {
		t.Fatalf("transport-owner staging cleanup = %#v, %v", cleanup, err)
	}
	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging survived transport-owner cleanup: %v", err)
	}
}

func TestAcceptCrossDevicePromotionPreservesModesMaskedByProcessUmask(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_cross_device_modes"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	directory := filepath.Join(staging, "shared")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(directory, "payload.txt")
	if err := os.WriteFile(payload, []byte("cross-device mode fidelity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(payload, 0o660); err != nil {
		t.Fatal(err)
	}
	importsRoot := filepath.Join(t.TempDir(), "imports")
	result, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
		AllowCrossDevicePromotion: true,
		DeviceID: func(pathValue string) (uint64, error) {
			if strings.Contains(filepath.ToSlash(pathValue), "/staging/") {
				return 1, nil
			}
			return 2, nil
		},
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PromotionMethod != "copy_verify_rename" {
		t.Fatalf("promotion method = %q", result.PromotionMethod)
	}
	for pathValue, want := range map[string]os.FileMode{
		filepath.Join(result.AcceptedPath, "shared"):                0o770,
		filepath.Join(result.AcceptedPath, "shared", "payload.txt"): 0o660,
	} {
		info, err := os.Stat(pathValue)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s = %04o, want %04o", pathValue, got, want)
		}
	}
}

func TestAcceptCrossDevicePromotionPreservesSymlinkTimestampWithoutFollowing(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_cross_device_symlink_mtime"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(staging, "payload-link")
	if err := os.Symlink("payload.txt", linkPath); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Date(2026, 8, 27, 12, 34, 56, 123456789, time.UTC)
	timestamp := unix.NsecToTimespec(wantTime.UnixNano())
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, linkPath, []unix.Timespec{timestamp, timestamp}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	importsRoot := filepath.Join(t.TempDir(), "imports")
	result, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
		AllowCrossDevicePromotion: true,
		DeviceID: func(pathValue string) (uint64, error) {
			if strings.Contains(filepath.ToSlash(pathValue), "/staging/") {
				return 1, nil
			}
			return 2, nil
		},
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Lstat(filepath.Join(result.AcceptedPath, "payload-link"))
	if err != nil {
		t.Fatal(err)
	}
	if !gotInfo.ModTime().Equal(wantTime) {
		t.Fatalf("promoted symlink mtime = %s, want %s", gotInfo.ModTime(), wantTime)
	}
	if target, err := os.Readlink(filepath.Join(result.AcceptedPath, "payload-link")); err != nil || target != "payload.txt" {
		t.Fatalf("promoted symlink target = %q, %v", target, err)
	}
}

func TestAcceptCrossDeviceCopyFailureRollsBackTemporaryDestination(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_copy_interrupted"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(filepath.Join(staging, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "folder", "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	importsRoot := filepath.Join(t.TempDir(), "imports")
	_, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
		AllowCrossDevicePromotion: true,
		DeviceID: func(pathValue string) (uint64, error) {
			if strings.Contains(filepath.ToSlash(pathValue), "/staging/") {
				return 1, nil
			}
			return 2, nil
		},
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
		BeforeCopyEntry: func(index int, _ string) error {
			if index == 1 {
				return errors.New("copy interrupted")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "copy interrupted") {
		t.Fatalf("interrupted copy error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "folder", "payload.txt")); err != nil {
		t.Fatalf("interrupted copy mutated staging: %v", err)
	}
	dateRoot := filepath.Join(importsRoot, "macbook", "2026-08-27")
	entries, readErr := os.ReadDir(dateRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("interrupted copy left canonical imports artifacts: %#v", entries)
	}
}

func TestAcceptRecoversUnlockedCrashLockAndAbandonedPromotionTree(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_crash_recovery"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleManifestTemp := filepath.Join(filepath.Dir(staging), laneCustodyManifestTempPrefix(staging)+"crashed")
	if err := os.WriteFile(staleManifestTemp, []byte("partial manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	payloadTempName := "." + laneCustodyManifestName + ".tmp-user-payload"
	if err := os.WriteFile(filepath.Join(staging, payloadTempName), []byte("user payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	importsRoot := filepath.Join(t.TempDir(), "imports")
	parent := filepath.Join(importsRoot, "macbook", "2026-08-27")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	lockParent := filepath.Join(remoteRoot, "promotion-locks", "macbook", "2026-08-27")
	if err := os.MkdirAll(lockParent, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockParent, batchID+".lock")
	if err := os.WriteFile(lockPath, []byte("crashed-owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	abandoned := filepath.Join(parent, "."+batchID+".promoting-abandoned")
	if err := os.MkdirAll(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, "partial"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
	})
	if err != nil {
		t.Fatalf("retry after crashed owner: %v", err)
	}
	if result.PromotionMethod != "atomic_rename" {
		t.Fatalf("promotion result = %#v", result)
	}
	if _, err := os.Lstat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned promotion tree survived retry: %v", err)
	}
	if _, err := os.Lstat(staleManifestTemp); !os.IsNotExist(err) {
		t.Fatalf("abandoned custody manifest temp survived retry: %v", err)
	}
	promotedPayload, err := os.ReadFile(filepath.Join(importsRoot, "macbook", "2026-08-27", batchID, payloadTempName))
	if err != nil || string(promotedPayload) != "user payload" {
		t.Fatalf("manifest-like user payload was not preserved: payload=%q err=%v", promotedPayload, err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("persistent advisory lock anchor missing: %v", err)
	}
}

func TestAcceptPreservesFormerCustodyTempNamespacePayloads(t *testing.T) {
	for _, test := range []struct {
		name        string
		crossDevice bool
	}{
		{name: "same device"},
		{name: "copy verify", crossDevice: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
			withLaneRemoteBoundary(t, remoteRoot)
			batchID := "lane_manifest_payload_" + strings.ReplaceAll(test.name, " ", "_")
			staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
			prefix := "." + laneCustodyManifestName + ".tmp-"
			fileName := prefix + "payload-file"
			dirName := prefix + "payload-dir"
			if err := os.MkdirAll(filepath.Join(staging, dirName), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staging, fileName), []byte("file payload"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staging, dirName, "nested.txt"), []byte("nested payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			importsRoot := filepath.Join(t.TempDir(), "imports")
			input := AcceptInput{
				AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
				SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
				AllowCrossDevicePromotion: test.crossDevice,
				AvailableBytes:            func(string) (int64, error) { return math.MaxInt64, nil },
			}
			if test.crossDevice {
				input.DeviceID = func(pathValue string) (uint64, error) {
					if strings.Contains(filepath.ToSlash(pathValue), "/staging/") {
						return 1, nil
					}
					return 2, nil
				}
			}
			result, err := Accept(context.Background(), &fakeLaneCatalog{}, input)
			if err != nil {
				t.Fatal(err)
			}
			wantMethod := "atomic_rename"
			if test.crossDevice {
				wantMethod = "copy_verify_rename"
			}
			if result.PromotionMethod != wantMethod {
				t.Fatalf("promotion method = %q, want %q", result.PromotionMethod, wantMethod)
			}
			target := filepath.Join(importsRoot, "macbook", "2026-08-27", batchID)
			for relative, want := range map[string]string{
				fileName:                             "file payload",
				filepath.Join(dirName, "nested.txt"): "nested payload",
			} {
				payload, err := os.ReadFile(filepath.Join(target, relative))
				if err != nil || string(payload) != want {
					t.Fatalf("payload %q = %q, err=%v, want %q", relative, payload, err, want)
				}
			}
			manifest, err := readLaneCustodyManifest(filepath.Join(target, laneCustodyManifestName))
			if err != nil {
				t.Fatal(err)
			}
			for _, relative := range []string{fileName, filepath.ToSlash(dirName), filepath.ToSlash(filepath.Join(dirName, "nested.txt"))} {
				found := false
				for _, entry := range manifest.Entries {
					if entry.RelativePath == relative {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("custody manifest omitted payload %q", relative)
				}
			}
		})
	}
}

func TestAbandonedPromotionCleanupRejectsSymlinkWithoutFollowingIt(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(outside, "payload")
	if err := os.WriteFile(payload, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, ".lane_symlink.promoting-hostile")); err != nil {
		t.Fatal(err)
	}
	err := cleanupAbandonedLanePromotions(parent, "lane_symlink")
	if err == nil || !strings.Contains(err.Error(), "not a no-follow directory") {
		t.Fatalf("symlinked abandoned promotion candidate error = %v", err)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("cleanup followed symlink outside imports parent: %v", err)
	}
}

func TestPromotionLockRejectsHardlinkBeforeTruncatingTarget(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0o640); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), ".lane_hardlink.loom-promotion.lock")
	if err := os.Link(outside, lockPath); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLanePromotionLock(lockPath, "sha256:inventory")
	if lock != nil {
		lock.close()
	}
	if err == nil || !strings.Contains(err.Error(), "single-link regular file") {
		t.Fatalf("hardlinked promotion lock error = %v", err)
	}
	payload, readErr := os.ReadFile(outside)
	if readErr != nil || string(payload) != "preserve" {
		t.Fatalf("hardlink target was mutated: payload=%q err=%v", payload, readErr)
	}
}

func TestLaneCustodyManifestRejectsTrailingJSON(t *testing.T) {
	pathValue := filepath.Join(t.TempDir(), laneCustodyManifestName)
	manifest := laneCustodyManifest{
		SchemaVersion: laneCustodyManifestSchemaVersion,
		SourceNodeKey: "macbook",
		BatchID:       "lane_manifest",
		AcceptedDate:  "2026-08-27",
	}
	var err error
	manifest.InventoryHash, err = hashLaneCustodyEntries(manifest.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLaneCustodyManifest(pathValue, manifest); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(pathValue, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readLaneCustodyManifest(pathValue); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("trailing manifest content error = %v", err)
	}
}

func TestLaneCustodyManifestRejectsSymlinkAndHardlinkMarkers(t *testing.T) {
	manifest := laneCustodyManifest{
		SchemaVersion: laneCustodyManifestSchemaVersion,
		SourceNodeKey: "macbook",
		BatchID:       "lane_manifest_links",
		AcceptedDate:  "2026-08-27",
	}
	var err error
	manifest.InventoryHash, err = hashLaneCustodyEntries(manifest.Entries)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := writeLaneCustodyManifest(outside, manifest); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		link func(string, string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hardlink", link: os.Link},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), laneCustodyManifestName)
			if err := test.link(outside, marker); err != nil {
				t.Fatal(err)
			}
			if _, err := readLaneCustodyManifest(marker); err == nil {
				t.Fatalf("%s custody marker was followed", test.name)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if _, err := readLaneCustodyManifest(outside); err != nil {
				t.Fatalf("source manifest changed after %s rejection: %v", test.name, err)
			}
		})
	}
}

func TestAcceptLivePromotionLockFailsClosedAndConcurrentOwnerCompletes(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_live_lock"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	importsRoot := filepath.Join(t.TempDir(), "imports")
	entered := make(chan struct{})
	release := make(chan struct{})
	input := AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
		AllowCrossDevicePromotion: true,
		DeviceID: func(pathValue string) (uint64, error) {
			if strings.Contains(filepath.ToSlash(pathValue), "/staging/") {
				return 1, nil
			}
			return 2, nil
		},
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
		BeforeCopyEntry: func(index int, _ string) error {
			if index == 0 {
				close(entered)
				<-release
			}
			return nil
		},
	}
	type acceptOutcome struct {
		result AcceptResult
		err    error
	}
	ownerDone := make(chan acceptOutcome, 1)
	go func() {
		result, err := Accept(context.Background(), &fakeLaneCatalog{}, input)
		ownerDone <- acceptOutcome{result: result, err: err}
	}()
	<-entered
	_, err := Accept(context.Background(), &fakeLaneCatalog{}, input)
	if err == nil || !strings.Contains(err.Error(), "another live process owns this batch promotion") {
		t.Fatalf("concurrent promotion did not fail closed: %v", err)
	}
	close(release)
	owner := <-ownerDone
	if owner.err != nil || owner.result.PromotionMethod != "copy_verify_rename" {
		t.Fatalf("live lock owner result=%#v err=%v", owner.result, owner.err)
	}
}

func TestAcceptRejectsUncommittedDestinationWithoutMerging(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_destination_collision"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	importsRoot := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(importsRoot, "macbook", "2026-08-27", batchID)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "partial.txt"), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with committed batch") {
		t.Fatalf("uncommitted destination was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "partial.txt")); err != nil {
		t.Fatalf("destination collision was mutated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "payload.txt")); err != nil {
		t.Fatalf("destination collision removed staging: %v", err)
	}
}

func TestAcceptDoesNotOverwriteDestinationThatAppearsAtCommit(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_destination_race"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	importsRoot := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	var racedDestination string
	_, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: importsRoot,
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
		Rename: func(source, destination string) error {
			racedDestination = destination
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(destination, "other-batch.txt"), []byte("do not overwrite"), 0o644); err != nil {
				return err
			}
			return os.Rename(source, destination)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "atomic Lane promotion failed") {
		t.Fatalf("destination race error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(racedDestination, "other-batch.txt")); err != nil {
		t.Fatalf("racing destination was overwritten: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "payload.txt")); err != nil {
		t.Fatalf("destination race removed staging: %v", err)
	}
}

func TestAcceptBoundsLargeBatchResponseWithoutDroppingCatalogWork(t *testing.T) {
	remoteRoot := filepath.Join(t.TempDir(), "lane-runtime")
	withLaneRemoteBoundary(t, remoteRoot)
	batchID := "lane_large_response"
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1005; index++ {
		name := filepath.Join(staging, fmt.Sprintf("item-%04d.txt", index))
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Accept(context.Background(), &fakeLaneCatalog{}, AcceptInput{
		AcceptedPath: staging, RemoteRoot: remoteRoot, ImportsRoot: filepath.Join(t.TempDir(), "imports"),
		SourceNodeKey: "macbook", BatchID: batchID, AcceptedDate: "2026-08-27",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesCataloged != 1005 || len(result.StorageEntryIDs) != 1000 || !result.StorageEntryIDsTruncated {
		t.Fatalf("large batch response = cataloged %d ids %d truncated=%t", result.FilesCataloged, len(result.StorageEntryIDs), result.StorageEntryIDsTruncated)
	}
}

func TestSendTransfersCatalogsAndClearsVisibleLaneAfterSuccess(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(filepath.Join(lanePath, "dataset"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "dataset", "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lanePath, "dataset", "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "dataset", "node_modules", "pkg", "index.js"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "note.md"), []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}
	var commands []string
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	result, err := Send(context.Background(), SendInput{
		RootPath:      root,
		SourceNodeKey: "macbook",
		MainHost:      "loom-main",
		Now:           func() time.Time { return now },
		NewBatchID:    func(time.Time) string { return "lane_20260614T120000Z" },
		LookupPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		SSHConfigCheck: func(string) error { return nil },
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			commands = append(commands, name+" "+strings.Join(args, " "))
			return "", "", nil
		}),
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result.Status != BatchStatusLocalCleanupDone {
		t.Fatalf("Status = %q, want local_cleanup_done", result.Status)
	}
	if result.VisibleStoragePath != "imports/macbook/2026-06-14/lane_20260614T120000Z" {
		t.Fatalf("VisibleStoragePath = %q", result.VisibleStoragePath)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "dataset")); !os.IsNotExist(err) {
		t.Fatalf("dataset should have been removed from visible Lane, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "note.md")); !os.IsNotExist(err) {
		t.Fatalf("note.md should have been removed from visible Lane, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateRelPath, "sent", "lane_20260614T120000Z")); !os.IsNotExist(err) {
		t.Fatalf("successful transport safety tree was not retired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.LocalCleanupQuarantinePath, "dataset", "a.txt")); err != nil {
		t.Fatalf("cleanup quarantine missing accepted payload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.LocalCleanupQuarantinePath, "dataset", "node_modules", "pkg", "index.js")); err != nil {
		t.Fatalf("faithful cleanup quarantine should include dependency tree: %v", err)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{"rsync", "--partial", "--progress", "--stats", "--files-from=", "--from0", "--relative", "--no-recursive", "lane accept-received"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("commands did not contain %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"--exclude node_modules/", "--exclude .git/", "--exclude .secrets/", "storage export refresh", "/lane/accepted/", "normalize accepted permissions"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("compiled transfer plan should replace direct excludes %q:\n%s", forbidden, joined)
		}
	}
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", "lane_20260614T120000Z.json"))
	if err != nil {
		t.Fatalf("read completed batch: %v", err)
	}
	if record.Profile != "faithful" || record.PolicyVersion == "" || record.PolicyFingerprint == "" || record.InventoryHash == "" || record.TransferManifestPath == "" {
		t.Fatalf("batch policy evidence missing: %#v", record)
	}
	if record.LocalSafetyCleanupState != SafetyArtifactRemovedAfterSuccess || record.LocalCleanupQuarantineExpiresAt == nil {
		t.Fatalf("batch recovery lifecycle evidence missing: %#v", record)
	}
}

func TestSendCleansSupersededInterruptedStagingAfterSuccessfulRetry(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "retry.bin"), []byte("retry"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldStarted := time.Date(2026, 6, 14, 11, 59, 0, 0, time.UTC)
	oldRecord := BatchRecord{
		SchemaVersion:     BatchSchemaVersion,
		BatchID:           "lane_20260614T115900Z",
		Status:            BatchStatusTransferring,
		SourceNodeKey:     "macbook",
		RemoteStagingPath: "/var/lib/loom/lane/staging/macbook/lane_20260614T115900Z",
		StartedAt:         &oldStarted,
	}
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), oldRecord); err != nil {
		t.Fatalf("write old batch: %v", err)
	}

	var commands []string
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	result, err := Send(context.Background(), SendInput{
		RootPath:      root,
		SourceNodeKey: "macbook",
		MainHost:      "loom-main",
		Now:           func() time.Time { return now },
		NewBatchID:    func(time.Time) string { return "lane_20260614T120000Z" },
		LookupPath:    func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error {
			return nil
		},
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			commands = append(commands, name+" "+strings.Join(args, " "))
			return "", "", nil
		}),
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result.Status != BatchStatusLocalCleanupDone {
		t.Fatalf("Status = %q, want local_cleanup_done", result.Status)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, remoteStagingCommand(RemoteStagingCleanup, "macbook", "lane_20260614T115900Z")) || strings.Contains(joined, "rm -rf") {
		t.Fatalf("cleanup command missing:\n%s", joined)
	}
	updated, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", "lane_20260614T115900Z.json"))
	if err != nil {
		t.Fatalf("read old batch: %v", err)
	}
	if updated.Status != BatchStatusSuperseded || !strings.Contains(updated.ErrorMessage, "lane_20260614T120000Z") {
		t.Fatalf("old batch not superseded: %#v", updated)
	}
}

func TestSupersededStagingCleanupRequiresExactRootAttestation(t *testing.T) {
	root := t.TempDir()
	mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "retry.bin"), "retry")
	oldBatchID := "lane_superseded_attestation_old"
	newBatchID := "lane_superseded_attestation_new"
	oldStarted := time.Now().UTC().Add(-time.Minute)
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: oldBatchID, Status: BatchStatusTransferring,
		SourceNodeKey: "macbook", RemoteRuntimeRoot: DefaultRemoteRoot,
		RemoteStagingPath: remotePathJoin(DefaultRemoteRoot, "staging", "macbook", oldBatchID), StartedAt: &oldStarted,
	}); err != nil {
		t.Fatal(err)
	}
	wrongRoot := "/var/lib/loom/lane-drifted"
	result, err := Send(context.Background(), SendInput{
		RootPath: root, SourceNodeKey: "macbook", MainHost: DefaultMainHost, KeepLocal: true,
		NewBatchID: func(time.Time) string { return newBatchID },
		LookupPath: func(name string) (string, error) { return "/usr/bin/" + name, nil }, SSHConfigCheck: func(string) error { return nil },
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			command := name + " " + strings.Join(args, " ")
			if strings.Contains(command, "lane staging --operation cleanup") && strings.Contains(command, "--batch-id "+oldBatchID) {
				return fmt.Sprintf(`{"source_node_key":"macbook","batch_id":%q,"operation":"cleanup","runtime_root":%q,"staging_path":%q,"status":"absent"}`,
					oldBatchID, wrongRoot, remotePathJoin(wrongRoot, "staging", "macbook", oldBatchID)), "", nil
			}
			return "", "", nil
		}),
	})
	if err != nil || result.Status != BatchStatusCataloged {
		t.Fatalf("new batch completion = %#v, %v", result, err)
	}
	oldRecord, readErr := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", oldBatchID+".json"))
	if readErr != nil || oldRecord.Status != BatchStatusTransferring || oldRecord.CompletedAt != nil {
		t.Fatalf("mismatched cleanup falsely superseded old batch: %#v, %v", oldRecord, readErr)
	}
}

func TestRsyncArgsResumeUsesAppendVerifyWhenSupported(t *testing.T) {
	previous := rsyncSupportsOption
	rsyncSupportsOption = func(_ string, option string) bool {
		return option == "--append-verify"
	}
	t.Cleanup(func() { rsyncSupportsOption = previous })

	args := rsyncArgs(SendInput{
		Resume: true,
		LookupPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		MainHost: "loom-main",
	}, "/tmp/lane", "/var/lib/loom/lane/staging/macbook/batch")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--append-verify") {
		t.Fatalf("resume args should use append-verify when supported:\n%s", joined)
	}
	if strings.Contains(joined, "--append ") {
		t.Fatalf("resume args should not include fallback append when append-verify is supported:\n%s", joined)
	}
	if strings.Contains(joined, "--partial-dir") {
		t.Fatalf("resume args should not include partial-dir with append mode:\n%s", joined)
	}
}

func TestRsyncArgsResumeFallsBackToAppendWhenAppendVerifyUnsupported(t *testing.T) {
	previous := rsyncSupportsOption
	rsyncSupportsOption = func(string, string) bool { return false }
	t.Cleanup(func() { rsyncSupportsOption = previous })

	args := rsyncArgs(SendInput{
		Resume: true,
		LookupPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		MainHost: "loom-main",
	}, "/tmp/lane", "/var/lib/loom/lane/staging/macbook/batch")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--append ") {
		t.Fatalf("resume args should fall back to append:\n%s", joined)
	}
	if strings.Contains(joined, "--append-verify") {
		t.Fatalf("resume args should not include unsupported append-verify:\n%s", joined)
	}
	if strings.Contains(joined, "--partial-dir") {
		t.Fatalf("resume args should not include partial-dir with append mode:\n%s", joined)
	}
}

func TestRsyncTransportsUseOnlyAttestedRuntimeOwnerReceiver(t *testing.T) {
	command, err := remoteReceiverCommand(RemoteStagingResult{
		ReceiverUser:           "loom",
		ReceiverSwitchRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := SendInput{MainHost: "loom-main", remoteReceiverCommand: command}
	for name, args := range map[string][]string{
		"file tree": rsyncArgs(input, "/tmp/lane", "/var/lib/loom/lane/staging/macbook/batch"),
		"bundle":    bundleRsyncArgs(input, "/tmp/archive.tar", "/tmp/manifest.json", "/var/lib/loom/lane/staging/macbook/batch"),
	} {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--rsync-path=sudo -n -u loom -- rsync") {
			t.Fatalf("%s omitted attested runtime-owner receiver: %s", name, joined)
		}
		if strings.Contains(joined, "rm -rf") || strings.Contains(joined, "mkdir -p") {
			t.Fatalf("%s introduced raw remote staging mutation: %s", name, joined)
		}
		if name == "file tree" && slices.Contains(args, "-r") {
			t.Fatalf("file-tree transfer can recurse beyond its reviewed file list: %s", joined)
		}
		if strings.Contains(joined, "--chmod") {
			t.Fatalf("%s rewrites reviewed source modes: %s", name, joined)
		}
	}

	command, err = remoteReceiverCommand(RemoteStagingResult{ReceiverUser: "loom"})
	if err != nil || command != "" {
		t.Fatalf("same-user receiver command = %q, %v", command, err)
	}
	args := rsyncArgs(SendInput{MainHost: "loom-main"}, "/tmp/lane", "/var/lib/loom/lane/staging/macbook/batch")
	if strings.Contains(strings.Join(args, " "), "--rsync-path") {
		t.Fatalf("same-user receiver unexpectedly invoked sudo: %v", args)
	}
}

func TestAcceptanceCommandsUseOnlyAttestedRuntimeOwner(t *testing.T) {
	for name, base := range map[string]string{
		"file tree": "loom --json lane accept-received --source-node macbook",
		"bundle":    "loom --json lane accept-bundle --source-node macbook",
	} {
		command, err := remoteRuntimeCommand(base, remoteStagingReceiver{User: "loom", SwitchRequired: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if command != "sudo -n -u loom -- "+base {
			t.Fatalf("%s command = %q", name, command)
		}
	}
	if _, err := remoteRuntimeCommand("loom --json lane accept-bundle", remoteStagingReceiver{User: "root", SwitchRequired: true}); err == nil {
		t.Fatal("root acceptance identity switch passed")
	}
	if _, err := remoteRuntimeCommand("loom --json lane accept-received", remoteStagingReceiver{User: "loom; touch outside", SwitchRequired: true}); err == nil {
		t.Fatal("unsafe acceptance identity switch passed")
	}
	command, err := remoteRuntimeCommand("loom --json lane accept-received", remoteStagingReceiver{User: "loom"})
	if err != nil || command != "loom --json lane accept-received" {
		t.Fatalf("same-user acceptance command = %q, %v", command, err)
	}
}

func TestTransportCleanupPersistsAndReusesAttestedRuntimeOwner(t *testing.T) {
	root := t.TempDir()
	batchID := "lane_receiver_cleanup"
	mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
	input := bundleSendTestInput(root, batchID, false)
	input.RequestedTransport = TransportModeFileTree
	input.AllowCrossDevicePromotion = true
	input.KeepLocal = true
	cleanupAttempts := 0
	input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
		command := name + " " + strings.Join(args, " ")
		if strings.Contains(command, "lane staging --operation prepare") {
			return fmt.Sprintf(`{"source_node_key":"macbook","batch_id":%q,"operation":"prepare","runtime_root":%q,"staging_path":%q,"status":"ready","receiver_user":"loom","receiver_switch_required":true}`+"\n",
				batchID, DefaultRemoteRoot, remotePathJoin(DefaultRemoteRoot, "staging", "macbook", batchID)), "", nil
		}
		if strings.Contains(command, "lane staging --operation cleanup") {
			cleanupAttempts++
			if !strings.Contains(command, "sudo -n -u loom -- loom --json lane staging") {
				t.Fatalf("cleanup omitted persisted runtime owner: %s", command)
			}
			if cleanupAttempts == 1 {
				return "", "interrupted cleanup", errors.New("connection lost")
			}
		}
		if strings.Contains(command, "lane accept-received") && !strings.Contains(command, "sudo -n -u loom -- loom --json lane accept-received") {
			t.Fatalf("file-tree accept omitted attested runtime owner: %s", command)
		}
		return "", "", nil
	})
	failed, err := Send(context.Background(), input)
	if err != nil || failed.Status != BatchStatusSourceCleanupFailed {
		t.Fatalf("cleanup interruption = %#v, %v", failed, err)
	}
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
	if err != nil || record.RemoteReceiverUser != "loom" || !record.RemoteReceiverSwitchRequired {
		t.Fatalf("persisted receiver evidence = %#v, %v", record, err)
	}

	var repairCommands []string
	repaired, err := Publish(context.Background(), PublishInput{
		RootPath: root, BatchID: batchID, MainHost: DefaultMainHost, RemoteRoot: DefaultRemoteRoot,
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			repairCommands = append(repairCommands, name+" "+strings.Join(args, " "))
			return "", "", nil
		}),
	})
	if err != nil || repaired.Status != BatchStatusCataloged {
		t.Fatalf("receiver-owned cleanup repair = %#v, %v", repaired, err)
	}
	joined := strings.Join(repairCommands, "\n")
	if !strings.Contains(joined, "sudo -n -u loom -- loom --json lane staging --operation cleanup") || !strings.Contains(joined, "--expected-receiver-user loom") {
		t.Fatalf("repair omitted persisted receiver identity:\n%s", joined)
	}
	if strings.Contains(joined, "rm -rf") || strings.Contains(joined, "mkdir -p") {
		t.Fatalf("repair introduced raw remote mutation:\n%s", joined)
	}
}

func TestFileTreeResumeReusesBatchIdentityAfterUnknownAcceptanceResult(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	input := SendInput{
		RootPath: root, SourceNodeKey: "macbook", SourceBoxID: "box-test", MainHost: "loom-main",
		Now: func() time.Time { return now }, NewBatchID: func(time.Time) string { return "lane_resume_identity" },
		RequestedTransport: TransportModeFileTree,
		LookupPath:         func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck:     func(string) error { return nil },
	}
	input.Runner = laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
		if name == "ssh" && strings.Contains(strings.Join(args, " "), "lane accept-received") {
			return "", "connection closed after request", errors.New("unknown remote result")
		}
		return "", "", nil
	})
	failed, err := Send(context.Background(), input)
	if err == nil || failed.Status != BatchStatusFailed || failed.BatchID != "lane_resume_identity" {
		t.Fatalf("unknown result = %#v err=%v", failed, err)
	}

	input.Resume = true
	input.NewBatchID = func(time.Time) string { return "must_not_be_used" }
	input.Runner = laneTestRunner(nil)
	resumed, err := Send(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.BatchID != failed.BatchID || resumed.Status != BatchStatusLocalCleanupDone {
		t.Fatalf("file-tree resume changed canonical identity: failed=%#v resumed=%#v", failed, resumed)
	}
}

func TestSendPreservesAcceptedPhaseWhenCatalogFails(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "accepted-but-not-cataloged.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	result, err := Send(context.Background(), SendInput{
		RootPath:      root,
		SourceNodeKey: "macbook",
		MainHost:      "loom-main",
		Now:           func() time.Time { return now },
		NewBatchID:    func(time.Time) string { return "lane_20260614T120000Z" },
		LookupPath:    func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error {
			return nil
		},
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			if name == "ssh" && strings.Contains(strings.Join(args, " "), "lane accept-received") {
				return "", `{"error":{"code":"storage.lane_catalog_failed"}}`, context.DeadlineExceeded
			}
			return "", "", nil
		}),
	})
	if err != nil {
		t.Fatalf("post-accept catalog failure should be repairable without returning an error: %v", err)
	}
	if result.Status != BatchStatusCatalogFailed {
		t.Fatalf("Status = %q, want catalog_failed", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "promote and catalog lane batch") {
		t.Fatalf("ErrorMessage = %q, want catalog repair hint", result.ErrorMessage)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "accepted-but-not-cataloged.txt")); err != nil {
		t.Fatalf("local Lane payload should remain until publish repair finishes: %v", err)
	}
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", "lane_20260614T120000Z.json"))
	if err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if record.Status != BatchStatusCatalogFailed || record.RemoteAcceptedPath == "" {
		t.Fatalf("record not preserved as catalog_failed: %#v", record)
	}
}

func TestPublishRepairsAcceptedBatchWithoutRsync(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 14, 13, 0, 0, 0, time.UTC)
	started := now.Add(-time.Minute)
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
		SchemaVersion:      BatchSchemaVersion,
		BatchID:            "lane_20260614T120000Z",
		Status:             BatchStatusAcceptedOnMain,
		SourceNodeKey:      "macbook",
		SourceBoxID:        "box_local",
		VisibleStoragePath: "macbook/Lane/2026-06-14/lane_20260614T120000Z",
		RemoteStagingPath:  "/var/lib/loom/lane/staging/macbook/lane_20260614T120000Z",
		RemoteAcceptedPath: "imports/macbook/2026-06-14/lane_20260614T120000Z",
		StartedAt:          &started,
	}); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	var commands []string
	result, err := Publish(context.Background(), PublishInput{
		RootPath:   root,
		BatchID:    "lane_20260614T120000Z",
		MainHost:   "loom-main",
		RemoteRoot: DefaultRemoteRoot,
		Now:        func() time.Time { return now },
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			commands = append(commands, name+" "+strings.Join(args, " "))
			return "", "", nil
		}),
	})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if result.Status != BatchStatusCataloged {
		t.Fatalf("Status = %q, want cataloged", result.Status)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "rsync") {
		t.Fatalf("publish repair should not re-upload files:\n%s", joined)
	}
	for _, want := range []string{"lane accept-received"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("publish commands missing %q:\n%s", want, joined)
		}
	}
	record, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", "lane_20260614T120000Z.json"))
	if err != nil {
		t.Fatalf("read repaired batch: %v", err)
	}
	if strings.Contains(joined, "storage export refresh") {
		t.Fatalf("publish repair must not require storage export refresh:\n%s", joined)
	}
	if record.Status != BatchStatusCataloged || record.CompletedAt == nil {
		t.Fatalf("record not repaired: %#v", record)
	}
}

func TestLaneAcceptedDatePreservesCanonicalAndLegacyBatchIdentity(t *testing.T) {
	started := time.Date(2026, 8, 25, 23, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		record BatchRecord
		want   string
	}{
		{name: "canonical remote custody", record: BatchRecord{BatchID: "lane_batch", RemoteAcceptedPath: "imports/macbook/2026-08-27/lane_batch", VisibleStoragePath: "imports/macbook/2026-08-28/lane_batch"}, want: "2026-08-27"},
		{name: "legacy visible path", record: BatchRecord{BatchID: "lane_batch", VisibleStoragePath: "macbook/Lane/2026-06-14/lane_batch"}, want: "2026-06-14"},
		{name: "durable start time", record: BatchRecord{BatchID: "lane_batch", StartedAt: &started}, want: "2026-08-25"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := laneAcceptedDate(test.record, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); got != test.want {
				t.Fatalf("accepted date = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPublishRepairsBundleThroughVerifiedBundleWorkflow(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	started := now.Add(-time.Minute)
	batchID := "lane_bundle_catalog_retry"
	remoteStaging := "/var/lib/loom/lane/staging/macbook/" + batchID
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusCatalogFailed,
		SourceNodeKey: "macbook", SourceBoxID: "box_local", SelectedTransport: TransportModeBundleSeed,
		VisibleStoragePath: "imports/macbook/2026-08-27/" + batchID,
		RemoteStagingPath:  remoteStaging, RemoteAcceptedPath: "imports/macbook/2026-08-27/" + batchID,
		StartedAt: &started,
	}); err != nil {
		t.Fatal(err)
	}
	var commands []string
	result, err := Publish(context.Background(), PublishInput{
		RootPath: root, BatchID: batchID, MainHost: "loom-main", RemoteRoot: DefaultRemoteRoot,
		Now: func() time.Time { return now },
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			commands = append(commands, name+" "+strings.Join(args, " "))
			return "", "", nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BatchStatusCataloged {
		t.Fatalf("bundle retry result = %#v", result)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{"lane accept-bundle", remoteStaging + "/tree", remoteStaging + "/" + bundleManifestFileName, remoteStaging + "/" + bundleArchiveFileName, remoteStagingCommand(RemoteStagingCleanup, "macbook", batchID)} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bundle retry missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "accept-received") || strings.Contains(joined, "storage export refresh") {
		t.Fatalf("bundle retry used obsolete workflow:\n%s", joined)
	}
}

func TestPublishPersistsTypedRepairFailurePhaseAndRetriesIdempotently(t *testing.T) {
	tests := []struct {
		name       string
		remoteCode string
		wantStatus string
	}{
		{name: "promotion", remoteCode: "storage.lane_promotion_failed", wantStatus: BatchStatusPromotionFailed},
		{name: "catalog", remoteCode: "storage.lane_catalog_failed", wantStatus: BatchStatusCatalogFailed},
		{name: "source cleanup", remoteCode: "storage.lane_source_cleanup_failed", wantStatus: BatchStatusSourceCleanupFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			batchID := "lane_repair_phase"
			now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
			started := now.Add(-time.Minute)
			if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
				SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusAcceptedOnMain,
				SourceNodeKey: "macbook", SourceBoxID: "box_local",
				VisibleStoragePath: "imports/macbook/2026-08-27/" + batchID,
				RemoteStagingPath:  "/var/lib/loom/lane/staging/macbook/" + batchID,
				RemoteAcceptedPath: "imports/macbook/2026-08-27/" + batchID,
				StartedAt:          &started,
			}); err != nil {
				t.Fatal(err)
			}
			input := PublishInput{
				RootPath: root, BatchID: batchID, MainHost: "loom-main", RemoteRoot: DefaultRemoteRoot,
				Now: func() time.Time { return now },
				Runner: func(context.Context, string, ...string) (string, string, error) {
					return "", fmt.Sprintf(`{"error":{"code":%q}}`, test.remoteCode), errors.New("remote repair failed")
				},
			}
			result, err := Publish(context.Background(), input)
			if err == nil || result.Status != test.wantStatus {
				t.Fatalf("typed repair result=%#v err=%v", result, err)
			}
			record, readErr := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
			if readErr != nil || record.Status != test.wantStatus {
				t.Fatalf("persisted repair phase=%#v err=%v", record, readErr)
			}

			input.Runner = laneTestRunner(nil)
			retried, retryErr := Publish(context.Background(), input)
			if retryErr != nil || retried.Status != BatchStatusCataloged {
				t.Fatalf("idempotent repair retry=%#v err=%v", retried, retryErr)
			}
		})
	}
}

func TestSendBoundsCommandOutputAndParsesProgress(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "large.bin"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", DefaultCommandOutputLimit+2048)
	progressLine := "      4,096 100%    2.00MB/s    0:00:02 (xfr#1, to-chk=0/1)\n"
	rsyncStdout := "rsync diagnostic head\n" + huge + progressLine
	rsyncStderr := "rsync stderr head\n" + huge
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	result, err := Send(context.Background(), SendInput{
		RootPath:      root,
		SourceNodeKey: "macbook",
		MainHost:      "loom-main",
		Now:           func() time.Time { return now },
		NewBatchID:    func(time.Time) string { return "lane_20260614T120000Z" },
		LookupPath:    func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error {
			return nil
		},
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			if strings.Contains(name, "rsync") {
				return rsyncStdout, rsyncStderr, nil
			}
			return "", "", nil
		}),
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !result.Progress.Observed || result.Progress.Percent != 100 || result.Progress.RateBytesPerSecond == 0 || result.Progress.ETASeconds != 2 {
		t.Fatalf("progress not parsed: %#v", result.Progress)
	}
	var sawTruncated bool
	for _, command := range result.Commands {
		if strings.Contains(command.Name, "rsync") {
			sawTruncated = true
			if !command.OutputTruncated || !command.StdoutTruncated || !command.StderrTruncated {
				t.Fatalf("expected per-stream truncation evidence, got %#v", command)
			}
			if command.StdoutBytes != int64(len(rsyncStdout)) || command.StderrBytes != int64(len(rsyncStderr)) {
				t.Fatalf("expected exact stream byte counts, got %#v", command)
			}
			if !strings.HasPrefix(command.Stdout, "rsync diagnostic head") || !strings.HasSuffix(command.Stdout, progressLine) || !strings.HasPrefix(command.Stderr, "rsync stderr head") {
				t.Fatalf("expected retained diagnostic heads and progress tail, got %#v", command)
			}
		}
	}
	if !sawTruncated {
		t.Fatalf("expected truncated command output: %#v", result.Commands)
	}
	status := BuildStatus(StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if status.LastTransfer == nil || !status.LastTransfer.Progress.Observed {
		t.Fatalf("last transfer progress not persisted: %#v", status.LastTransfer)
	}
}

func TestParseRsyncProgress(t *testing.T) {
	updated := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	progress := parseRsyncProgress("  1,048,576  50%  1.50MB/s    0:00:03 (xfr#1, to-chk=1/2)", 2*1024*1024, updated)
	if !progress.Observed || progress.BytesTransferred != 1048576 || progress.Percent != 50 || progress.ETASeconds != 3 {
		t.Fatalf("unexpected progress: %#v", progress)
	}
	if progress.RateBytesPerSecond != 1.5*1024*1024 {
		t.Fatalf("rate = %f", progress.RateBytesPerSecond)
	}
}

func TestSendRejectsRemoteRootOutsideLaneBoundary(t *testing.T) {
	_, err := Send(context.Background(), SendInput{
		RootPath:      t.TempDir(),
		SourceNodeKey: "macbook",
		RemoteRoot:    "/tmp/not-loom-lane",
		LookupPath:    func(name string) (string, error) { return "/usr/bin/" + name, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "remote root must stay under") {
		t.Fatalf("expected remote root containment error, got %v", err)
	}
}

func withLaneRemoteBoundary(t *testing.T, boundary string) {
	t.Helper()
	previous := remoteRootBoundary
	remoteRootBoundary = boundary
	t.Cleanup(func() { remoteRootBoundary = previous })
}

type fakeLaneCatalog struct {
	inputs       []storagecatalog.LaneCustodyInput
	observations []storagecatalog.RegisterFilesystemObservationInput
	failAt       int
	failErr      error
	calls        int
}

func (f *fakeLaneCatalog) RegisterLaneCustody(_ context.Context, input storagecatalog.LaneCustodyInput) (storagecatalog.EntryDetail, error) {
	f.calls++
	if f.failAt > 0 && f.calls == f.failAt {
		return storagecatalog.EntryDetail{}, f.failErr
	}
	f.inputs = append(f.inputs, input)
	id := "storage_entry_01J00000000000000000000000"
	return storagecatalog.EntryDetail{
		Entry: storagecatalog.Entry{
			StorageEntryID: id,
			StorageClass:   storagecatalog.StorageClassLaneCustody,
			SourceArea:     storagecatalog.SourceAreaLane,
			OriginNodeKey:  input.SourceNodeKey,
			LogicalPath:    input.RelativeLanePath,
		},
	}, nil
}

func (f *fakeLaneCatalog) RegisterFilesystemObservation(_ context.Context, input storagecatalog.RegisterFilesystemObservationInput) (storagecatalog.FilesystemObservation, error) {
	f.observations = append(f.observations, input)
	return storagecatalog.FilesystemObservation{
		StorageFilesystemObservationID: ids.NewStorageFilesystemObservationID(),
		StorageEntryID:                 optionalString(input.StorageEntryID),
		SourceArea:                     input.SourceArea,
		SourceNodeKey:                  input.SourceNodeKey,
		SourceRef:                      input.SourceRef,
		LogicalPath:                    input.LogicalPath,
		ObjectKind:                     input.ObjectKind,
	}, nil
}

func fakeLaneObservationExists(observations []storagecatalog.RegisterFilesystemObservationInput, logicalPath, kind string) bool {
	for _, observation := range observations {
		if observation.LogicalPath == logicalPath && observation.ObjectKind == kind {
			return true
		}
	}
	return false
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
