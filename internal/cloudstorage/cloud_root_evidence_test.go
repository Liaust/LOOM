package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/hermesprofile"
)

func TestSnapshotRecoveryEvidenceRedactsSourcePaths(t *testing.T) {
	prepared := backupstrategy.PreparedDirectArchiveManifest{ManifestV2: &backupstrategy.DirectArchiveManifestV2{
		Schema: backupstrategy.DirectArchiveManifestSchemaV2,
		OperationalPackage: backupstrategy.DirectArchiveOperationalPackageManifestV2{
			PackageID: "operational-frozen", ManifestSHA256: "op-hash", SourcePath: "/private/operational"},
		ProvenancePackage: backupstrategy.DirectArchiveProvenancePackageManifestV2{
			PackageID: "provenance-frozen", ManifestSHA256: "graph-hash", SourcePath: "/private/provenance"},
		HermesRecovery: []hermesprofile.Evidence{{ID: "native-frozen", Path: "/private/agent",
			ManifestSHA256: "native-hash", Files: []hermesprofile.File{{Path: "profile.zip", Size: 12, SHA256: "payload-hash"}}}},
	}}
	evidence := snapshotRecoveryEvidence(prepared)
	if evidence.OperationalPackage.ID != "operational-frozen" || evidence.ProvenancePackage.ManifestSHA256 != "graph-hash" ||
		len(evidence.HermesRecovery) != 1 || evidence.HermesRecovery[0].ManifestSHA256 != "native-hash" ||
		evidence.HermesRecovery[0].Files["profile.zip"].Size != 12 {
		t.Fatalf("frozen identities lost: %+v", evidence)
	}
	raw, _ := json.Marshal(evidence)
	if strings.Contains(string(raw), "/private/") {
		t.Fatal("source paths leaked")
	}
	if snapshotRecoveryEvidence(backupstrategy.PreparedDirectArchiveManifest{}) != nil {
		t.Fatal("missing envelope fabricated identities")
	}
}

func TestDirectArchiveCloudRootEvidenceUsesFrozenManifest(t *testing.T) {
	f := newDirectArchiveCloudFixture(t)
	q := directArchiveV2Request(f)
	q.Roots[0].Name = "application_data"
	q.Exclusions = nil
	name := "__loom-direct-user-data-" + q.NodeID + "-" + q.ArchiveRef
	p, err := backupstrategy.PrepareDirectArchiveManifestV2(t.Context(), backupstrategy.DirectArchiveManifestPrepareInputV2{Backend: backupstrategy.DirectArchiveBackendBorg, Repository: f.cfg.Snapshots.Borg.Repository, ArchiveName: name, Request: q})
	if err != nil {
		t.Fatal(err)
	}
	evidence := backupcoverage.CloudRootsFromManifest(p.ManifestV2, p.ManifestSHA256)
	found := false
	for _, e := range evidence {
		if e.Name == "application_data" {
			found = e.PathSHA256 == backupcoverage.CloudRootPathSHA256(q.Roots[0].Path) && e.SnapshotAt.Equal(q.CreatedAt) && e.NodeID == q.NodeID
		}
	}
	if !found {
		t.Fatal("authenticated root absent", evidence)
	}
	raw, _ := json.Marshal(evidence)
	if strings.Contains(string(raw), q.Roots[0].Path) {
		t.Fatal("physical path leaked")
	}
	if len(backupcoverage.CloudRootsFromManifest(p.ManifestV2, strings.Repeat("a", 64))) != 0 {
		t.Fatal("hash mismatch accepted")
	}
	p.ManifestV2.CreatedAt = p.ManifestV2.CreatedAt.AddDate(0, 0, 1)
	if len(backupcoverage.CloudRootsFromManifest(p.ManifestV2, p.ManifestSHA256)) != 0 {
		t.Fatal("live timestamp substituted for frozen snapshot")
	}
	q.Exclusions = []backupstrategy.DirectArchiveExclusion{{Root: "application_data", RelativePath: ".loom-acceptance"}}
	p, err = backupstrategy.PrepareDirectArchiveManifestV2(t.Context(), backupstrategy.DirectArchiveManifestPrepareInputV2{Backend: backupstrategy.DirectArchiveBackendBorg, Repository: f.cfg.Snapshots.Borg.Repository, ArchiveName: name, Request: q})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range backupcoverage.CloudRootsFromManifest(p.ManifestV2, p.ManifestSHA256) {
		if e.Name == "application_data" {
			t.Fatal("partial root represented as complete coverage")
		}
	}
}

