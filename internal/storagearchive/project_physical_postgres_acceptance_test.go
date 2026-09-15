package storagearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/storagecatalog"
)

// This hook diagnoses the same disposable database/path used by the real
// daemon, without adding private error details to a production transport.
func TestProjectPhysicalPostgresAcceptanceLivePlan(t *testing.T) {
	db, service, _ := newProjectPostgresAcceptanceService(t, "")
	defer db.Close()
	ctx := context.Background()
	req := projectPostgresAcceptanceRequest(t, db, service, "physical-acceptance")
	_, err := service.PlanProjectPhysicalArchive(ctx, req, "physical-acceptance", ProjectPhysicalArchivePlanInput{Reason: "disposable independent acceptance"})
	if err != nil {
		t.Fatalf("underlying disposable plan failure: %v", err)
	}
}

func newProjectPostgresAcceptanceService(t *testing.T, fixture string) (*sql.DB, ProjectRuntimeService, *WorkspaceMoveService) {
	t.Helper()
	dbURL := os.Getenv("LOOM_PROJECT_PHYSICAL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("only the smoke-owned PostgreSQL acceptance invokes this diagnostic")
	}
	parsed, err := url.Parse(dbURL)
	if err != nil || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/loom_project_physical_") || !strings.HasPrefix(parsed.Query().Get("host"), "/tmp/loom-project-physical.") {
		t.Fatal("refusing a database outside the Unix-socket disposable smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	root := parsed.Query().Get("host")
	credentials, err := filepath.EvalSymlinks(filepath.Join(root, "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewSystemdWorkspaceManifestKeyProvider(credentials, "project-physical-smoke-v1")
	if err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.NewService(db)
	if fixture == "" {
		fixture = root
	}
	if fixture != root && !strings.HasPrefix(fixture, root+string(filepath.Separator)) {
		t.Fatal("fixture is outside smoke-owned root")
	}
	roots := TrustedWorkspaceRoots{BoxRoot: filepath.Join(fixture, "box"), StorageRoot: filepath.Join(fixture, "storage")}
	workspace := &WorkspaceMoveService{Roots: roots, Catalog: catalog, Journal: catalog, ManifestKeyID: "project-physical-smoke-v1", ManifestKey: key.Lookup}
	project := projects.NewService(db)
	service := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects: project, RepositoryState: project, WorkspaceMove: workspace, WorkspaceRoots: roots,
		Activation:        projectactivation.NewService(projectactivation.Deps{Projects: project}),
		RuntimeQuiescence: NewRoutedProjectRuntimeQuiescenceVerifier(routing.NewService(db)),
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	return db, service, workspace
}

func projectPostgresAcceptanceRequest(t *testing.T, db *sql.DB, service ProjectRuntimeService, ref string) requestctx.Context {
	t.Helper()
	ctx := context.Background()
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_project_acceptance_diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	_, scope, keyName, err := service.ProjectPhysicalScope(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	req.ScopeID, req.ScopeKey = scope, keyName
	return req
}

const projectAcceptanceCrashExit = 91

func TestProjectPhysicalPostgresAcceptanceCrashHelper(t *testing.T) {
	if os.Getenv("LOOM_PROJECT_PHYSICAL_CHILD") != "1" {
		t.Skip("crash child only")
	}
	fixture := os.Getenv("LOOM_PROJECT_PHYSICAL_FIXTURE")
	db, service, workspace := newProjectPostgresAcceptanceService(t, fixture)
	defer db.Close()
	var review ProjectPhysicalPlanReview
	raw, err := os.ReadFile(filepath.Join(fixture, "review.json"))
	if err != nil || json.Unmarshal(raw, &review) != nil {
		t.Fatal("read child compact review", err)
	}
	req := projectPostgresAcceptanceRequest(t, db, service, review.ProjectID)
	boundary := os.Getenv("LOOM_PROJECT_PHYSICAL_BOUNDARY")
	crash := func(got string) {
		if boundary == got {
			os.Exit(projectAcceptanceCrashExit)
		}
	}
	service.ArchiveFailureHook = func(b ProjectPhysicalArchiveFailureBoundary) error { crash("project:" + string(b)); return nil }
	service.RestoreFailureHook = func(b ProjectPhysicalRestoreFailureBoundary) error { crash("project:" + string(b)); return nil }
	workspace.FailureHook = func(b WorkspaceMoveBoundary) error { crash("workspace:" + string(b)); return nil }
	_, err = service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true})
	if err != nil {
		t.Fatal("child did not reach boundary", err)
	}
	t.Fatalf("child returned without boundary %s", boundary)
}

type projectPostgresAcceptanceFixture struct {
	root, projectID, active, archived string
	input                             projects.RegisterProjectContractInput
	req                               requestctx.Context
	review                            ProjectPhysicalPlanReview
	before                            []payloadSnapshotEntry
	allocation                        blocksAndInodes
	repoIDs                           []string
}

func newProjectPostgresAcceptanceFixture(t *testing.T, suffix string) projectPostgresAcceptanceFixture {
	t.Helper()
	parsed, err := url.Parse(os.Getenv("LOOM_PROJECT_PHYSICAL_TEST_DB_URL"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parsed.Query().Get("host"), "acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	db, service, _ := newProjectPostgresAcceptanceService(t, root)
	defer db.Close()
	projectID := ids.NewProjectID()
	slug := "fixture-" + suffix
	active := filepath.Join(service.WorkspaceRoots.BoxRoot, "Projects", slug)
	for _, dir := range []string{filepath.Join(active, ".loom", "contracts"), filepath.Join(active, "Notes"), filepath.Join(service.WorkspaceRoots.StorageRoot, "archive", "projects")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(active, ".loom", "project.yaml"), fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject:\n  id: %s\n  slug: %s\n  name: Disposable acceptance\n  owner_node: main\n  status: active\nfacets:\n  repos: true\n", projectID, slug), 0600)
	repoIDs := []string{"repo_" + strings.TrimPrefix(ids.NewProjectID(), "project_"), "repo_" + strings.TrimPrefix(ids.NewProjectID(), "project_")}
	repos := "kind: loom.repos\nschema_version: repos.contract.v0.4\nrepos:\n  status: active\n  defaults: {sync: false, backup: false, index: false}\n  watch_roots: [{key: repos, path: ., display_name: Repositories}]\n  members:\n"
	for i, name := range []string{"primary", "component"} {
		repos += fmt.Sprintf("    - {id: %s, key: %s, path: %s, role: %s, state_root: .repo}\n", repoIDs[i], name, name, name)
		repoRoot := filepath.Join(active, "repos", name)
		if err := os.MkdirAll(filepath.Join(repoRoot, ".repo"), 0700); err != nil {
			t.Fatal(err)
		}
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture git: %v %s", err, out)
			}
		}
		git("init", "-q", "-b", "main")
		write(filepath.Join(repoRoot, "README.md"), "acceptance payload\n", 0640)
		write(filepath.Join(repoRoot, ".repo", "repo.yaml"), fmt.Sprintf("repository_id: %s\nproject_id: %s\n", repoIDs[i], projectID), 0600)
		git("add", "README.md", ".repo/repo.yaml")
		git("-c", "user.name=LOOM Acceptance", "-c", "user.email=acceptance@invalid", "commit", "-qm", "fixture")
		if i == 1 {
			write(filepath.Join(repoRoot, "README.md"), "retained uncommitted change\n", 0640)
		}
	}
	write(filepath.Join(active, ".loom", "contracts", "repos.yaml"), repos, 0600)
	write(filepath.Join(active, "Notes", "decision.md"), "# Disposable decision\nPreserve this source, not a synthetic Notes archive event.\n", 0640)
	write(filepath.Join(active, "payload.bin"), string([]byte{0, 1, 2, 3, 255}), 0600)
	if err := os.Link(filepath.Join(active, "payload.bin"), filepath.Join(active, "hardlink.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Notes/decision.md", filepath.Join(active, "source-link")); err != nil {
		t.Fatal(err)
	}
	input, err := projectregistration.BuildInput(projectcontracts.Analyze(active), "project-physical-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_project_physical_acceptance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.NewService(db).RegisterProjectContract(ctx, req, input); err != nil {
		t.Fatal(err)
	}
	req = projectPostgresAcceptanceRequest(t, db, service, projectID)
	review, err := service.ReviewProjectPhysicalArchive(ctx, req, projectID, ProjectPhysicalArchivePlanInput{Reason: "independent process crash acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	return projectPostgresAcceptanceFixture{root: root, projectID: projectID, active: active, archived: filepath.Join(service.WorkspaceRoots.StorageRoot, "archive", "projects", slug, "project"), input: input, req: req, review: review, before: snapshotWorkspacePayload(t, active), allocation: allocatedBlocksAndInodes(t, active), repoIDs: repoIDs}
}

func runProjectAcceptanceCrash(t *testing.T, f projectPostgresAcceptanceFixture, review ProjectPhysicalPlanReview, boundary string) {
	t.Helper()
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "review.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestProjectPhysicalPostgresAcceptanceCrashHelper$")
	cmd.Env = append(os.Environ(), "LOOM_PROJECT_PHYSICAL_CHILD=1", "LOOM_PROJECT_PHYSICAL_FIXTURE="+f.root, "LOOM_PROJECT_PHYSICAL_BOUNDARY="+boundary)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != projectAcceptanceCrashExit {
		t.Fatalf("child at %s: %v\n%s", boundary, err, out)
	}
}

func requireProjectAcceptanceState(t *testing.T, f projectPostgresAcceptanceFixture, restored bool) {
	t.Helper()
	db, service, _ := newProjectPostgresAcceptanceService(t, f.root)
	defer db.Close()
	ctx := context.Background()
	current, absent, state := f.archived, f.active, "archived"
	if restored {
		current, absent, state = f.active, f.archived, "active"
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatalf("duplicate payload: %v", err)
	}
	if got := snapshotWorkspacePayload(t, current); !reflect.DeepEqual(got, f.before) {
		t.Fatal("payload bytes/modes/inodes/mtimes/links changed")
	}
	if got := allocatedBlocksAndInodes(t, current); !reflect.DeepEqual(got, f.allocation) {
		t.Fatal("payload allocation or inode identity changed")
	}
	detail, err := service.Projects.GetProjectRegistrationStatus(ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Project.Status != state || detail.Registration.ActivationStatus != projects.ProjectActivationStatusInactive {
		t.Fatal("lifecycle or inactive posture mismatch")
	}
	model, err := service.RepositoryState.ReadProjectRepositoryState(ctx, f.projectID)
	if err != nil || model.Source == nil || model.Source.SourceRevision != 1 || len(model.Members) != 2 {
		t.Fatal("repository identity/source drift", err)
	}
	for _, member := range model.Members {
		if member.RepositoryID != f.repoIDs[0] && member.RepositoryID != f.repoIDs[1] {
			t.Fatal("repository identity replaced")
		}
		if string(member.RepositoryLifecycle) != state || string(member.MembershipLifecycle) != state {
			t.Fatal("repository lifecycle mismatch")
		}
	}
	var count int
	for event, want := range map[string]int{"project.archived": 1, "project.restored": 0} {
		if restored && event == "project.restored" {
			want = 1
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type=$2`, f.projectID, event).Scan(&count); err != nil || count != want {
			t.Fatalf("event %s count=%d want=%d err=%v", event, count, want, err)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, f.projectID).Scan(&count); err != nil || count != 1 {
		t.Fatal("immutable repository source history changed", count, err)
	}
	wantOperations := 1
	if restored {
		wantOperations = 2
	}
	for _, query := range []string{
		`SELECT count(*) FROM storage.workspace_lifecycle_events WHERE object_id=$1`,
		`SELECT count(*) FROM projects.physical_archive_plan_evidence WHERE project_id=$1`,
	} {
		if err := db.QueryRowContext(ctx, query, f.projectID).Scan(&count); err != nil || count != wantOperations {
			t.Fatalf("durable evidence/event count=%d want=%d err=%v", count, wantOperations, err)
		}
	}
	activation := projectactivation.NewService(projectactivation.Deps{Projects: projects.NewService(db)})
	if _, err := activation.Activate(ctx, f.req, f.projectID, projects.ActivateProjectInput{Facet: "all"}); err == nil {
		t.Fatal("runtime activation bypassed archive/restored-inactive guard")
	}
	if _, err := projects.NewService(db).RegisterProjectContract(ctx, f.req, f.input); err == nil {
		t.Fatal("registration replay bypassed mutation guard")
	}
}

func TestProjectPhysicalPostgresAcceptanceCrashMatrix(t *testing.T) {
	if os.Getenv("LOOM_PROJECT_PHYSICAL_TEST_DB_URL") == "" {
		t.Skip("requires smoke-owned PostgreSQL")
	}
	archiveBoundaries := []string{
		"project:" + string(ProjectArchiveBoundaryAfterDeactivation),
		"project:" + string(ProjectArchiveBoundaryAfterWorkspaceMove),
		"project:" + string(ProjectArchiveBoundaryBeforeProjectStateCommit),
		"workspace:" + string(BoundaryAfterIntent), "workspace:" + string(BoundaryAfterPayloadMove),
		"workspace:" + string(BoundaryAfterMovedRecord), "workspace:" + string(BoundaryAfterProjections), "workspace:" + string(BoundaryAfterComplete),
	}
	restoreBoundaries := []string{
		"project:" + string(ProjectRestoreBoundaryAfterIntent), "project:" + string(ProjectRestoreBoundaryAfterWorkspaceMove),
		"project:" + string(ProjectRestoreBoundaryBeforeProjectCommit), "project:" + string(ProjectRestoreBoundaryAfterProjectCommit),
		"workspace:" + string(BoundaryAfterRestoreIntent), "workspace:" + string(BoundaryAfterRestorePayloadMove),
		"workspace:" + string(BoundaryAfterRestoreMovedRecord), "workspace:" + string(BoundaryAfterRestoreProjections), "workspace:" + string(BoundaryAfterRestoreComplete),
	}
	for i, boundary := range append(archiveBoundaries, restoreBoundaries...) {
		t.Run(boundary, func(t *testing.T) {
			f := newProjectPostgresAcceptanceFixture(t, fmt.Sprintf("crash-%02d", i))
			ctx := context.Background()
			review := f.review
			if i >= len(archiveBoundaries) {
				db, service, _ := newProjectPostgresAcceptanceService(t, f.root)
				defer db.Close()
				if _, err := service.ApplyReviewedProjectPhysicalPlan(ctx, f.req, f.projectID, ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true}); err != nil {
					t.Fatal("archive before restore", err)
				}
				var err error
				review, err = service.ReviewProjectPhysicalRestore(ctx, f.req, f.projectID, ProjectPhysicalRestorePlanInput{Reason: "independent restore crash"})
				if err != nil {
					t.Fatal(err)
				}
			}
			runProjectAcceptanceCrash(t, f, review, boundary)
			db, service, _ := newProjectPostgresAcceptanceService(t, f.root)
			defer db.Close()
			input := ProjectPhysicalRecoverRequest{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Confirm: true}
			result, err := service.RecoverReviewedProjectPhysicalKind(ctx, f.req, f.projectID, input, review.Workspace.OperationKind)
			if err != nil || result.Phase != "complete" || result.Recoverable {
				t.Fatalf("fresh recovery=%#v err=%v", result, err)
			}
			before, err := service.Projects.GetProjectRegistrationStatus(ctx, f.projectID)
			if err != nil {
				t.Fatal(err)
			}
			if result, err = service.RecoverReviewedProjectPhysicalKind(ctx, f.req, f.projectID, input, review.Workspace.OperationKind); err != nil || !result.Replay {
				t.Fatalf("replay=%#v err=%v", result, err)
			}
			after, err := service.Projects.GetProjectRegistrationStatus(ctx, f.projectID)
			if err != nil || !reflect.DeepEqual(before.Project.Project.ArchiveState, after.Project.Project.ArchiveState) {
				t.Fatal("replay changed durable evidence or occurrence times", err)
			}
			requireProjectAcceptanceState(t, f, i >= len(archiveBoundaries))
		})
	}
}

func TestProjectPhysicalPostgresAcceptanceIdentityConstraints(t *testing.T) {
	db, _, _ := newProjectPostgresAcceptanceService(t, "")
	defer db.Close()
	ctx := context.Background()
	for _, table := range []string{"workspace_archive_operations", "workspace_archive_manifests", "workspace_lifecycle_events"} {
		var expression string
		if err := db.QueryRowContext(ctx, `SELECT pg_get_expr(conbin,conrelid) FROM pg_constraint WHERE conrelid=$1::regclass AND conname=$2`, "storage."+table, table+"_object_id_check").Scan(&expression); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"legacy_object_1", "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", "project_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", "project_81ARZ3NDEKTSV4RRFFQ69G5FAV", "project_01ARZ3NDEKTSV4RRFFQ69G5FAI", "project_01ARZ3NDEKTSV4RRFFQ69G5FA", "project_Legacy", "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
			for _, kind := range []WorkspaceKind{WorkspaceKindProject, WorkspaceKindTopic, WorkspaceKindLibraryItem} {
				var accepted bool
				// Evaluate the constraint actually installed by migration, not a
				// second hand-written SQL regex that could hide migration drift.
				if err := db.QueryRowContext(ctx, `SELECT `+expression+` FROM (VALUES ($1::text,$2::text)) AS fixture(object_id,workspace_kind)`, id, string(kind)).Scan(&accepted); err != nil {
					t.Fatal(err)
				}
				if want := validateWorkspaceObject(kind, id, "fixture-project") == nil; accepted != want {
					t.Fatalf("Go/SQL identity mismatch %s %s %s", table, kind, id)
				}
			}
		}
	}
	// The real daemon has already stored canonical project archive evidence.
	// The old check cannot be restored; the transaction must preserve all rows
	// and the still-valid new checks rather than normalize identifiers.
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00066_workspace_archive_project_identity.sql"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("missing migration down boundary")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, parts[1])
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("down migration did not refuse retained canonical evidence: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM storage.workspace_archive_operations WHERE workspace_kind='project' AND object_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'`).Scan(&count); err != nil || count < 2 {
		t.Fatal("failed downgrade lost canonical archive evidence", count, err)
	}
}

func TestProjectPhysicalPostgresAcceptanceConcurrentRecovery(t *testing.T) {
	if os.Getenv("LOOM_PROJECT_PHYSICAL_TEST_DB_URL") == "" {
		t.Skip("requires smoke-owned PostgreSQL")
	}
	f := newProjectPostgresAcceptanceFixture(t, "concurrent")
	runProjectAcceptanceCrash(t, f, f.review, "project:"+string(ProjectArchiveBoundaryAfterDeactivation))
	var wg sync.WaitGroup
	errorsCh := make(chan error, 4)
	for range 4 {
		db, service, _ := newProjectPostgresAcceptanceService(t, f.root)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer db.Close()
			_, err := service.RecoverReviewedProjectPhysicalKind(context.Background(), f.req, f.projectID, ProjectPhysicalRecoverRequest{OperationID: f.review.Workspace.OperationID, PlanDigest: f.review.PlanDigest, Confirm: true}, WorkspaceOperationArchive)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	requireProjectAcceptanceState(t, f, false)
}
