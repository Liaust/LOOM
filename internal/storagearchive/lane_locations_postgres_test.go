package storagearchive

import (
	"bytes"
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/storagecatalog"
)

func TestLaneLocationsPostgres(t *testing.T) {
	dsn := os.Getenv("LOOM_LANE_LOCATION_TEST_DB_URL")
	if dsn == "" {
		t.Skip("owned Main disposable database only")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(parsed.Path, "/loom_workspace_archive_") {
		t.Fatal("owned database required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := t.TempDir()
	roots := TrustedWorkspaceRoots{StorageRoot: filepath.Join(root, "storage")}
	if err = os.MkdirAll(roots.StorageRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err = PrepareRetainedRoots(roots.StorageRoot); err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(root, "runtime")
	batch := "lane_locations_postgres_" + strings.TrimPrefix(newWorkspaceID("owned"), "owned_")
	input := lane.AcceptInput{AcceptedPath: filepath.Join(runtime, "staging", "main", batch), RemoteRoot: runtime, TrustedRemoteRoot: true, ImportsRoot: filepath.Join(roots.StorageRoot, "imports"), SourceNodeKey: "main", BatchID: batch, AcceptedDate: "2026-11-05"}
	for _, dir := range []string{"parent/selected", "stays"} {
		if err = os.MkdirAll(filepath.Join(input.AcceptedPath, dir), 0750); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"parent/selected/note.md", "stays/other.md"} {
		if err = os.WriteFile(filepath.Join(input.AcceptedPath, name), []byte(name), 0640); err != nil {
			t.Fatal(err)
		}
	}
	catalog := storagecatalog.NewService(db)
	accepted, err := lane.Accept(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(accepted.AcceptedPath, ".loom-lane-custody.json")
	original, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(accepted.AcceptedPath, "parent/selected")
	owner := RetainedRelocationService{Roots: roots, Catalog: catalog, ReadersReady: func(context.Context, RetainedRelocationPlan) error { return nil }}
	owner.Prove = owner.ProveCommittedLane
	rel, err := filepath.Rel(roots.StorageRoot, selected)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := owner.Plan(ctx, filepath.ToSlash(rel), "Projects/committed-lane", "actor_lane_test", "owned split-member check")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.PrepareOperator(ctx, plan.ID, plan.Digest); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(plan.Source.AbsolutePath, plan.Destination.AbsolutePath); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Apply(ctx, plan.ID, plan.Digest); err != nil {
		t.Fatal(err)
	}
	replayed, err := lane.Accept(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.PromotionIdempotent || replayed.FilesCataloged != accepted.FilesCataloged || len(replayed.StorageEntryIDs) != len(accepted.StorageEntryIDs) {
		t.Fatal("replay changed member identity or counts")
	}
	for i, id := range accepted.StorageEntryIDs {
		if id != replayed.StorageEntryIDs[i] {
			t.Fatal("replay registered duplicate member")
		}
	}
	after, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("manifest changed")
	}
	if _, _, err = lane.VerifyCommittedCustody(ctx, accepted.AcceptedPath, "main", batch, input.AcceptedDate, catalog); err != nil {
		t.Fatal(err)
	}
	path, err := catalog.ResolveLaneCustodyMember(ctx, "main", batch, "parent/selected/note.md")
	if err != nil || path != filepath.Join(roots.StorageRoot, "Projects/committed-lane/note.md") {
		t.Fatalf("current member: %s %v", path, err)
	}
	if err = os.WriteFile(path, []byte("tampered"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err = lane.Accept(ctx, catalog, input); err == nil {
		t.Fatal("changed retained bytes were accepted")
	}
}
