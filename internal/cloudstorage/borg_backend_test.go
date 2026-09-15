package cloudstorage

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/backup"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/hermesprofile"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/restoreauthority"
)

type cloudRestoreAuthorityStub struct {
	restores []restoreauthority.RestoreRequest
	drops    []restoreauthority.DropRequest
}

func (stub *cloudRestoreAuthorityStub) Restore(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
	if _, err := io.Copy(io.Discard, request.Dump); err != nil {
		return restoreauthority.Result{}, err
	}
	request.Dump = nil
	stub.restores = append(stub.restores, request)
	return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database}, nil
}

func (stub *cloudRestoreAuthorityStub) Drop(_ context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
	stub.drops = append(stub.drops, request)
	return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database, CleanupAttempted: true, CleanupSucceeded: true}, nil
}

func TestBorgDirectArchiveUsesPendingCommitWithoutUserDataStaging(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}

	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatalf("ArchiveCanonicalRoots returned error: %v", err)
	}
	if result.Status != DirectArchiveStatusSucceeded || !result.Committed || result.Idempotent || result.Manifest == nil {
		t.Fatalf("result = %#v", result)
	}
	if !fake.archives[result.Archive] || fake.archives[result.PendingArchive] {
		t.Fatalf("archive commit state = %#v", fake.archives)
	}
	if fake.createDir == "" || filepath.Dir(fake.createDir) != fixture.cfg.StateDir {
		t.Fatalf("Borg create dir = %q", fake.createDir)
	}
	if _, err := os.Stat(fake.createDir); !os.IsNotExist(err) {
		t.Fatalf("temporary evidence directory survived: %v", err)
	}
	joinedArgs := strings.Join(fake.createArgs, "\n")
	for _, source := range []string{fixture.root, fixture.operational.PackageDir, fixture.provenanceDir} {
		if !strings.Contains(joinedArgs, source) {
			t.Fatalf("create did not bind direct source %q: %s", source, joinedArgs)
		}
	}
	if !strings.Contains(joinedArgs, "pp:"+directArchiveTestPath(fixture.root)+"/.loom-acceptance") {
		t.Fatalf("create exclusion policy = %s", joinedArgs)
	}
	if borgArchiveArg(fake.createArgs) != result.PendingArchive || fake.commandCount("rename") != 1 {
		t.Fatalf("canonical archive was not committed only by rename: %v", fake.calls)
	}
	var timestamp string
	for index, arg := range fake.createArgs {
		if arg == "--timestamp" && index+1 < len(fake.createArgs) {
			timestamp = fake.createArgs[index+1]
			break
		}
	}
	expectedTimestamp := fixture.request.CreatedAt.UTC().Truncate(time.Second)
	parsedTimestamp, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		t.Fatalf("Borg timestamp %q is not RFC3339: %v", timestamp, err)
	}
	if timestamp != expectedTimestamp.Format(time.RFC3339) || !strings.HasSuffix(timestamp, "Z") || strings.Contains(timestamp, ".") || !parsedTimestamp.Equal(expectedTimestamp) {
		t.Fatalf("Borg timestamp = %q parsed=%s, want whole UTC second %s", timestamp, parsedTimestamp, expectedTimestamp)
	}
	if result.Manifest == nil || !result.Manifest.CreatedAt.Equal(fixture.request.CreatedAt) || result.Manifest.CreatedAt.Nanosecond() != fixture.request.CreatedAt.Nanosecond() {
		t.Fatalf("authenticated manifest CreatedAt changed: got %#v want %s", result.Manifest, fixture.request.CreatedAt)
	}
	if fake.commandCount("delete") != 0 || fake.commandCount("prune") != 0 || fake.commandCount("compact") != 0 {
		t.Fatalf("direct archive performed retention mutation: %v", fake.calls)
	}
	assertDirectOperationalPackageUnchanged(t, fixture)
}

func TestBorgDirectArchiveCanonicalReplayUsesFrozenManifestAfterLiveDrift(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	first, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	creates := fake.commandCount("create")
	replay, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatalf("same-manifest replay returned error: %v", err)
	}
	if replay.Status != DirectArchiveStatusSucceeded || !replay.Idempotent || !replay.Committed || replay.ManifestSHA256 != first.ManifestSHA256 {
		t.Fatalf("replay result = %#v", replay)
	}
	if fake.commandCount("create") != creates || fake.commandCount("rename") != 1 {
		t.Fatalf("idempotent replay wrote Borg state: %v", fake.calls)
	}

	if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "payload.txt"), []byte("different live canonical bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "post-canonical-live-entry"), []byte("not part of the frozen archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	driftedPrepared := prepareDirectArchiveForTest(t, fixture.cfg, fixture.request)
	if driftedPrepared.ManifestSHA256 == first.ManifestSHA256 {
		t.Fatal("live drift did not change the fresh manifest identity")
	}
	replay, err = backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil || !replay.Committed || !replay.Idempotent || replay.ManifestSHA256 != first.ManifestSHA256 || replay.Manifest == nil || replay.Manifest.SourceSnapshotSHA256 != first.Manifest.SourceSnapshotSHA256 {
		t.Fatalf("drifted canonical replay result = %#v err=%v", replay, err)
	}
	if replay.Checks["resume_envelope"] != DirectArchiveStatusSucceeded || replay.Checks["source_stability"] != "frozen_canonical_not_rechecked" {
		t.Fatalf("drifted canonical replay checks = %#v", replay.Checks)
	}
	if fake.commandCount("create") != creates || fake.commandCount("rename") != 1 || fake.commandCount("delete") != 0 || fake.commandCount("export-tar") != 0 {
		t.Fatalf("drifted canonical replay mutated Borg repository: %v", fake.calls)
	}
	assertDirectOperationalPackageUnchanged(t, fixture)
}

func TestBorgDirectArchiveFetchPreservesIndependentProvenanceIdentity(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(t.TempDir(), "fetched-provenance")
	if err := os.Mkdir(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	prefix := directArchiveTestPath(fixture.provenanceDir)
	reader := tar.NewReader(bytes.NewReader(fake.tarByArchive[result.Archive]))
	fetched := map[string]bool{}
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		name := strings.TrimPrefix(header.Name, prefix+"/")
		if header.Name == prefix || (name != provenance.RecoveryManifestFile && name != provenance.RecoveryDumpFile) {
			continue
		}
		payload, readErr := io.ReadAll(reader)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(packageDir, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
		fetched[name] = true
	}
	if !fetched[provenance.RecoveryManifestFile] || !fetched[provenance.RecoveryDumpFile] {
		t.Fatalf("fetched provenance entries = %#v", fetched)
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(context.Background(), packageDir, fixture.provenanceSHA256)
	if err != nil || verification.ManifestSHA256 != fixture.provenanceSHA256 || verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("fetched provenance verification = %#v err=%v", verification, err)
	}
	dumpSHA, _, err := maintenance.HashFile(filepath.Join(packageDir, provenance.RecoveryDumpFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.VerifyProvenanceBackupPackage(context.Background(), packageDir, dumpSHA); err == nil || !strings.Contains(err.Error(), "manifest identity mismatch") {
		t.Fatalf("fetched package accepted dump hash as manifest identity: %v", err)
	}
}

func TestBorgDirectArchiveSourceInstabilityLeavesOnlyUncommittedPendingArchive(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	fake.afterCreate = func() {
		if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "payload.txt"), []byte("changed during create"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}

	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || !errors.Is(err, backupstrategy.ErrDirectArchiveSourceChanged) {
		t.Fatalf("instability error = %v", err)
	}
	if result.Status != DirectArchiveStatusFailed || result.Code != DirectArchiveCodeSourceUnstable || !result.Retryable || result.Committed {
		t.Fatalf("instability result = %#v", result)
	}
	if fake.archives[result.Archive] || !fake.archives[result.PendingArchive] || fake.commandCount("rename") != 0 {
		t.Fatalf("unstable source gained committed evidence: %#v calls=%v", fake.archives, fake.calls)
	}
	if fake.commandCount("delete") != 0 {
		t.Fatalf("instability cleanup mutated remote evidence: %v", fake.calls)
	}
	assertDirectOperationalPackageUnchanged(t, fixture)
}

func TestBorgDirectArchiveRejectsABAContentAndAncestorSubstitution(t *testing.T) {
	t.Run("same-size content A-B-A", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		payloadPath := filepath.Join(fixture.root, "Documents", "payload.txt")
		original, err := os.ReadFile(payloadPath)
		if err != nil {
			t.Fatal(err)
		}
		replacement := bytes.Repeat([]byte{'X'}, len(original))
		fake.beforeArchiveRead = func() {
			if err := os.WriteFile(payloadPath, replacement, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(payloadPath, fixture.payloadMTime, fixture.payloadMTime); err != nil {
				t.Fatal(err)
			}
		}
		fake.afterArchiveRead = func() {
			if err := os.WriteFile(payloadPath, original, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(payloadPath, fixture.payloadMTime, fixture.payloadMTime); err != nil {
				t.Fatal(err)
			}
		}
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || result.Code != DirectArchiveCodeArchiveInvalid || result.Committed {
			t.Fatalf("A-B-A result=%#v err=%v", result, err)
		}
		if fake.commandCount("rename") != 0 || fake.archives[result.Archive] {
			t.Fatalf("A-B-A content substitution committed: %#v calls=%v", fake.archives, fake.calls)
		}
	})

	t.Run("ancestor A-B-A", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		parent := filepath.Dir(fixture.root)
		savedParent := parent + "-saved"
		rootInfo, err := os.Lstat(fixture.root)
		if err != nil {
			t.Fatal(err)
		}
		documentsInfo, err := os.Lstat(filepath.Join(fixture.root, "Documents"))
		if err != nil {
			t.Fatal(err)
		}
		fake.beforeArchiveRead = func() {
			if err := os.Rename(parent, savedParent); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(fixture.root, "Documents"), documentsInfo.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			payloadPath := filepath.Join(fixture.root, "Documents", "payload.txt")
			if err := os.WriteFile(payloadPath, bytes.Repeat([]byte{'Y'}, len("canonical bytes")), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(payloadPath, fixture.payloadMTime, fixture.payloadMTime); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(filepath.Join(fixture.root, "Documents"), documentsInfo.ModTime(), documentsInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(fixture.root, rootInfo.ModTime(), rootInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
		fake.afterArchiveRead = func() {
			if err := os.RemoveAll(parent); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(savedParent, parent); err != nil {
				t.Fatal(err)
			}
		}
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || result.Code != DirectArchiveCodeArchiveInvalid || result.Committed {
			t.Fatalf("ancestor substitution result=%#v err=%v", result, err)
		}
		if fake.commandCount("rename") != 0 || fake.archives[result.Archive] {
			t.Fatalf("ancestor substitution committed: %#v calls=%v", fake.archives, fake.calls)
		}
	})
}

func TestBorgDirectArchiveRejectsInvalidUTF8BeforeCreate(t *testing.T) {
	t.Run("filename", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		invalidName := string([]byte{'i', 'n', 'v', 'a', 'l', 'i', 'd', '-', 0xff})
		if err := os.WriteFile(filepath.Join(fixture.root, invalidName), []byte("invalid"), 0o600); err != nil {
			// APFS rejects invalid UTF-8 names at the filesystem boundary. Linux
			// admits the fixture and exercises LOOM's pre-create rejection below.
			if fake.commandCount("create") != 0 {
				t.Fatalf("Borg create ran after filesystem rejected invalid UTF-8 filename: %v", fake.calls)
			}
			return
		}
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		if _, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request}); err == nil {
			t.Fatal("invalid UTF-8 filename was accepted")
		}
		if fake.commandCount("create") != 0 {
			t.Fatalf("Borg create ran for invalid UTF-8 filename: %v", fake.calls)
		}
	})

	t.Run("symlink target", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		invalidTarget := string([]byte{'t', 'a', 'r', 'g', 'e', 't', '-', 0xff})
		if err := os.Symlink(invalidTarget, filepath.Join(fixture.root, "Documents", "invalid-link")); err != nil {
			t.Fatal(err)
		}
		fake := newFakeDirectBorg(t)
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		if _, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request}); err == nil {
			t.Fatal("invalid UTF-8 symlink target was accepted")
		}
		if fake.commandCount("create") != 0 {
			t.Fatalf("Borg create ran for invalid UTF-8 symlink target: %v", fake.calls)
		}
	})
}

func TestBorgDirectArchiveUsesExactArchiveCheckWithRepositoryDefault(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	if fixture.cfg.Snapshots.Borg.CheckMode != DefaultBorgCheckMode || DefaultBorgCheckMode != "repository" {
		t.Fatalf("fixture does not exercise repository default: %q", fixture.cfg.Snapshots.Borg.CheckMode)
	}
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "check:") && strings.HasSuffix(call, "check ::"+result.PendingArchive) {
			found = true
		}
		if strings.HasPrefix(call, "check:") && strings.Contains(call, "--repository-only") {
			t.Fatalf("direct archive delegated to repository-only check: %v", fake.calls)
		}
	}
	if !found {
		t.Fatalf("exact pending archive check missing for %q: calls=%v", result.PendingArchive, fake.calls)
	}
}

func TestBorgSnapshotVerifyUsesExplicitDeepAssuranceProfiles(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	archive := borgArchiveName(fixture.request.NodeID, fixture.request.ArchiveRef)

	for _, test := range []struct {
		name         string
		input        SnapshotVerifyInput
		wantProfile  SnapshotVerifyProfile
		wantCoverage SnapshotVerifyCoverage
		wantCommands []string
	}{
		{
			name: "metadata",
			input: SnapshotVerifyInput{Config: fixture.cfg, NodeID: fixture.request.NodeID, Ref: fixture.request.ArchiveRef,
				SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileMetadata}},
			wantProfile:  SnapshotVerifyProfileMetadata,
			wantCoverage: SnapshotVerifyCoverageArchiveMetadata,
			wantCommands: []string{"list --json", "info --json ::" + archive, "check --archives-only ::" + archive},
		},
		{
			name: "rolling repository",
			input: SnapshotVerifyInput{Config: fixture.cfg, NodeID: fixture.request.NodeID,
				SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileRollingRepository, MaxDurationSeconds: 600}},
			wantProfile:  SnapshotVerifyProfileRollingRepository,
			wantCoverage: SnapshotVerifyCoverageRepositoryTimeBounded,
			wantCommands: []string{"check --repository-only --max-duration 600"},
		},
		{
			name: "archive data",
			input: SnapshotVerifyInput{Config: fixture.cfg, NodeID: fixture.request.NodeID, Ref: archive,
				SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileArchiveData}},
			wantProfile:  SnapshotVerifyProfileArchiveData,
			wantCoverage: SnapshotVerifyCoverageArchiveData,
			wantCommands: []string{"info --json ::" + archive, "check --archives-only --verify-data ::" + archive},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeDirectBorg(t)
			fake.archives[archive] = true
			fake.timeByArchive[archive] = fixture.request.CreatedAt
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, DisableRemoteLock: true}}
			result, err := backend.Verify(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != SnapshotStatusSucceeded || result.Profile != test.wantProfile || result.Coverage != test.wantCoverage {
				t.Fatalf("verify result = %#v", result)
			}
			if test.wantProfile == SnapshotVerifyProfileRollingRepository && (result.Ref != "" || result.Archive != "" || result.MaxDurationSeconds != 600) {
				t.Fatalf("rolling repository result claimed archive completion: %#v", result)
			}
			if len(fake.calls) != len(test.wantCommands) {
				t.Fatalf("Borg calls = %v, want %v", fake.calls, test.wantCommands)
			}
			for index, want := range test.wantCommands {
				if !strings.HasSuffix(fake.calls[index], want) {
					t.Fatalf("Borg call %d = %q, want suffix %q", index, fake.calls[index], want)
				}
			}
		})
	}
}

func TestBorgSnapshotVerifyRejectsMixedOrIncompleteProfilesBeforeBorg(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	called := false
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, DisableRemoteLock: true, Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
		called = true
		return nil, nil
	}}}
	for _, input := range []SnapshotVerifyInput{
		{Config: fixture.cfg, Ref: "archive", SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileMetadata, MaxDurationSeconds: 1}},
		{Config: fixture.cfg, Ref: "archive", SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileRollingRepository, MaxDurationSeconds: 1}},
		{Config: fixture.cfg, SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileRollingRepository}},
		{Config: fixture.cfg, Ref: "latest", SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileArchiveData}},
		{Config: fixture.cfg, Ref: "archive", SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileArchiveData, MaxDurationSeconds: 1}},
		{Config: fixture.cfg, Ref: "archive", SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: "full"}},
	} {
		if _, err := backend.Verify(context.Background(), input); err == nil {
			t.Fatalf("invalid verify input was accepted: %#v", input)
		}
	}
	if called {
		t.Fatal("invalid verify profile reached Borg")
	}
}

func TestBorgSnapshotVerifyFailureDoesNotClaimCompletedCoverage(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	archive := borgArchiveName(fixture.request.NodeID, fixture.request.ArchiveRef)
	fake := newFakeDirectBorg(t)
	fake.archives[archive] = true
	fake.failCheck = errors.New("synthetic deep verification failure")
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, DisableRemoteLock: true}}
	result, err := backend.Verify(context.Background(), SnapshotVerifyInput{
		Config: fixture.cfg, Ref: archive,
		SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileArchiveData},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SnapshotStatusFailed || result.Coverage != "" || result.Checks["borg_check"] != SnapshotStatusFailed {
		t.Fatalf("failed archive-data check claimed completed coverage: %#v", result)
	}
}

func TestBorgDirectArchiveArchiveItemOrderingDoesNotChangeIdentity(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	fake.afterCreate = func() {
		for archive, payload := range fake.listByArchive {
			lines := bytes.Split(bytes.TrimSpace(payload), []byte{'\n'})
			for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
				lines[left], lines[right] = lines[right], lines[left]
			}
			fake.listByArchive[archive] = append(bytes.Join(lines, []byte{'\n'}), '\n')
		}
	}
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil || !result.Committed {
		t.Fatalf("reordered archive items changed identity: result=%#v err=%v", result, err)
	}
}

