package loomcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/lane"
)

func TestLaneCommandsExposeTransferProfileFlags(t *testing.T) {
	root := NewRootCommand()
	for _, commandPath := range [][]string{{"lane", "status"}, {"lane", "send"}} {
		command, _, err := root.Find(commandPath)
		if err != nil {
			t.Fatalf("find %v: %v", commandPath, err)
		}
		for _, name := range []string{"source-only", "exact"} {
			if command.Flags().Lookup(name) == nil {
				t.Fatalf("%v missing --%s", commandPath, name)
			}
		}
	}
	send, _, err := root.Find([]string{"lane", "send"})
	if err != nil {
		t.Fatal(err)
	}
	if send.Flags().Lookup("expected-policy-fingerprint") == nil {
		t.Fatal("lane send missing reviewed dry-run fingerprint guard")
	}
	for _, name := range []string{"bundle", "no-bundle"} {
		if send.Flags().Lookup(name) == nil {
			t.Fatalf("lane send missing --%s", name)
		}
	}
}

func TestLaneRequestedTransportFlagsAreExplicitAndExclusive(t *testing.T) {
	tests := []struct {
		bundle, noBundle bool
		want             lane.TransportMode
		wantErr          bool
	}{
		{want: lane.TransportModeAuto},
		{bundle: true, want: lane.TransportModeBundleSeed},
		{noBundle: true, want: lane.TransportModeFileTree},
		{bundle: true, noBundle: true, wantErr: true},
	}
	for _, test := range tests {
		got, err := laneRequestedTransport(test.bundle, test.noBundle)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("laneRequestedTransport(%t, %t) = %q, %v", test.bundle, test.noBundle, got, err)
		}
	}
}

func TestLaneRepairHintCoversEveryResumableMainCustodyPhase(t *testing.T) {
	for _, status := range []string{lane.BatchStatusPromotionFailed, lane.BatchStatusAcceptedOnMain, lane.BatchStatusCatalogFailed, lane.BatchStatusSourceCleanupFailed} {
		if !laneSendStatusNeedsRepair(status) {
			t.Fatalf("status %q did not offer same-batch custody repair", status)
		}
	}
	for _, status := range []string{lane.BatchStatusCataloged, lane.BatchStatusLocalCleanupDone, lane.BatchStatusLocalCleanupWithheld} {
		if laneSendStatusNeedsRepair(status) {
			t.Fatalf("terminal/local-only status %q incorrectly offered main-custody repair", status)
		}
	}
}

func TestLaneHumanSendOutputExplainsBundleEvidence(t *testing.T) {
	buffer := &bytes.Buffer{}
	command := &cobra.Command{}
	command.SetOut(buffer)
	renderLaneSendResult(command, lane.SendResult{
		Status:             "dry_run",
		RequestedTransport: lane.TransportModeBundleSeed,
		SelectedTransport:  lane.TransportModeBundleSeed,
		TransportReason:    "many small files",
		Transport: lane.BundleRecommendation{
			RecommendedMode:         lane.TransportModeFileTree,
			EstimatedArchiveBytes:   4096,
			EstimatedTemporaryBytes: 8192,
			AverageFileBytes:        12,
			ForceWarning:            "bundle_seed was forced",
		},
		BundleArtifactPath:   "/tmp/state/bundles/batch",
		LocalSafetyPath:      "/tmp/state/bundles/batch",
		BundleArchivePath:    "/tmp/state/bundles/batch/bundle.tar",
		BundleManifestPath:   "/tmp/state/bundles/batch/manifest.json",
		BundleArchiveSHA256:  "archive-sha",
		BundleManifestSHA256: "manifest-sha",
		BundleArchiveBytes:   4096,
		BundleCleanupState:   lane.BundleCleanupRetainedForRetry,
		Metrics: lane.Metrics{
			BundleCreateDurationMS: 2,
			BundleVerifyDurationMS: 3,
			BundleArchiveBytes:     4096,
		},
	})
	output := buffer.String()
	for _, want := range []string{
		"requested=bundle_seed recommended=file_tree selected=bundle_seed",
		"Transport warning: bundle_seed was forced",
		"Bundle safety artifact: /tmp/state/bundles/batch",
		"archive_sha256=archive-sha",
		"cleanup=retained_for_retry",
		"bundle_create=2ms bundle_verify=3ms bundle_archive=4.0 KB",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Lane bundle output missing %q:\n%s", want, output)
		}
	}
}

func TestLaneHumanStatusShowsRecoveryStorageAndExpiry(t *testing.T) {
	buffer := &bytes.Buffer{}
	command := &cobra.Command{}
	command.SetOut(buffer)
	expires := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	renderLaneStatus(command, box.Status{RootPath: "/tmp/loom-box", Profile: box.ProfileWorkspace}, lane.Status{
		State: "empty", LanePath: "/tmp/loom-box/loom-lane", StatePath: "/tmp/loom-box/.loom/state/lane",
		RecoveryStorage: lane.RecoveryStorage{
			RetainedBytes: 4096, SuccessfulGraceBytes: 2048, ProtectedEvidenceBytes: 1024,
			CleanupQuarantineBytes: 2048, TransportSafetyBytes: 2048, UntrackedBytes: 512,
			NextSuccessfulQuarantineExpiresAt: &expires,
		},
	})
	output := buffer.String()
	for _, want := range []string{"Recovery storage: retained=4.0 KB", "grace=2.0 KB", "protected=1.0 KB", "safety=2.0 KB", "untracked=512 B", expires.Format(time.RFC3339)} {
		if !strings.Contains(output, want) {
			t.Fatalf("Lane recovery status output missing %q:\n%s", want, output)
		}
	}
}

