package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"loom.local/loom/internal/projectstate"
)

func projectDevelopmentTestInput(text string) ProjectProjectionSyncInput {
	input := projectionTestSyncInput()
	input.Repositories = nil
	input.Project.Members = nil
	digest := digestProjection([]byte(text))
	input.Project.Development = projectstate.ProjectDevelopmentState{
		ProjectID: input.Project.Project.ProjectID, SourceRevision: input.Project.Source.SourceRevision,
		Posture: projectstate.ProjectDevelopmentReady, Complete: true,
		RootDigest: digestProjection([]byte("registered root")), SourceDigest: digest,
		Purpose: "Project-wide development", CurrentFocus: text, CapturedBytes: len(text),
		Documents: []projectstate.ProjectDevelopmentDocument{{
			Path: ".project/STATE.md", Hash: digest, Excerpt: text, Posture: "present", SizeBytes: int64(len(text)),
		}},
	}
	return input
}

func TestProjectDevelopmentProjectionCapturesWithoutRegistrationChurn(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	input := projectDevelopmentTestInput("Investigate lexical retrieval")
	first, repos, err := prepareProjectProjectionSync(input, now)
	if err != nil || len(repos) != 0 {
		t.Fatalf("zero-repository project: repositories=%d err=%v", len(repos), err)
	}
	input.Project.ObservedAt = input.Project.ObservedAt.Add(time.Hour)
	repeated, _, err := prepareProjectProjectionSync(input, now.Add(time.Hour))
	if err != nil || first.SourceIdentityDigest != repeated.SourceIdentityDigest || first.SnapshotDigest != repeated.SnapshotDigest {
		t.Fatalf("observation time changed deterministic identity: %v", err)
	}
	changed, _, err := prepareProjectProjectionSync(projectDevelopmentTestInput("Finish lexical retrieval"), now)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceDigest != changed.SourceDigest || first.SourceRevision != changed.SourceRevision || first.SnapshotDigest == changed.SnapshotDigest || first.SourceIdentityDigest == changed.SourceIdentityDigest {
		t.Fatal("Markdown change confused registration and captured content identity")
	}
	project, err := decodeProjectContext(first.ProjectionJSON, first.ProjectID)
	if err != nil || project.Development == nil || project.Development.Documents[0].Excerpt != "Investigate lexical retrieval" {
		t.Fatalf("captured source was not preserved: %#v %v", project, err)
	}
	if !strings.Contains(first.SearchableSummary, "Investigate lexical retrieval") {
		t.Fatalf("missing development search text: %q", first.SearchableSummary)
	}
	input.Project.Development.Git = &projectstate.ProjectDevelopmentGit{Posture: "committed", Head: strings.Repeat("a", 40), Commit: strings.Repeat("a", 40)}
	gitFirst, _, err := prepareProjectProjectionSync(input, now)
	if err != nil {
		t.Fatal(err)
	}
	input.Project.Development.Git.Head = strings.Repeat("b", 40)
	input.Project.Development.Git.Commit = strings.Repeat("b", 40)
	gitNext, _, err := prepareProjectProjectionSync(input, now)
	if err != nil || gitFirst.SourceIdentityDigest != gitNext.SourceIdentityDigest {
		t.Fatalf("unrelated Git commit churned unchanged source: %v", err)
	}
	input.Project.Development.Git.Posture = "uncommitted"
	gitDirty, _, err := prepareProjectProjectionSync(input, now)
	if err != nil || gitNext.SourceIdentityDigest == gitDirty.SourceIdentityDigest {
		t.Fatalf("Git source posture change was lost: %v", err)
	}
	legacy := projectionTestSyncInput()
	legacy.Project.Development = projectstate.ProjectDevelopmentState{}
	legacySnapshot, _, err := prepareProjectProjectionSync(legacy, now)
	if err != nil || strings.Contains(string(legacySnapshot.ProjectionJSON), "development") {
		t.Fatalf("legacy snapshot payload changed: %s %v", legacySnapshot.ProjectionJSON, err)
	}
}

func TestProjectDevelopmentProjectionValidatesCapture(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*projectstate.ProjectDevelopmentState)
	}{
		{"wrong project", func(d *projectstate.ProjectDevelopmentState) { d.ProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FB0" }},
		{"wrong binding", func(d *projectstate.ProjectDevelopmentState) { d.SourceRevision++ }},
		{"invalid digest", func(d *projectstate.ProjectDevelopmentState) { d.SourceDigest = "invalid" }},
		{"unknown posture", func(d *projectstate.ProjectDevelopmentState) { d.Posture = "accepted" }},
		{"escaped path", func(d *projectstate.ProjectDevelopmentState) { d.Documents[0].Path = ".project/../../other" }},
		{"absolute path", func(d *projectstate.ProjectDevelopmentState) { d.Documents[0].Path = "/private/project/STATE.md" }},
		{"too many documents", func(d *projectstate.ProjectDevelopmentState) {
			d.Documents = make([]projectstate.ProjectDevelopmentDocument, 129)
		}},
		{"too many bytes", func(d *projectstate.ProjectDevelopmentState) { d.CapturedBytes = (2 << 20) + 1 }},
		{"missing posture", func(d *projectstate.ProjectDevelopmentState) { d.Posture = ""; d.ProjectID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := projectDevelopmentTestInput("captured source")
			test.change(&input.Project.Development)
			if _, _, err := prepareProjectProjectionSync(input, time.Now()); err == nil {
				t.Fatal("invalid capture accepted")
			}
		})
	}
}

type projectProjectionTestRow func(...any) error

func (row projectProjectionTestRow) Scan(dest ...any) error { return row(dest...) }

func projectProjectionValues(values ...any) pgx.Row {
	return projectProjectionTestRow(func(dest ...any) error {
		if len(dest) != len(values) {
			return fmt.Errorf("scan got %d destinations, want %d", len(dest), len(values))
		}
		for index, value := range values {
			reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(value))
		}
		return nil
	})
}