func TestBorgDirectArchiveInterruptedAndAmbiguousNetworkFailureAreSafeToRetry(t *testing.T) {
	t.Run("interrupted create", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		fake.failCreate = errors.New("synthetic network interruption")
		fake.failCreateOutput = []byte("\x1b[31mconnection closed by remote host\x1b[0m\x00\nBORG_PASSPHRASE=do-not-leak\n" + strings.Repeat("leading diagnostic\n", borgFailureInspectLimit) + "FINAL BORG ERROR: transport endpoint closed\n")
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || result.Code != DirectArchiveCodeInterrupted || result.Committed {
			t.Fatalf("interrupted result=%#v err=%v", result, err)
		}
		var executionErr *BorgExecutionError
		if !errors.As(err, &executionErr) || !executionErr.DiagnosticTruncated || len(executionErr.Diagnostic) > borgFailureDiagnosticLimit {
			t.Fatalf("bounded Borg diagnostic missing: result=%#v err=%v", result, err)
		}
		if !strings.Contains(result.Error, "connection closed by remote host") || !strings.Contains(result.Error, borgFailureOmissionMarker) || !strings.Contains(result.Error, "FINAL BORG ERROR: transport endpoint closed") {
			t.Fatalf("direct archive did not retain bounded diagnostic: %q", result.Error)
		}
		for _, forbidden := range []string{"\x1b", "\x00", "do-not-leak"} {
			if strings.Contains(result.Error, forbidden) {
				t.Fatalf("direct archive diagnostic leaked %q: %q", forbidden, result.Error)
			}
		}
		if result.PendingArchive == "" || !slices.Contains(fake.createArgs, "::"+result.PendingArchive) || fake.commandCount("list") != 2 || fake.archives[result.PendingArchive] || fake.archives[result.Archive] || fake.commandCount("rename") != 0 || fake.commandCount("delete") != 0 {
			t.Fatalf("interrupted create committed or cleaned remote state: %#v calls=%v", fake.archives, fake.calls)
		}
		assertDirectOperationalPackageUnchanged(t, fixture)
	})

	t.Run("create response lost after durable pending archive", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		fake.failCreateAfterDurable = errors.New("synthetic create response lost")
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if err != nil || !result.Committed || result.Status != DirectArchiveStatusSucceeded {
			t.Fatalf("ambiguous durable create result=%#v err=%v", result, err)
		}
		if result.Checks["borg_create_pending"] != "ambiguous_durable" || fake.commandCount("create") != 1 || fake.commandCount("rename") != 1 {
			t.Fatalf("ambiguous create was not inspected and committed exactly once: result=%#v calls=%v", result, fake.calls)
		}
		if fake.commandCount("delete") != 0 || fake.commandCount("prune") != 0 || fake.commandCount("compact") != 0 {
			t.Fatalf("ambiguous create mutated retention state: %v", fake.calls)
		}
		assertDirectOperationalPackageUnchanged(t, fixture)
	})

	t.Run("rename response lost after commit", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		fake.failRenameAfterCommit = true
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		failed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || failed.Committed || !fake.archives[failed.Archive] {
			t.Fatalf("ambiguous rename result=%#v archives=%#v err=%v", failed, fake.archives, err)
		}
		fake.failRenameAfterCommit = false
		replay, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if err != nil || !replay.Committed || !replay.Idempotent || replay.ManifestSHA256 != failed.ManifestSHA256 {
			t.Fatalf("safe replay result=%#v err=%v", replay, err)
		}
		if fake.commandCount("rename") != 1 || fake.commandCount("delete") != 0 {
			t.Fatalf("safe replay performed extra mutation: %v", fake.calls)
		}
		assertDirectOperationalPackageUnchanged(t, fixture)
	})
}

func TestBorgDirectArchiveResumesAuthenticatedPendingWithoutCreateOrDelete(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	fake.failArchiveListStreams = 1
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}

	interrupted, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.PendingArchive == "" || !fake.archives[interrupted.PendingArchive] || fake.archives[interrupted.Archive] {
		t.Fatalf("interrupted archive = %#v archives=%#v err=%v", interrupted, fake.archives, err)
	}
	fake.calls = nil

	resumed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil || !resumed.Committed || !resumed.ResumedPending || resumed.PendingArchive != interrupted.PendingArchive || resumed.VerificationPhase != "committed" {
		t.Fatalf("resumed archive = %#v err=%v", resumed, err)
	}
	if resumed.BorgCommandCounts["create"] != 0 || resumed.BorgCommandCounts["rename"] != 1 || fake.commandCount("create") != 0 || fake.commandCount("rename") != 1 || fake.commandCount("delete") != 0 {
		t.Fatalf("resume command truth = %#v calls=%v", resumed.BorgCommandCounts, fake.calls)
	}
	if resumed.Checks["resume_envelope"] != DirectArchiveStatusSucceeded || resumed.Checks["source_stability"] != "frozen_pending_not_rechecked" {
		t.Fatalf("resume checks = %#v", resumed.Checks)
	}
	if resumed.PackedBytes != 123 || resumed.DeduplicatedBytes != 45 {
		t.Fatalf("resumed authenticated info stats = packed:%d deduplicated:%d", resumed.PackedBytes, resumed.DeduplicatedBytes)
	}
	if countDirectArchiveContentStreams(fake.calls) != 1 || fake.commandCount("export-tar") != 0 {
		t.Fatalf("resume verification was not one content-hash stream: %v", fake.calls)
	}
	if fake.archives[interrupted.PendingArchive] || !fake.archives[resumed.Archive] {
		t.Fatalf("resume did not atomically commit pending archive: %#v", fake.archives)
	}
}

func TestBorgDirectArchiveFrozenPendingResumeIgnoresOnlyLiveRootInventoryDrift(t *testing.T) {
	tests := map[string]func(t *testing.T, fixture directArchiveCloudFixture){
		"root-directory-mtime": func(t *testing.T, fixture directArchiveCloudFixture) {
			info, err := os.Lstat(fixture.root)
			if err != nil {
				t.Fatal(err)
			}
			changed := info.ModTime().Add(2 * time.Second)
			if err := os.Chtimes(fixture.root, changed, changed); err != nil {
				t.Fatal(err)
			}
		},
		"live-content-and-entry": func(t *testing.T, fixture directArchiveCloudFixture) {
			if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "payload.txt"), []byte("live bytes after frozen create"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.root, "new-live-entry"), []byte("live entry after frozen create"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			frozenPrepared := prepareDirectArchiveForTest(t, fixture.cfg, fixture.request)
			fake := newFakeDirectBorg(t)
			fake.failArchiveListStreams = 1
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
			interrupted, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
			if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.PendingArchive == "" {
				t.Fatalf("interrupted archive = %#v err=%v", interrupted, err)
			}
			frozenHash := interrupted.ManifestSHA256
			frozenSnapshot := frozenPrepared.Manifest.SourceSnapshotSHA256
			mutate(t, fixture)
			livePrepared := prepareDirectArchiveForTest(t, fixture.cfg, fixture.request)
			if livePrepared.ManifestSHA256 == frozenHash || livePrepared.Manifest.SourceSnapshotSHA256 == frozenSnapshot {
				t.Fatalf("fixture did not create live inventory drift: live=%s frozen=%s", livePrepared.ManifestSHA256, frozenHash)
			}
			fake.calls = nil
			resumed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
			if err != nil || !resumed.Committed || !resumed.ResumedPending || resumed.Idempotent || resumed.Manifest == nil {
				t.Fatalf("resumed frozen archive = %#v err=%v", resumed, err)
			}
			if resumed.ManifestSHA256 != frozenHash || resumed.Manifest.SourceSnapshotSHA256 != frozenSnapshot || resumed.ManifestSHA256 == livePrepared.ManifestSHA256 {
				t.Fatalf("resume rebound frozen manifest: resumed=%s live=%s frozen=%s", resumed.ManifestSHA256, livePrepared.ManifestSHA256, frozenHash)
			}
			if resumed.Checks["resume_envelope"] != DirectArchiveStatusSucceeded || resumed.Checks["source_stability"] != "frozen_pending_not_rechecked" || resumed.Checks["pending_archive_verify"] != DirectArchiveStatusSucceeded {
				t.Fatalf("resume checks = %#v", resumed.Checks)
			}
			if resumed.BorgCommandCounts["create"] != 0 || resumed.BorgCommandCounts["rename"] != 1 || resumed.BorgCommandCounts["delete"] != 0 || resumed.BorgCommandCounts["export-tar"] != 0 || countDirectArchiveContentStreams(fake.calls) != 1 {
				t.Fatalf("resume command truth = %#v calls=%v", resumed.BorgCommandCounts, fake.calls)
			}
		})
	}
}

func TestBorgDirectArchiveFrozenPendingTamperRefusesAfterLiveDrift(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	fake.failArchiveListStreams = 1
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	interrupted, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.PendingArchive == "" {
		t.Fatalf("interrupted archive = %#v err=%v", interrupted, err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "new-live-entry"), []byte("live drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	items := decodeDirectArchiveListItems(t, fake.listByArchive[interrupted.PendingArchive])
	for index := range items {
		if strings.HasSuffix(items[index].Path, "/payload.txt") && items[index].Type == "-" {
			items[index].SHA256 = strings.Repeat("0", sha256.Size*2)
			break
		}
	}
	fake.listByArchive[interrupted.PendingArchive] = encodeDirectArchiveListItems(t, items)
	fake.calls = nil
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || result.Code != DirectArchiveCodeArchiveInvalid || result.Committed || !result.ResumedPending {
		t.Fatalf("tampered frozen pending result=%#v err=%v", result, err)
	}
	if result.ManifestSHA256 != interrupted.ManifestSHA256 || fake.commandCount("create") != 0 || fake.commandCount("rename") != 0 || fake.commandCount("delete") != 0 || countDirectArchiveContentStreams(fake.calls) != 1 {
		t.Fatalf("tampered frozen pending command/result truth=%#v calls=%v", result, fake.calls)
	}
}

func TestBorgDirectArchiveSecondVerificationInterruptionRemainsResumable(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	fake.failArchiveListStreams = 2
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}

	first, firstErr := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(firstErr, ErrDirectArchiveRetryable) || !fake.archives[first.PendingArchive] {
		t.Fatalf("first interruption = %#v err=%v", first, firstErr)
	}
	second, secondErr := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(secondErr, ErrDirectArchiveRetryable) || !second.ResumedPending || second.PendingArchive != first.PendingArchive || second.BorgCommandCounts["create"] != 0 || second.BorgCommandCounts["rename"] != 0 || !fake.archives[first.PendingArchive] {
		t.Fatalf("second interruption = %#v archives=%#v err=%v", second, fake.archives, secondErr)
	}
	third, thirdErr := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if thirdErr != nil || !third.Committed || !third.ResumedPending || third.PendingArchive != first.PendingArchive || third.BorgCommandCounts["create"] != 0 || third.BorgCommandCounts["rename"] != 1 {
		t.Fatalf("third resumed result = %#v err=%v", third, thirdErr)
	}
}

func TestBorgDirectArchivePendingDiscoveryFailsClosedAndLeavesUnrelatedValidPendingUntouched(t *testing.T) {
	newInterrupted := func(t *testing.T) (directArchiveCloudFixture, *fakeDirectBorg, BorgSnapshotBackend, DirectArchiveResult) {
		t.Helper()
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		fake.failArchiveListStreams = 1
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || result.PendingArchive == "" {
			t.Fatalf("pending fixture result = %#v err=%v", result, err)
		}
		fake.calls = nil
		return fixture, fake, backend, result
	}

	t.Run("multiple matching", func(t *testing.T) {
		fixture, fake, backend, pending := newInterrupted(t)
		second := "__loom-direct-pending-" + pending.ManifestSHA256[:12] + "-cafebabe"
		fake.cloneArchive(pending.PendingArchive, second)
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		assertPendingDiscoveryConflict(t, fake, result, err)
	})

	t.Run("malformed reserved name", func(t *testing.T) {
		fixture, fake, backend, pending := newInterrupted(t)
		fake.archives["__loom-direct-pending-"+pending.ManifestSHA256[:12]+"-short"] = true
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		assertPendingDiscoveryConflict(t, fake, result, err)
	})

	t.Run("unauthenticated reserved name", func(t *testing.T) {
		fixture, fake, backend, pending := newInterrupted(t)
		fake.archives["__loom-direct-pending-"+pending.ManifestSHA256[:12]+"-deadbeef"] = true
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		assertPendingDiscoveryConflict(t, fake, result, err)
	})

	t.Run("same canonical different authenticated identity", func(t *testing.T) {
		fixture, fake, backend, pending := newInterrupted(t)
		fake.archives[pending.PendingArchive] = false
		request := fixture.request
		request.CreatedAt = request.CreatedAt.Add(time.Second)
		conflict := prepareDirectArchiveForTest(t, fixture.cfg, request)
		name := "__loom-direct-pending-" + conflict.ManifestSHA256[:12] + "-decafbad"
		fake.installAuthenticatedPending(name, conflict)
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		assertPendingDiscoveryConflict(t, fake, result, err)
	})

	t.Run("unrelated authenticated pending", func(t *testing.T) {
		fixture, fake, backend, pending := newInterrupted(t)
		request := fixture.request
		request.ArchiveRef = "history-unrelated"
		unrelated := prepareDirectArchiveForTest(t, fixture.cfg, request)
		unrelatedName := "__loom-direct-pending-" + unrelated.ManifestSHA256[:12] + "-0123abcd"
		fake.installAuthenticatedPending(unrelatedName, unrelated)
		result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if err != nil || !result.Committed || !result.ResumedPending || result.PendingArchive != pending.PendingArchive || result.BorgCommandCounts["create"] != 0 || !fake.archives[unrelatedName] {
			t.Fatalf("unrelated pending handling result=%#v archives=%#v err=%v", result, fake.archives, err)
		}
	})
}

func TestBorgDirectArchiveFrozenResumeEnvelopeRejectsStructuralMismatches(t *testing.T) {
	tests := map[string]func(t *testing.T, fixture directArchiveCloudFixture, request *backupstrategy.DirectArchiveRequest){
		"package": func(t *testing.T, _ directArchiveCloudFixture, request *backupstrategy.DirectArchiveRequest) {
			other, schemaHead := createDirectOperationalPackage(t)
			request.OperationalPackage = backupstrategy.DirectArchiveOperationalPackage{
				Path: other.PackageDir, ManifestSHA256: other.ManifestSHA256,
				PackageID: other.Verification.PackageID, ExpectedSchemaHead: &schemaHead,
			}
		},
		"root-name": func(_ *testing.T, _ directArchiveCloudFixture, request *backupstrategy.DirectArchiveRequest) {
			request.Roots[0].Name = "box-renamed"
			request.Exclusions[0].Root = "box-renamed"
		},
		"root-path": func(t *testing.T, _ directArchiveCloudFixture, request *backupstrategy.DirectArchiveRequest) {
			other := filepath.Join(t.TempDir(), "other-root")
			if err := os.Mkdir(other, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other, "other-entry"), []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
			request.Roots[0].Path = other
		},
		"exclusion": func(_ *testing.T, _ directArchiveCloudFixture, request *backupstrategy.DirectArchiveRequest) {
			request.Exclusions[0].RelativePath = "Documents/not-present"
		},
		"created-at": func(_ *testing.T, _ directArchiveCloudFixture, request *backupstrategy.DirectArchiveRequest) {
			request.CreatedAt = request.CreatedAt.Add(time.Second)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			fake := newFakeDirectBorg(t)
			fake.failArchiveListStreams = 1
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
			interrupted, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
			if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.PendingArchive == "" {
				t.Fatalf("interrupted archive = %#v err=%v", interrupted, err)
			}
			request := fixture.request
			request.Roots = append([]backupstrategy.DirectArchiveRoot(nil), fixture.request.Roots...)
			request.Exclusions = append([]backupstrategy.DirectArchiveExclusion(nil), fixture.request.Exclusions...)
			mutate(t, fixture, &request)
			fake.calls = nil
			result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: request})
			assertPendingDiscoveryConflict(t, fake, result, err)
			if !fake.archives[interrupted.PendingArchive] {
				t.Fatalf("structural mismatch removed pending archive: %#v", fake.archives)
			}
		})
	}
}

func TestBorgDirectArchiveSingleHashStreamRejectsContentAndMetadataDiscrepancies(t *testing.T) {
	tests := map[string]func([]directArchiveListItem) []directArchiveListItem{
		"byte": func(items []directArchiveListItem) []directArchiveListItem {
			for index := range items {
				if strings.HasSuffix(items[index].Path, "/payload.txt") {
					items[index].SHA256 = strings.Repeat("0", sha256.Size*2)
					break
				}
			}
			return items
		},
		"metadata": func(items []directArchiveListItem) []directArchiveListItem {
			for index := range items {
				if strings.HasSuffix(items[index].Path, "/payload.txt") {
					items[index].Mode = "-rw-------"
					break
				}
			}
			return items
		},
		"missing": func(items []directArchiveListItem) []directArchiveListItem { return items[1:] },
		"duplicate": func(items []directArchiveListItem) []directArchiveListItem {
			return append(items, items[0])
		},
		"symlink": func(items []directArchiveListItem) []directArchiveListItem {
			for index := range items {
				if strings.HasSuffix(items[index].Path, "/payload-link") {
					items[index].LinkTarget = "different-target"
					break
				}
			}
			return items
		},
		"hardlink": func(items []directArchiveListItem) []directArchiveListItem {
			for index := range items {
				if strings.HasSuffix(items[index].Path, "/payload.txt-hardlink") {
					items[index].LinkTarget = "different-hardlink-target"
					break
				}
			}
			return items
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			fake := newFakeDirectBorg(t)
			fake.afterCreate = func() {
				for archive, payload := range fake.listByArchive {
					items := decodeDirectArchiveListItems(t, payload)
					fake.listByArchive[archive] = encodeDirectArchiveListItems(t, mutate(items))
				}
			}
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
			result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
			if !errors.Is(err, ErrDirectArchiveRetryable) || result.Code != DirectArchiveCodeArchiveInvalid || result.Committed || fake.commandCount("rename") != 0 {
				t.Fatalf("discrepancy result=%#v err=%v calls=%v", result, err, fake.calls)
			}
			if countDirectArchiveContentStreams(fake.calls) != 1 || fake.commandCount("export-tar") != 0 {
				t.Fatalf("verification stream count for %s = %v", name, fake.calls)
			}
		})
	}
}

