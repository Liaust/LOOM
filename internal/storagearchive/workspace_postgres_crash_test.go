package storagearchive

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
)

const workspaceArchiveCrashExitCode = 91

func TestWorkspacePostgresCrashHelper(t *testing.T) {
	if os.Getenv("LOOM_WORKSPACE_ARCHIVE_CRASH_HELPER") != "1" {
		t.Skip("helper is launched only by the disposable PostgreSQL crash acceptance")
	}
	planPayload, err := os.ReadFile(os.Getenv("LOOM_WORKSPACE_ARCHIVE_HELPER_PLAN"))
	if err != nil {
		t.Fatal(err)
	}
	var plan WorkspaceArchivePlan
	if err := json.Unmarshal(planPayload, &plan); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", os.Getenv("LOOM_WORKSPACE_ARCHIVE_HELPER_DB_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := newWorkspacePostgresService(t, db, TrustedWorkspaceRoots{
		BoxRoot:     os.Getenv("LOOM_WORKSPACE_ARCHIVE_HELPER_BOX"),
		StorageRoot: os.Getenv("LOOM_WORKSPACE_ARCHIVE_HELPER_STORAGE"),
	}, os.Getenv("LOOM_WORKSPACE_ARCHIVE_HELPER_CREDENTIALS"))
	boundary := WorkspaceMoveBoundary(os.Getenv("LOOM_WORKSPACE_ARCHIVE_HELPER_BOUNDARY"))
	service.FailureHook = func(observed WorkspaceMoveBoundary) error {
		if observed == boundary {
			os.Exit(workspaceArchiveCrashExitCode)
		}
		return nil
	}
	if plan.OperationKind == WorkspaceOperationArchive {
		_, err = service.ApplyArchive(context.Background(), plan, plan.PlanDigest)
	} else {
		_, err = service.ApplyRestore(context.Background(), plan, plan.PlanDigest)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("crash boundary %q was not reached", boundary)
}

func TestWorkspacePostgresCrashAcceptance(t *testing.T) {
	dbURL := strings.TrimSpace(os.Getenv("LOOM_WORKSPACE_ARCHIVE_TEST_DB_URL"))
	if dbURL == "" {
		t.Skip("LOOM_WORKSPACE_ARCHIVE_TEST_DB_URL is not set; disposable PostgreSQL 17 acceptance is owned by the Slice 4 smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	db := openWorkspaceArchiveAcceptanceDB(t, ctx, dbURL)
	defer db.Close()

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrations.Up(ctx, dbURL, filepath.Join(repoRoot, "migrations"))
	if err != nil || result.CurrentVersion < 63 {
		t.Fatalf("migrate disposable PostgreSQL through 00063: result=%#v err=%v", result, err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatalf("bootstrap disposable PostgreSQL: %v", err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_workspace_archive_acceptance")
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	boxRoot := filepath.Join(root, "box")
	storageRoot := filepath.Join(root, "storage")
	credentialDirectory := filepath.Join(root, "credentials")
	for _, directory := range []string{
		filepath.Join(boxRoot, "Topics"), filepath.Join(boxRoot, "Projects"), filepath.Join(boxRoot, "Library"),
		filepath.Join(storageRoot, "archive", "topics"), filepath.Join(storageRoot, "archive", "projects"), filepath.Join(storageRoot, "archive", "library"), credentialDirectory,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credentialDirectory, err = filepath.EvalSymlinks(credentialDirectory)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey := hex.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	credentialPath := filepath.Join(credentialDirectory, WorkspaceArchiveManifestCredentialName)
	if err := os.WriteFile(credentialPath, []byte(encodedKey), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credentialPath, 0o400); err != nil {
		t.Fatal(err)
	}
	roots := TrustedWorkspaceRoots{BoxRoot: boxRoot, StorageRoot: storageRoot}

	t.Run("topic_and_library_surface_kernel", func(t *testing.T) {
		for _, kind := range []WorkspaceKind{WorkspaceKindTopic, WorkspaceKindLibraryItem} {
			fixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, kind, "complete")
			before := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath)
			beforeAllocation := allocatedBlocksAndInodes(t, fixture.paths.Active.AbsolutePath)
			archive, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			requireWorkspacePostgresSteadyState(t, fixture, archive, CustodyArchived, before, beforeAllocation)
			if replay, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest); err != nil || replay.Operation.Phase != PhaseArchiveComplete {
				t.Fatalf("archive replay = %#v, %v", replay, err)
			}
			conflict := fixture.archivePlan
			conflict.Reason = "conflicting reviewed reason"
			if err := SealWorkspaceArchivePlan(&conflict); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.service.ApplyArchive(ctx, conflict, conflict.PlanDigest); err == nil {
				t.Fatal("archive replay conflict was accepted")
			}
			restorePlan, err := fixture.service.PlanRestore(ctx, WorkspaceRestorePlanInput{ArchiveOperationID: fixture.archivePlan.OperationID, ActorID: req.ActorID, Reason: "acceptance restore"})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := fixture.service.ApplyRestore(ctx, restorePlan, restorePlan.PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			requireWorkspacePostgresSteadyState(t, fixture, restored, CustodyActive, before, beforeAllocation)
			if replay, err := fixture.service.ApplyRestore(ctx, restorePlan, restorePlan.PlanDigest); err != nil || replay.Operation.Phase != PhaseRestoreComplete {
				t.Fatalf("restore replay = %#v, %v", replay, err)
			}
			requireWorkspaceLifecycleEventCount(t, ctx, db, fixture.archivePlan.OperationID, 1)
			requireWorkspaceLifecycleEventCount(t, ctx, db, restorePlan.OperationID, 1)
		}
	})

	t.Run("large_identity_replay_conflicts_are_lossless", func(t *testing.T) {
		const firstAliasedInteger uint64 = 1 << 53
		for _, test := range []struct {
			name string
			set  func(*PathIdentity, uint64)
		}{
			{name: "device", set: func(identity *PathIdentity, value uint64) { identity.DeviceID = value }},
			{name: "inode", set: func(identity *PathIdentity, value uint64) { identity.Inode = value }},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "lossless-replay-"+test.name)
				inspection, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest)
				if err != nil || inspection.Manifest == nil || inspection.Operation.ProjectionsCommittedAt == nil {
					t.Fatalf("prepare replay fixture = %#v, %v", inspection, err)
				}
				storedManifest := *inspection.Manifest
				candidateManifest := storedManifest
				test.set(&storedManifest.ArchiveSourceIdentity, firstAliasedInteger)
				test.set(&candidateManifest.ArchiveSourceIdentity, firstAliasedInteger+1)
				storedIdentity, err := json.Marshal(storedManifest.ArchiveSourceIdentity)
				if err != nil {
					t.Fatal(err)
				}
				storedJSON, err := json.Marshal(storedManifest)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `
					UPDATE storage.workspace_archive_manifests
					SET archive_source_identity_json=$2, manifest_json=$3
					WHERE workspace_archive_operation_id=$1
				`, fixture.archivePlan.OperationID, storedIdentity, storedJSON); err != nil {
					t.Fatal(err)
				}
				event := eventForArchive(inspection.Operation, candidateManifest, *inspection.Operation.ProjectionsCommittedAt)
				projection, err := journalProjectionInput(fixture.archivePlan, candidateManifest, event, *inspection.Operation.ProjectionsCommittedAt)
				if err != nil {
					t.Fatal(err)
				}
				projection.ArchiveSourceIdentityJSON, err = json.Marshal(candidateManifest.ArchiveSourceIdentity)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := storagecatalog.NewService(db).CommitWorkspaceArchiveProjection(ctx, projection); err == nil || !strings.Contains(err.Error(), "manifest replay conflict") {
					t.Fatalf("adjacent %s integers above 2^53 did not conflict: %v", test.name, err)
				}
			})
		}
	})

	t.Run("archive_process_restart_boundaries", func(t *testing.T) {
		boundaries := []WorkspaceMoveBoundary{BoundaryAfterIntent, BoundaryAfterPayloadMove, BoundaryAfterMovedRecord, BoundaryAfterProjections, BoundaryAfterComplete}
		for index, boundary := range boundaries {
			t.Run(string(boundary), func(t *testing.T) {
				fixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, fmt.Sprintf("archive-crash-%02d", index))
				before := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath)
				runWorkspaceCrashProcess(t, dbURL, roots, credentialDirectory, fixture.archivePlan, boundary)
				restarted := newWorkspacePostgresService(t, db, roots, credentialDirectory)
				inspection, err := restarted.RecoverOperation(ctx, fixture.archivePlan.OperationID)
				if err != nil || inspection.Operation.Phase != PhaseArchiveComplete || inspection.Custody != CustodyArchived {
					t.Fatalf("archive restart recovery = %#v, %v", inspection, err)
				}
				if got := snapshotWorkspacePayload(t, fixture.paths.ArchivePayload.AbsolutePath); !reflect.DeepEqual(before, got) {
					t.Fatalf("archive crash fidelity mismatch at %s", boundary)
				}
				requireWorkspaceLifecycleEventCount(t, ctx, db, fixture.archivePlan.OperationID, 1)
			})
		}
	})

	t.Run("restore_process_restart_boundaries", func(t *testing.T) {
		boundaries := []WorkspaceMoveBoundary{BoundaryAfterRestoreIntent, BoundaryAfterRestorePayloadMove, BoundaryAfterRestoreMovedRecord, BoundaryAfterRestoreProjections, BoundaryAfterRestoreComplete}
		for index, boundary := range boundaries {
			t.Run(string(boundary), func(t *testing.T) {
				fixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindLibraryItem, fmt.Sprintf("restore-crash-%02d", index))
				before := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath)
				if _, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest); err != nil {
					t.Fatal(err)
				}
				restorePlan, err := fixture.service.PlanRestore(ctx, WorkspaceRestorePlanInput{ArchiveOperationID: fixture.archivePlan.OperationID, ActorID: req.ActorID, Reason: "crash restore"})
				if err != nil {
					t.Fatal(err)
				}
				runWorkspaceCrashProcess(t, dbURL, roots, credentialDirectory, restorePlan, boundary)
				restarted := newWorkspacePostgresService(t, db, roots, credentialDirectory)
				inspection, err := restarted.RecoverOperation(ctx, restorePlan.OperationID)
				if err != nil || inspection.Operation.Phase != PhaseRestoreComplete || inspection.Custody != CustodyActive {
					t.Fatalf("restore restart recovery = %#v, %v", inspection, err)
				}
				if got := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath); !reflect.DeepEqual(before, got) {
					t.Fatalf("restore crash fidelity mismatch at %s", boundary)
				}
				requireWorkspaceLifecycleEventCount(t, ctx, db, restorePlan.OperationID, 1)
			})
		}
	})

	t.Run("catalog_and_event_transaction_failures", func(t *testing.T) {
		for _, target := range []string{"catalog", "event"} {
			t.Run(target, func(t *testing.T) {
				fixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "transaction-"+target)
				installWorkspaceArchiveFailureTrigger(t, ctx, db, target)
				_, applyErr := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest)
				if applyErr == nil {
					t.Fatalf("%s transaction failure was not injected", target)
				}
				dropWorkspaceArchiveFailureTrigger(t, ctx, db, target)
				requireWorkspaceLifecycleEventCount(t, ctx, db, fixture.archivePlan.OperationID, 0)
				restarted := newWorkspacePostgresService(t, db, roots, credentialDirectory)
				inspection, err := restarted.RecoverOperation(ctx, fixture.archivePlan.OperationID)
				if err != nil || inspection.Operation.Phase != PhaseArchiveComplete {
					t.Fatalf("%s rollback recovery = %#v, %v", target, inspection, err)
				}
				requireWorkspaceLifecycleEventCount(t, ctx, db, fixture.archivePlan.OperationID, 1)
			})
		}
	})

	t.Run("path_and_evidence_attacks", func(t *testing.T) {
		fixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "source-substitute")
		original := fixture.paths.Active.AbsolutePath + "-original"
		if err := os.Rename(fixture.paths.Active.AbsolutePath, original); err != nil {
			t.Fatal(err)
		}
		copyWorkspaceFixture(t, original, fixture.paths.Active.AbsolutePath)
		if _, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest); err == nil {
			t.Fatal("same-content source inode substitution was accepted")
		}

		fixture = newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "destination-collision")
		if err := os.MkdirAll(fixture.paths.ArchivePayload.AbsolutePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest); err == nil {
			t.Fatal("destination collision was accepted")
		}

		fixture = newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "ancestor-substitute")
		topicsParent := filepath.Join(boxRoot, "Topics")
		heldTopicsParent := filepath.Join(boxRoot, "Topics-held-for-acceptance")
		if err := os.Rename(topicsParent, heldTopicsParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(topicsParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest); err == nil {
			t.Fatal("source ancestor substitution was accepted")
		}
		if err := os.Remove(topicsParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(heldTopicsParent, topicsParent); err != nil {
			t.Fatal(err)
		}

		fixture = newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "destination-ancestor-substitute")
		archiveTopicsParent := filepath.Join(storageRoot, "archive", "topics")
		heldArchiveTopicsParent := filepath.Join(storageRoot, "archive", "topics-held-for-acceptance")
		if err := os.Rename(archiveTopicsParent, heldArchiveTopicsParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(archiveTopicsParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.ApplyArchive(ctx, fixture.archivePlan, fixture.archivePlan.PlanDigest); err == nil {
			t.Fatal("destination ancestor substitution was accepted")
		}
		if err := os.Remove(archiveTopicsParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(heldArchiveTopicsParent, archiveTopicsParent); err != nil {
			t.Fatal(err)
		}

		symlinkSlug := "symlink-source"
		external := filepath.Join(root, "external-source")
		if err := os.MkdirAll(external, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(boxRoot, "Topics", symlinkSlug)); err != nil {
			t.Fatal(err)
		}
		service := newWorkspacePostgresService(t, db, roots, credentialDirectory)
		if _, err := service.PlanArchive(ctx, WorkspaceArchivePlanInput{Kind: WorkspaceKindTopic, ObjectID: "topic_symlink", Slug: symlinkSlug, ActorID: req.ActorID, Reason: "reject symlink"}); err == nil {
			t.Fatal("symlink source was accepted")
		}

		specialSlug := "special-source"
		specialRoot := filepath.Join(boxRoot, "Topics", specialSlug)
		if err := os.MkdirAll(specialRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(specialRoot, "pipe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.PlanArchive(ctx, WorkspaceArchivePlanInput{Kind: WorkspaceKindTopic, ObjectID: "topic_special", Slug: specialSlug, ActorID: req.ActorID, Reason: "reject special"}); err == nil {
			t.Fatal("special file was accepted")
		}

		validFixture := newWorkspacePostgresFixture(t, ctx, db, roots, credentialDirectory, req.ActorID, WorkspaceKindTopic, "cross-device-evidence")
		crossDevice := validFixture.archivePlan
		crossDevice.Destination.ParentIdentity.DeviceID++
		if err := SealWorkspaceArchivePlan(&crossDevice); err != nil {
			t.Fatal(err)
		}
		if err := ValidateWorkspaceArchivePlan(crossDevice, roots); err == nil || !strings.Contains(err.Error(), "same filesystem") {
			t.Fatalf("cross-device evidence error = %v", err)
		}
	})

	t.Run("historical_copy_and_secret_absence", func(t *testing.T) {
		payload, err := os.ReadFile(filepath.Join("testdata", "historical_copy_manifest_v07.json"))
		if err != nil {
			t.Fatal(err)
		}
		compatibility, err := ClassifyManifestEvidence(payload)
		if err != nil || compatibility.Class != ManifestEvidenceHistoricalCopy || compatibility.Physical != nil {
			t.Fatalf("historical compatibility = %#v, %v", compatibility, err)
		}
		var matches int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM (
				SELECT plan_json::text AS payload FROM storage.workspace_archive_operations
				UNION ALL SELECT manifest_json::text FROM storage.workspace_archive_manifests
				UNION ALL SELECT details::text FROM storage.workspace_lifecycle_events
				UNION ALL SELECT evidence_json::text FROM storage.workspace_archive_findings
			) evidence WHERE payload LIKE '%' || $1 || '%'
		`, encodedKey).Scan(&matches); err != nil {
			t.Fatal(err)
		}
		if matches != 0 {
			t.Fatalf("manifest key bytes appeared in %d PostgreSQL evidence rows", matches)
		}
	})
}

type workspacePostgresFixture struct {
	service     WorkspaceMoveService
	paths       ResolvedWorkspacePaths
	archivePlan WorkspaceArchivePlan
	entryID     string
	refID       string
}

func newWorkspacePostgresFixture(t *testing.T, ctx context.Context, db *sql.DB, roots TrustedWorkspaceRoots, credentialsDirectory, actorID string, kind WorkspaceKind, suffix string) workspacePostgresFixture {
	t.Helper()
	kindSlug := string(kind)
	if kind == WorkspaceKindLibraryItem {
		kindSlug = "library"
	}
	slug := kindSlug + "-" + strings.ReplaceAll(suffix, "_", "-")
	paths, err := ResolveWorkspacePaths(roots, kind, slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Active.AbsolutePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Active.AbsolutePath, "payload.txt"), []byte("workspace archive acceptance "+slug+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(paths.Active.AbsolutePath, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Active.AbsolutePath, "nested", "data.bin"), []byte{0, 1, 2, 3, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("payload.txt", filepath.Join(paths.Active.AbsolutePath, "payload-link")); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 2, 10, 11, 12, 123000000, time.UTC)
	if err := os.Chtimes(filepath.Join(paths.Active.AbsolutePath, "payload.txt"), stamp, stamp); err != nil {
		t.Fatal(err)
	}

	catalog := storagecatalog.NewService(db)
	entryID := ids.NewStorageEntryID()
	refID := ids.NewStoragePhysicalRefID()
	filePath := filepath.Join(paths.Active.AbsolutePath, "payload.txt")
	entry, err := catalog.RegisterEntry(ctx, storagecatalog.RegisterEntryInput{
		StorageEntryID: entryID, StorageClass: storagecatalog.StorageClassViewEntry,
		SourceArea: storagecatalog.SourceAreaNotes, LogicalPath: filepath.ToSlash(filepath.Join(paths.Active.RelativePath, "payload.txt")),
		OriginalSourcePath: filePath, CurrentViewPath: filepath.ToSlash(filepath.Join(paths.Active.RelativePath, "payload.txt")),
		FileClass: storagecatalog.FileClassText, ProcessingState: storagecatalog.ProcessingStateUnprocessed,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable, RetentionState: storagecatalog.RetentionStateNone,
		Metadata: json.RawMessage(`{"source":"workspace_archive_acceptance"}`),
	})
	if err != nil {
		t.Fatalf("register acceptance catalog entry: %v", err)
	}
	if _, err := catalog.RegisterPhysicalRef(ctx, storagecatalog.RegisterPhysicalRefInput{
		StoragePhysicalRefID: refID, StorageEntryID: entry.StorageEntryID,
		RefKind: storagecatalog.PhysicalRefKindLocalPath, URI: filePath,
		Status: storagecatalog.PhysicalRefStatusAvailable, Metadata: json.RawMessage(`{"source":"workspace_archive_acceptance"}`),
	}); err != nil {
		t.Fatalf("register acceptance physical ref: %v", err)
	}
	service := newWorkspacePostgresService(t, db, roots, credentialsDirectory)
	plan, err := service.PlanArchive(ctx, WorkspaceArchivePlanInput{Kind: kind, ObjectID: strings.ReplaceAll(string(kind)+"_object_"+suffix, "-", "_"), Slug: slug, ActorID: actorID, Reason: "independent disposable acceptance"})
	if err != nil {
		t.Fatalf("plan acceptance archive: %v", err)
	}
	if len(plan.CatalogRebinds) != 2 {
		t.Fatalf("catalog rebind count = %d, want entry and physical ref", len(plan.CatalogRebinds))
	}
	return workspacePostgresFixture{service: service, paths: paths, archivePlan: plan, entryID: entryID, refID: refID}
}

func newWorkspacePostgresService(t *testing.T, db *sql.DB, roots TrustedWorkspaceRoots, credentialsDirectory string) WorkspaceMoveService {
	t.Helper()
	provider, err := NewSystemdWorkspaceManifestKeyProvider(credentialsDirectory, "workspace-archive-acceptance-v1")
	if err != nil {
		t.Fatal(err)
	}
	key, err := provider.Lookup(context.Background(), "workspace-archive-acceptance-v1")
	if err != nil {
		t.Fatal(err)
	}
	zeroWorkspaceManifestKey(key)
	catalog := storagecatalog.NewService(db)
	return WorkspaceMoveService{Roots: roots, Catalog: catalog, Journal: catalog, ManifestKeyID: "workspace-archive-acceptance-v1", ManifestKey: provider.Lookup}
}

func openWorkspaceArchiveAcceptanceDB(t *testing.T, ctx context.Context, dbURL string) *sql.DB {
	t.Helper()
	parsed, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if !strings.HasPrefix(name, "loom_workspace_archive_") {
		t.Fatalf("refusing non-disposable PostgreSQL database %q", name)
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var relations int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND c.relkind IN ('r','p')`).Scan(&relations); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if relations != 0 {
		db.Close()
		t.Fatalf("disposable PostgreSQL database is not empty: %d relations", relations)
	}
	return db
}

func runWorkspaceCrashProcess(t *testing.T, dbURL string, roots TrustedWorkspaceRoots, credentialsDirectory string, plan WorkspaceArchivePlan, boundary WorkspaceMoveBoundary) {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "plan.json")
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkspacePostgresCrashHelper$", "-test.count=1")
	command.Env = append(os.Environ(),
		"LOOM_WORKSPACE_ARCHIVE_CRASH_HELPER=1",
		"LOOM_WORKSPACE_ARCHIVE_HELPER_DB_URL="+dbURL,
		"LOOM_WORKSPACE_ARCHIVE_HELPER_BOX="+roots.BoxRoot,
		"LOOM_WORKSPACE_ARCHIVE_HELPER_STORAGE="+roots.StorageRoot,
		"LOOM_WORKSPACE_ARCHIVE_HELPER_CREDENTIALS="+credentialsDirectory,
		"LOOM_WORKSPACE_ARCHIVE_HELPER_PLAN="+planPath,
		"LOOM_WORKSPACE_ARCHIVE_HELPER_BOUNDARY="+string(boundary),
	)
	err = command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != workspaceArchiveCrashExitCode {
		t.Fatalf("crash helper at %s returned %v", boundary, err)
	}
}

func requireWorkspacePostgresSteadyState(t *testing.T, fixture workspacePostgresFixture, inspection WorkspaceArchiveInspection, custody WorkspaceCustodyState, want []payloadSnapshotEntry, allocation blocksAndInodes) {
	t.Helper()
	if inspection.Custody != custody {
		t.Fatalf("custody=%s want=%s", inspection.Custody, custody)
	}
	activeExists := pathExists(fixture.paths.Active.AbsolutePath)
	archivedExists := pathExists(fixture.paths.ArchivePayload.AbsolutePath)
	if activeExists == archivedExists {
		t.Fatalf("steady state has active=%t archived=%t; want exactly one payload", activeExists, archivedExists)
	}
	root := fixture.paths.Active.AbsolutePath
	if custody == CustodyArchived {
		root = fixture.paths.ArchivePayload.AbsolutePath
	}
	if got := snapshotWorkspacePayload(t, root); !reflect.DeepEqual(want, got) {
		t.Fatalf("payload fidelity changed\nwant=%#v\ngot=%#v", want, got)
	}
	if got := allocatedBlocksAndInodes(t, root); !reflect.DeepEqual(allocation, got) {
		t.Fatalf("allocated-byte/inode evidence changed\nwant=%#v\ngot=%#v", allocation, got)
	}
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func requireWorkspaceLifecycleEventCount(t *testing.T, ctx context.Context, db *sql.DB, operationID string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM storage.workspace_lifecycle_events WHERE workspace_archive_operation_id=$1`, operationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("event count for %s = %d, want %d", operationID, count, want)
	}
}

func installWorkspaceArchiveFailureTrigger(t *testing.T, ctx context.Context, db *sql.DB, target string) {
	t.Helper()
	table, action := "storage.workspace_lifecycle_events", "INSERT"
	if target == "catalog" {
		table, action = "storage.storage_entries", "UPDATE"
	}
	statement := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION storage.workspace_archive_acceptance_%s_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected %s failure'; END $$;
		CREATE TRIGGER workspace_archive_acceptance_%s_failure BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION storage.workspace_archive_acceptance_%s_failure();
	`, target, target, target, action, table, target)
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
}

func dropWorkspaceArchiveFailureTrigger(t *testing.T, ctx context.Context, db *sql.DB, target string) {
	t.Helper()
	table := "storage.workspace_lifecycle_events"
	if target == "catalog" {
		table = "storage.storage_entries"
	}
	statement := fmt.Sprintf(`DROP TRIGGER IF EXISTS workspace_archive_acceptance_%s_failure ON %s; DROP FUNCTION IF EXISTS storage.workspace_archive_acceptance_%s_failure()`, target, table, target)
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
}
