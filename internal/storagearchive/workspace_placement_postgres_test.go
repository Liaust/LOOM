package storagearchive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
)

func TestWorkspacePlacementPostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_WORKSPACE_PLACEMENT_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("owned Main disposable database only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := openWorkspaceArchiveAcceptanceDB(t, ctx, dbURL)
	defer db.Close()
	migrationRoot := os.Getenv("LOOM_WORKSPACE_PLACEMENT_MIGRATIONS")
	if migrationRoot == "" {
		t.Fatal("exact migration directory required")
	}
	if _, err := migrations.Up(ctx, dbURL, migrationRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_workspace_placement")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	roots := TrustedWorkspaceRoots{BoxRoot: filepath.Join(root, "box"), StorageRoot: filepath.Join(root, "storage")}
	credentials := filepath.Join(root, "credentials")
	for _, dir := range []string{filepath.Join(roots.BoxRoot, "Projects"), filepath.Join(roots.StorageRoot, "archive/projects"), credentials} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = PrepareRetainedRoots(roots.StorageRoot); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(credentials, WorkspaceArchiveManifestCredentialName), []byte(hex.EncodeToString(key)), 0400); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "relocated-v1", false: "named-v2"}[legacy], func(t *testing.T) {
			f := newWorkspacePostgresFixture(t, ctx, db, roots, credentials, req.ActorID, WorkspaceKindProject, map[bool]string{true: "old-placement", false: "named-placement"}[legacy])
			f.service.NamedPlacement = !legacy
			if !legacy {
				f.archivePlan, err = f.service.PlanArchive(ctx, WorkspaceArchivePlanInput{Kind: WorkspaceKindProject, ObjectID: f.archivePlan.ObjectID, Slug: f.archivePlan.Slug, ActorID: req.ActorID, Reason: "named placement"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.service.ApplyArchive(ctx, f.archivePlan, f.archivePlan.PlanDigest); err != nil {
				t.Fatal(err)
			}
			paths, err := ResolveWorkspacePlanPaths(roots, f.archivePlan)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(paths.ArchiveManifest.AbsolutePath)
			if err != nil {
				t.Fatal(err)
			}
			catalog := storagecatalog.NewService(db)
			if legacy {
				owner := RetainedRelocationService{Roots: roots, Catalog: catalog, ReadersReady: func(context.Context, RetainedRelocationPlan) error { return nil }, Prove: func(context.Context, WorkspacePathRef, string, NoFollowInventory) (RetainedCustodyProof, error) {
					return RetainedCustodyProof{ID: "workspace:" + f.archivePlan.OperationID, Original: f.archivePlan.Destination.Path.AbsolutePath, Digest: f.archivePlan.PlanDigest}, nil
				}}
				move, err := owner.Plan(ctx, paths.ArchivePayload.RelativePath, "Projects/"+f.archivePlan.Slug, req.ActorID, "owned mixed-version check")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = owner.Apply(ctx, move.ID, move.Digest); err != nil {
					t.Fatal(err)
				}
			}
			f.service.NamedPlacement = true
			f.service.CurrentLocation = catalog.ResolveCustodyLocation
			restore, err := f.service.PlanRestore(ctx, WorkspaceRestorePlanInput{ArchiveOperationID: f.archivePlan.OperationID, ActorID: req.ActorID, Reason: "inactive restore"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.ApplyRestore(ctx, restore, restore.PlanDigest); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(paths.ArchiveManifest.AbsolutePath)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("original manifest changed")
			}
			next, err := f.service.PlanArchive(ctx, WorkspaceArchivePlanInput{Kind: WorkspaceKindProject, ObjectID: f.archivePlan.ObjectID, Slug: f.archivePlan.Slug, ActorID: req.ActorID, Reason: "next operation-scoped cycle"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.ApplyArchive(ctx, next, next.PlanDigest); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.InspectOperation(ctx, restore.OperationID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
