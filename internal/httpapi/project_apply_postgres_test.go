package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"loom.local/loom/internal/loomcli"
	"loom.local/loom/internal/setup"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

// Every test owns a new database. Never migrate or clear the supplied database.
func declarationHTTPDatabase(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires disposable LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && !(u.Hostname() == "" && strings.HasPrefix(u.Query().Get("host"), "/tmp/")) {
		t.Fatal("requires local disposable PostgreSQL")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "decl_http_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	result, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations"))
	if err != nil || result.CurrentVersion != 71 {
		t.Fatalf("migrate through 71: %+v %v", result, err)
	}
	if _, err = bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}
func declarationDBSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT table_schema,table_name FROM information_schema.tables WHERE table_type='BASE TABLE' AND table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1,2`)
	if err != nil {
		t.Fatal(err)
	}
	var tables [][2]string
	for rows.Next() {
		var table [2]string
		if err = rows.Scan(&table[0], &table[1]); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	state := map[string]string{}
	for _, table := range tables {
		key := table[0] + "." + table[1]
		var hash string
		q := fmt.Sprintf(`SELECT md5(COALESCE(string_agg(to_jsonb(t)::text,',' ORDER BY to_jsonb(t)::text),'')) FROM %q.%q t`, table[0], table[1])
		if err = db.QueryRow(q).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		state[key] = hash
	}
	return state
}
func declarationAssertUnchanged(t *testing.T, db *sql.DB, before map[string]string) {
	t.Helper()
	after := declarationDBSnapshot(t, db)
	if !reflect.DeepEqual(before, after) {
		for k, v := range before {
			if after[k] != v {
				t.Errorf("read/replay changed %s", k)
			}
		}
	}
}
func declarationOwnedServer(t *testing.T, db *sql.DB, box string, resolver ProjectDeclarationRequestResolver) *httptest.Server {
	t.Helper()
	local := projectapply.NewLocalResolver(db, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: box}, nil })
	watch := projectapply.WatchOwner{Resolver: local, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	service := projectapply.NewService(db, local, projectapply.NewCurrentAuthority(db), map[pc.DeclarationOwner]projectapply.Owner{pc.DeclarationOwnerProjects: projectapply.ProjectsOwner{Resolver: local, Projects: projects.NewService(db)}, pc.DeclarationOwnerKnowledge: watch, pc.DeclarationOwnerProtection: watch})
	server := NewServer(Services{DB: db, ProjectDeclaration: service, ProjectDeclarationRequestResolver: resolver, Projects: projects.NewService(db), Communication: communication.NewService(db), RuntimeConfig: config.Config{NodeID: "main", BoxPath: box}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}
func declarationFixtureSource(t *testing.T, root, id string, resources map[string]any) {
	t.Helper()
	for _, dir := range []string{".loom", "code", "material"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV05, "project": map[string]string{"id": id, "slug": "declaration-http", "name": "Declaration HTTP", "owner_node": "main", "status": "active"}, "resources": resources}
	if err := os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), []byte(declarationJSONTest(t, source)), 0600); err != nil {
		t.Fatal(err)
	}
}
func declarationAssertClientFailure(t *testing.T, err error, code pc.DeclarationErrorCode) *localclient.DeclarationRequestError {
	t.Helper()
	var failure *localclient.DeclarationRequestError
	if !errors.As(err, &failure) || failure.Envelope.Error.Code != string(code) {
		t.Fatalf("want %s: %v", code, err)
	}
	return failure
}

func TestProjectDeclarationHTTPWorkflowPostgres(t *testing.T) {
	db := declarationHTTPDatabase(t)
	ctx := t.Context()
	box := t.TempDir()
	root := filepath.Join(box, "project")
	id := ids.NewProjectID()
	resources := map[string]any{"source": map[string]any{"kind": "repository", "repository": map[string]string{"path": "code", "role": "component"}}, "reading": map[string]any{"kind": "knowledge", "knowledge": map[string]string{"path": "material", "category": "research", "protection": "retained"}}, "retained": map[string]any{"kind": "protection", "protection": map[string]string{"policy_ref": ".loom/backup.yaml"}}}
	declarationFixtureSource(t, root, id, resources)
	policy := `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  defaults:
    max_file_bytes: 1048576
    max_batch_bytes: 2097152
    max_pending_items: 9
    max_pending_bytes: 9007199254740993
    include_deletion_markers: false
    on_limit: degrade_and_require_manual_action
    exclude: ['private/**']
    include: ['**/*.md']
  roots:
    - key: material
      path: material