func TestBorgDirectArchiveV2IncrementalCreateUsesBoundedVerificationAndEvidence(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}

	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatalf("v2 archive returned error: %v", err)
	}
	if !result.Committed || result.Idempotent || result.Manifest != nil || result.ManifestV2 == nil || result.ManifestSchema != backupstrategy.DirectArchiveManifestSchemaV2 || result.VerificationProfile != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 {
		t.Fatalf("v2 result = %#v", result)
	}
	if result.OperationalPackageID != fixture.operational.Verification.PackageID || result.OperationalPackageManifestSHA256 != fixture.operational.ManifestSHA256 || result.ProvenancePackageID != "provenance-direct" || result.ProvenancePackageManifestSHA256 != fixture.provenanceSHA256 {
		t.Fatalf("v2 package evidence = %#v", result)
	}
	if result.PackedBytes != 123 || result.DeduplicatedBytes != 45 || result.BorgCommandCounts["create"] != 1 || result.BorgCommandCounts["rename"] != 1 {
		t.Fatalf("v2 create evidence = %#v", result)
	}
	for _, stage := range []string{"package_preparation", "envelope_preparation", "create", "package_stability", "metadata_check", "envelope_authentication", "rename"} {
		if duration, ok := result.StageDurationsMS[stage]; !ok || duration < 0 {
			t.Fatalf("v2 stage duration %q = %d present=%t", stage, duration, ok)
		}
	}
	joinedArgs := strings.Join(fake.createArgs, " ")
	if !strings.Contains(joinedArgs, "--files-cache ctime,size,inode") || !strings.Contains(joinedArgs, "--files-changed ctime") {
		t.Fatalf("v2 create did not pin Borg cache modes: %s", joinedArgs)
	}
	items := decodeDirectArchiveListItems(t, fake.listByArchive[result.Archive])
	var foundSymlink, foundHardlink bool
	for _, item := range items {
		if strings.Contains(item.Path, ".loom-acceptance") {
			t.Fatalf("v2 archive captured excluded entry %q", item.Path)
		}
		if strings.HasSuffix(item.Path, "/payload-link") && item.Type == "l" && item.LinkTarget == "payload.txt" {
			foundSymlink = true
		}
		if strings.HasSuffix(item.Path, "/payload.txt-hardlink") && item.Type == "-" && item.LinkTarget != "" {
			foundHardlink = true
		}
	}
	if !foundSymlink || !foundHardlink {
		t.Fatalf("v2 Borg capture lost link topology: symlink=%t hardlink=%t", foundSymlink, foundHardlink)
	}
	assertV2DirectArchiveBoundedCommands(t, fake.calls, result.PendingArchive)
	if _, err := os.Stat(directArchiveResumeMarkerPath(fixture.cfg, prepareDirectArchiveV2ForTest(t, fixture.cfg, request))); !os.IsNotExist(err) {
		t.Fatalf("successful v2 commit retained resume marker: %v", err)
	}

	fake.calls = nil
	replay, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil || !replay.Committed || !replay.Idempotent || replay.BorgCommandCounts["create"] != 0 || replay.BorgCommandCounts["rename"] != 0 || replay.ManifestSHA256 != result.ManifestSHA256 {
		t.Fatalf("v2 canonical replay = %#v err=%v", replay, err)
	}
	assertV2DirectArchiveBoundedCommands(t, fake.calls, replay.Archive)
}

func TestBorgDirectArchiveV2AddRemoveAndMetadataOnlyCases(t *testing.T) {
	tests := map[string]func(t *testing.T, fixture directArchiveCloudFixture){
		"add": func(t *testing.T, fixture directArchiveCloudFixture) {
			if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "added.txt"), []byte("added"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"remove": func(t *testing.T, fixture directArchiveCloudFixture) {
			if err := os.Remove(filepath.Join(fixture.root, "Documents", "payload-link")); err != nil {
				t.Fatal(err)
			}
		},
		"metadata-only": func(t *testing.T, fixture directArchiveCloudFixture) {
			if err := os.Chmod(filepath.Join(fixture.root, "Documents", "payload.txt"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			mutate(t, fixture)
			request := directArchiveV2Request(fixture)
			fake := newFakeDirectBorg(t)
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
			result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
			if err != nil || !result.Committed || result.BorgCommandCounts["create"] != 1 || result.BorgCommandCounts["rename"] != 1 {
				t.Fatalf("v2 %s case = %#v err=%v", name, result, err)
			}
			assertV2DirectArchiveBoundedCommands(t, fake.calls, result.PendingArchive)
		})
	}
}

func TestBorgDirectArchiveV2PendingResumeRequiresTrustedLocalEvidence(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	fake.failChecksRemaining = 1
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}

	interrupted, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.Code != DirectArchiveCodeArchiveInvalid || interrupted.Committed || interrupted.PendingArchive == "" || !fake.archives[interrupted.PendingArchive] || fake.commandCount("create") != 1 || fake.commandCount("rename") != 0 {
		t.Fatalf("v2 interrupted verification = %#v archives=%#v err=%v", interrupted, fake.archives, err)
	}
	prepared := prepareDirectArchiveV2ForTest(t, fixture.cfg, request)
	if err := verifyDirectArchiveResumeMarker(fixture.cfg, interrupted.PendingArchive, prepared); err != nil {
		t.Fatalf("v2 resume marker missing: %v", err)
	}

	fake.calls = nil
	resumed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil || !resumed.Committed || !resumed.ResumedPending || resumed.Idempotent || resumed.PendingArchive != interrupted.PendingArchive || resumed.BorgCommandCounts["create"] != 0 || resumed.BorgCommandCounts["rename"] != 1 {
		t.Fatalf("v2 pending resume = %#v err=%v", resumed, err)
	}
	if resumed.PackedBytes != 123 || resumed.DeduplicatedBytes != 45 || resumed.Checks["source_stability"] != "borg_create_no_second_source_pass" || resumed.Checks["package_stability"] != DirectArchiveStatusSucceeded {
		t.Fatalf("v2 resume evidence = %#v", resumed)
	}
	assertV2DirectArchiveBoundedCommands(t, fake.calls, interrupted.PendingArchive)
	if err := verifyDirectArchiveResumeMarker(fixture.cfg, interrupted.PendingArchive, prepared); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v2 committed resume marker survived: %v", err)
	}
}

func TestBorgDirectArchiveV2RequestPreservesV1CanonicalReplayAndPendingResume(t *testing.T) {
	t.Run("canonical replay", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		legacy, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if err != nil || !legacy.Committed || legacy.ManifestSchema != backupstrategy.DirectArchiveManifestSchema {
			t.Fatalf("create v1 canonical fixture = %#v err=%v", legacy, err)
		}
		fake.calls = nil
		request := directArchiveV2Request(fixture)
		replay, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err != nil || !replay.Committed || !replay.Idempotent || replay.ManifestSchema != backupstrategy.DirectArchiveManifestSchema || replay.VerificationProfile != "" || replay.BorgCommandCounts["create"] != 0 || replay.BorgCommandCounts["rename"] != 0 || countDirectArchiveContentStreams(fake.calls) != 1 {
			t.Fatalf("v1 canonical replay through v2 request = %#v err=%v calls=%v", replay, err, fake.calls)
		}
	})

	t.Run("pending resume", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fake := newFakeDirectBorg(t)
		fake.failArchiveListStreams = 1
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		legacyPending, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || legacyPending.PendingArchive == "" || !fake.archives[legacyPending.PendingArchive] {
			t.Fatalf("create v1 pending fixture = %#v err=%v", legacyPending, err)
		}
		fake.calls = nil
		request := directArchiveV2Request(fixture)
		resumed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err != nil || !resumed.Committed || !resumed.ResumedPending || resumed.ManifestSchema != backupstrategy.DirectArchiveManifestSchema || resumed.VerificationProfile != "" || resumed.BorgCommandCounts["create"] != 0 || resumed.BorgCommandCounts["rename"] != 1 || countDirectArchiveContentStreams(fake.calls) != 1 {
			t.Fatalf("v1 pending resume through v2 request = %#v err=%v calls=%v", resumed, err, fake.calls)
		}
	})
}

func TestBorgDirectArchiveV2SharedRemoteLockConflictFailsBeforeBorg(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.RemoteLockEffectfulWaitSeconds = 1
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	var (
		result DirectArchiveResult
		runErr error
	)
	err := WithRemoteLock(context.Background(), fixture.cfg, RemoteLockOptions{Operation: "test-holder"}, func(context.Context) error {
		result, runErr = backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		return nil
	})
	if err != nil {
		t.Fatalf("hold shared remote lock: %v", err)
	}
	if !errors.Is(runErr, ErrDirectArchiveRetryable) || result.Code != DirectArchiveCodeRemoteFailure || !result.Retryable || result.Committed || len(fake.calls) != 0 {
		t.Fatalf("v2 shared lock conflict = %#v err=%v calls=%v", result, runErr, fake.calls)
	}
}

func TestBorgDirectArchiveV2WarningAndPackageDriftRemainFailures(t *testing.T) {
	t.Run("missing quick statistics", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		request := directArchiveV2Request(fixture)
		fake := newFakeDirectBorg(t)
		fake.createOutput = []byte(`{"archive":{"stats":{"compressed_size":123}}}`)
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		failed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err == nil || failed.Code != DirectArchiveCodeArchiveInvalid || failed.Retryable || failed.Committed || fake.commandCount("create") != 1 || fake.commandCount("rename") != 0 {
			t.Fatalf("v2 incomplete quick stats result = %#v err=%v", failed, err)
		}
	})

	t.Run("Borg non-zero pending is not resumable", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		request := directArchiveV2Request(fixture)
		fake := newFakeDirectBorg(t)
		fake.failCreateAfterDurable = fakeBorgExitError{code: 1}
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		failed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err == nil || failed.Code != DirectArchiveCodeSourceUnstable || failed.Retryable || failed.Committed || !fake.archives[failed.PendingArchive] || fake.commandCount("rename") != 0 {
			t.Fatalf("v2 warning result = %#v err=%v", failed, err)
		}
		fake.failCreateAfterDurable = nil
		fake.calls = nil
		retry, retryErr := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if !errors.Is(retryErr, ErrDirectArchiveConflict) || retry.Committed || fake.commandCount("create") != 0 || fake.commandCount("rename") != 0 {
			t.Fatalf("v2 warning pending was resumed: result=%#v err=%v calls=%v", retry, retryErr, fake.calls)
		}
	})

	t.Run("bounded package drift", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		request := directArchiveV2Request(fixture)
		fake := newFakeDirectBorg(t)
		fake.afterCreate = func() {
			if err := os.WriteFile(filepath.Join(fixture.operational.PackageDir, backupstrategy.OperationalManifestFile), []byte("tampered after create\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
		failed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if !errors.Is(err, ErrDirectArchiveRetryable) || failed.Code != DirectArchiveCodeSourceUnstable || failed.Committed || failed.Checks["package_stability"] != DirectArchiveStatusFailed || fake.commandCount("rename") != 0 {
			t.Fatalf("v2 package drift result = %#v err=%v", failed, err)
		}
	})
}

func TestBorgDirectArchiveV2DoesNotPerformSecondUserRootPreparation(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	fake.afterCreate = func() {
		if err := os.RemoveAll(fixture.root); err != nil {
			t.Fatal(err)
		}
	}
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil || !result.Committed || result.Checks["source_stability"] != "borg_create_no_second_source_pass" {
		t.Fatalf("v2 routine path re-read live user roots: result=%#v err=%v", result, err)
	}
	assertV2DirectArchiveBoundedCommands(t, fake.calls, result.PendingArchive)
}

func assertV2DirectArchiveBoundedCommands(t *testing.T, calls []string, archive string) {
	t.Helper()
	foundArchivesOnly := false
	for _, call := range calls {
		if strings.HasPrefix(call, "list:") && (strings.Contains(call, "--json-lines") || strings.Contains(call, "{sha256}")) {
			t.Fatalf("v2 routine used archive-wide metadata/hash list: %v", calls)
		}
		if strings.HasPrefix(call, "export-tar:") || strings.HasPrefix(call, "delete:") || strings.HasPrefix(call, "prune:") || strings.HasPrefix(call, "compact:") {
			t.Fatalf("v2 routine used forbidden Borg command: %v", calls)
		}
		if strings.HasPrefix(call, "check:") && strings.Contains(call, "--archives-only") && strings.Contains(call, "::"+archive) {
			foundArchivesOnly = true
		}
	}
	if !foundArchivesOnly {
		t.Fatalf("v2 exact archives-only check missing: %v", calls)
	}
}

func productionDirectArchiveEvidenceCreatedAt(t *testing.T) time.Time {
	t.Helper()
	createdAt, err := time.Parse(time.RFC3339Nano, "2026-09-01T01:00:00.460330646Z")
	if err != nil {
		t.Fatal(err)
	}
	return createdAt
}

func assertDirectArchiveEvidenceTimestamp(t *testing.T, root string, want int64) {
	t.Helper()
	for _, relative := range []string{
		backupstrategy.DirectArchiveEvidenceDir,
		path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile),
		path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile),
	} {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("stat generated evidence %q: %v", relative, err)
		}
		if got := info.ModTime().UnixNano(); got != want {
			t.Fatalf("generated evidence %q mtime = %d; want %d", relative, got, want)
		}
	}
}

func TestBorgDirectArchiveV2DisposableRepositoryIncrementalCreateRepeatChangeAndPendingResume(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LOOM_TEST_BORG_BINARY"))
	if binary == "" {
		t.Skip("set LOOM_TEST_BORG_BINARY to run the disposable Borg 1.4.3 end-to-end")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "borg 1.4.3" {
		t.Fatalf("isolated gate requires Borg 1.4.3, got %q err=%v", strings.TrimSpace(string(version)), err)
	}
	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.Snapshots.Borg.Binary = binary
	fixture.cfg.Snapshots.Borg.Repository = filepath.Join(t.TempDir(), "borg-repository")
	fixture.cfg.Snapshots.Borg.Compression = "none"
	runner := NewBorgCommandRunner(fixture.cfg)
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
		t.Fatalf("initialize disposable Borg repository: %v", err)
	}
	backend := BorgSnapshotBackend{Runner: runner}

	firstRequest := directArchiveV2Request(fixture)
	firstRequest.ArchiveRef = "history-v2-first"
	first, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &firstRequest})
	if err != nil || !first.Committed || first.BorgCommandCounts["create"] != 1 || first.BorgCommandCounts["rename"] != 1 || first.PackedBytes <= 0 || first.DeduplicatedBytes < 0 {
		t.Fatalf("first disposable v2 archive = %#v err=%v", first, err)
	}
	if first.BorgCommandCounts["export-tar"] != 0 || first.BorgCommandCounts["delete"] != 0 || first.BorgCommandCounts["prune"] != 0 || first.BorgCommandCounts["compact"] != 0 {
		t.Fatalf("first disposable v2 forbidden commands = %#v", first.BorgCommandCounts)
	}

	replay, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &firstRequest})
	if err != nil || !replay.Committed || !replay.Idempotent || replay.BorgCommandCounts["create"] != 0 || replay.BorgCommandCounts["rename"] != 0 {
		t.Fatalf("disposable v2 canonical repeat = %#v err=%v", replay, err)
	}

	unchangedRequest := firstRequest
	unchangedRequest.ArchiveRef = "history-v2-unchanged"
	unchangedRequest.CreatedAt = unchangedRequest.CreatedAt.Add(time.Second)
	unchanged, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &unchangedRequest})
	if err != nil || !unchanged.Committed || unchanged.BorgCommandCounts["create"] != 1 || unchanged.BorgCommandCounts["rename"] != 1 {
		t.Fatalf("disposable v2 unchanged incremental archive = %#v err=%v", unchanged, err)
	}

	if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "payload.txt"), []byte("one changed file"), 0o640); err != nil {
		t.Fatal(err)
	}
	changedRequest := firstRequest
	changedRequest.ArchiveRef = "history-v2-changed"
	changedRequest.CreatedAt = changedRequest.CreatedAt.Add(2 * time.Second)
	changed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &changedRequest})
	if err != nil || !changed.Committed || changed.BorgCommandCounts["create"] != 1 || changed.BorgCommandCounts["rename"] != 1 || changed.PackedBytes <= 0 {
		t.Fatalf("disposable v2 one-file change archive = %#v err=%v", changed, err)
	}

	pendingRequest := changedRequest
	pendingRequest.ArchiveRef = "history-v2-pending"
	pendingRequest.CreatedAt = pendingRequest.CreatedAt.Add(time.Second)
	interruptedCtx, cancelInterrupted := context.WithCancel(context.Background())
	interruptedRunner := NewBorgCommandRunner(fixture.cfg)
	interruptedRunner.Exec = func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		out, runErr := defaultBorgExec(ctx, binary, command, env)
		if command.Name() == "create" && runErr == nil {
			cancelInterrupted()
			return out, context.Canceled
		}
		return out, runErr
	}
	interruptedBackend := BorgSnapshotBackend{Runner: interruptedRunner}
	interrupted, err := interruptedBackend.ArchiveCanonicalRoots(interruptedCtx, DirectArchiveInput{Config: fixture.cfg, RequestV2: &pendingRequest})
	if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.Committed || interrupted.PendingArchive == "" || interrupted.BorgCommandCounts["create"] != 1 || interrupted.BorgCommandCounts["rename"] != 0 {
		t.Fatalf("disposable v2 interrupted pending = %#v err=%v", interrupted, err)
	}
	resumed, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &pendingRequest})
	if err != nil || !resumed.Committed || !resumed.ResumedPending || resumed.PendingArchive != interrupted.PendingArchive || resumed.BorgCommandCounts["create"] != 0 || resumed.BorgCommandCounts["rename"] != 1 {
		t.Fatalf("disposable v2 pending resume = %#v err=%v", resumed, err)
	}
	for label, result := range map[string]DirectArchiveResult{"first": first, "replay": replay, "unchanged": unchanged, "changed": changed, "resumed": resumed} {
		if result.ManifestSchema != backupstrategy.DirectArchiveManifestSchemaV2 || result.VerificationProfile != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 || result.BorgCommandCounts["export-tar"] != 0 || result.BorgCommandCounts["delete"] != 0 || result.BorgCommandCounts["prune"] != 0 || result.BorgCommandCounts["compact"] != 0 || result.BorgCommandCounts["list"] < 1 {
			t.Fatalf("%s disposable v2 evidence = %#v", label, result)
		}
	}
}

