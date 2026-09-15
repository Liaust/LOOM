package projectapply

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	localwatch "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	backendwatch "loom.local/loom/internal/watchedroots"
)

// The current desired source is fixture setup, not a publication operation.
// Register and report the real old owners first, retaining their identities.
func legacyOwnerFixture(t *testing.T) migrationFixture {
	t.Helper()
	f := migrationFixturePG(t, "plain")
	f.raw = []byte(fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject: {id: %s, slug: migration-fixture, name: Migration fixture, owner_node: main, status: active}\nfacets: {notes: true, repos: true, backup_policy: true}\n", f.id))
	notes := "kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes: {status: active, sync: true, index: true, backup: true, root_key: notes, path: ., include: ['**/*'], exclude: []}\n"
	repos := "kind: loom.repos\nschema_version: repos.contract.v0.4\nrepos:\n  defaults: {sync: false, backup: true, index: false, include: ['**/*'], exclude: []}\n  watch_roots: [{key: repos, path: ., display_name: Project Repositories}]\n  members: []\n"
	backup := "kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup:\n  enabled: true\n  defaults: {max_file_bytes: 4096, max_batch_bytes: 8192, max_pending_bytes: 9007199254740993}\n  roots:\n    - {key: notes, path: notes}\n    - {key: repos, path: repos}\n    - {key: controls, path: ., include: ['.loom/**']}\n"
	for ref, raw := range map[string]string{pc.CanonicalRootContractPath: string(f.raw), ".loom/contracts/notes.yaml": notes, ".loom/contracts/repos.yaml": repos, ".loom/contracts/backup.yaml": backup, "notes/retained.md": "Retained cobalt observatory decision", "repos/retained.txt": "retained source"} {
		migrationWrite(t, f.root, ref, raw)
	}
	f.analysis = pc.Analyze(f.root)
	input, err := projectregistration.BuildInput(f.analysis, "legacy-owner-fixture")
	if err != nil {
		t.Fatalf("old fixture: %v %+v", err, f.analysis.Report.Diagnostics)
	}
	f.registration, err = projects.NewService(f.db).RegisterProjectContract(t.Context(), f.req, input)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := nodes.NewService(f.db).IssueNodeCredential(t.Context(), f.req, nodes.IssueNodeCredentialInput{NodeRef: f.req.OriginNodeID, Reason: "owned D4e fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range f.analysis.Plan.WatchedRoots {
		kinds, _ := json.Marshal(w.SourceKinds)
		metadata, _ := json.Marshal(w.Metadata)
		_, err = projects.NewService(f.db).UpsertProjectWatchedRootRegistration(t.Context(), f.req, projects.UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: f.registration.Detail.Registration.ProjectContractRegistrationID, ProjectID: f.id, NodeID: f.req.OriginNodeID, OwnerNodeKey: "main", LocalRootKey: w.Key, BackendRootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, SourceKinds: kinds, SafeRootKey: w.SafeRootKey, RootRelativePath: w.RootRelativePath, DisplayName: w.DisplayName, SyncMode: w.SyncMode, BackupMode: w.BackupMode, IndexMode: w.IndexMode, DeleteMode: w.DeleteMode, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CommandJSON: json.RawMessage(`[]`), ActivationStatus: "applied", Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		_, err = backendwatch.NewService(f.db).Report(t.Context(), backendwatch.ReportInput{NodeRef: f.req.OriginNodeID, CredentialToken: credential.CredentialToken, RootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, DisplayName: w.DisplayName, SafeRootKey: w.SafeRootKey, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, Status: backendwatch.StatusHealthy})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = projects.NewService(f.db).CorrelateProjectWatchedRootReports(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	migrationWrite(t, f.root, ".loom/contracts/retained-project-v04.yaml", string(f.raw))
	migrationWrite(t, f.root, pc.CanonicalRootContractPath, fmt.Sprintf(`kind: loom.project
schema_version: project.contract.v0.5
project: {id: %s, slug: migration-fixture, name: Migration fixture, owner_node: main, status: active}
resources:
  notes_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: notes}}
  repos_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: repos}}
  controls_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: .}}
legacy_contracts:
  project: {ref: .loom/contracts/retained-project-v04.yaml, schema_version: project.contract.v0.4, digest: %s}
  notes: {key: imported_notes, ref: .loom/contracts/notes.yaml, schema_version: notes.contract.v0.3, digest: %s, protection: notes_backup}
  repos: {key: imported_repos, ref: .loom/contracts/repos.yaml, schema_version: repos.contract.v0.4, digest: %s, protection: repos_backup}
`, f.id, hashBytes(f.raw), hashBytes([]byte(notes)), hashBytes([]byte(repos))))
	a := pc.Analyze(f.root)
	if !a.Report.OK || a.Report.Registerable || a.Plan.Registerable || len(a.Plan.WatchedRoots) != 3 {
		t.Fatalf("desired fixture: %+v", a.Report.Diagnostics)
	}
	if _, err := projectregistration.BuildDeclarationIntent(a, "project.declaration"); err != nil {
		t.Fatalf("desired intent mapping: %v", err)
	}
	watch := WatchOwner{Resolver: f.resolver, Reconciler: projectwatch.NewDeclarationReconciler(f.db)}
	f.service = NewService(f.db, f.resolver, NewCurrentAuthority(f.db), map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: ProjectsOwner{Resolver: f.resolver, Projects: projects.NewService(f.db)}, pc.DeclarationOwnerKnowledge: watchTestOwner{watch, t}, pc.DeclarationOwnerProtection: watchTestOwner{watch, t}})
	return f
}

func TestDeclarationLegacyOwnerTransitionPostgres(t *testing.T) {
	f := legacyOwnerFixture(t)
	plan, err := f.service.Plan(t.Context(), f.principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.root})
	if err != nil {
		t.Fatalf("exact existing legacy owners must plan: %v", err)
	}
	if len(plan.Basis.Actions) != 6 || len(plan.Basis.Bindings) != 5 || plan.Basis.Bindings["imported_notes"].Kind != pc.DeclarationKnowledge || plan.Basis.Bindings["imported_repos"].Kind != pc.DeclarationProtection {
		t.Fatalf("incomplete imported owner closure: %+v", plan)
	}
	result, err := f.service.Apply(t.Context(), f.principal, realApplyRequest(plan, f.root))
	if failureCause(err) != "owner_pending" || result.State != pc.DeclarationOperationPartial {
		t.Fatalf("legacy owner transition: %+v %v", result, err)
	}
	message := watchMessage(t, f.service, result.OperationID)
	payload, err := projectwatch.DecodeDeclarationWatchPayload(message.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := projects.NewService(f.db).GetProjectRegistrationStatus(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	beforeRegistration := f.registration.Detail.Registration
	if registered.Registration.ProjectContractRegistrationID != beforeRegistration.ProjectContractRegistrationID || registered.Registration.RegistrationRevision != beforeRegistration.RegistrationRevision+1 || registered.Registration.ContractSchemaVersion != pc.ProjectSchemaV05 {
		t.Fatal("registration identity/revision transition differs")
	}
	rows, err := projects.NewService(f.db).ListProjectWatchedRootRegistrations(t.Context(), f.id)
	if err != nil || len(rows) != len(payload.Group.Predecessor.Members) {
		t.Fatal("watch set membership changed")
	}
	byKey := map[string]projects.ProjectWatchedRootRegistration{}
	for _, row := range rows {
		byKey[row.BackendRootKey] = row
	}
	for i, member := range payload.Group.Predecessor.Members {
		old, row := member.Registration, byKey[member.Registration.BackendRootKey]
		if row.ProjectWatchedRootRegistrationID != old.ProjectWatchedRootRegistrationID || row.WatchedRootID == nil || *row.WatchedRootID != *old.WatchedRootID || row.NodeID != old.NodeID || row.WorkerKey != old.WorkerKey || row.LocalRootKey != old.LocalRootKey || row.ConfigHash != payload.Group.Roots[i].ConfigHash {
			t.Fatal("watch/root/node identity or effective configuration changed")
		}
	}
	t.Logf("registration revision %d -> %d; preserved watch/root pairs=%d; full control bytes=%d", beforeRegistration.RegistrationRevision, registered.Registration.RegistrationRevision, len(rows), len(message.PayloadJSON))
	base := t.TempDir()
	store := nodeagent.Store{ConfigPath: filepath.Join(base, "config.json"), StatePath: filepath.Join(base, "state.json"), DataDir: filepath.Join(base, "data")}
	if err = store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	safe := filesystemconnector.DefaultSafeRoot("project", f.root)
	safe.MaxFileBytes = 1024 * 1024 * 1024
	safe.AllowList = false
	safe.IncludeHiddenDefault = true
	safe.Metadata = json.RawMessage(`{"old_policy":"preserve"}`)
	cfg := nodeagent.Config{NodeKey: "main", DisplayName: "Main", MainURL: "http://main.test", BoxRootPath: f.box, Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{safe}}}
	if err = store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	prior := map[string]noderuntime.WorkerInstance{}
	for _, w := range f.analysis.Plan.WatchedRoots {
		instance := noderuntime.WorkerInstance{WorkerKey: w.WorkerKey, Kind: noderuntime.KindWatchedRoot, DisplayName: w.DisplayName, Enabled: true, IntervalSeconds: 83, LeaseTimeoutSeconds: 157, LocalRootKey: w.Key, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CreatedAt: time.Now().UTC().Add(-time.Hour)}
		if err = runtimeStore.SaveInstance(instance); err != nil {
			t.Fatal(err)
		}
		prior[w.WorkerKey], err = runtimeStore.LoadInstance(w.WorkerKey)
		if err != nil {
			t.Fatal(err)
		}
		if err = localwatch.NewStore(store.DataDir).EnsureRoot(w.BackendRootKey); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(localwatch.NewStore(store.DataDir).RootDir(w.BackendRootKey), "retained-marker"), []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := nodeagent.State{NodeID: f.req.OriginNodeID}
	ack := nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, state, message)
	if ack.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("legacy node adoption: %s", ack.ResultJSON)
	}
	afterConfig, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	old, ok := filesystemconnector.FindSafeRoot(afterConfig.Filesystem, "project")
	oldJSON, _ := pc.DecodeDeclarationEvidenceJSON(mustWatchJSON(old))
	safeJSON, _ := pc.DecodeDeclarationEvidenceJSON(mustWatchJSON(safe))
	if !ok || !reflect.DeepEqual(oldJSON, safeJSON) {
		t.Fatalf("old safe root changed: before=%+v after=%+v", safe, old)
	}
	safeKey, _ := projectwatch.DeclarationSafeRootKey(f.id)
	successor, ok := filesystemconnector.FindSafeRoot(afterConfig.Filesystem, safeKey)
	if !ok || successor.AllowList != safe.AllowList || successor.AllowMetadata != safe.AllowMetadata || successor.AllowIngest != safe.AllowIngest || successor.MaxFileBytes != safe.MaxFileBytes || successor.IncludeHiddenDefault != safe.IncludeHiddenDefault || successor.PrivateBackupOnly != safe.PrivateBackupOnly {
		t.Fatal("successor widened the old safe-root policy")
	}
	for _, w := range payload.Group.Roots {
		after, err := runtimeStore.LoadInstance(w.WorkerKey)
		if err != nil {
			t.Fatal(err)
		}
		before := prior[w.WorkerKey]
		if after.ConfigHash != w.ConfigHash || after.IntervalSeconds != before.IntervalSeconds || after.LeaseTimeoutSeconds != before.LeaseTimeoutSeconds || !after.CreatedAt.Equal(before.CreatedAt) || after.LocalRootKey != before.LocalRootKey {
			t.Fatal("worker identity/schedule changed")
		}
		if raw, err := os.ReadFile(filepath.Join(localwatch.NewStore(store.DataDir).RootDir(w.BackendRootKey), "retained-marker")); err != nil || string(raw) != "retained" {
			t.Fatal("root state replaced")
		}
	}
	if again := nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, state, message); again.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("legacy node replay: %s", again.ResultJSON)
	}
}

func realWatchFixture(t *testing.T) (*Service, *LocalResolver, Principal, string) {
	t.Helper()
	service, resolver, p, root, db := realOwnerFixture(t, 0)
	path := filepath.Join(root, pc.CanonicalRootContractPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	source["resources"] = map[string]any{"reading": map[string]any{"kind": "knowledge", "knowledge": map[string]any{"path": "material", "category": "research", "protection": "retained"}}, "retained": map[string]any{"kind": "protection", "protection": map[string]any{"policy_ref": ".loom/backup.yaml"}}}
	raw, _ = json.Marshal(source)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "material"), 0700); err != nil {
		t.Fatal(err)
	}
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
	if err = os.WriteFile(filepath.Join(root, ".loom/backup.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	owner := WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	service.owners[pc.DeclarationOwnerKnowledge] = watchTestOwner{Owner: owner, t: t}
	service.owners[pc.DeclarationOwnerProtection] = watchTestOwner{Owner: owner, t: t}
	return service, resolver, p, root
}

type watchTestOwner struct {
	Owner
	t *testing.T
}

func (o watchTestOwner) Apply(ctx context.Context, c ActionCall) (Observation, error) {
	v, e := o.Owner.Apply(ctx, c)
	if e != nil {
		o.t.Logf("watch owner apply: %v", e)
	}
	return v, e
}
func (o watchTestOwner) Observe(ctx context.Context, c ActionCall) (Observation, error) {
	v, e := o.Owner.Observe(ctx, c)
	if e != nil {
		o.t.Logf("watch owner observe: %v", e)
	}
	return v, e
}
func watchPending(t *testing.T, service *Service, p Principal, root string) (pc.DeclarationApplyRequest, pc.DeclarationResult) {
	t.Helper()
	plan, err := service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Basis.Actions) != 3 {
		t.Fatalf("watch action closure=%+v", plan)
	}
	input := realApplyRequest(plan, root)
	input.IdempotencyKey = "watch:" + plan.PlanID
	result, err := service.Apply(t.Context(), p, input)
	if failureCause(err) != "owner_pending" || result.State != pc.DeclarationOperationPartial || result.Readiness.Applied.State == pc.DeclarationSatisfied {
		t.Fatalf("pending=%+v %v", result, err)
	}
	input.OperationID = result.OperationID
	return input, result
}
func watchMessage(t *testing.T, service *Service, op string) communication.Message {
	t.Helper()
	var id string
	if err := service.db.QueryRow(`SELECT message_id FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, op).Scan(&id); err != nil {
		t.Fatal(err)
	}
	message, err := communication.NewService(service.db).GetMessage(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return message
}
func TestDeclarationRealWatchPipelinePostgres(t *testing.T) {
	service, resolver, p, root := realWatchFixture(t)
	sourcePath := filepath.Join(root, pc.CanonicalRootContractPath)
	sourceBefore, _ := os.ReadFile(sourcePath)
	input, pending := watchPending(t, service, p, root)
	message := watchMessage(t, service, pending.OperationID)
	payload, err := projectwatch.DecodeDeclarationWatchPayload(message.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Group.Roots) != 1 || len(payload.Tokens) != 2 {
		t.Fatalf("combined group=%+v", payload.Group)
	}
	var rows int
	var applied bool
	if err = service.db.QueryRow(`SELECT count(*),bool_or(last_applied_at IS NOT NULL) FROM projects.project_watched_root_registrations`).Scan(&rows, &applied); err != nil || rows != 1 || applied {
		t.Fatalf("desired rows=%d applied=%v %v", rows, applied, err)
	}
	for _, sql := range []string{`UPDATE projects.declaration_watch_intents SET stage='applied' WHERE final_intent`, `DELETE FROM projects.declaration_watch_intents`, `UPDATE projects.declaration_watch_deliveries SET desired_revision=9007199254740993`, `DELETE FROM projects.declaration_watch_deliveries`} {
		if _, err = service.db.Exec(sql); err == nil {
			t.Fatalf("forged evidence allowed: %s", sql)
		}
	}
	runtimeDir := t.TempDir()
	store := nodeagent.Store{ConfigPath: filepath.Join(runtimeDir, "config.json"), StatePath: filepath.Join(runtimeDir, "state.json"), DataDir: filepath.Join(runtimeDir, "data")}
	if err = store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveConfig(nodeagent.Config{NodeKey: "main", DisplayName: "Main", MainURL: "http://main.test", BoxRootPath: filepath.Dir(root)}); err != nil {
		t.Fatal(err)
	}
	state := nodeagent.State{NodeID: payload.Group.NodeID}
	ack := nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, state, message)
	if ack.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("node failed: %s", ack.ResultJSON)
	}
	// A forged message status without the transport's authenticated ACK row
	// cannot be consumed as completed owner evidence.
	if _, err = service.db.Exec(`UPDATE communication.messages SET status='acked',result_json=$2 WHERE communication_message_id=$1`, message.CommunicationMessageID, ack.ResultJSON); err != nil {
		t.Fatal(err)
	}
	forged, err := service.Apply(t.Context(), p, input)
	if failureCause(err) != "owner_pending" || forged.State != pc.DeclarationOperationPartial {
		t.Fatalf("unrecorded ACK was accepted: %+v %v", forged, err)
	}
	if _, err = service.db.Exec(`UPDATE projects.declaration_watch_intents SET stage='applied' WHERE operation_id=$1 AND final_intent`, pending.OperationID); err == nil {
		t.Fatal("SQL guard accepted absent ACK row")
	}
	if _, err = service.db.Exec(`UPDATE communication.messages SET status=$2,result_json=$3 WHERE communication_message_id=$1`, message.CommunicationMessageID, message.Status, message.ResultJSON); err != nil {
		t.Fatal(err)
	}
	// Lose the ACK, restart both owners and node, and replay the exact message.
	service = NewService(service.db, resolver, NewCurrentAuthority(service.db), map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: ProjectsOwner{Resolver: resolver, Projects: projects.NewService(service.db)}, pc.DeclarationOwnerKnowledge: WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(service.db)}, pc.DeclarationOwnerProtection: WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(service.db)}})
	lost, err := service.Apply(t.Context(), p, input)
	if failureCause(err) != "owner_pending" || lost.State != pc.DeclarationOperationPartial {
		t.Fatalf("lost ACK=%+v %v", lost, err)
	}
	if again := watchMessage(t, service, pending.OperationID); again.CommunicationMessageID != message.CommunicationMessageID {
		t.Fatal("lost ACK reminted delivery")
	}
	ack = nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, state, message)
	credential, err := nodes.NewService(service.db).IssueNodeCredential(t.Context(), declarationRequest(p), nodes.IssueNodeCredentialInput{NodeRef: state.NodeID, Reason: "owned D3b fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ack.CredentialToken = credential.CredentialToken
	if _, err = communication.NewService(service.db).Ack(t.Context(), declarationRequest(p), ack); err != nil {
		t.Fatal(err)
	}
	// Read-only status/plan cannot consume an available ACK or activate owners.
	if _, err = service.Status(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Operation(t.Context(), p, pending.OperationID); err != nil {
		t.Fatal(err)
	}
	var stage string
	if err = service.db.QueryRow(`SELECT stage FROM projects.declaration_watch_intents WHERE operation_id=$1 AND final_intent`, pending.OperationID).Scan(&stage); err != nil || stage != "pending" {
		t.Fatalf("read-only status consumed ACK: %s %v", stage, err)
	}
	done, err := service.Apply(t.Context(), p, input)
	if err != nil || done.State != pc.DeclarationOperationSucceeded {
		t.Fatalf("acknowledged=%+v %v", done, err)
	}
	item := payload.Group.Roots[0]
	report, err := backendwatch.NewService(service.db).Report(t.Context(), backendwatch.ReportInput{NodeRef: state.NodeID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: backendwatch.StatusHealthy})
	if err != nil {
		t.Fatal(err)
	}
	_ = report
	if err = projects.NewService(service.db).CorrelateProjectWatchedRootReports(t.Context(), payload.Group.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err = service.db.QueryRow(`SELECT count(*) FROM projects.project_watched_root_registrations p JOIN watched_roots.roots r ON r.watched_root_id=p.watched_root_id WHERE p.config_hash=r.config_hash AND p.config_json=r.config_json AND p.last_applied_at IS NOT NULL`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("report correlation=%d %v", rows, err)
	}
	sourceAfter, _ := os.ReadFile(sourcePath)
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("source rewritten")
	}
	replay, err := service.Apply(t.Context(), p, input)
	if err != nil || replay.State != pc.DeclarationOperationSucceeded {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	// Removal is another explicit desired snapshot; retirement keeps source bytes.
	var document map[string]any
	_ = json.Unmarshal(sourceBefore, &document)
	document["resources"] = map[string]any{}
	raw, _ := json.Marshal(document)
	if err = os.WriteFile(sourcePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	next, retiring := watchPending(t, service, p, root)
	retireMessage := watchMessage(t, service, retiring.OperationID)
	retirement, err := projectwatch.DecodeDeclarationWatchPayload(retireMessage.PayloadJSON)
	if err != nil || len(retirement.Group.Roots) != 0 {
		t.Fatalf("empty retirement: %v", err)
	}
	ack = nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, state, retireMessage)
	ack.CredentialToken = credential.CredentialToken
	if _, err = communication.NewService(service.db).Ack(t.Context(), declarationRequest(p), ack); err != nil {
		t.Fatal(err)
	}
	retired, err := service.Apply(t.Context(), p, next)
	if err != nil || retired.State != pc.DeclarationOperationSucceeded {
		t.Fatalf("retired=%+v %v", retired, err)
	}
}
func TestDeclarationRealWatchCancelledSiblingPostgres(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "source"} {
		t.Run(mode, func(t *testing.T) {
			service, _, p, root := realWatchFixture(t)
			plan, err := service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			service.owners[pc.DeclarationOwnerKnowledge] = realOwnerIntercept{Owner: service.owners[pc.DeclarationOwnerKnowledge], after: func(ActionCall) {
				switch mode {
				case "cancel":
					cancel()
				case "revoke":
					if _, e := service.db.Exec(`UPDATE identity.actor_node_authorizations SET authorization_level=2 WHERE actor_id=$1`, p.ActorID); e != nil {
						t.Fatal(e)
					}
				case "source":
					f, e := os.OpenFile(filepath.Join(root, pc.CanonicalRootContractPath), os.O_APPEND|os.O_WRONLY, 0600)
					if e != nil {
						t.Fatal(e)
					}
					_, _ = f.WriteString("\n ")
					_ = f.Close()
				}
			}}
			result, _ := service.Apply(ctx, p, realApplyRequest(plan, root))
			if result.State == pc.DeclarationOperationSucceeded {
				t.Fatal("cancelled sibling completed")
			}
			var rows int
			if err = service.db.QueryRow(`SELECT count(*) FROM projects.declaration_watch_deliveries`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("denied sibling queued=%d %v", rows, err)
			}
			if err = service.db.QueryRow(`SELECT count(*) FROM projects.declaration_watch_intents WHERE stage='registered'`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("registered intent=%d %v", rows, err)
			}
		})
	}
}

func failureCause(err error) string {
	if f, ok := err.(*Failure); ok {
		return f.Cause
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func mustWatchJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