func TestProjectDevelopmentProjectionAppendReplayAndReversion(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	first, _, err := prepareProjectProjectionSync(projectDevelopmentTestInput("First focus"), now)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := prepareProjectProjectionSync(projectDevelopmentTestInput("Second focus"), now)
	if err != nil {
		t.Fatal(err)
	}
	first.ID = stableProjectionID("project", first.ProjectID, "1", first.SnapshotDigest)
	first.ProjectionRevision = 1
	second.ID = stableProjectionID("project", second.ProjectID, "2", second.SnapshotDigest)
	second.ProjectionRevision = 2
	for _, test := range []struct {
		name         string
		latest, next projectSnapshot
		wantRevision int64
		replayed     bool
	}{
		{"unchanged", first, first, 1, true},
		{"changed Markdown at same binding", first, second, 2, false},
		{"reverted source creates third observation", second, first, 3, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			inserted := false
			store := &Store{q: queryAdapter{
				queryRow: func(context.Context, string, ...any) pgx.Row {
					p := test.latest
					return projectProjectionValues(string(p.ID), p.ProjectionRevision, p.SourceIdentityDigest, p.SourceRevision, p.SourceSchemaVersion, p.SnapshotDigest, p.SourceDigest)
				},
				exec: func(_ context.Context, sql string, args ...any) (int64, error) {
					if strings.Contains(sql, "INSERT INTO") {
						inserted = true
						if args[2] != test.wantRevision || args[3] != first.SourceRevision {
							t.Fatalf("revision allocation: %v", args[:4])
						}
					}
					return 1, nil
				},
			}}
			id, revision, replayed, err := store.appendProjectProjection(ctx, test.next)
			if err != nil || revision != test.wantRevision || replayed != test.replayed || inserted == replayed {
				t.Fatalf("append: id=%s revision=%d replayed=%t inserted=%t err=%v", id, revision, replayed, inserted, err)
			}
			if !replayed && id == test.latest.ID {
				t.Fatal("new observation reused a historical snapshot ID")
			}
		})
	}
}

func TestGetProjectProjectionReturnsCapturedSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	captured, _, err := prepareProjectProjectionSync(projectDevelopmentTestInput("Original exact excerpt"), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := stableProjectionID("historical snapshot")
	for _, selected := range []SemanticID{"", snapshotID} {
		api := &FoundationAPI{store: &Store{q: queryAdapter{queryRow: func(_ context.Context, sql string, args ...any) pgx.Row {
			if args[0] != captured.ProjectID || args[1] != optionalProjectSnapshotID(selected) || !strings.Contains(sql, "project_id=$1 AND ($2::uuid IS NULL OR id=$2::uuid)") || !strings.Contains(sql, "ORDER BY projection_revision DESC") {
				t.Fatalf("exact-get did not bind project and optional snapshot: %v %s", args, sql)
			}
			return projectProjectionValues(string(snapshotID), captured.ProjectID, int64(7), captured.SourceRevision,
				captured.SourceDigest, captured.SourceSchemaVersion, captured.SnapshotDigest, now, now, []byte(captured.ProjectionJSON))
		}}}}
		result, err := api.GetProjectProjection(context.Background(), captured.ProjectID, selected)
		if err != nil || result.SnapshotID != snapshotID || result.ProjectionRevision != 7 || result.Context.Development.Documents[0].Excerpt != "Original exact excerpt" || result.Context.Development.Documents[0].Hash != digestProjection([]byte("Original exact excerpt")) {
			t.Fatalf("captured get: %#v %v", result, err)
		}
	}
	api := &FoundationAPI{store: &Store{q: queryAdapter{queryRow: func(context.Context, string, ...any) pgx.Row {
		return projectProjectionTestRow(func(...any) error { return pgx.ErrNoRows })
	}}}}
	if _, err := api.GetProjectProjection(context.Background(), captured.ProjectID, snapshotID); !errors.Is(err, ErrProjectProjectionNotFound) {
		t.Fatalf("missing/wrong-project snapshot: %v", err)
	}
	for _, test := range []struct {
		project  string
		snapshot SemanticID
	}{
		{"bad-project", snapshotID}, {captured.ProjectID, "bad-snapshot"},
	} {
		_, err := api.GetProjectProjection(context.Background(), test.project, test.snapshot)
		var validation *FoundationValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("invalid exact identity: %v", err)
		}
	}
	var legacy ProjectContextProjection
	if err := json.Unmarshal([]byte(`{"project_id":"`+captured.ProjectID+`","name":"Legacy"}`), &legacy); err != nil || legacy.Development != nil {
		t.Fatalf("legacy historical capture: %#v %v", legacy, err)
	}
}