func TestBorgDirectArchiveV2DisposableRepositoryHistoricalAndCurrentStrictRestore(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LOOM_TEST_BORG_BINARY"))
	if binary == "" {
		t.Skip("set LOOM_TEST_BORG_BINARY to run the disposable Borg 1.4.3 historical/current restore acceptance")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "borg 1.4.3" {
		t.Fatalf("isolated gate requires Borg 1.4.3, got %q err=%v", strings.TrimSpace(string(version)), err)
	}
	for _, test := range []struct {
		name       string
		schemaHead int
		archiveRef string
	}{
		{name: "retained_head_6", schemaHead: 6, archiveRef: "history-45624d0d87ed29798754f1d6"},
		{name: "current_head_7", schemaHead: provenance.SchemaHead, archiveRef: "history-current-head-7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixtureAtProvenanceHead(t, test.schemaHead)
			fixture.cfg.Snapshots.Borg.Binary = binary
			fixture.cfg.Snapshots.Borg.Repository = filepath.Join(t.TempDir(), "borg-repository")
			fixture.cfg.Snapshots.Borg.Compression = "none"
			runner := NewBorgCommandRunner(fixture.cfg)
			if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
				t.Fatalf("initialize disposable Borg repository: %v", err)
			}
			backend := BorgSnapshotBackend{Runner: runner}
			request := directArchiveV2Request(fixture)
			request.ArchiveRef = test.archiveRef
			request.CreatedAt = productionDirectArchiveEvidenceCreatedAt(t)
			archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
			if err != nil || !archived.Committed {
				t.Fatalf("create head-%d archive = %#v err=%v", test.schemaHead, archived, err)
			}
			target := filepath.Join(t.TempDir(), "strict-fetch")
			fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
			if err != nil || fetched.Status != SnapshotStatusSucceeded || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
				t.Fatalf("fetch head-%d archive = %#v err=%v", test.schemaHead, fetched, err)
			}
			assertDirectArchiveEvidenceTimestamp(t, target, 1788224400460330000)
			authority := &cloudRestoreAuthorityStub{}
			directInput := backup.DirectArchiveRestoreDrillInput{
				ArchiveRoot: target, ExpectedManifestSHA256: archived.ManifestSHA256,
				ExpectedRepository: fixture.cfg.Snapshots.Borg.Repository, ExpectedArchive: archived.Archive,
				V2UserSymlinkTargets: fetched.V2UserSymlinkTargets,
				OperationalTarget:    "loom_restore_drill_borg_acceptance", ActiveDatabase: "loom_main", Owner: "loom",
				Authority: authority,
				Runner: func(_ context.Context, name string, _ []string, _ io.Reader) ([]byte, error) {
					if name != "psql" {
						return nil, fmt.Errorf("unexpected operational verification command %q", name)
					}
					return []byte(`{"nodes":1,"worker_instances":1}`), nil
				},
				ProvenanceRestore: exactRecoveryStep,
				Now:               func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) },
			}
			plan, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), directInput)
			if err != nil || plan.ProvenanceVerification.SchemaHead != test.schemaHead {
				t.Fatalf("plan head-%d restore = %#v err=%v", test.schemaHead, plan, err)
			}
			restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), directInput)
			if err != nil || restored.Status != SnapshotStatusSucceeded || restored.ProvenanceRecovery.SchemaHead != int64(test.schemaHead) {
				t.Fatalf("strict head-%d restore = %#v err=%v", test.schemaHead, restored, err)
			}
			if len(authority.restores) != 1 || authority.restores[0].Kind != restoreauthority.KindOperational || authority.restores[0].Database != "loom_restore_drill_borg_acceptance" || len(authority.drops) != 1 || authority.drops[0].Database != "loom_restore_drill_borg_acceptance" {
				t.Fatalf("strict Borg restore authority calls: restores=%#v drops=%#v", authority.restores, authority.drops)
			}
		})
	}
}

func TestBorgDirectArchiveV2GeneratedEvidenceTimestampAndMetadataRemainExact(t *testing.T) {
	fixture := newDirectArchiveCloudFixtureAtProvenanceHead(t, 6)
	request := directArchiveV2Request(fixture)
	request.ArchiveRef = "history-45624d0d87ed29798754f1d6"
	request.CreatedAt = productionDirectArchiveEvidenceCreatedAt(t)
	prepared := prepareDirectArchiveV2ForTest(t, fixture.cfg, request)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil || !archived.Committed {
		t.Fatalf("create production-timestamp fixture = %#v err=%v", archived, err)
	}
	if !bytes.Equal(fake.manifestByArchive[archived.Archive], prepared.ManifestBytes) || archived.ManifestSHA256 != prepared.ManifestSHA256 {
		t.Fatal("generated-evidence timestamp contract changed authenticated manifest bytes")
	}
	target := filepath.Join(t.TempDir(), "exact-fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil || fetched.Status != SnapshotStatusSucceeded || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
		t.Fatalf("fetch production-timestamp fixture = %#v err=%v", fetched, err)
	}
	assertDirectArchiveEvidenceTimestamp(t, target, 1788224400460330000)

	originalTar := append([]byte(nil), fake.tarByArchive[archived.Archive]...)
	tests := []struct {
		name string
		path string
		edit func(*tar.Header, []byte) []byte
	}{
		{name: "directory mtime", path: backupstrategy.DirectArchiveEvidenceDir, edit: func(header *tar.Header, body []byte) []byte {
			header.ModTime = header.ModTime.Add(time.Microsecond)
			return body
		}},
		{name: "manifest mtime", path: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile), edit: func(header *tar.Header, body []byte) []byte {
			header.ModTime = header.ModTime.Add(time.Microsecond)
			return body
		}},
		{name: "hash mtime", path: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile), edit: func(header *tar.Header, body []byte) []byte {
			header.ModTime = header.ModTime.Add(time.Microsecond)
			return body
		}},
		{name: "manifest mode", path: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile), edit: func(header *tar.Header, body []byte) []byte {
			header.Mode = 0o640
			return body
		}},
		{name: "hash type", path: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile), edit: func(header *tar.Header, _ []byte) []byte {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = backupstrategy.DirectArchiveManifestFile
			header.Size = 0
			return nil
		}},
		{name: "manifest hash", path: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile), edit: func(_ *tar.Header, body []byte) []byte {
			body[0] ^= 1
			return body
		}},
		{name: "hash size", path: path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile), edit: func(header *tar.Header, body []byte) []byte {
			body = append(body, '\n')
			header.Size = int64(len(body))
			return body
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake.tarByArchive[archived.Archive] = mutateFakeDirectTarEntry(t, originalTar, test.path, test.edit)
			drifted, err := backend.Fetch(context.Background(), SnapshotFetchInput{
				Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: filepath.Join(t.TempDir(), "drifted-fetch"),
			})
			if err != nil || drifted.Status != SnapshotStatusFailed || drifted.ExtractionVerification == nil || len(drifted.ExtractionVerification.Errors) == 0 {
				t.Fatalf("%s drift did not fail exact extraction: result=%#v err=%v", test.name, drifted, err)
			}
		})
	}
	fake.tarByArchive[archived.Archive] = originalTar
}

func TestBorgDirectArchiveV1GeneratedEvidenceTimestampUsesSharedContract(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := fixture.request
	request.ArchiveRef = "history-v1-production-evidence-timestamp"
	request.CreatedAt = productionDirectArchiveEvidenceCreatedAt(t)
	prepared := prepareDirectArchiveForTest(t, fixture.cfg, request)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: request})
	if err != nil || !archived.Committed {
		t.Fatalf("create v1 production-timestamp fixture = %#v err=%v", archived, err)
	}
	if !bytes.Equal(fake.manifestByArchive[archived.Archive], prepared.ManifestBytes) || archived.ManifestSHA256 != prepared.ManifestSHA256 {
		t.Fatal("v1 generated-evidence timestamp contract changed authenticated manifest bytes")
	}
	target := filepath.Join(t.TempDir(), "v1-exact-fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil || fetched.Status != SnapshotStatusSucceeded || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
		t.Fatalf("fetch v1 production-timestamp fixture = %#v err=%v", fetched, err)
	}
	assertDirectArchiveEvidenceTimestamp(t, target, 1788224400460330000)
}

func TestBorgDirectArchiveV2ExternalLeafSymlinkDisposableBorg143(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LOOM_TEST_BORG_BINARY"))
	if binary == "" {
		t.Skip("set LOOM_TEST_BORG_BINARY to run the disposable Borg 1.4.3 external leaf-symlink acceptance")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "borg 1.4.3" {
		t.Fatalf("isolated gate requires Borg 1.4.3, got %q err=%v", strings.TrimSpace(string(version)), err)
	}

	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.Snapshots.Borg.Binary = binary
	fixture.cfg.Snapshots.Borg.Repository = filepath.Join(t.TempDir(), "borg-repository")
	fixture.cfg.Snapshots.Borg.Compression = "none"
	acceptanceRoot := t.TempDir()
	v2FetchRoot := filepath.Join(acceptanceRoot, "v2-fetch")
	links := installDirectArchiveExternalLeafSymlinks(t, fixture.root, v2FetchRoot)
	links.lock(t)
	runner := NewBorgCommandRunner(fixture.cfg)
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
		t.Fatalf("initialize disposable Borg repository: %v", err)
	}
	backend := BorgSnapshotBackend{Runner: runner}

	v2Request := directArchiveV2Request(fixture)
	v2Request.ArchiveRef = "history-v2-external-leaf-links"
	v2Archive, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &v2Request})
	if err != nil || !v2Archive.Committed {
		t.Fatalf("create disposable v2 leaf-link archive = %#v err=%v", v2Archive, err)
	}
	v2Fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{
		Config: fixture.cfg, NodeID: "loom-main", Ref: v2Archive.Archive, To: v2FetchRoot,
	})
	if err != nil || v2Fetched.Status != SnapshotStatusSucceeded || v2Fetched.ExtractionVerification == nil || v2Fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
		t.Fatalf("fetch disposable v2 leaf-link archive = %#v err=%v", v2Fetched, err)
	}
	links.assertRestored(t, v2FetchRoot, fixture.root)
	links.assertOutsideUnchanged(t)
	v2RestoreInput := backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: v2FetchRoot, ExpectedManifestSHA256: v2Fetched.DirectArchiveManifestSHA256,
		ExpectedRepository: v2Fetched.Repository, ExpectedArchive: v2Fetched.Archive,
		V2UserSymlinkTargets: v2Fetched.V2UserSymlinkTargets,
		OperationalTarget:    "loom_restore_drill_external_links", ActiveDatabase: "loom_main", Owner: "loom",
		OperationalRestore: exactRecoveryStep, ProvenanceRestore: exactRecoveryStep,
	}
	if plan, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), v2RestoreInput); err != nil || plan.Status != "planned" {
		t.Fatalf("plan disposable v2 leaf-link restore = %#v err=%v", plan, err)
	}
	if restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), v2RestoreInput); err != nil || restored.Status != SnapshotStatusSucceeded {
		t.Fatalf("run disposable v2 leaf-link restore = %#v err=%v", restored, err)
	}
	links.assertOutsideUnchanged(t)

	v1Request := fixture.request
	v1Request.ArchiveRef = "history-v1-external-leaf-links"
	v1Request.CreatedAt = v1Request.CreatedAt.Add(time.Second)
	v1Archive, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: v1Request})
	if err != nil || !v1Archive.Committed {
		t.Fatalf("create disposable v1 leaf-link archive = %#v err=%v", v1Archive, err)
	}
	v1FetchRoot := filepath.Join(acceptanceRoot, "v1-fetch")
	v1Fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{
		Config: fixture.cfg, NodeID: "loom-main", Ref: v1Archive.Archive, To: v1FetchRoot,
	})
	if err != nil || v1Fetched.Status != SnapshotStatusSucceeded || v1Fetched.DirectArchiveManifest == nil || v1Fetched.ExtractionVerification == nil || v1Fetched.ExtractionVerification.Status != SnapshotStatusSucceeded || v1Fetched.V2UserSymlinkTargets != nil {
		t.Fatalf("fetch disposable v1 leaf-link archive = %#v err=%v", v1Fetched, err)
	}
	links.assertRestored(t, v1FetchRoot, fixture.root)
	links.assertOutsideUnchanged(t)
}

func TestBorgDirectArchiveV2RetainedProductionMetadataUsesHistoricalHeadSixFixture(t *testing.T) {
	fixture := newDirectArchiveCloudFixtureAtProvenanceHead(t, 6)
	request := directArchiveV2Request(fixture)
	request.ArchiveRef = "history-45624d0d87ed29798754f1d6"
	request.CreatedAt = productionDirectArchiveEvidenceCreatedAt(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := backupstrategy.ParseAuthenticatedDirectArchiveManifest(fake.manifestByArchive[archived.Archive], fake.hashByArchive[archived.Archive])
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ManifestV2 == nil || prepared.ManifestV2.ArchiveRef != "history-45624d0d87ed29798754f1d6" || prepared.ManifestV2.ProvenancePackage.SchemaHead != 6 || !prepared.ManifestV2.CreatedAt.Equal(request.CreatedAt) {
		t.Fatalf("retained production metadata fixture = %#v", prepared.ManifestV2)
	}
	target := filepath.Join(t.TempDir(), "retained-head-6-fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil || fetched.Status != SnapshotStatusSucceeded || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
		t.Fatalf("retained production metadata fetch = %#v err=%v", fetched, err)
	}
	assertDirectArchiveEvidenceTimestamp(t, target, 1788224400460330000)
}

func TestBorgDirectArchiveDisposableRepository(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LOOM_TEST_BORG_BINARY"))
	if binary == "" {
		t.Skip("set LOOM_TEST_BORG_BINARY to run the disposable Borg end-to-end")
	}
	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.Snapshots.Borg.Binary = binary
	fixture.cfg.Snapshots.Borg.Repository = filepath.Join(t.TempDir(), "borg-repository")
	fixture.cfg.Snapshots.Borg.Compression = "none"
	frozenPrepared := prepareDirectArchiveForTest(t, fixture.cfg, fixture.request)
	runner := NewBorgCommandRunner(fixture.cfg)
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
		t.Fatalf("initialize disposable Borg repository: %v", err)
	}
	interruptedCtx, cancelInterrupted := context.WithCancel(context.Background())
	interruptedRunner := NewBorgCommandRunner(fixture.cfg)
	interruptedRunner.Exec = func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		out, err := defaultBorgExec(ctx, binary, command, env)
		if command.Name() == "create" && err == nil {
			cancelInterrupted()
			return out, context.Canceled
		}
		return out, err
	}
	interruptedBackend := BorgSnapshotBackend{Runner: interruptedRunner}
	interrupted, err := interruptedBackend.ArchiveCanonicalRoots(interruptedCtx, DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || interrupted.PendingArchive == "" || interrupted.Committed {
		t.Fatalf("interrupted disposable create result = %#v err=%v", interrupted, err)
	}
	archives, err := listBorgArchives(context.Background(), runner)
	if err != nil || !borgArchiveExists(archives, interrupted.PendingArchive) || borgArchiveExists(archives, interrupted.Archive) {
		t.Fatalf("disposable pending inventory = %#v err=%v", archives, err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "payload.txt"), []byte("live bytes after interrupted create"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "post-pending-live-entry"), []byte("live-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	livePrepared := prepareDirectArchiveForTest(t, fixture.cfg, fixture.request)
	if livePrepared.ManifestSHA256 == interrupted.ManifestSHA256 {
		t.Fatal("disposable live mutation did not change the fresh manifest")
	}

	backend := BorgSnapshotBackend{Runner: runner}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatalf("resume direct archive against disposable Borg repository: %v", err)
	}
	if !result.Committed || result.Idempotent || !result.ResumedPending || result.PendingArchive != interrupted.PendingArchive || result.Manifest == nil || result.ManifestSHA256 != interrupted.ManifestSHA256 || result.ManifestSHA256 == livePrepared.ManifestSHA256 || result.BorgCommandCounts["create"] != 0 || result.BorgCommandCounts["rename"] != 1 || result.BorgCommandCounts["delete"] != 0 || result.BorgCommandCounts["export-tar"] != 0 {
		t.Fatalf("disposable Borg result = %#v", result)
	}
	if result.Checks["resume_envelope"] != DirectArchiveStatusSucceeded || result.Checks["source_stability"] != "frozen_pending_not_rechecked" {
		t.Fatalf("disposable resume checks = %#v", result.Checks)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "post-canonical-live-entry"), []byte("later-live-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	replay, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil || !replay.Idempotent || !replay.Committed || replay.ManifestSHA256 != interrupted.ManifestSHA256 || replay.Manifest == nil || replay.Manifest.SourceSnapshotSHA256 != frozenPrepared.Manifest.SourceSnapshotSHA256 {
		t.Fatalf("disposable Borg replay = %#v err=%v", replay, err)
	}
	if replay.Checks["resume_envelope"] != DirectArchiveStatusSucceeded || replay.Checks["source_stability"] != "frozen_canonical_not_rechecked" {
		t.Fatalf("disposable replay checks = %#v", replay.Checks)
	}

	target := filepath.Join(t.TempDir(), "fetch")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"extract", "::" + result.Archive}, Dir: target}); err != nil {
		t.Fatalf("fetch disposable Borg archive: %v", err)
	}
	restoredRoot := filepath.Join(target, filepath.FromSlash(directArchiveTestPath(fixture.root)))
	restoredPayload := filepath.Join(restoredRoot, "Documents", "payload.txt")
	payload, err := os.ReadFile(restoredPayload)
	if err != nil || string(payload) != "canonical bytes" {
		t.Fatalf("restored canonical payload = %q err=%v", payload, err)
	}
	if info, err := os.Lstat(restoredPayload); err != nil || info.Mode().Perm() != 0o640 || !info.ModTime().UTC().Equal(fixture.payloadMTime) {
		t.Fatalf("restored canonical mode = %#v err=%v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(restoredRoot, "Documents", "payload-link")); err != nil || target != "payload.txt" {
		t.Fatalf("restored canonical symlink = %q err=%v", target, err)
	}
	restoredHardlink, err := os.Lstat(filepath.Join(restoredRoot, "Documents", "payload.txt-hardlink"))
	restoredPayloadInfo, payloadInfoErr := os.Lstat(restoredPayload)
	if err != nil || payloadInfoErr != nil || !os.SameFile(restoredHardlink, restoredPayloadInfo) {
		t.Fatalf("restored canonical hard link was not preserved: hardlink=%#v payload=%#v err=%v payloadErr=%v", restoredHardlink, restoredPayloadInfo, err, payloadInfoErr)
	}
	if _, err := os.Lstat(filepath.Join(restoredRoot, ".loom-acceptance")); !os.IsNotExist(err) {
		t.Fatalf("excluded subtree was restored: %v", err)
	}
	restoredOperational := filepath.Join(target, filepath.FromSlash(directArchiveTestPath(fixture.operational.PackageDir)))
	verification, err := backupstrategy.VerifyOperationalPackage(context.Background(), backupstrategy.OperationalPackageVerificationInput{
		PackageDir: restoredOperational, ExpectedManifestSHA256: fixture.operational.ManifestSHA256,
		ExpectedPackageID: fixture.operational.Verification.PackageID,
	})
	if err != nil || verification.Status != "succeeded" {
		t.Fatalf("restored operational package verification = %#v err=%v", verification.Findings, err)
	}
	restoredProvenance := filepath.Join(target, filepath.FromSlash(directArchiveTestPath(fixture.provenanceDir)))
	provenanceVerification, err := maintenance.VerifyProvenanceBackupPackage(context.Background(), restoredProvenance, fixture.provenanceSHA256)
	if err != nil || provenanceVerification.Status != maintenance.VerificationSucceeded || provenanceVerification.ManifestSHA256 != fixture.provenanceSHA256 {
		t.Fatalf("restored provenance package verification = %#v err=%v", provenanceVerification, err)
	}
}