func TestDirectArchiveV3CloudRootEvidenceAuthenticatesOuterEnvelope(t *testing.T) {
	f := newDirectArchiveCloudFixture(t)
	q := directArchiveV2Request(f)
	app := filepath.Join(t.TempDir(), "application-data")
	id := "notes-20261019T020000Z-12345678"
	capture := filepath.Join(app, "notes-recovery", id)
	if err := os.MkdirAll(capture, 0700); err != nil {
		t.Fatal(err)
	}
	ref := backupstrategy.NotesRecoveryOperationalReference{Schema: backupstrategy.NotesRecoveryReferenceSchema, PackageID: f.operational.Verification.PackageID, ManifestSHA256: f.operational.ManifestSHA256}
	refRaw, _ := json.Marshal(ref)
	state := []byte("tiny coherent fixture")
	hash := func(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
	cohort, _ := json.Marshal(map[string]any{"schema": "loom.notes_recovery.v2", "id": id, "captured_at": time.Now().UTC(), "package_id": ref.PackageID, "manifest_sha256": ref.ManifestSHA256, "main_state_sha256": hash(state), "operational_reference_sha256": hash(refRaw)})
	for name, data := range map[string][]byte{"cohort.json": cohort, "main-state.tar.gz": state, "operational-reference.json": refRaw} {
		if err := os.WriteFile(filepath.Join(capture, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pub, err := backupstrategy.RecordNotesRecoveryPublication(t.Context(), filepath.Dir(capture), id, filepath.Join(t.TempDir(), "publications"), false, filepath.Dir(f.operational.PackageDir))
	if err != nil {
		t.Fatal(err)
	}
	q.Roots = append(q.Roots, backupstrategy.DirectArchiveRoot{Name: "application_data", Path: app})
	prepared, err := backupstrategy.PrepareDirectArchiveManifestV3(t.Context(), backupstrategy.DirectArchiveManifestPrepareInputV2{Backend: backupstrategy.DirectArchiveBackendBorg, Repository: f.cfg.Snapshots.Borg.Repository, ArchiveName: directBorgArchiveName(q.NodeID, q.ArchiveRef, q.ArchiveClass), Request: q}, backupstrategy.DirectArchiveRequestV3{Schema: backupstrategy.DirectArchiveRequestSchemaV3, Archive: q, NotesRecovery: []backupstrategy.NotesRecoveryPublication{pub}})
	if err != nil {
		t.Fatal(err)
	}
	if len(backupcoverage.CloudRootsFromManifest(prepared.ManifestV2, prepared.ManifestSHA256)) != 0 {
		t.Fatal("inner manifest incorrectly matched outer hash")
	}
	evidence := backupcoverage.CloudRootsFromVersionedManifest(prepared.ManifestV2, prepared.ManifestV3, prepared.ManifestSHA256)
	if len(evidence) == 0 {
		t.Fatal("valid v3 lost all root evidence")
	}
	fake := newFakeDirectBorg(t)
	name := prepared.ManifestV2.ArchiveName
	fake.archives[name], fake.manifestByArchive[name], fake.hashByArchive[name] = true, prepared.ManifestBytes, prepared.HashFileBytes
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: f.cfg, Exec: fake.exec, StreamExec: fake.streamExec}}
	verified, err := backend.Verify(context.Background(), SnapshotVerifyInput{Config: f.cfg, NodeID: q.NodeID, Ref: q.ArchiveRef, SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileMetadata, RootEvidence: true}})
	if err != nil || verified.Status != SnapshotStatusSucceeded || verified.SourceManifestSHA256 != prepared.ManifestSHA256 || len(verified.RootCoverage) != len(evidence) {
		t.Fatalf("exact historical roots: %+v %v", verified, err)
	}
	if verified.RecoveryEvidence == nil || verified.RecoveryEvidence.ManifestSchema != prepared.ManifestV3.Schema ||
		verified.RecoveryEvidence.OperationalPackage.ID != q.OperationalPackage.PackageID ||
		len(verified.RecoveryEvidence.NotesRecovery) != 1 || verified.RecoveryEvidence.NotesRecovery[0].ID != id ||
		verified.RecoveryEvidence.NotesRecovery[0].CohortSHA256 != pub.CohortSHA256 ||
		verified.RecoveryEvidence.NotesRecovery[0].Files["cohort.json"].SHA256 != pub.CohortSHA256 {
		t.Fatalf("authenticated recovery identities missing: %+v", verified.RecoveryEvidence)
	}
	rawEvidence, _ := json.Marshal(verified.RecoveryEvidence)
	if strings.Contains(string(rawEvidence), app) || strings.Contains(string(rawEvidence), f.operational.PackageDir) {
		t.Fatal("absolute source path leaked into recovery identities")
	}
	ordinary, err := backend.Verify(t.Context(), SnapshotVerifyInput{Config: f.cfg, NodeID: q.NodeID, Ref: q.ArchiveRef})
	if err != nil || ordinary.Status != SnapshotStatusSucceeded || ordinary.RecoveryEvidence != nil {
		t.Fatalf("ordinary verify exposed extra identities: %+v %v", ordinary, err)
	}
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "extract:") && !strings.Contains(call, backupstrategy.DirectArchiveEvidenceDir) {
			t.Fatal("file payload fetched", call)
		}
	}
	fake.hashByArchive[name] = []byte(strings.Repeat("0", 64) + "  " + backupstrategy.DirectArchiveManifestFile + "\n")
	tampered, err := backend.Verify(t.Context(), SnapshotVerifyInput{Config: f.cfg, NodeID: q.NodeID, Ref: q.ArchiveRef, SnapshotVerifyOptions: SnapshotVerifyOptions{Profile: SnapshotVerifyProfileMetadata, RootEvidence: true}})
	if err != nil || tampered.Status != SnapshotStatusFailed || tampered.RecoveryEvidence != nil {
		t.Fatalf("unauthenticated recovery identities returned: %+v %v", tampered, err)
	}
	prepared.ManifestV3.Archive.CreatedAt = prepared.ManifestV3.Archive.CreatedAt.Add(time.Minute)
	if len(backupcoverage.CloudRootsFromVersionedManifest(prepared.ManifestV2, prepared.ManifestV3, prepared.ManifestSHA256)) != 0 {
		t.Fatal("changed v3 snapshot accepted")
	}
	if _, err := NormalizeSnapshotVerifyOptions(q.ArchiveRef, SnapshotVerifyOptions{Profile: SnapshotVerifyProfileArchiveData, RootEvidence: true}); err == nil {
		t.Fatal("root metadata flag enabled data verification")
	}
}