func TestLaneBundleDryRunJSONExposesForcedChoiceWithoutArtifactWrite(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")
	if _, stderr, err := executeRootCommand("box", "repair", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("box repair: %v stderr=%s", err, stderr)
	}
	if err := os.WriteFile(filepath.Join(root, box.DefaultLaneDirName, "small.txt"), []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeRootCommand("--json", "lane", "send", "--dry-run", "--bundle", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("bundle dry-run: %v stderr=%s", err, stderr)
	}
	var result lane.SendResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode bundle dry-run: %v output=%s", err, stdout)
	}
	if !result.DryRun || result.RequestedTransport != lane.TransportModeBundleSeed || result.SelectedTransport != lane.TransportModeBundleSeed || result.Transport.RecommendedMode != lane.TransportModeFileTree || result.Transport.ForceWarning == "" {
		t.Fatalf("bundle dry-run evidence = %#v", result)
	}
	if result.BundleArtifactPath != "" {
		t.Fatalf("dry-run wrote bundle artifact evidence: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, ".loom", "state", "lane", "bundles")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created bundle state: %v", err)
	}

	if _, _, err := executeRootCommand("lane", "send", "--bundle", "--no-bundle"); err == nil || !strings.Contains(err.Error(), "choose either --bundle or --no-bundle") {
		t.Fatalf("conflicting bundle flags error = %v", err)
	}
}

func TestLaneAcceptBundleIsNarrowHiddenMainCommand(t *testing.T) {
	root := NewRootCommand()
	command, _, err := root.Find([]string{"lane", "accept-bundle"})
	if err != nil {
		t.Fatal(err)
	}
	if !command.Hidden {
		t.Fatal("lane accept-bundle must not be a general operator command")
	}
	for _, name := range []string{"source-node", "batch-id", "accepted-path", "date", "remote-root", "manifest-path", "archive-path"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("lane accept-bundle missing --%s", name)
		}
	}
	root.SetArgs([]string{"lane", "accept-bundle"})
	if err := root.Execute(); err == nil {
		t.Fatal("lane accept-bundle accepted missing batch-bound flags")
	}
}

func TestBundleLaneAcceptInputPreservesTrustedRuntimeRoots(t *testing.T) {
	got := bundleLaneAcceptInput(lane.BundleAcceptInput{
		RemoteRoot: "/custom/runtime/lane", TrustedRemoteRoot: true, ImportsRoot: "/custom/storage/imports",
	}, lane.BundleUnpackResult{AcceptedPath: "/custom/runtime/lane/staging/macbook/batch/tree"})
	if got.RemoteRoot != "/custom/runtime/lane" || !got.TrustedRemoteRoot || got.ImportsRoot != "/custom/storage/imports" {
		t.Fatalf("bundle acceptance roots = %#v", got)
	}
}

func TestLaneStagingIsIdentityOnlyHiddenMainCommand(t *testing.T) {
	root := NewRootCommand()
	command, _, err := root.Find([]string{"lane", "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if !command.Hidden {
		t.Fatal("lane staging must not be a general operator command")
	}
	for _, name := range []string{"operation", "source-node", "batch-id", "expected-runtime-root"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("lane staging missing --%s", name)
		}
	}
	for _, forbidden := range []string{"path", "root", "remote-root", "accepted-path"} {
		if command.Flags().Lookup(forbidden) != nil {
			t.Fatalf("lane staging exposes arbitrary filesystem flag --%s", forbidden)
		}
	}
	root.SetArgs([]string{"lane", "staging"})
	if err := root.Execute(); err == nil {
		t.Fatal("lane staging accepted missing identity-bound flags")
	}
}

func TestLanePromotionFlagsAndBundleWorkflowPreserveExplicitPolicy(t *testing.T) {
	root := NewRootCommand()
	for _, commandPath := range [][]string{{"lane", "send"}, {"lane", "accept-received"}, {"lane", "accept-bundle"}} {
		command, _, err := root.Find(commandPath)
		if err != nil {
			t.Fatal(err)
		}
		if command.Flags().Lookup("allow-cross-device-promotion") == nil {
			t.Fatalf("%v missing explicit cross-device promotion flag", commandPath)
		}
	}
	got := bundleLaneAcceptInput(lane.BundleAcceptInput{
		RemoteRoot: "/var/lib/loom/lane", SourceNodeKey: "macbook", SourceBoxID: "box-test",
		BatchID: "batch-test", AcceptedDate: "2026-08-27", CustodyNodeKey: "main",
		AllowCrossDevicePromotion: true,
	}, lane.BundleUnpackResult{AcceptedPath: "/var/lib/loom/lane/staging/macbook/batch-test/tree"})
	if got.AcceptedPath != "/var/lib/loom/lane/staging/macbook/batch-test/tree" || !got.AllowCrossDevicePromotion || got.SourceNodeKey != "macbook" || got.BatchID != "batch-test" {
		t.Fatalf("bundle acceptance workflow lost promotion input: %#v", got)
	}
}