func TestBorgSnapshotListKeepsLegacyAndCommittedDirectNamespacesSeparate(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	fake.archives["__loom-direct-pending-deadbeef0000-12345678"] = true
	fake.archives["__loom-direct-loom-main-history-new"] = true
	fake.archives["loom-main-history-committed"] = true
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	result, err := backend.List(context.Background(), SnapshotListInput{Config: fixture.cfg, NodeID: "loom-main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshots) != 2 || result.Snapshots[0].ArchiveClass != "legacy_unclassified" || result.Snapshots[1].Archive != "loom-main-history-committed" {
		t.Fatalf("snapshot namespaces = %#v", result.Snapshots)
	}
}

func TestBorgDirectArchiveFetchAndStrictOperationalProvenanceRestore(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "isolated-fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil {
		t.Fatalf("strict direct fetch: %v", err)
	}
	if fetched.Status != SnapshotStatusSucceeded || fetched.DirectArchiveManifestSHA256 != archived.ManifestSHA256 || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != "succeeded" || fetched.V2UserSymlinkTargets != nil {
		if fetched.ExtractionVerification != nil {
			t.Fatalf("fetch extraction evidence = %#v", *fetched.ExtractionVerification)
		}
		t.Fatalf("fetch evidence = %#v", fetched)
	}
	directInput := backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: target, ExpectedManifestSHA256: archived.ManifestSHA256,
		ExpectedRepository: fixture.cfg.Snapshots.Borg.Repository, ExpectedArchive: archived.Archive,
		OperationalTarget: "loom_restore_drill_direct", ActiveDatabase: "loom_main", Owner: "loom",
		OperationalRestore: exactRecoveryStep, ProvenanceRestore: exactRecoveryStep,
		Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	}
	plan, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), directInput)
	if err != nil || plan.ManifestSHA256 != archived.ManifestSHA256 || plan.ProvenancePackageID != "provenance-direct" {
		t.Fatalf("direct restore plan = %#v err=%v", plan, err)
	}
	restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), directInput)
	if err != nil || restored.Status != "succeeded" || restored.OperationalRecovery.Status != "succeeded" || restored.ProvenanceRecovery.Status != "succeeded" {
		t.Fatalf("strict restore = %#v err=%v", restored, err)
	}
	evidence, err := backup.MigrationEvidenceFromDirectArchiveRestore(fake.repositoryID, restored, time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC))
	if err != nil || evidence.Digest == "" || evidence.RepositoryID != fake.repositoryID {
		t.Fatalf("migration restore evidence = %#v err=%v", evidence, err)
	}
	withoutProvenance := directInput
	withoutProvenance.ProvenanceRestore = nil
	if _, err := backup.RunDirectArchiveRestoreDrill(context.Background(), withoutProvenance); err == nil || !strings.Contains(err.Error(), "independent disposable provenance restore") {
		t.Fatalf("direct restore without independent provenance step error = %v", err)
	}
	operationalDump := filepath.Join(plan.OperationalPackageDir, "postgres.dump")
	if err := os.WriteFile(operationalDump, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), directInput); err == nil {
		t.Fatal("strict restore plan accepted a tampered fetched operational package")
	}
}

func TestBorgDirectArchiveV2FetchAuthenticatesConfinesAndStrictlyRestores(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatalf("create v2 direct archive: %v", err)
	}

	fake.calls = nil
	target := filepath.Join(t.TempDir(), "isolated-v2-fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil {
		t.Fatalf("strict v2 direct fetch: %v", err)
	}
	if fetched.Status != SnapshotStatusSucceeded || fetched.DirectArchiveManifest != nil || fetched.DirectArchiveManifestSHA256 != archived.ManifestSHA256 || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
		t.Fatalf("v2 fetch evidence = %#v", fetched)
	}
	for _, check := range []string{"manifest_identity", "declared_payload_roots", "user_payload_confinement", "bounded_package_entries", "operational_package", "provenance_package"} {
		if fetched.ExtractionVerification.Checks[check] != SnapshotStatusSucceeded {
			t.Fatalf("v2 extraction check %q = %#v", check, fetched.ExtractionVerification.Checks)
		}
	}
	manifestExtract, hashExtract, payloadExtract := -1, -1, -1
	for index, call := range fake.calls {
		switch {
		case strings.HasPrefix(call, "extract:") && strings.Contains(call, " extract --stdout ") && strings.HasSuffix(call, backupstrategy.DirectArchiveManifestFile):
			manifestExtract = index
		case strings.HasPrefix(call, "extract:") && strings.Contains(call, " extract --stdout ") && strings.HasSuffix(call, backupstrategy.DirectArchiveManifestHashFile):
			hashExtract = index
		case strings.HasPrefix(call, "extract:") && strings.Contains(call, " extract ::"):
			payloadExtract = index
		}
		if strings.Contains(call, "{sha256}") || strings.HasPrefix(call, "export-tar:") {
			t.Fatalf("v2 fetch recreated full payload verification: %v", fake.calls)
		}
	}
	if manifestExtract < 0 || hashExtract <= manifestExtract || payloadExtract <= hashExtract {
		t.Fatalf("v2 fetch authentication/extraction ordering = %v", fake.calls)
	}

	directInput := backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: target, ExpectedManifestSHA256: archived.ManifestSHA256,
		ExpectedRepository: fixture.cfg.Snapshots.Borg.Repository, ExpectedArchive: archived.Archive,
		V2UserSymlinkTargets: fetched.V2UserSymlinkTargets,
		OperationalTarget:    "loom_restore_drill_direct_v2", ActiveDatabase: "loom_main", Owner: "loom",
		OperationalRestore: exactRecoveryStep, ProvenanceRestore: exactRecoveryStep,
		Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	}
	plan, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), directInput)
	if err != nil || plan.ManifestSHA256 != archived.ManifestSHA256 || plan.ProvenancePackageID != "provenance-direct" || plan.Extraction.Checks["metadata_and_payload"] != "" {
		t.Fatalf("v2 direct restore plan = %#v err=%v", plan, err)
	}
	restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), directInput)
	if err != nil || restored.Status != SnapshotStatusSucceeded || restored.OperationalRecovery.Status != SnapshotStatusSucceeded || restored.ProvenanceRecovery.Status != SnapshotStatusSucceeded {
		t.Fatalf("strict v2 restore = %#v err=%v", restored, err)
	}
	evidence, err := backup.MigrationEvidenceFromDirectArchiveRestore(fake.repositoryID, restored, time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC))
	if err != nil || evidence.Digest == "" || evidence.DirectArchiveManifestSHA != archived.ManifestSHA256 {
		t.Fatalf("strict v2 restore evidence = %#v err=%v", evidence, err)
	}
	withoutSymlinkEvidence := directInput
	withoutSymlinkEvidence.V2UserSymlinkTargets = nil
	if _, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), withoutSymlinkEvidence); err == nil || !strings.Contains(err.Error(), "authenticated user-symlink target evidence") {
		t.Fatalf("v2 plan without fetch symlink evidence error = %v", err)
	}
	if _, err := backup.RunDirectArchiveRestoreDrill(context.Background(), withoutSymlinkEvidence); err == nil || !strings.Contains(err.Error(), "authenticated user-symlink target evidence") {
		t.Fatalf("v2 run without fetch symlink evidence error = %v", err)
	}
}

func TestBorgDirectArchiveV2AuthenticatedEmptySymlinkEvidenceRestoresNoSymlinkArchive(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	if err := os.Remove(filepath.Join(fixture.root, "Documents", "payload-link")); err != nil {
		t.Fatal(err)
	}
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "no-symlink-v2-fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil || fetched.Status != SnapshotStatusSucceeded || fetched.V2UserSymlinkTargets == nil || len(fetched.V2UserSymlinkTargets) != 0 {
		t.Fatalf("authenticated empty v2 symlink evidence = %#v err=%v", fetched.V2UserSymlinkTargets, err)
	}
	directInput := backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: target, ExpectedManifestSHA256: fetched.DirectArchiveManifestSHA256,
		ExpectedRepository: fetched.Repository, ExpectedArchive: fetched.Archive,
		V2UserSymlinkTargets: fetched.V2UserSymlinkTargets,
		OperationalTarget:    "loom_restore_drill_no_symlinks", ActiveDatabase: "loom_main", Owner: "loom",
		OperationalRestore: exactRecoveryStep, ProvenanceRestore: exactRecoveryStep,
	}
	if plan, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), directInput); err != nil || plan.Status != "planned" {
		t.Fatalf("plan no-symlink v2 restore = %#v err=%v", plan, err)
	}
	if restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), directInput); err != nil || restored.Status != SnapshotStatusSucceeded {
		t.Fatalf("run no-symlink v2 restore = %#v err=%v", restored, err)
	}
}

func TestBorgDirectArchiveV2RestoreRejectsTargetDriftAfterFetchAndRevalidatesFinalState(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fetchRoot := filepath.Join(t.TempDir(), "target-binding-fetch")
	links := installDirectArchiveExternalLeafSymlinks(t, fixture.root, fetchRoot)
	links.lock(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: fetchRoot})
	if err != nil || fetched.Status != SnapshotStatusSucceeded || len(fetched.V2UserSymlinkTargets) < 2 {
		t.Fatalf("target-binding fetch = %#v err=%v", fetched, err)
	}
	var archiveLinkPath, exactTarget string
	for candidate, target := range fetched.V2UserSymlinkTargets {
		if strings.HasSuffix(candidate, "/Documents/external-absolute-link") {
			archiveLinkPath, exactTarget = candidate, target
			break
		}
	}
	if archiveLinkPath == "" {
		t.Fatalf("fetch evidence has no external absolute link: %#v", fetched.V2UserSymlinkTargets)
	}
	restoredLink := filepath.Join(fetchRoot, filepath.FromSlash(archiveLinkPath))
	replaceTarget := func(target string) {
		t.Helper()
		if err := os.Remove(restoredLink); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, restoredLink); err != nil {
			t.Fatal(err)
		}
	}
	directInput := backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: fetchRoot, ExpectedManifestSHA256: fetched.DirectArchiveManifestSHA256,
		ExpectedRepository: fetched.Repository, ExpectedArchive: fetched.Archive,
		V2UserSymlinkTargets: fetched.V2UserSymlinkTargets,
		OperationalTarget:    "loom_restore_drill_target_binding", ActiveDatabase: "loom_main", Owner: "loom",
		OperationalRestore: exactRecoveryStep, ProvenanceRestore: exactRecoveryStep,
	}
	if _, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), directInput); err != nil {
		t.Fatalf("baseline target-bound plan: %v", err)
	}
	assertPlanAndRunReject := func(name string, candidate backup.DirectArchiveRestoreDrillInput) {
		t.Helper()
		if _, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), candidate); err == nil {
			t.Fatalf("v2 plan accepted %s", name)
		}
		if _, err := backup.RunDirectArchiveRestoreDrill(context.Background(), candidate); err == nil {
			t.Fatalf("v2 run accepted %s", name)
		}
	}
	missingEntryInput := directInput
	missingEntryInput.V2UserSymlinkTargets = cloneV2UserSymlinkTargets(fetched.V2UserSymlinkTargets)
	delete(missingEntryInput.V2UserSymlinkTargets, archiveLinkPath)
	assertPlanAndRunReject("missing authenticated symlink entry", missingEntryInput)
	extraEntryInput := directInput
	extraEntryInput.V2UserSymlinkTargets = cloneV2UserSymlinkTargets(fetched.V2UserSymlinkTargets)
	extraEntryInput.V2UserSymlinkTargets[strings.TrimSuffix(archiveLinkPath, "external-absolute-link")+"not-present"] = "/outside/not-present"
	assertPlanAndRunReject("extra authenticated symlink entry", extraEntryInput)
	if err := os.Remove(restoredLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoredLink, []byte("type drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPlanAndRunReject("symlink type drift", directInput)
	if err := os.Remove(restoredLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exactTarget, restoredLink); err != nil {
		t.Fatal(err)
	}
	replaceTarget("/attacker/changed-after-fetch")
	assertPlanAndRunReject("symlink target drift after Fetch", directInput)

	replaceTarget(exactTarget)
	callerEvidence := cloneV2UserSymlinkTargets(fetched.V2UserSymlinkTargets)
	callerMutationInput := directInput
	callerMutationInput.V2UserSymlinkTargets = callerEvidence
	callerMutationInput.OperationalRestore = func(ctx context.Context, input backup.DirectArchiveRecoveryStepInput) (backup.DirectArchiveRecoveryStepResult, error) {
		callerEvidence[archiveLinkPath] = "/caller/mutated-evidence"
		return exactRecoveryStep(ctx, input)
	}
	if restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), callerMutationInput); err != nil || restored.Status != SnapshotStatusSucceeded {
		t.Fatalf("caller mutation changed owned v2 evidence: result=%#v err=%v", restored, err)
	}

	finalDriftInput := directInput
	finalDriftInput.OperationalRestore = func(ctx context.Context, input backup.DirectArchiveRecoveryStepInput) (backup.DirectArchiveRecoveryStepResult, error) {
		replaceTarget("/attacker/changed-during-database-work")
		return exactRecoveryStep(ctx, input)
	}
	if result, err := backup.RunDirectArchiveRestoreDrill(context.Background(), finalDriftInput); err == nil || result.Status == SnapshotStatusSucceeded || !strings.Contains(err.Error(), "final direct-archive extraction verification") {
		t.Fatalf("final v2 revalidation result=%#v err=%v", result, err)
	}
	links.assertOutsideUnchanged(t)
}

func TestCloudRestoreDrillForwardsClonedPrivateV2SymlinkEvidence(t *testing.T) {
	const target = "/outside/secret-target"
	fetch := SnapshotFetchResult{
		Repository: "repository", Archive: "archive", DirectArchiveManifestSHA256: strings.Repeat("a", 64),
		V2UserSymlinkTargets: map[string]string{"root/link": target},
	}
	authority := &cloudRestoreAuthorityStub{}
	direct := directArchiveRestoreDrillInput(fetch, CloudRestoreDrillInput{TargetDatabase: "loom_restore_drill_forwarded", RestoreAuthority: authority}, "/tmp/disposable-staging")
	if direct.V2UserSymlinkTargets["root/link"] != target {
		t.Fatalf("forwarded v2 symlink evidence = %#v", direct.V2UserSymlinkTargets)
	}
	if direct.Authority != authority {
		t.Fatal("cloud restore did not forward the typed restore authority")
	}
	fetch.V2UserSymlinkTargets["root/link"] = "/caller/mutation"
	if direct.V2UserSymlinkTargets["root/link"] != target {
		t.Fatal("cloud restore input aliases caller-owned fetch evidence")
	}
	direct.V2UserSymlinkTargets["root/link"] = "/direct/mutation"
	if fetch.V2UserSymlinkTargets["root/link"] != "/caller/mutation" {
		t.Fatal("fetch evidence aliases direct-restore input evidence")
	}
	for label, value := range map[string]any{
		"fetch":        fetch,
		"cloud result": CloudRestoreDrillResult{Fetch: fetch},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(target)) || bytes.Contains(raw, []byte("/caller/mutation")) || bytes.Contains(raw, []byte("/direct/mutation")) {
			t.Fatalf("%s JSON exposed v2 symlink target evidence: %s", label, raw)
		}
	}
}

