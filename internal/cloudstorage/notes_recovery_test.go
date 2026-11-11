package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/hermesprofile"
)

func notesCloudFixture(t *testing.T) (backupstrategy.NotesRecoveryPublication, map[string][]byte) {
	t.Helper()
	pub := backupstrategy.NotesRecoveryPublication{Schema: backupstrategy.NotesRecoveryPublicationSchema, ID: "notes-20261019T020000Z-12345678",
		Root: filepath.Join(t.TempDir(), "captures"), PackageID: "operational-fixture", CapturedAt: time.Now().UTC(), RecordedAt: time.Now().UTC(),
		ManifestSHA256: strings.Repeat("a", 64), CohortSHA256: strings.Repeat("b", 64), Identity: strings.Repeat("c", 64), OwnerUID: uint64(os.Getuid()),
		Directories: []backupstrategy.NotesRecoveryDirectory{{Path: ".", Mode: 0700}, {Path: "operational-package", Mode: 0700}}}
	contents := map[string][]byte{}
	for _, name := range []string{"cohort.json", "main-state.tar.gz", "operational-package/manifest.json", "operational-package/manifest.sha256"} {
		raw := []byte(name + " fixture")
		hash := sha256.Sum256(raw)
		pub.Files = append(pub.Files, backupstrategy.NotesRecoveryFile{Path: name, Mode: 0600, Size: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])})
		contents[name] = raw
	}
	return pub, contents
}

func TestNotesRecoveryFrozenMetadataAssurance(t *testing.T) {
	for _, scenario := range []string{"exact", "missing", "extra", "foreign-owner", "receipt-mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			pub, contents := notesCloudFixture(t)
			root := strings.TrimPrefix(filepath.ToSlash(filepath.Join(pub.Root, pub.ID)), "/")
			extracts := 0
			runner := BorgCommandRunner{Config: fixture.cfg, StreamExec: func(_ context.Context, _ string, cmd BorgCommand, _ []string, w io.Writer) error {
				if cmd.Name() == "list" {
					if strings.Contains(strings.Join(cmd.Args, " "), "{sha256}") {
						t.Fatal("large payload hashing requested")
					}
					uid := pub.OwnerUID
					if scenario == "foreign-owner" {
						uid++
					}
					enc := json.NewEncoder(w)
					for _, dir := range pub.Directories {
						if err := enc.Encode(map[string]any{"path": path.Join(root, dir.Path), "type": "d", "mode": "drwx------", "size": 0, "uid": uid, "healthy": true}); err != nil {
							return err
						}
					}
					for i, file := range pub.Files {
						if scenario == "missing" && i == 0 {
							continue
						}
						if err := enc.Encode(map[string]any{"path": root + "/" + file.Path, "type": "-", "mode": "-rw-------", "size": file.Size, "uid": uid, "healthy": true}); err != nil {
							return err
						}
					}
					if scenario == "extra" {
						return enc.Encode(map[string]any{"path": root + "/unknown", "type": "-", "mode": "-rw-------", "size": 1, "uid": uid, "healthy": true})
					}
					return nil
				}
				if cmd.Name() != "extract" {
					return fmt.Errorf("unexpected command %v", cmd.Args)
				}
				name := strings.TrimPrefix(cmd.Args[len(cmd.Args)-1], root+"/")
				if name == "main-state.tar.gz" {
					t.Fatal("large archive payload extracted")
				}
				raw := contents[name]
				if raw == nil {
					return fmt.Errorf("unexpected member %s", name)
				}
				if scenario == "receipt-mismatch" {
					raw = []byte("different")
				}
				extracts++
				_, err := w.Write(raw)
				return err
			}}
			err := verifyFrozenNotesRecovery(context.Background(), runner, "fixture-archive", pub)
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("scenario=%s error=%v", scenario, err)
			}
			if err == nil && extracts != 3 {
				t.Fatalf("expected just three small receipts, got %d", extracts)
			}
		})
	}
}