`
	if err := os.WriteFile(filepath.Join(root, ".loom/backup.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(root, "material/retained.md")
	if err := os.WriteFile(payloadPath, []byte("payload must survive retirement"), 0600); err != nil {
		t.Fatal(err)
	}
	if analysis := pc.Analyze(root); analysis.Report.Summary.Errors != 0 {
		t.Fatalf("invalid fixture: %+v", analysis.Report)
	}
	server := declarationOwnedServer(t, db, box, nil)
	client, _ := localclient.NewHTTP(server.URL)
	req, err := requestctx.ResolveBootstrap(ctx, db, "declaration-http-fixture")
	if err != nil {
		t.Fatal(err)
	}
	input := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root, NodeRef: "main"}
	before := declarationDBSnapshot(t, db)
	var plan pc.DeclarationPlan
	t.Run("pure_plan_http_and_client", func(t *testing.T) {
		resp, err := http.Post(server.URL+"/v1/project-declaration-plans", "application/json", strings.NewReader(declarationJSONTest(t, input)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var env response.Envelope[pc.DeclarationPlan]
		raw, readErr := io.ReadAll(resp.Body)
		if readErr != nil || json.Unmarshal(raw, &env) != nil || resp.StatusCode != 200 {
			t.Fatalf("plan HTTP status=%d body=%s", resp.StatusCode, raw)
		}
		plan = env.Data
		repeated, err := client.PlanProjectDeclaration(ctx, "repeat", input)
		if err != nil || plan.PlanID != repeated.Data.PlanID || plan.Basis.Target.ProjectRoot != root || plan.Basis.Target.OwnerNodeID != req.OriginNodeID || plan.Basis.Bindings["source"].RepositoryID != "" {
			t.Fatalf("plan identity=%+v err=%v", plan, err)
		}
		declarationAssertUnchanged(t, db, before)
	})
	if t.Failed() {
		return
	}
	apply := pc.DeclarationApplyRequest{SchemaVersion: input.SchemaVersion, ProjectRef: root, NodeRef: "main", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, PlanID: plan.PlanID, IdempotencyKey: "original-http"}
	first, err := client.ApplyProjectDeclaration(ctx, "first", apply)
	if err != nil || first.Data.State != pc.DeclarationOperationPartial || first.Data.OperationID == "" {
		t.Fatalf("first apply=%+v %v", first, err)
	}
	operation := first.Data.OperationID
	state, err := projects.NewService(db).GetDeclarationProjectState(ctx, id)
	if err != nil || ids.Validate("repo", state.Repositories["source"]) != nil {
		t.Fatalf("generated repository ID absent: %+v %v", state, err)
	}
	before = declarationDBSnapshot(t, db)
	t.Run("lost_response_before_ack", func(t *testing.T) {
		again, err := client.ApplyProjectDeclaration(ctx, "lost", apply)
		if err != nil || again.Data.OperationID != operation {
			t.Fatalf("lost response reminted operation: %v", err)
		}
		var deliveries int
		if err = db.QueryRow(`SELECT count(*) FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, operation).Scan(&deliveries); err != nil || deliveries != 1 {
			t.Fatalf("duplicate delivery: %d %v", deliveries, err)
		}
	})
	// Poll the actual installed command, which calls pollOnceCore and the mailbox
	// dispatcher, and flush its durable ACK using the authenticated HTTP endpoint.
	credential, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: req.OriginNodeID, Reason: "owned D3c HTTP fixture"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := nodeagent.Store{ConfigPath: filepath.Join(dir, "config.json"), StatePath: filepath.Join(dir, "state.json"), DataDir: filepath.Join(dir, "data")}
	if err = store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	nodeConfig := nodeagent.Config{NodeKey: "main", DisplayName: "Fixture main", MainURL: server.URL, BoxRootPath: box}
	if err = store.SaveConfig(nodeConfig); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveState(nodeagent.State{NodeID: req.OriginNodeID, CredentialToken: credential.CredentialToken}); err != nil {
		t.Fatal(err)
	}
	poll := func(t *testing.T) {
		t.Helper()
		cmd := nodeagent.NewRootCommand()
		cmd.SetArgs([]string{"--config", store.ConfigPath, "--state", store.StatePath, "--data-dir", store.DataDir, "poll", "--once"})
		if err := cmd.ExecuteContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("actual_mailbox_and_authenticated_ack", func(t *testing.T) {
		poll(t)
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM communication.message_acks WHERE node_id=$1 AND ack_status='completed'`, req.OriginNodeID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("actual ACK absent: %d %v", count, err)
		}
		summary, err := noderuntime.NewStore(store.DataDir).OutboxSummary()
		if err != nil || summary.Counts[noderuntime.OutboxStatusDone] != 1 {
			t.Fatalf("outbox not durable: %+v %v", summary, err)
		}
	})
	if t.Failed() {
		return
	}
	t.Run("readonly_ack_waiting", func(t *testing.T) {
		before = declarationDBSnapshot(t, db)
		status, err := client.GetProjectDeclarationStatus(ctx, "status", id, "main")
		if err != nil || status.Data.Readiness.Verified.State != pc.DeclarationUnknown || status.Data.Readiness.Healthy.State != pc.DeclarationUnknown {
			t.Fatalf("status=%+v %v", status, err)
		}
		op, err := client.GetProjectDeclarationOperation(ctx, "operation", operation)
		if err != nil || op.Data.State != pc.DeclarationOperationPartial {
			t.Fatalf("read consumed ACK: %+v %v", op, err)
		}
		declarationAssertUnchanged(t, db, before)
	})
	// A new HTTP server, resolver, coordinator and adapters reuse only durable DB.
	server.Close()
	server = declarationOwnedServer(t, db, box, nil)
	client, _ = localclient.NewHTTP(server.URL)
	nodeConfig.MainURL = server.URL
	if err = store.SaveConfig(nodeConfig); err != nil {
		t.Fatal(err)
	}
	apply.OperationID = operation
	done, err := client.ApplyProjectDeclaration(ctx, "resume", apply)
	if err != nil || done.Data.State != pc.DeclarationOperationSucceeded || done.Data.OperationID != operation || done.Data.Readiness.Verified.State != pc.DeclarationUnknown {
		t.Fatalf("resume=%+v %v", done, err)
	}
	t.Run("terminal_replay_and_readonly", func(t *testing.T) {
		before = declarationDBSnapshot(t, db)
		again, err := client.ApplyProjectDeclaration(ctx, "terminal", apply)
		if err != nil || !reflect.DeepEqual(again.Data, done.Data) {
			t.Fatalf("terminal replay=%+v %v", again, err)
		}
		_, err = client.GetProjectDeclarationOperation(ctx, "read", operation)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetProjectDeclarationStatus(ctx, "read", id, "main")
		if err != nil {
			t.Fatal(err)
		}
		declarationAssertUnchanged(t, db, before)
	})
	t.Run("malformed_unknown_remote_and_denied", func(t *testing.T) {
		before := declarationDBSnapshot(t, db)
		unknown := input
		unknown.NodeRef = "unknown-node"
		_, err := client.PlanProjectDeclaration(ctx, "unknown", unknown)
		declarationAssertClientFailure(t, err, pc.DeclarationTargetUnavailable)
		malformed := `{"schema_version":"` + input.SchemaVersion + `","project_ref":"` + root + `","actor_id":"owner"}`
		resp, err := http.Post(server.URL+"/v1/project-declaration-plans", "application/json", strings.NewReader(malformed))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 422 {
			t.Fatal("malformed request reached owner")
		}
		denied := declarationOwnedServer(t, db, box, func(context.Context, string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: ids.NewActorID(), OriginNodeID: req.OriginNodeID}, nil
		})
		deniedClient, _ := localclient.NewHTTP(denied.URL)
		_, err = deniedClient.PlanProjectDeclaration(ctx, "denied", input)
		declarationAssertClientFailure(t, err, pc.DeclarationUnauthorized)
		_, err = deniedClient.GetProjectDeclarationOperation(ctx, "foreign", operation)
		failure := declarationAssertClientFailure(t, err, pc.DeclarationReferenceMissing)
		if failure.StatusCode != 404 || failure.Result != nil {
			t.Fatal("foreign operation leaked")
		}
		declarationAssertUnchanged(t, db, before)
	})
	t.Run("source_drift_and_revoked_authority", func(t *testing.T) {
		reviewed, err := client.PlanProjectDeclaration(ctx, "review", input)
		if err != nil {
			t.Fatal(err)
		}
		fresh := apply
		fresh.OperationID = ""
		fresh.IdempotencyKey = "drift"
		fresh.PlanID = reviewed.Data.PlanID
		sourcePath := filepath.Join(root, pc.CanonicalRootContractPath)
		raw, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(sourcePath, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = client.ApplyProjectDeclaration(ctx, "drift", fresh)
		declarationAssertClientFailure(t, err, pc.DeclarationPlanStale)
		if err = os.WriteFile(sourcePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		reviewed, err = client.PlanProjectDeclaration(ctx, "review", input)
		if err != nil {
			t.Fatal(err)
		}
		fresh.PlanID = reviewed.Data.PlanID
		fresh.IdempotencyKey = "revoked"
		if _, err = db.Exec(`UPDATE identity.actor_node_authorizations SET authorization_level=2 WHERE actor_id=$1`, req.ActorID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, e := db.Exec(`UPDATE identity.actor_node_authorizations SET authorization_level=5 WHERE actor_id=$1`, req.ActorID); e != nil {
				t.Error(e)
			}
		})
		_, err = client.ApplyProjectDeclaration(ctx, "revoked", apply)
		declarationAssertClientFailure(t, err, pc.DeclarationUnauthorized)
		if _, err = db.Exec(`UPDATE identity.actor_node_authorizations SET authorization_level=5 WHERE actor_id=$1`, req.ActorID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("empty_retirement_preserves_payload", func(t *testing.T) {
		declarationFixtureSource(t, root, id, map[string]any{})
		reviewed, err := client.PlanProjectDeclaration(ctx, "retire-plan", input)
		if err != nil {
			t.Fatal(err)
		}
		retire := apply
		retire.OperationID = ""
		retire.IdempotencyKey = "retire"
		retire.PlanID = reviewed.Data.PlanID
		pending, err := client.ApplyProjectDeclaration(ctx, "retire", retire)
		if err != nil || pending.Data.State != pc.DeclarationOperationPartial {
			t.Fatalf("retire=%+v %v", pending, err)
		}
		poll(t)
		retire.OperationID = pending.Data.OperationID
		final, err := client.ApplyProjectDeclaration(ctx, "retire-resume", retire)
		if err != nil || final.Data.State != pc.DeclarationOperationSucceeded {
			t.Fatalf("retire resume=%+v %v", final, err)
		}
		raw, err := os.ReadFile(payloadPath)
		if err != nil || string(raw) != "payload must survive retirement" {
			t.Fatal("retirement lost payload")
		}
	})
	t.Run("archived_and_restored_inactive_refusal", func(t *testing.T) { // Lifecycle admission is an independent fixture. Physical archive after a
		// retired repository exposed an existing owner count mismatch reported
		// to the integrator; this transport slice does not alter that owner.
		lifecycleDB := declarationHTTPDatabase(t)
		lifecycleBox := t.TempDir()
		lifecycleRoot := filepath.Join(lifecycleBox, "project")
		lifecycleID := ids.NewProjectID()
		declarationFixtureSource(t, lifecycleRoot, lifecycleID, map[string]any{})
		lifecycleServer := declarationOwnedServer(t, lifecycleDB, lifecycleBox, nil)
		lifecycleClient, _ := localclient.NewHTTP(lifecycleServer.URL)
		lifecycleInput := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: lifecycleRoot, NodeRef: "main"}
		reviewed, err := lifecycleClient.PlanProjectDeclaration(ctx, "lifecycle-plan", lifecycleInput)
		if err != nil {
			t.Fatal(err)
		}
		_, err = lifecycleClient.ApplyProjectDeclaration(ctx, "lifecycle-register", pc.DeclarationApplyRequest{SchemaVersion: lifecycleInput.SchemaVersion, ProjectRef: lifecycleRoot, NodeRef: "main", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, PlanID: reviewed.Data.PlanID, IdempotencyKey: "lifecycle"})
		if err != nil {
			t.Fatal(err)
		}
		lifecycleReq, err := requestctx.ResolveBootstrap(ctx, lifecycleDB, "lifecycle")
		if err != nil {
			t.Fatal(err)
		}
		declarationHTTPArchiveRefusal(t, lifecycleDB, lifecycleReq, lifecycleClient, lifecycleInput, lifecycleID, lifecycleRoot)
	})
}

func declarationHTTPArchiveRefusal(t *testing.T, db *sql.DB, req requestctx.Context, client localclient.Client, input pc.DeclarationPlanRequest, id, root string) {
	t.Helper()
	service := projects.NewService(db)
	ctx := t.Context()
	registered, err := service.GetProjectRegistrationStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.ReadProjectRepositoryState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	at := time.Now().UTC().Truncate(time.Microsecond)
	state := projects.ProjectPhysicalArchiveState{SchemaVersion: projects.ProjectPhysicalArchiveStateSchemaVersion, Status: "in_progress", Phase: projects.ProjectArchivePhaseDeactivationPending, MutationBlocked: true, ProjectID: id, ProjectSlug: registered.Project.Project.Slug, OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", PlanDigest: digest, WorkspacePlanDigest: digest, ActivePath: root, ArchivePath: root + "-archive", ActorID: req.ActorID, Reason: "disposable declaration transport lifecycle guard", StartedAt: at}
	transition := projects.ProjectPhysicalArchiveTransitionInput{State: state, ExpectedScopeID: registered.Project.Project.ProjectScopeID, ExpectedRegistrationID: registered.Registration.ProjectContractRegistrationID, ExpectedRegistrationRevision: registered.Registration.RegistrationRevision, ExpectedRepositorySourceRevision: repository.Source.SourceRevision}
	for _, member := range repository.Members {
		transition.ExpectedMembers = append(transition.ExpectedMembers, projects.ProjectArchiveRepositoryMemberBinding{RepositoryID: member.RepositoryID, RepositoryOwnerProjectID: member.RepositoryOwnerProjectID, MemberKey: member.Key, Role: member.Role})
	}
	for i, phase := range []projects.ProjectPhysicalArchivePhase{projects.ProjectArchivePhaseDeactivationPending, projects.ProjectArchivePhaseRuntimeDeactivated, projects.ProjectArchivePhaseProjectStatePending, projects.ProjectArchivePhaseComplete} {
		stamp := at.Add(time.Duration(i) * time.Second)
		transition.State.Phase = phase
		switch phase {
		case projects.ProjectArchivePhaseRuntimeDeactivated:
			transition.State.RuntimeDeactivatedAt = &stamp
			transition.State.RuntimeQuiescenceDigest = digest
		case projects.ProjectArchivePhaseProjectStatePending:
			transition.State.WorkspaceCompletedAt = &stamp
			transition.State.ArchiveManifestDigest = digest
		case projects.ProjectArchivePhaseComplete:
			transition.State.Status = "archived"
			transition.State.ArchivedAt = &stamp
		}
		if _, err = service.TransitionProjectPhysicalArchive(ctx, req, transition); err != nil {
			t.Fatal(err)
		}
	}
	before := declarationDBSnapshot(t, db)
	_, err = client.PlanProjectDeclaration(ctx, "archived", input)
	failure := declarationAssertClientFailure(t, err, pc.DeclarationTargetUnavailable)
	if failure.Detail.CauseCode != "project_archive_recovery_required" {
		t.Fatalf("archive cause=%s", failure.Detail.CauseCode)
	}
	declarationAssertUnchanged(t, db, before)
	start, moved, completed := at.Add(4*time.Second), at.Add(5*time.Second), at.Add(6*time.Second)
	restore := projects.ProjectPhysicalRestoreState{SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion, Request: req, OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: digest, WorkspacePlanDigest: digest, Phase: projects.ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: start}
	for _, phase := range []projects.ProjectPhysicalRestorePhase{projects.ProjectRestorePhasePending, projects.ProjectRestorePhaseProjectStatePending, projects.ProjectRestorePhaseComplete} {
		restore.Phase = phase
		if phase != projects.ProjectRestorePhasePending {
			restore.WorkspaceCompletedAt = &moved
			restore.ActiveManifestDigest = digest
		}
		if phase == projects.ProjectRestorePhaseComplete {
			restore.Status = "restored"
			restore.RestoredAt = &completed
		}
		if _, err = service.TransitionProjectPhysicalRestore(ctx, req, projects.ProjectPhysicalRestoreTransitionInput{Archive: transition, State: restore}); err != nil {
			t.Fatal(err)
		}
	}
	before = declarationDBSnapshot(t, db)
	_, err = client.PlanProjectDeclaration(ctx, "restored", input)
	failure = declarationAssertClientFailure(t, err, pc.DeclarationTargetUnavailable)
	if failure.Detail.CauseCode != "project_archive_recovery_required" {
		t.Fatalf("restore cause=%s", failure.Detail.CauseCode)
	}
	declarationAssertUnchanged(t, db, before)
}

func TestProjectDeclarationMissingProjectPostgres(t *testing.T) {
	db := declarationHTTPDatabase(t)
	box := t.TempDir()
	server := declarationOwnedServer(t, db, box, nil)
	client, _ := localclient.NewHTTP(server.URL)
	before := declarationDBSnapshot(t, db)
	t.Run("http_404", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/v1/projects/unknown-project/declaration-status?node_ref=main")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var env ProjectDeclarationErrorEnvelope
		_ = json.NewDecoder(resp.Body).Decode(&env)
		if resp.StatusCode != 404 || env.DeclarationError.CauseCode != "project_missing" {
			t.Fatalf("unknown project HTTP status=%d code=%s cause=%s", resp.StatusCode, env.Error.Code, env.DeclarationError.CauseCode)
		}
	})
	t.Run("client_404", func(t *testing.T) {
		_, err := client.GetProjectDeclarationStatus(t.Context(), "missing-client", "unknown-project", "main")
		failure := declarationAssertClientFailure(t, err, pc.DeclarationReferenceMissing)
		if failure.StatusCode != 404 {
			t.Fatalf("missing project client status=%d", failure.StatusCode)
		}
	})
	t.Run("cli_404", func(t *testing.T) {
		dir := t.TempDir()
		manifest := filepath.Join(dir, "install.yaml")
		if err := setup.WriteManifest(manifest, setup.InstallManifest{SchemaVersion: setup.ManifestSchemaVersion, NodeKey: "fixture-client", NodeID: ids.NewNodeID(), NodeKind: "workspace", NodeRole: "primary_workspace", RuntimeClass: "workspace_full", MainURL: server.URL, DataDir: filepath.Join(dir, "data"), BoxPath: filepath.Join(dir, "box"), BoxProfile: "workspace", MigrationsDir: "migrations", BootstrapMode: "none", ObjectStorePath: filepath.Join(dir, "data/object-store"), MainDocumentsPath: filepath.Join(dir, "data/main-documents"), StorageExportRoot: filepath.Join(dir, "data/storage-views/main-export")}); err != nil {
			t.Fatal(err)
		}
		cmd := loomcli.NewRootCommand()
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--config", manifest, "--json", "project", "status", "unknown-project", "--node", "main"})
		err := cmd.ExecuteContext(t.Context())
		var envelope response.ErrorEnvelope
		if err == nil || json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.Error.Code != string(pc.DeclarationReferenceMissing) || strings.Contains(out.String(), "transport.unavailable") {
			t.Fatalf("missing project CLI=%s error=%v", out.String(), err)
		}
	})
	declarationAssertUnchanged(t, db, before)
}