func TestBorgDirectArchiveV2FetchRejectsUnexpectedOrEscapingPayloadBeforeExtraction(t *testing.T) {
	tests := map[string]string{
		"unexpected root": "unexpected-root",
		"path escape":     "../escape",
	}
	for name, injectedPath := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			request := directArchiveV2Request(fixture)
			fake := newFakeDirectBorg(t)
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
			archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
			if err != nil {
				t.Fatal(err)
			}
			items := decodeDirectArchiveListItems(t, fake.listByArchive[archived.Archive])
			items = append(items, directArchiveListItem{Path: injectedPath, Type: "d", Mode: "drwx------", Healthy: true})
			fake.listByArchive[archived.Archive] = encodeDirectArchiveListItems(t, items)
			fake.calls = nil
			_, err = backend.Fetch(context.Background(), SnapshotFetchInput{
				Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive,
				To: filepath.Join(t.TempDir(), "rejected-fetch"),
			})
			if err == nil {
				t.Fatalf("v2 fetch accepted injected path %q", injectedPath)
			}
			for _, call := range fake.calls {
				if strings.HasPrefix(call, "extract:") && strings.Contains(call, " extract ::") {
					t.Fatalf("v2 fetch extracted payload before rejecting %q: %v", injectedPath, fake.calls)
				}
			}
		})
	}
	t.Run("hardlink escape", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		request := directArchiveV2Request(fixture)
		fake := newFakeDirectBorg(t)
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
		archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err != nil {
			t.Fatal(err)
		}
		items := decodeDirectArchiveListItems(t, fake.listByArchive[archived.Archive])
		mutated := false
		for index := range items {
			if strings.HasSuffix(items[index].Path, "/payload.txt-hardlink") {
				items[index].LinkTarget = "../escape"
				mutated = true
				break
			}
		}
		if !mutated {
			t.Fatal("v2 archive fixture has no hardlink")
		}
		fake.listByArchive[archived.Archive] = encodeDirectArchiveListItems(t, items)
		fake.calls = nil
		_, err = backend.Fetch(context.Background(), SnapshotFetchInput{
			Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive,
			To: filepath.Join(t.TempDir(), "hardlink-rejected"),
		})
		if err == nil || !strings.Contains(err.Error(), "hardlink") {
			t.Fatalf("v2 fetch accepted escaping hardlink: %v", err)
		}
		for _, call := range fake.calls {
			if strings.HasPrefix(call, "extract:") && strings.Contains(call, " extract ::") {
				t.Fatalf("v2 fetch extracted escaping hardlink payload: %v", fake.calls)
			}
		}
	})
}

func TestBorgDirectArchiveV2FetchRejectsEscapingSymlinkAndPackageDrift(t *testing.T) {
	t.Run("external user symlink leaves remain inert", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		fetchRoot := filepath.Join(t.TempDir(), "symlink-restored")
		links := installDirectArchiveExternalLeafSymlinks(t, fixture.root, fetchRoot)
		links.lock(t)
		request := directArchiveV2Request(fixture)
		fake := newFakeDirectBorg(t)
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
		archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err != nil {
			t.Fatal(err)
		}
		fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{
			Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive,
			To: fetchRoot,
		})
		if err != nil || fetched.Status != SnapshotStatusSucceeded || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
			t.Fatalf("external leaf symlink fetch = %#v err=%v", fetched, err)
		}
		links.assertRestored(t, fetchRoot, fixture.root)
		links.assertOutsideUnchanged(t)
	})

	t.Run("bounded package bytes", func(t *testing.T) {
		fixture := newDirectArchiveCloudFixture(t)
		request := directArchiveV2Request(fixture)
		fake := newFakeDirectBorg(t)
		backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
		archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
		if err != nil {
			t.Fatal(err)
		}
		fake.tarByArchive[archived.Archive] = mutateFakeDirectTarRegularFile(t, fake.tarByArchive[archived.Archive], "/postgres.dump")
		target := filepath.Join(t.TempDir(), "package-rejected")
		fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{
			Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target,
		})
		if err != nil || fetched.Status != SnapshotStatusFailed || fetched.ExtractionVerification == nil || len(fetched.ExtractionVerification.Errors) == 0 {
			t.Fatalf("package drift fetch = %#v err=%v", fetched, err)
		}
		if _, err := backup.PlanDirectArchiveRestoreDrill(context.Background(), backup.DirectArchiveRestoreDrillInput{
			ArchiveRoot: target, ExpectedManifestSHA256: archived.ManifestSHA256,
			ExpectedRepository: fixture.cfg.Snapshots.Borg.Repository, ExpectedArchive: archived.Archive,
			V2UserSymlinkTargets: fetched.V2UserSymlinkTargets,
		}); err == nil {
			t.Fatal("strict v2 restore plan accepted drifted bounded package")
		}
	})
}

func TestBorgDirectArchiveV2FetchRejectsSymlinkDescendantsBeforeExtraction(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	if err := os.Symlink("../../../../outside", filepath.Join(fixture.root, "Documents", "leaf-link")); err != nil {
		t.Fatal(err)
	}
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatal(err)
	}
	original := decodeDirectArchiveListItems(t, fake.listByArchive[archived.Archive])
	var symlinkPath string
	for _, item := range original {
		if strings.HasSuffix(item.Path, "/Documents/leaf-link") {
			symlinkPath = item.Path
			break
		}
	}
	if symlinkPath == "" {
		t.Fatal("v2 archive fixture has no external leaf symlink")
	}
	descendant := directArchiveListItem{Path: symlinkPath + "/descendant", Type: "-", Mode: "-rw-------", Healthy: true}
	for _, test := range []struct {
		name  string
		items []directArchiveListItem
	}{
		{name: "descendant listed before link", items: append([]directArchiveListItem{descendant}, original...)},
		{name: "descendant listed after link", items: append(append([]directArchiveListItem(nil), original...), descendant)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake.listByArchive[archived.Archive] = encodeDirectArchiveListItems(t, test.items)
			fake.calls = nil
			_, err := backend.Fetch(context.Background(), SnapshotFetchInput{
				Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive,
				To: filepath.Join(t.TempDir(), "descendant-rejected"),
			})
			if err == nil || !strings.Contains(err.Error(), "archive ancestor") {
				t.Fatalf("symlink descendant error = %v", err)
			}
			assertNoDirectPayloadExtraction(t, fake.calls)
		})
	}
}

func TestBorgDirectArchiveV2FetchRejectsSymlinkTargetDriftAfterPreflight(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatal(err)
	}
	fake.tarByArchive[archived.Archive] = mutateFakeDirectTarSymlinkTarget(t, fake.tarByArchive[archived.Archive], "/Documents/payload-link", "different-target")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{
		Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive,
		To: filepath.Join(t.TempDir(), "target-drift-rejected"),
	})
	if err != nil || fetched.Status != SnapshotStatusFailed || fetched.ExtractionVerification == nil || len(fetched.ExtractionVerification.Errors) != 1 || !strings.Contains(fetched.ExtractionVerification.Errors[0], "authenticated archive evidence") {
		t.Fatalf("symlink target drift fetch = %#v err=%v", fetched, err)
	}
}

func TestBorgDirectArchiveV2FetchKeepsRootPackageEvidenceAncestorAndSpecialEntriesFailClosed(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
	archived, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if err != nil {
		t.Fatal(err)
	}
	original := decodeDirectArchiveListItems(t, fake.listByArchive[archived.Archive])
	manifest, err := backupstrategy.ParseAuthenticatedDirectArchiveManifest(fake.manifestByArchive[archived.Archive], fake.hashByArchive[archived.Archive])
	if err != nil || manifest.ManifestV2 == nil {
		t.Fatalf("parse v2 fixture: %#v err=%v", manifest, err)
	}
	userRoot := manifest.ManifestV2.Roots[0].ArchivePath
	operationalRoot := manifest.ManifestV2.OperationalPackage.ArchivePath
	provenanceRoot := manifest.ManifestV2.ProvenancePackage.ArchivePath
	tests := []struct {
		name   string
		mutate func([]directArchiveListItem) []directArchiveListItem
	}{
		{name: "declared root symlink", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return mutateDirectArchiveListItem(t, items, userRoot, func(item *directArchiveListItem) {
				item.Type, item.Mode, item.LinkTarget = "l", "lrwxrwxrwx", "/outside"
			})
		}},
		{name: "operational package symlink", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return mutateDirectArchiveListItem(t, items, operationalRoot+"/"+backupstrategy.OperationalManifestFile, func(item *directArchiveListItem) {
				item.Type, item.Mode, item.LinkTarget = "l", "lrwxrwxrwx", "/outside"
			})
		}},
		{name: "provenance package symlink", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return mutateDirectArchiveListItem(t, items, provenanceRoot+"/"+provenance.RecoveryManifestFile, func(item *directArchiveListItem) {
				item.Type, item.Mode, item.LinkTarget = "l", "lrwxrwxrwx", "/outside"
			})
		}},
		{name: "evidence symlink", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return mutateDirectArchiveListItem(t, items, path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile), func(item *directArchiveListItem) {
				item.Type, item.Mode, item.LinkTarget = "l", "lrwxrwxrwx", "/outside"
			})
		}},
		{name: "synthetic ancestor symlink", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return append(items, directArchiveListItem{Path: path.Dir(userRoot), Type: "l", Mode: "lrwxrwxrwx", LinkTarget: "/outside", Healthy: true})
		}},
		{name: "special user entry", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return mutateDirectArchiveListItem(t, items, userRoot+"/Documents/payload.txt", func(item *directArchiveListItem) {
				item.Type, item.Mode = "p", "prw-------"
			})
		}},
		{name: "unhealthy user entry", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return mutateDirectArchiveListItem(t, items, userRoot+"/Documents/payload.txt", func(item *directArchiveListItem) {
				item.Healthy = false
			})
		}},
		{name: "duplicate entry", mutate: func(items []directArchiveListItem) []directArchiveListItem {
			return append(items, items[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := append([]directArchiveListItem(nil), original...)
			fake.listByArchive[archived.Archive] = encodeDirectArchiveListItems(t, test.mutate(items))
			fake.calls = nil
			if _, err := backend.Fetch(context.Background(), SnapshotFetchInput{
				Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive,
				To: filepath.Join(t.TempDir(), "boundary-rejected"),
			}); err == nil {
				t.Fatalf("v2 fetch accepted %s", test.name)
			}
			assertNoDirectPayloadExtraction(t, fake.calls)
		})
	}
}

type directArchiveExternalLeafLinks struct {
	targets      map[string]string
	outsideDirs  []string
	sentinelData map[string]string
}