func TestNotesRecoveryProofHoldsExactCloudHistory(t *testing.T) {
	pub, _ := notesCloudFixture(t)
	cfg := DefaultConfig()
	cfg.StateDir = filepath.Join(filepath.Dir(pub.Root), "cloud")
	cfg.Snapshots.Borg.Repository = "fixture-repository"
	if err := os.Mkdir(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(pub)
	hash := sha256.Sum256(raw)
	proof := backupstrategy.NotesRecoveryProtection{Schema: backupstrategy.NotesRecoveryProtectionSchema, Publication: pub,
		PublicationSHA256: hex.EncodeToString(hash[:]), Repository: cfg.Snapshots.Borg.Repository, Archive: "history-exact", ArchiveRef: "exact",
		ArchiveManifestSHA256: strings.Repeat("d", 64), VerifiedAt: time.Now().UTC(), VerificationBasis: "stable_publication_and_authenticated_archive_metadata_v1"}
	root := backupstrategy.NotesRecoveryProtectionRoot(cfg.StateDir)
	if err := backupstrategy.WriteNotesRecoveryRecord(root, pub.ID, proof, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkNotesRecoveryCloudPrunePins(cfg, SnapshotRetentionPlan{Remove: []SnapshotRetentionDecision{{Archive: "history-other"}}}); err != nil {
		t.Fatal(err)
	}
	if err := checkNotesRecoveryCloudPrunePins(cfg, SnapshotRetentionPlan{Remove: []SnapshotRetentionDecision{{Archive: proof.Archive}}}); err == nil {
		t.Fatal("exact recovery history prune allowed")
	}
	if err := os.WriteFile(filepath.Join(root, pub.ID+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkNotesRecoveryCloudPrunePins(cfg, SnapshotRetentionPlan{}); err == nil {
		t.Fatal("invalid history reference ignored")
	}
}

func TestNotesRecoveryRecheckRefusesForeignCacheOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root/operator ownership check runs on Main")
	}
	root := filepath.Join(t.TempDir(), "cloud")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StateDir = root
	cfg.Snapshots.Backend = SnapshotBackendBorg
	cfg.Snapshots.Borg.Repository = "fixture"
	err := RecheckNotesRecoveryProtection(context.Background(), cfg, backupstrategy.NotesRecoveryProtection{Repository: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "existing state owner") {
		t.Fatalf("ownership refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "locks")); !os.IsNotExist(err) {
		t.Fatal("wrong owner created cloud state")
	}
}

func TestBorgDirectArchiveV3ReferenceCaptureLocal(t *testing.T) {
	binary := os.Getenv("LOOM_TEST_BORG_BINARY")
	if binary == "" {
		t.Skip("one explicit tiny local Borg check on Main")
	}
	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.Snapshots.Borg.Binary = binary
	distinct, _ := createDirectOperationalPackageAt(t, filepath.Dir(fixture.operational.PackageDir), "operational-distinct")
	app := filepath.Join(t.TempDir(), "application-data")
	id := "notes-20261019T020000Z-12345678"
	capture := filepath.Join(app, "notes-recovery", id)
	if err := os.MkdirAll(capture, 0700); err != nil {
		t.Fatal(err)
	}
	reference := backupstrategy.NotesRecoveryOperationalReference{Schema: backupstrategy.NotesRecoveryReferenceSchema,
		PackageID: distinct.Verification.PackageID, ManifestSHA256: distinct.ManifestSHA256}
	refRaw, _ := json.Marshal(reference)
	hash := func(raw []byte) string { sha := sha256.Sum256(raw); return hex.EncodeToString(sha[:]) }
	state := []byte("owned local Notes recovery fixture")
	cohort, _ := json.Marshal(map[string]any{"schema": "loom.notes_recovery.v2", "id": id, "captured_at": time.Now().UTC(),
		"package_id": reference.PackageID, "manifest_sha256": reference.ManifestSHA256, "main_state_sha256": hash(state), "operational_reference_sha256": hash(refRaw)})
	for name, raw := range map[string][]byte{"cohort.json": cohort, "operational-reference.json": refRaw, "main-state.tar.gz": state} {
		if err := os.WriteFile(filepath.Join(capture, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	records := filepath.Join(filepath.Dir(app), "publications")
	pub, err := backupstrategy.RecordNotesRecoveryPublication(context.Background(), filepath.Dir(capture), id, records, false, filepath.Dir(fixture.operational.PackageDir))
	if err != nil {
		t.Fatal(err)
	}
	base := directArchiveV2Request(fixture)
	base.Roots = append(base.Roots, backupstrategy.DirectArchiveRoot{Name: "application_data", Path: app})
	request := backupstrategy.DirectArchiveRequestV3{Schema: backupstrategy.DirectArchiveRequestSchemaV3, Archive: base, NotesRecovery: []backupstrategy.NotesRecoveryPublication{pub}}
	backend := NewBorgSnapshotBackend(fixture.cfg)
	if _, err := backend.Runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
		t.Fatal(err)
	}
	policy := hermesprofile.Policy{Enabled: false}
	result, err := backend.ArchiveCanonicalRoots(context.Background(), DirectArchiveInput{Config: fixture.cfg, RequestV3: &request, HermesPolicy: &policy, NotesRecoveryPublicationsRoot: records})
	if err != nil || !result.Committed || result.ManifestSchema != backupstrategy.DirectArchiveManifestSchemaV3 {
		t.Fatalf("archive %+v %v", result, err)
	}
	proof, err := backupstrategy.ReadNotesRecoveryProtection(backupstrategy.NotesRecoveryProtectionRoot(fixture.cfg.StateDir), id)
	if err != nil || proof.Dependency == nil {
		t.Fatalf("protection %+v %v; observations=%+v", proof, err, result.NotesRecovery)
	}
	if err := os.Remove(filepath.Join(backupstrategy.NotesRecoveryProtectionRoot(fixture.cfg.StateDir), id+".json")); err != nil {
		t.Fatal(err)
	}
	proof, err = RecordExistingNotesRecoveryProtection(context.Background(), fixture.cfg, records, id, result.Archive)
	if err != nil || proof.Dependency == nil || proof.ArchiveManifestSHA256 != result.ManifestSHA256 {
		t.Fatalf("existing archive protection %+v %v", proof, err)
	}
	if err := RecheckNotesRecoveryProtection(context.Background(), fixture.cfg, proof); err != nil {
		t.Fatal(err)
	}
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: base.NodeID, Ref: result.Archive, To: filepath.Join(t.TempDir(), "fetched")})
	if err != nil || fetched.Status != SnapshotStatusSucceeded {
		t.Fatalf("fetch %+v %v", fetched, err)
	}
	prepared, err := readAuthenticatedDirectArchiveManifest(context.Background(), backend.Runner, fixture.cfg, result.Archive)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := backupstrategy.ResolveExtractedNotesRecovery(context.Background(), fetched.TargetDir, prepared, id)
	if err != nil || filepath.Base(resolved.OperationalPackagePath) != distinct.Verification.PackageID {
		t.Fatalf("distinct recovery dependency: %+v %v", resolved, err)
	}
	if result.BorgCommandCounts["create"] != 1 || result.BorgCommandCounts["delete"] != 0 || result.BorgCommandCounts["prune"] != 0 {
		t.Fatal("unexpected mutation outside the tiny fixture archive")
	}
}

func TestNotesRecoveryReferenceFetchIncludesDistinctPackage(t *testing.T) {
	primary := backupstrategy.DirectArchiveOperationalPackageManifestV2{PackageID: "operational-primary", ArchivePath: "packages/primary"}
	distinct := backupstrategy.DirectArchiveOperationalPackageManifestV2{PackageID: "operational-distinct", ArchivePath: "packages/distinct"}
	base := backupstrategy.DirectArchiveManifestV2{OperationalPackage: primary,
		ProvenancePackage: backupstrategy.DirectArchiveProvenancePackageManifestV2{ArchivePath: "provenance"}}
	prepared := backupstrategy.PreparedDirectArchiveManifest{ManifestV2: &base, ManifestV3: &backupstrategy.DirectArchiveManifestV3{
		Archive: base, OperationalDependencies: []backupstrategy.NotesRecoveryDependency{{Package: primary}, {Package: distinct}}}}
	counts := map[string]int{}
	for _, name := range directArchiveV2FetchPaths(prepared) {
		counts[name]++
	}
	if counts[primary.ArchivePath] != 1 || counts[distinct.ArchivePath] != 1 {
		t.Fatalf("fetch must select each distinct package once: %+v", counts)
	}
}