func installDirectArchiveExternalLeafSymlinks(t *testing.T, userRoot, fetchRoot string) directArchiveExternalLeafLinks {
	t.Helper()
	acceptanceRoot := filepath.Dir(fetchRoot)
	absoluteDir := filepath.Join(acceptanceRoot, "outside-absolute")
	relativeDir := filepath.Join(acceptanceRoot, "outside-relative")
	for _, dir := range []string{absoluteDir, relativeDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	absoluteSentinel := filepath.Join(absoluteDir, "sentinel")
	relativeSentinel := filepath.Join(relativeDir, "sentinel")
	sentinelData := map[string]string{
		absoluteSentinel: "absolute outside bytes must remain untouched\n",
		relativeSentinel: "relative outside bytes must remain untouched\n",
	}
	for sentinel, payload := range sentinelData {
		if err := os.WriteFile(sentinel, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restoredRoot := filepath.Join(fetchRoot, filepath.FromSlash(directArchiveTestPath(userRoot)))
	relativeRestoredLink := filepath.Join(restoredRoot, "Documents", "external-relative-link")
	relativeTarget, err := filepath.Rel(filepath.Dir(relativeRestoredLink), relativeSentinel)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{
		"Documents/external-absolute-link": absoluteSentinel,
		"Documents/external-relative-link": relativeTarget,
	}
	for relative, target := range targets {
		if err := os.Symlink(target, filepath.Join(userRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
	}
	return directArchiveExternalLeafLinks{
		targets: targets, outsideDirs: []string{absoluteDir, relativeDir}, sentinelData: sentinelData,
	}
}

func (links directArchiveExternalLeafLinks) lock(t *testing.T) {
	t.Helper()
	for _, dir := range links.outsideDirs {
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, dir := range links.outsideDirs {
			_ = os.Chmod(dir, 0o700)
		}
	})
}

func (links directArchiveExternalLeafLinks) assertRestored(t *testing.T, fetchRoot, userRoot string) {
	t.Helper()
	restoredRoot := filepath.Join(fetchRoot, filepath.FromSlash(directArchiveTestPath(userRoot)))
	for relative, wantTarget := range links.targets {
		linkPath := filepath.Join(restoredRoot, filepath.FromSlash(relative))
		info, err := os.Lstat(linkPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("restored leaf %q is not a symlink: info=%#v err=%v", relative, info, err)
		}
		if target, err := os.Readlink(linkPath); err != nil || target != wantTarget {
			t.Fatalf("restored leaf %q target = %q err=%v; want %q", relative, target, err, wantTarget)
		}
	}
}

func (links directArchiveExternalLeafLinks) assertOutsideUnchanged(t *testing.T) {
	t.Helper()
	for _, dir := range links.outsideDirs {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for sentinel, want := range links.sentinelData {
		payload, err := os.ReadFile(sentinel)
		if err != nil || string(payload) != want {
			t.Fatalf("outside sentinel %q = %q err=%v", sentinel, payload, err)
		}
	}
	for _, dir := range links.outsideDirs {
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func mutateDirectArchiveListItem(t *testing.T, items []directArchiveListItem, exactPath string, mutate func(*directArchiveListItem)) []directArchiveListItem {
	t.Helper()
	for index := range items {
		if items[index].Path == exactPath {
			mutate(&items[index])
			return items
		}
	}
	t.Fatalf("v2 archive fixture has no path %q", exactPath)
	return nil
}

func assertNoDirectPayloadExtraction(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, "extract:") && strings.Contains(call, " extract ::") {
			t.Fatalf("v2 payload extraction ran after failed preflight: %v", calls)
		}
	}
}

func exactRecoveryStep(_ context.Context, input backup.DirectArchiveRecoveryStepInput) (backup.DirectArchiveRecoveryStepResult, error) {
	return backup.DirectArchiveRecoveryStepResult{Status: "succeeded", PackageID: input.PackageID, ManifestSHA256: input.ManifestSHA256, SchemaHead: input.SchemaHead}, nil
}

type directArchiveCloudFixture struct {
	cfg               Config
	request           backupstrategy.DirectArchiveRequest
	root              string
	operational       backupstrategy.OperationalPackageCreateResult
	provenanceDir     string
	provenanceSHA256  string
	operationalDigest string
	payloadMTime      time.Time
}

func newDirectArchiveCloudFixture(t *testing.T) directArchiveCloudFixture {
	return newDirectArchiveCloudFixtureAtProvenanceHead(t, provenance.SchemaHead)
}

func newDirectArchiveCloudFixtureAtProvenanceHead(t *testing.T, provenanceSchemaHead int) directArchiveCloudFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "canonical-box")
	if err := os.MkdirAll(filepath.Join(root, "Documents"), 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "Documents", "payload.txt")
	if err := os.WriteFile(payload, []byte("canonical bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	payloadMTime := time.Date(2026, 8, 29, 9, 45, 0, 123_000_000, time.UTC)
	if err := os.Chtimes(payload, payloadMTime, payloadMTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("payload.txt", filepath.Join(root, "Documents", "payload-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(payload, filepath.Join(root, "Documents", "payload.txt-hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".loom-acceptance"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom-acceptance", "excluded"), []byte("excluded"), 0o600); err != nil {
		t.Fatal(err)
	}
	operational, schemaHead := createDirectOperationalPackage(t)
	provenanceDir := filepath.Join(t.TempDir(), "provenance-direct")
	if err := os.Mkdir(provenanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	provenanceDump := []byte("PGDMP\x01isolated provenance dump\n")
	if err := os.WriteFile(filepath.Join(provenanceDir, provenance.RecoveryDumpFile), provenanceDump, 0o600); err != nil {
		t.Fatal(err)
	}
	dumpDigest := sha256.Sum256(provenanceDump)
	dumpSHA256 := fmt.Sprintf("%x", dumpDigest[:])
	dumpSize := int64(len(provenanceDump))
	fileCount := int64(1)
	relations, err := provenance.RecoveryRelationsForSchemaHead(provenanceSchemaHead)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int64, len(relations))
	for _, relation := range relations {
		counts[relation] = 0
	}
	completedAt := time.Date(2026, 8, 29, 8, 30, 0, 0, time.UTC)
	provenanceManifest := maintenance.BackupManifest{
		Schema: provenance.RecoveryManifestSchema, BackupKind: provenance.RecoveryBackupKind,
		CreatedAt: completedAt.Format(time.RFC3339Nano),
		Source:    maintenance.BackupManifestSource{NodeID: "loom-main"},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: maintenance.ArtifactKindProvenanceDump, Path: provenance.RecoveryDumpFile, FileCount: &fileCount, SizeBytes: &dumpSize, SHA256: dumpSHA256}},
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: completedAt.Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: provenance.RecoveryDumpFile, Format: "pg_dump_custom"},
			SchemaHead: provenanceSchemaHead, RequiredRelations: relations, LogicalCounts: counts,
			GraphDigest: "sha256:" + strings.Repeat("d", sha256.Size*2), DumpSizeBytes: dumpSize, DumpSHA256: dumpSHA256,
		},
	}
	provenanceManifestBytes, err := json.MarshalIndent(provenanceManifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	provenanceManifestBytes = append(provenanceManifestBytes, '\n')
	if err := os.WriteFile(filepath.Join(provenanceDir, provenance.RecoveryManifestFile), provenanceManifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	provenanceDigest := sha256.Sum256(provenanceManifestBytes)
	provenanceSHA256 := fmt.Sprintf("%x", provenanceDigest[:])
	passphrase := filepath.Join(t.TempDir(), "borg.passphrase")
	if err := os.WriteFile(passphrase, []byte("disposable-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion, Enabled: true, Provider: DefaultProvider,
		StateDir: filepath.Join(t.TempDir(), "state"), RemoteLockPath: filepath.Join(t.TempDir(), "locks", "borg.lock"),
		Snapshots: SnapshotsConfig{Backend: SnapshotBackendBorg, Borg: BorgConfig{
			Binary: "borg", Repository: filepath.Join(t.TempDir(), "repo"), PassphraseFile: passphrase,
			CacheDir: filepath.Join(t.TempDir(), "cache"), SecurityDir: filepath.Join(t.TempDir(), "security"),
			Compression: "zstd,1", Encryption: DefaultBorgEncryption,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := backupstrategy.DirectArchiveRequest{
		Schema: backupstrategy.DirectArchiveRequestSchema, NodeID: "loom-main", ArchiveRef: "history-20260829T100000Z",
		CreatedAt:  time.Date(2026, 8, 29, 10, 0, 0, 987_654_321, time.UTC),
		Roots:      []backupstrategy.DirectArchiveRoot{{Name: "box", Path: root}},
		Exclusions: []backupstrategy.DirectArchiveExclusion{{Root: "box", RelativePath: ".loom-acceptance"}},
		OperationalPackage: backupstrategy.DirectArchiveOperationalPackage{
			Path: operational.PackageDir, ManifestSHA256: operational.ManifestSHA256,
			PackageID: operational.Verification.PackageID, ExpectedSchemaHead: &schemaHead,
		},
		ProvenancePackage: backupstrategy.DirectArchiveProvenancePackage{
			Path: provenanceDir, ManifestSHA256: provenanceSHA256, PackageID: "provenance-direct",
			Verify: func(ctx context.Context, packageDir, manifestSHA string) (backupstrategy.DirectArchiveProvenanceVerification, error) {
				if packageDir != provenanceDir || manifestSHA != provenanceSHA256 {
					return backupstrategy.DirectArchiveProvenanceVerification{}, errors.New("provenance identity mismatch")
				}
				verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, packageDir, manifestSHA)
				if err != nil {
					return backupstrategy.DirectArchiveProvenanceVerification{}, err
				}
				return backupstrategy.DirectArchiveProvenanceVerification{
					ManifestSHA256: verification.ManifestSHA256, SchemaHead: verification.SchemaHead,
					GraphDigest: verification.GraphDigest, CompletedAt: verification.CompletedAt, DumpSizeBytes: verification.DumpSizeBytes,
				}, nil
			},
		},
	}
	return directArchiveCloudFixture{
		cfg: cfg, request: request, root: root, operational: operational, provenanceDir: provenanceDir, provenanceSHA256: provenanceSHA256,
		operationalDigest: digestDirectoryForDirectTest(t, operational.PackageDir), payloadMTime: payloadMTime,
	}
}

func prepareDirectArchiveForTest(t *testing.T, cfg Config, request backupstrategy.DirectArchiveRequest) backupstrategy.PreparedDirectArchiveManifest {
	t.Helper()
	archiveClass, err := backupstrategy.NormalizeDirectArchiveClass(request.ArchiveClass)
	if err != nil {
		t.Fatal(err)
	}
	request.ArchiveClass = archiveClass
	prepared, err := backupstrategy.PrepareDirectArchiveManifest(context.Background(), backupstrategy.DirectArchiveManifestPrepareInput{
		Request: request, Backend: backupstrategy.DirectArchiveBackendBorg,
		Repository:  cfg.Snapshots.Borg.Repository,
		ArchiveName: directBorgArchiveName(request.NodeID, request.ArchiveRef, archiveClass),
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func directArchiveV2Request(fixture directArchiveCloudFixture) backupstrategy.DirectArchiveRequestV2 {
	return backupstrategy.DirectArchiveRequestV2{
		Schema:              backupstrategy.DirectArchiveRequestSchemaV2,
		VerificationProfile: backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1,
		NodeID:              fixture.request.NodeID,
		ArchiveRef:          fixture.request.ArchiveRef,
		ArchiveClass:        backupstrategy.DirectArchiveClassUserData,
		CreatedAt:           fixture.request.CreatedAt,
		Roots:               append([]backupstrategy.DirectArchiveRoot(nil), fixture.request.Roots...),
		Exclusions:          append([]backupstrategy.DirectArchiveExclusion(nil), fixture.request.Exclusions...),
		OperationalPackage:  fixture.request.OperationalPackage,
		ProvenancePackage:   fixture.request.ProvenancePackage,
	}
}

func prepareDirectArchiveV2ForTest(t *testing.T, cfg Config, request backupstrategy.DirectArchiveRequestV2) backupstrategy.PreparedDirectArchiveManifest {
	t.Helper()
	prepared, err := backupstrategy.PrepareDirectArchiveManifestV2(context.Background(), backupstrategy.DirectArchiveManifestPrepareInputV2{
		Request: request, Backend: backupstrategy.DirectArchiveBackendBorg,
		Repository:  cfg.Snapshots.Borg.Repository,
		ArchiveName: directBorgArchiveName(request.NodeID, request.ArchiveRef, request.ArchiveClass),
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func assertPendingDiscoveryConflict(t *testing.T, fake *fakeDirectBorg, result DirectArchiveResult, err error) {
	t.Helper()
	if !errors.Is(err, ErrDirectArchiveConflict) || result.Status != DirectArchiveStatusConflict || result.Committed || result.BorgCommandCounts["create"] != 0 || result.BorgCommandCounts["rename"] != 0 || fake.commandCount("create") != 0 || fake.commandCount("rename") != 0 || fake.commandCount("delete") != 0 {
		t.Fatalf("pending discovery did not fail closed: result=%#v err=%v calls=%v", result, err, fake.calls)
	}
}

func decodeDirectArchiveListItems(t *testing.T, payload []byte) []directArchiveListItem {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var items []directArchiveListItem
	for {
		var item directArchiveListItem
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	return items
}

func encodeDirectArchiveListItems(t *testing.T, items []directArchiveListItem) []byte {
	t.Helper()
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, item := range items {
		if err := encoder.Encode(item); err != nil {
			t.Fatal(err)
		}
	}
	return payload.Bytes()
}

func countDirectArchiveContentStreams(calls []string) int {
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "list:") && strings.Contains(call, "--json-lines") && strings.Contains(call, "{sha256}") {
			count++
		}
	}
	return count
}

func createDirectOperationalPackage(t *testing.T) (backupstrategy.OperationalPackageCreateResult, int64) {
	t.Helper()
	root := t.TempDir()
	packages := filepath.Join(root, "packages")
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, payload string) string {
		path := filepath.Join(sources, name)
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	schemaHead := int64(61)
	migration, err := json.Marshal(backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: schemaHead,
		LatestVersion: schemaHead, Status: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packages, PackageID: "operational-direct", CreatedAt: time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), SchemaHead: schemaHead,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP\x01disposable"), 0o600)
		},
		ServiceConfig:  write("service.env", "LOOM_ENV=test\n"),
		InstallConfig:  write("install.json", `{"profile":"test"}`),
		ReleaseConfig:  write("release.json", `{"release":"test"}`),
		MigrationState: write("migration.json", string(migration)),
		UpdateState:    write("update.json", `{"status":"idle"}`),
		Health:         write("health.json", `{"status":"ok"}`),
	}
	result, err := backupstrategy.CreateOperationalPackage(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateOperationalPackage returned error: %v", err)
	}
	return result, schemaHead
}

type fakeDirectBorg struct {
	t                      *testing.T
	archives               map[string]bool
	manifestByArchive      map[string][]byte
	hashByArchive          map[string][]byte
	listByArchive          map[string][]byte
	tarByArchive           map[string][]byte
	timeByArchive          map[string]time.Time
	repositoryID           string
	pruneRemove            []string
	failCheck              error
	failChecksRemaining    int
	failPrune              error
	failCompact            error
	calls                  []string
	createArgs             []string
	createDir              string
	beforeArchiveRead      func()
	afterArchiveRead       func()
	afterCreate            func()
	failCreate             error
	failCreateOutput       []byte
	createOutput           []byte
	failCreateAfterDurable error
	failRenameAfterCommit  bool
	failArchiveListStreams int
}

type fakeBorgExitError struct {
	code int
}

func (err fakeBorgExitError) Error() string { return fmt.Sprintf("synthetic Borg exit %d", err.code) }
func (err fakeBorgExitError) ExitCode() int { return err.code }

func newFakeDirectBorg(t *testing.T) *fakeDirectBorg {
	return &fakeDirectBorg{
		t: t, archives: map[string]bool{}, manifestByArchive: map[string][]byte{}, hashByArchive: map[string][]byte{},
		listByArchive: map[string][]byte{}, tarByArchive: map[string][]byte{},
		timeByArchive: map[string]time.Time{}, repositoryID: "disposable-repository-id",
	}
}

func (fake *fakeDirectBorg) exec(_ context.Context, _ string, command BorgCommand, _ []string) ([]byte, error) {
	name := command.Name()
	fake.calls = append(fake.calls, name+":"+strings.Join(command.Args, " "))
	switch name {
	case "list":
		if archive := borgArchiveArg(command.Args); archive != "" {
			if !fake.archives[archive] {
				return nil, fmt.Errorf("archive %s not found", archive)
			}
			return append([]byte(nil), fake.listByArchive[archive]...), nil
		}
		names := make([]string, 0, len(fake.archives))
		for archive, exists := range fake.archives {
			if exists {
				names = append(names, archive)
			}
		}
		sort.Strings(names)
		var items []map[string]string
		for _, archive := range names {
			archiveTime := fake.timeByArchive[archive]
			if archiveTime.IsZero() {
				archiveTime = time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
			}
			items = append(items, map[string]string{"name": archive, "time": archiveTime.UTC().Format(time.RFC3339Nano)})
		}
		return json.Marshal(map[string]any{"archives": items})
	case "create":
		fake.createArgs = append([]string(nil), command.Args...)
		fake.createDir = command.Dir
		archive := borgArchiveArg(command.Args)
		if fake.failCreate != nil {
			return append([]byte(nil), fake.failCreateOutput...), fake.failCreate
		}
		if fake.beforeArchiveRead != nil {
			fake.beforeArchiveRead()
		}
		manifest, err := os.ReadFile(filepath.Join(command.Dir, backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile))
		if err != nil {
			return nil, err
		}
		hash, err := os.ReadFile(filepath.Join(command.Dir, backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile))
		if err != nil {
			return nil, err
		}
		listPayload, tarPayload, err := fake.captureArchive(command)
		if err != nil {
			return nil, err
		}
		if fake.afterArchiveRead != nil {
			fake.afterArchiveRead()
		}
		fake.archives[archive] = true
		fake.manifestByArchive[archive] = manifest
		fake.hashByArchive[archive] = hash
		fake.listByArchive[archive] = listPayload
		fake.tarByArchive[archive] = tarPayload
		for index, arg := range command.Args {
			if arg == "--timestamp" && index+1 < len(command.Args) {
				fake.timeByArchive[archive], _ = time.Parse(time.RFC3339Nano, command.Args[index+1])
			}
		}
		if fake.afterCreate != nil {
			fake.afterCreate()
		}
		if fake.failCreateAfterDurable != nil {
			return nil, fake.failCreateAfterDurable
		}
		if fake.createOutput != nil {
			return append([]byte(nil), fake.createOutput...), nil
		}
		return []byte(`{"archive":{"stats":{"compressed_size":123,"deduplicated_size":45}}}`), nil
	case "info":
		archive := borgArchiveArg(command.Args)
		if archive != "" && !fake.archives[archive] {
			return nil, fmt.Errorf("archive %s not found", archive)
		}
		if archive == "" {
			return json.Marshal(map[string]any{"repository": map[string]string{"id": fake.repositoryID}})
		}
		return []byte(`{"archives":[{"stats":{"compressed_size":123,"deduplicated_size":45}}]}`), nil
	case "check":
		if fake.failChecksRemaining > 0 {
			fake.failChecksRemaining--
			return nil, errors.New("synthetic archive metadata verification interruption")
		}
		if fake.failCheck != nil {
			return nil, fake.failCheck
		}
		return []byte(`{}`), nil
	case "extract":
		archive := borgArchiveArg(command.Args)
		if !fake.archives[archive] {
			return nil, fmt.Errorf("archive %s not found", archive)
		}
		stdout := false
		for _, arg := range command.Args {
			if arg == "--stdout" {
				stdout = true
				break
			}
		}
		if !stdout {
			if err := extractFakeDirectTar(fake.tarByArchive[archive], command.Dir); err != nil {
				return nil, err
			}
			return []byte(`extracted`), nil
		}
		requested := command.Args[len(command.Args)-1]
		if strings.HasSuffix(requested, backupstrategy.DirectArchiveManifestHashFile) {
			return append([]byte(nil), fake.hashByArchive[archive]...), nil
		}
		return append([]byte(nil), fake.manifestByArchive[archive]...), nil
	case "rename":
		from := borgArchiveArg(command.Args)
		to := command.Args[len(command.Args)-1]
		if !fake.archives[from] {
			return nil, fmt.Errorf("archive %s not found", from)
		}
		fake.archives[from] = false
		fake.archives[to] = true
		fake.manifestByArchive[to] = fake.manifestByArchive[from]
		fake.hashByArchive[to] = fake.hashByArchive[from]
		fake.listByArchive[to] = fake.listByArchive[from]
		fake.tarByArchive[to] = fake.tarByArchive[from]
		fake.timeByArchive[to] = fake.timeByArchive[from]
		if fake.failRenameAfterCommit {
			return nil, errors.New("network response lost")
		}
		return []byte(`{}`), nil
	case "prune":
		if fake.failPrune != nil {
			return nil, fake.failPrune
		}
		for _, archive := range fake.pruneRemove {
			fake.archives[archive] = false
		}
		return []byte(`pruned`), nil
	case "compact":
		if fake.failCompact != nil {
			return nil, fake.failCompact
		}
		return []byte(`compacted`), nil
	default:
		return nil, fmt.Errorf("unexpected Borg command: %#v", command.Args)
	}
}

func (fake *fakeDirectBorg) streamExec(ctx context.Context, binary string, command BorgCommand, env []string, stdout io.Writer) error {
	if command.Name() == "list" && borgArchiveArg(command.Args) != "" && fake.failArchiveListStreams > 0 {
		fake.calls = append(fake.calls, "list:"+strings.Join(command.Args, " "))
		fake.failArchiveListStreams--
		return errors.New("synthetic archive verification interruption")
	}
	if command.Name() == "export-tar" {
		fake.calls = append(fake.calls, "export-tar:"+strings.Join(command.Args, " "))
		archive := borgArchiveArg(command.Args)
		if !fake.archives[archive] {
			return fmt.Errorf("archive %s not found", archive)
		}
		_, err := stdout.Write(fake.tarByArchive[archive])
		return err
	}
	payload, err := fake.exec(ctx, binary, command, env)
	if len(payload) > 0 {
		if _, writeErr := stdout.Write(payload); err == nil {
			err = writeErr
		}
	}
	return err
}

func (fake *fakeDirectBorg) cloneArchive(from, to string) {
	fake.archives[to] = true
	fake.manifestByArchive[to] = append([]byte(nil), fake.manifestByArchive[from]...)
	fake.hashByArchive[to] = append([]byte(nil), fake.hashByArchive[from]...)
	fake.listByArchive[to] = append([]byte(nil), fake.listByArchive[from]...)
	fake.tarByArchive[to] = append([]byte(nil), fake.tarByArchive[from]...)
	fake.timeByArchive[to] = fake.timeByArchive[from]
}

func (fake *fakeDirectBorg) installAuthenticatedPending(name string, prepared backupstrategy.PreparedDirectArchiveManifest) {
	fake.archives[name] = true
	fake.manifestByArchive[name] = append([]byte(nil), prepared.ManifestBytes...)
	fake.hashByArchive[name] = append([]byte(nil), prepared.HashFileBytes...)
}

func (fake *fakeDirectBorg) captureArchive(command BorgCommand) ([]byte, []byte, error) {
	exclusions := make([]string, 0)
	sources := make([]string, 0)
	foundArchive := false
	for index := 0; index < len(command.Args); index++ {
		arg := command.Args[index]
		if arg == "--exclude" && index+1 < len(command.Args) {
			index++
			exclusions = append(exclusions, strings.TrimPrefix(command.Args[index], "pp:"))
			continue
		}
		if strings.HasPrefix(arg, "::") {
			foundArchive = true
			continue
		}
		if foundArchive {
			sources = append(sources, arg)
		}
	}
	var listPayload bytes.Buffer
	var tarPayload bytes.Buffer
	tarWriter := tar.NewWriter(&tarPayload)
	hardlinks := make(map[[2]uint64]string)
	for _, source := range sources {
		sourcePath := source
		if !filepath.IsAbs(sourcePath) {
			sourcePath = filepath.Join(command.Dir, sourcePath)
		}
		archiveRoot := directArchiveTestPath(source)
		if !filepath.IsAbs(source) {
			archiveRoot = filepath.ToSlash(filepath.Clean(source))
		}
		err := filepath.WalkDir(sourcePath, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(sourcePath, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			archivePath := archiveRoot
			if relative != "." {
				archivePath = filepath.ToSlash(filepath.Join(archiveRoot, relative))
			}
			for _, exclusion := range exclusions {
				if archivePath == exclusion || strings.HasPrefix(archivePath, exclusion+"/") {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			linkTarget := ""
			if info.Mode()&os.ModeSymlink != 0 {
				linkTarget, err = os.Readlink(current)
				if err != nil {
					return err
				}
			}
			header, err := tar.FileInfoHeader(info, linkTarget)
			if err != nil {
				return err
			}
			header.Name = archivePath
			header.Format = tar.FormatPAX
			item := directArchiveListItem{
				Path: archivePath, LinkTarget: linkTarget, Healthy: true,
				Mode:     fakeBorgMode(info.Mode(), false),
				ISOMTime: fakeBorgMTime(info.ModTime()).In(time.Local).Format("2006-01-02T15:04:05.999999"),
			}
			switch {
			case info.IsDir():
				item.Type = "d"
			case info.Mode()&os.ModeSymlink != 0:
				item.Type = "l"
				item.Size = int64(len(linkTarget))
			case info.Mode().IsRegular():
				item.Type = "-"
				stat, _ := info.Sys().(*syscall.Stat_t)
				var key [2]uint64
				if stat != nil {
					key = [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
				}
				if target, exists := hardlinks[key]; exists && key != [2]uint64{} {
					item.Mode = fakeBorgMode(info.Mode(), true)
					item.LinkTarget = target
					header.Typeflag = tar.TypeLink
					header.Linkname = target
					header.Size = 0
				} else {
					if key != [2]uint64{} {
						hardlinks[key] = archivePath
					}
					payload, err := os.ReadFile(current)
					if err != nil {
						return err
					}
					digest := sha256.Sum256(payload)
					item.Size = int64(len(payload))
					item.SHA256 = fmt.Sprintf("%x", digest[:])
				}
			default:
				return fmt.Errorf("unsupported fake archive item %s", current)
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				return err
			}
			listPayload.Write(encoded)
			listPayload.WriteByte('\n')
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			if item.Type == "-" && item.LinkTarget == "" {
				file, err := os.Open(current)
				if err != nil {
					return err
				}
				_, copyErr := io.Copy(tarWriter, file)
				closeErr := file.Close()
				if copyErr != nil {
					return copyErr
				}
				if closeErr != nil {
					return closeErr
				}
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, nil, err
	}
	return listPayload.Bytes(), tarPayload.Bytes(), nil
}

func extractFakeDirectTar(payload []byte, destination string) error {
	reader := tar.NewReader(bytes.NewReader(payload))
	type directoryMetadata struct {
		path  string
		mode  os.FileMode
		mtime time.Time
	}
	directories := []directoryMetadata{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe fake archive path %q", header.Name)
		}
		target := filepath.Join(destination, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
			directories = append(directories, directoryMetadata{path: target, mode: os.FileMode(header.Mode), mtime: header.ModTime})
		case tar.TypeReg, tar.TypeRegA:
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
			timestamp := unix.NsecToTimespec(header.ModTime.UnixNano())
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, target, []unix.Timespec{timestamp, timestamp}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(destination, filepath.Clean(filepath.FromSlash(header.Linkname)))
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported fake tar type %d", header.Typeflag)
		}
		if header.Typeflag != tar.TypeSymlink && header.Typeflag != tar.TypeDir {
			if err := os.Chtimes(target, header.ModTime, header.ModTime); err != nil {
				return err
			}
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return err
		}
		if err := os.Chtimes(directory.path, directory.mtime, directory.mtime); err != nil {
			return err
		}
	}
	return nil
}

func mutateFakeDirectTarRegularFile(t *testing.T, payload []byte, suffix string) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(payload))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	mutated := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) && strings.HasSuffix(header.Name, suffix) {
			if len(body) == 0 {
				t.Fatalf("cannot mutate empty fake tar entry %q", header.Name)
			}
			body[0] ^= 0x1
			mutated = true
		}
		if err := writer.WriteHeader(&copyHeader); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !mutated {
		t.Fatalf("fake tar has no regular file ending in %q", suffix)
	}
	return output.Bytes()
}

func mutateFakeDirectTarEntry(t *testing.T, payload []byte, name string, mutate func(*tar.Header, []byte) []byte) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(payload))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	mutated := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == name {
			body = mutate(&copyHeader, body)
			mutated = true
		}
		if err := writer.WriteHeader(&copyHeader); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !mutated {
		t.Fatalf("fake tar has no entry %q", name)
	}
	return output.Bytes()
}

func mutateFakeDirectTarSymlinkTarget(t *testing.T, payload []byte, suffix, target string) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(payload))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	mutated := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeSymlink && strings.HasSuffix(header.Name, suffix) {
			copyHeader.Linkname = target
			mutated = true
		}
		if err := writer.WriteHeader(&copyHeader); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !mutated {
		t.Fatalf("fake tar has no symlink ending in %q", suffix)
	}
	return output.Bytes()
}

func fakeBorgMode(mode os.FileMode, hardlink bool) string {
	result := []byte("----------")
	switch {
	case hardlink:
		result[0] = 'h'
	case mode.IsDir():
		result[0] = 'd'
	case mode&os.ModeSymlink != 0:
		result[0] = 'l'
	}
	permissions := []struct {
		bit  os.FileMode
		char byte
	}{
		{0o400, 'r'}, {0o200, 'w'}, {0o100, 'x'},
		{0o040, 'r'}, {0o020, 'w'}, {0o010, 'x'},
		{0o004, 'r'}, {0o002, 'w'}, {0o001, 'x'},
	}
	for index, permission := range permissions {
		if mode.Perm()&permission.bit != 0 {
			result[index+1] = permission.char
		}
	}
	if mode&os.ModeSetuid != 0 {
		if result[3] == 'x' {
			result[3] = 's'
		} else {
			result[3] = 'S'
		}
	}
	if mode&os.ModeSetgid != 0 {
		if result[6] == 'x' {
			result[6] = 's'
		} else {
			result[6] = 'S'
		}
	}
	if mode&os.ModeSticky != 0 {
		if result[9] == 'x' {
			result[9] = 't'
		} else {
			result[9] = 'T'
		}
	}
	return string(result)
}

func fakeBorgMTime(value time.Time) time.Time {
	seconds := float64(value.UnixNano()) / float64(time.Second)
	whole, fraction := math.Modf(seconds)
	microseconds := math.Round(fraction * float64(time.Second/time.Microsecond))
	return time.Unix(int64(whole), int64(microseconds)*int64(time.Microsecond))
}

func (fake *fakeDirectBorg) commandCount(name string) int {
	count := 0
	for _, call := range fake.calls {
		if strings.HasPrefix(call, name+":") {
			count++
		}
	}
	return count
}

func borgArchiveArg(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "::") {
			return strings.TrimPrefix(arg, "::")
		}
	}
	return ""
}

func digestDirectoryForDirectTest(t *testing.T, root string) string {
	t.Helper()
	var buffer bytes.Buffer
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&buffer, "%s:%v:%d:%d\n", relative, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			payload, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			buffer.Write(payload)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(buffer.Bytes())
	return fmt.Sprintf("%x", digest[:])
}

func assertDirectOperationalPackageUnchanged(t *testing.T, fixture directArchiveCloudFixture) {
	t.Helper()
	if got := digestDirectoryForDirectTest(t, fixture.operational.PackageDir); got != fixture.operationalDigest {
		t.Fatalf("operational package changed: got %s want %s", got, fixture.operationalDigest)
	}
}

func directArchiveTestPath(value string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "/")
}

// The shell acceptance supplies only its synthetic workspace and public key.
// All operational/provenance artifacts and Borg state below are local fixtures.
func TestBorgMorathustraNativeRecoveryRestore(t *testing.T) {
	binary := os.Getenv("LOOM_TEST_BORG_BINARY")
	workspace := os.Getenv("LOOM_TEST_HERMES_WORKSPACE")
	receipts := os.Getenv("LOOM_TEST_HERMES_RECEIPTS")
	if binary == "" || workspace == "" {
		t.Skip("requires isolated Hermes/Borg smoke fixtures")
	}
	if !strings.Contains(workspace, "/.loom-acceptance/") || filepath.Base(receipts) != ".loom-acceptance" || workspace != filepath.Join(receipts, "morathustra") {
		t.Fatal("non-fixture paths")
	}
	key, err := hex.DecodeString(os.Getenv("LOOM_TEST_HERMES_PUBLIC_KEY"))
	if err != nil || len(key) != 32 {
		t.Fatal("invalid fixture public key")
	}
	policy := hermesprofile.Policy{Enabled: true, Workspace: workspace, PublicKey: key}
	evidence, err := hermesprofile.Check(context.Background(), policy, time.Now().UTC())
	if err != nil || len(evidence) != 1 {
		t.Fatalf("recovery %v: %v", evidence, err)
	}
	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.Snapshots.Borg.Binary = binary
	runner := NewBorgCommandRunner(fixture.cfg)
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
		t.Fatal(err)
	}
	request := directArchiveV2Request(fixture)
	request.Roots = []backupstrategy.DirectArchiveRoot{{Name: "morathustra", Path: workspace}}
	request.Exclusions = nil
	request.ArchiveRef = "history-morathustra-recovery"
	request.CreatedAt = time.Now().UTC()
	backend := BorgSnapshotBackend{Runner: runner}
	input := DirectArchiveInput{Config: fixture.cfg, RequestV2: &request, HermesPolicy: &policy}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), input)
	if err != nil || !result.Committed {
		t.Fatalf("cloud create failed: %v; %s", err, result.Error)
	}
	replay, err := backend.ArchiveCanonicalRoots(context.Background(), input)
	if err != nil || !replay.Idempotent || replay.BorgCommandCounts["create"] != 0 {
		t.Fatalf("cloud replay: %v", err)
	}
	listed, err := runner.Run(context.Background(), BorgCommand{Args: []string{"list", "--short", "::" + result.Archive}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Split(string(listed), "\n") {
		if strings.Contains(name, "/.hermes") || strings.Contains(name, "/recovery/.staging") {
			t.Fatalf("live/staging state archived: %s", name)
		}
	}
	if !strings.Contains(string(listed), strings.TrimPrefix(filepath.ToSlash(filepath.Join(workspace, "AGENTS.md")), "/")) {
		t.Fatal("workspace contract missing")
	}
	readme := filepath.Join(workspace, "recovery", hermesprofile.RecoveryReadmeFile)
	if _, err := os.Lstat(readme); err == nil {
		if !slices.Contains(strings.Split(string(listed), "\n"), strings.TrimPrefix(filepath.ToSlash(readme), "/")) {
			t.Fatal("ordinary recovery README missing from archive")
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if result.Checks["source_stability"] != "borg_create_no_second_source_pass" {
		t.Fatal("v2 ordinary-source optimization changed")
	}
	for _, command := range []string{"delete", "prune", "compact"} {
		if result.BorgCommandCounts[command] != 0 {
			t.Fatal("unexpected destructive command")
		}
	}
	fetched := filepath.Join(receipts, "fetched")
	if err := os.Mkdir(fetched, 0700); err != nil {
		t.Fatal(err)
	}
	archivePackage := strings.TrimPrefix(filepath.ToSlash(evidence[0].Path), "/")
	// Recovery v1 authenticates bytes and POSIX modes, not host xattrs. macOS
	// prevents reapplying com.apple.provenance to the retained read-only files.
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"extract", "--noxattrs", "::" + result.Archive, "pp:" + archivePackage}, Dir: fetched}); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(fetched, filepath.FromSlash(archivePackage))
	verified, err := hermesprofile.Verify(context.Background(), restored, workspace, key)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ManifestSHA256 != evidence[0].ManifestSHA256 {
		t.Fatal("restored identity mismatch")
	}
	out := map[string]any{"archive": result.Archive, "manifest_sha256": result.ManifestSHA256, "recovery_manifest_sha256": verified.ManifestSHA256, "restored_package": restored, "cloud_exclusions": true, "exact_replay": true, "forbidden_commands": false, "profile_payload_sha256": verified.Files[1].SHA256}
	raw, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(filepath.Join(receipts, "cloud-restore.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBorgHermesFrozenRecoveryRejectsTampering(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	pkg := hermesprofile.Evidence{ID: "fixture", Path: hermesprofile.WorkspaceRoot + "/recovery/fixture", Files: []hermesprofile.File{{Path: "manifest.json", Mode: 0440, Size: 10, SHA256: strings.Repeat("a", 64)}, {Path: "profile.zip", Mode: 0440, Size: 20, SHA256: strings.Repeat("b", 64)}}}
	root := strings.TrimPrefix(pkg.Path, "/")
	prepared := backupstrategy.PreparedDirectArchiveManifest{Manifest: backupstrategy.DirectArchiveManifest{HermesRecovery: []hermesprofile.Evidence{pkg}}}
	for _, scenario := range []string{"exact", "bytes", "mode", "link", "extra", "rogue-sibling", "missing", "duplicate", "unhealthy"} {
		t.Run(scenario, func(t *testing.T) {
			items := []directArchiveListItem{{Path: root, Type: "d", Mode: "drwxr-x---", Healthy: true}, {Path: root + "/manifest.json", Type: "-", Mode: "-r--r-----", Size: 10, SHA256: pkg.Files[0].SHA256, Healthy: true}, {Path: root + "/profile.zip", Type: "-", Mode: "-r--r-----", Size: 20, SHA256: pkg.Files[1].SHA256, Healthy: true}}
			switch scenario {
			case "bytes":
				items[2].SHA256 = strings.Repeat("c", 64)
			case "mode":
				items[2].Mode = "-rw-r-----"
			case "link":
				items[2].LinkTarget = "/outside"
			case "extra":
				items = append(items, directArchiveListItem{Path: root + "/extra", Mode: "-r--r-----", Healthy: true})
			case "rogue-sibling":
				items = append(items, directArchiveListItem{Path: path.Dir(root) + "/rogue", Mode: "-r--r-----", Healthy: true})
			case "missing":
				items = items[:2]
			case "duplicate":
				items = append(items, items[2])
			case "unhealthy":
				items[2].Healthy = false
			}
			runner := BorgCommandRunner{Config: fixture.cfg, StreamExec: func(ctx context.Context, binary string, command BorgCommand, env []string, w io.Writer) error {
				enc := json.NewEncoder(w)
				if command.Args[len(command.Args)-1] == "pp:"+path.Dir(root) && slices.Contains(command.Args, "{path}{type}{mode}{size}{uid}{linktarget}{health}") {
					if err := enc.Encode(directArchiveListItem{Path: path.Dir(root), Type: "d", Mode: "drwxr-x---", Healthy: true}); err != nil {
						return err
					}
				} else if command.Args[len(command.Args)-1] != "pp:"+root || !slices.Contains(command.Args, "{path}{type}{mode}{size}{linktarget}{sha256}{health}") {
					return fmt.Errorf("unbounded or wrong evidence query")
				}
				for _, item := range items {
					if err := enc.Encode(item); err != nil {
						return err
					}
				}
				return nil
			}}
			err := verifyArchivedHermesRecovery(context.Background(), runner, "fixture-archive", prepared)
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("scenario %s: %v", scenario, err)
			}
		})
	}
}

func TestBorgHermesPolicyRefusesBeforeBorgEffects(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fake := newFakeDirectBorg(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	policy := hermesprofile.Policy{Enabled: true, Workspace: fixture.root, PublicKey: make([]byte, 32)}
	_, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, Request: fixture.request, HermesPolicy: &policy})
	if err == nil || len(fake.calls) != 0 {
		t.Fatalf("missing evidence reached Borg: %v %v", err, fake.calls)
	}
}

// Produces only a compact local archive for the separately executed real HTTP
// acceptance. The test runner removes source fixtures; Borg/evidence stay in the
// smoke-owned root. No production repository can be supplied as an input.
func TestPrepareCloudFetchFailureFixture(t *testing.T) {
	root := os.Getenv("LOOM_TEST_FETCH_FAILURE_ROOT")
	binary := os.Getenv("LOOM_TEST_BORG_BINARY")
	if root == "" || binary == "" {
		t.Skip("requires compact local smoke")
	}
	if !filepath.IsAbs(root) || filepath.Base(root) != ".loom-acceptance" || !strings.HasPrefix(filepath.Base(filepath.Dir(root)), "loom-fetch-failure-") {
		t.Fatal("non-fixture root")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "borg 1.4.3" {
		t.Fatal("requires Borg 1.4.3")
	}
	fixture := newDirectArchiveCloudFixture(t)
	// Two MiB, entirely synthetic and generated locally; one archive only.
	if err := os.WriteFile(filepath.Join(fixture.root, "Documents", "megabytes.bin"), bytes.Repeat([]byte("fixture-payload\n"), 131072), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := fixture.cfg
	cfg.StateDir = filepath.Join(root, "state")
	cfg.RemoteLockPath = filepath.Join(root, "borg.lock")
	cfg.Snapshots.Borg.Binary = binary
	cfg.Snapshots.Borg.Repository = filepath.Join(root, "repository")
	cfg.Snapshots.Borg.CacheDir = filepath.Join(root, "cache")
	cfg.Snapshots.Borg.SecurityDir = filepath.Join(root, "security")
	cfg.Snapshots.Borg.PassphraseFile = filepath.Join(root, "passphrase")
	if err := os.WriteFile(cfg.Snapshots.Borg.PassphraseFile, []byte("disposable-fixture-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(cfg.Snapshots.Borg.Repository); !os.IsNotExist(err) {
		t.Fatal("repository must be newly absent")
	}
	request := directArchiveV2Request(fixture)
	var sourceBytes int64
	entries := 0
	for _, source := range []string{fixture.root, fixture.operational.PackageDir, fixture.provenanceDir} {
		err := filepath.Walk(source, func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			entries++
			if info.Mode().IsRegular() {
				sourceBytes += info.Size()
			}
			if sourceBytes > 8<<20 || entries > 128 {
				return fmt.Errorf("fixture size limit")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	backend := NewBorgSnapshotBackend(cfg)
	if _, err := backend.Runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
		t.Fatal(err)
	}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: cfg, RequestV2: &request})
	if err != nil || !result.Committed {
		t.Fatalf("fixture archive: %v %+v", err, result)
	}
	if result.BorgCommandCounts["create"] != 1 || result.BorgCommandCounts["delete"] != 0 || result.BorgCommandCounts["prune"] != 0 || result.BorgCommandCounts["compact"] != 0 {
		t.Fatal("unexpected archive operations")
	}
	for name, value := range map[string]any{"cloud.json": cfg, "fixture.json": map[string]any{"ref": request.ArchiveRef, "archive": result.Archive, "manifest_sha256": result.ManifestSHA256, "source_bytes": sourceBytes, "source_entries": entries, "payload_root": result.ManifestV2.Roots[0].ArchivePath}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("compact archive: %d bytes, %d entries, manifest %s", sourceBytes, entries, result.ManifestSHA256)
}
