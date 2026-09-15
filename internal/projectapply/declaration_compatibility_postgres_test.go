package projectapply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/config"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

func TestDeclarationCompatibilityPlanApplyStatePostgres(t *testing.T) {
	for _, state := range []string{"", ".repo", "development/state"} {
		t.Run(fmt.Sprintf("state_%q", state), func(t *testing.T) {
			service, resolver, principal, root, database := realOwnerFixture(t, 1)
			path := filepath.Join(root, pc.CanonicalRootContractPath)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Add explicit state intent only as fixture setup. Omission stays
			// omitted, and plan/apply must preserve these exact source bytes.
			if state != "" {
				var document pc.ProjectDeclaration
				if err = json.Unmarshal(raw, &document); err != nil {
					t.Fatal(err)
				}
				document.Resources["repo0"].Repository.StateRoot = state
				if raw, err = json.Marshal(document); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := migrationPureSnapshot(t, database, root)
			request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root}
			plan, err := service.Plan(t.Context(), principal, request)
			if err != nil {
				t.Fatalf("normal plan rejects canonical state intent: %v", err)
			}
			if len(plan.Basis.Actions) != 1 || plan.Basis.Actions[0].Owner != pc.DeclarationOwnerProjects || len(plan.Basis.Bindings) != 1 || plan.Basis.Bindings["repo0"].RepositoryID != "" {
				t.Fatalf("unexpected registration plan: %+v", plan)
			}
			again, err := service.Plan(t.Context(), principal, request)
			if err != nil || again.PlanID != plan.PlanID {
				t.Fatalf("unstable normal plan: %v", err)
			}
			var rows int
			if err = database.QueryRow(`SELECT count(*) FROM projects.projects`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("plan registered a project: %d %v", rows, err)
			}
			apply := realApplyRequest(plan, root)
			result, err := service.Apply(t.Context(), principal, apply)
			if err != nil || result.State != pc.DeclarationOperationSucceeded {
				t.Fatalf("normal apply: %+v %v", result, err)
			}
			registered, err := projects.NewService(database).ReadProjectRepositoryState(t.Context(), plan.Basis.Target.ProjectID)
			if err != nil || registered.Source == nil || len(registered.Members) != 1 {
				t.Fatalf("registered source: %+v %v", registered, err)
			}
			member := registered.Members[0]
			if member.StateRoot != state || member.Key != "repo0" || member.Path != "repo0" || member.Role != projects.ProjectRepositoryRoleComponent || member.RepositoryOwnerProjectID != plan.Basis.Target.ProjectID || member.RepositoryID == "" || member.SourceBindingDigest == "" || member.StoredObservationPosture != projects.ProjectRepositoryObservationNotObserved {
				t.Fatalf("registered binding lost exact intent/ownership: %+v", member)
			}
			var sourceRaw []byte
			var sourceHash string
			if err = database.QueryRow(`SELECT source_snapshot_json,project_contract_digest FROM projects.project_repository_sources WHERE project_id=$1`, plan.Basis.Target.ProjectID).Scan(&sourceRaw, &sourceHash); err != nil {
				t.Fatal(err)
			}
			var source projects.ProjectRepositorySourceSnapshot
			if err = json.Unmarshal(sourceRaw, &source); err != nil || sourceHash != hashBytes(raw) || source.ProjectContractDigest != sourceHash || len(source.Members) != 1 || source.Members[0].StateRoot != state || source.Members[0].RepositoryID != member.RepositoryID || source.Members[0].ObservationBindingDigest != member.SourceBindingDigest {
				t.Fatalf("source-bound state intent changed: %+v %v", source, err)
			}
			if err = database.QueryRow(`SELECT count(*) FROM projects.declaration_owner_receipts`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("owner receipts=%d: %v", rows, err)
			}
			after := migrationPureSnapshot(t, database, root)
			for key, value := range before {
				if strings.HasPrefix(key, "file:") && after[key] != value {
					t.Fatalf("normal plan/apply changed source path %s", key)
				}
			}
			for key := range after {
				if strings.HasPrefix(key, "file:") {
					if _, ok := before[key]; !ok {
						t.Fatalf("normal plan/apply created path %s", key)
					}
				}
			}
			currentRaw, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, currentRaw) {
				t.Fatalf("declaration source bytes changed: %v", err)
			}
			restarted := NewService(database, resolver, NewCurrentAuthority(database), map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: ProjectsOwner{Resolver: resolver, Projects: projects.NewService(database)}})
			apply.OperationID = result.OperationID
			replay, err := restarted.Apply(t.Context(), principal, apply)
			if err != nil || replay.OperationID != result.OperationID || replay.State != pc.DeclarationOperationSucceeded {
				t.Fatalf("restarted apply replay: %+v %v", replay, err)
			}
			afterReplay, err := projects.NewService(database).ReadProjectRepositoryState(t.Context(), plan.Basis.Target.ProjectID)
			if err != nil || !reflect.DeepEqual(registered, afterReplay) || !reflect.DeepEqual(after, migrationPureSnapshot(t, database, root)) {
				t.Fatalf("replay changed registered intent, database or files: %v", err)
			}
		})
	}
}

func TestDeclarationCompatibilityRegisteredRootPostgres(t *testing.T) {
	f := migrationFixturePG(t, "dot")
	migrationSeedWatches(t, f)
	before := migrationPureSnapshot(t, f.db, f.root)
	a, e := f.assess(t, nil)
	if e != nil || a.State != "eligible" || a.Preview.Candidate == nil {
		t.Fatalf("complete registered dot: %v %+v %+v", e, a.Issues, a.Preview.Issues)
	}
	if !reflect.DeepEqual(a.Preview.BeforeRoots, a.Preview.AfterRoots) || len(a.Preview.AfterRoots) != 3 {
		t.Fatal("owner keys/configuration changed")
	}
	if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
		t.Fatal("assessment wrote state")
	}
	// Source replacement here is test setup in the owned temporary project.
	// The assessment itself neither publishes source nor enrolls a root.
	migrationWrite(t, f.root, pc.CanonicalRootContractPath, a.Preview.Candidate.YAML)
	compiled := pc.Analyze(f.root)
	input, e := projectregistration.BuildInput(compiled, "compatibility-fixture")
	if e != nil {
		t.Fatalf("candidate binding: %v %+v", e, compiled.Report.Diagnostics)
	}
	registered, e := projects.NewService(f.db).RegisterProjectContract(t.Context(), f.req, input)
	if e != nil {
		t.Fatal(e)
	}
	if registered.Detail.Registration.ProjectRoot != f.root {
		t.Fatal("root rebound outside registered project")
	}
	for _, w := range compiled.Plan.WatchedRoots {
		if w.SafeRootKey != "project" {
			t.Fatal("portable root owner changed")
		}
	}
	local, e := f.resolver.load(t.Context(), f.principal, f.id, "main")
	if e != nil || local.Target.ProjectRoot != f.root {
		t.Fatalf("configured physical project binding: %+v %v", local.Target, e)
	}
	for _, box := range []string{f.root, t.TempDir()} {
		outside := NewLocalResolver(f.db, func() (config.Config, error) {
			return config.Config{NodeID: "main", BoxPath: box}, nil
		})
		if _, e := outside.load(t.Context(), f.principal, f.id, "main"); e == nil {
			t.Fatal("dot protection rebound to Box or outside configured Box")
		}
	}
	replay, e := projects.NewService(f.db).RegisterProjectContract(t.Context(), f.req, input)
	if e != nil || !replay.Unchanged {
		t.Fatalf("exact replay: %+v %v", replay, e)
	}
	for _, path := range []string{".git", ".repo"} {
		if _, e := os.Lstat(filepath.Join(f.root, path)); !os.IsNotExist(e) {
			t.Fatal("implicit development state created")
		}
	}
}

func TestDeclarationCompatibilityRegisteredStatePostgres(t *testing.T) {
	f := migrationFixturePG(t, "plain")
	service := projects.NewService(f.db)
	var prior projects.ProjectRepositoryReadModel
	var repositoryID string
	for index, state := range []string{"", ".repo", "development/state", ""} {
		t.Run(fmt.Sprintf("revision_%d_%q", index+1, state), func(t *testing.T) {
			field := ""
			if state != "" {
				field = fmt.Sprintf(", state_root: %q", state)
			}
			raw := fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: %s, slug: migration-fixture, name: Migration fixture, owner_node: main, status: active}\nresources:\n  api:\n    kind: repository\n    repository: {path: workspace, role: primary%s}\n", f.id, field)
			migrationWrite(t, f.root, pc.CanonicalRootContractPath, raw)
			if e := os.MkdirAll(filepath.Join(f.root, "workspace"), 0700); e != nil {
				t.Fatal(e)
			}
			compiled := pc.Analyze(f.root)
			input, e := projectregistration.BuildInput(compiled, "compatibility-state-fixture")
			if e != nil {
				t.Fatalf("state compiler: %v %+v", e, compiled.Report.Diagnostics)
			}
			if _, e = service.RegisterProjectContract(t.Context(), f.req, input); e != nil {
				t.Fatal(e)
			}
			current, e := service.ReadProjectRepositoryState(t.Context(), f.id)
			if e != nil || len(current.Members) != 1 || current.Members[0].StateRoot != state {
				t.Fatalf("registered state: %+v %v", current.Members, e)
			}
			m := current.Members[0]
			if m.RepositoryOwnerProjectID != f.id || m.Path != "workspace" || m.Role != projects.ProjectRepositoryRolePrimary {
				t.Fatal("member ownership changed")
			}
			if repositoryID == "" {
				repositoryID = m.RepositoryID
			} else if m.RepositoryID != repositoryID || current.Source.SourceRevision != prior.Source.SourceRevision+1 || m.SourceBindingDigest == prior.Members[0].SourceBindingDigest {
				t.Fatal("state change failed existing source-binding revision")
			}
			if m.StoredObservationPosture != projects.ProjectRepositoryObservationNotObserved {
				t.Fatal("registration invented observation")
			}
			replay, e := service.RegisterProjectContract(t.Context(), f.req, input)
			if e != nil || !replay.Unchanged || len(replay.EventIDs) != 0 {
				t.Fatalf("state replay: %+v %v", replay, e)
			}
			afterReplay, e := service.ReadProjectRepositoryState(t.Context(), f.id)
			if e != nil || !reflect.DeepEqual(current, afterReplay) {
				t.Fatal("exact replay changed source binding")
			}
			before := migrationPureSnapshot(t, f.db, f.root)
			projection, e := (projectstate.Service{Reader: service, LocalNode: "main"}).ObserveProject(t.Context(), f.id)
			if e != nil || len(projection.Members) != 1 {
				t.Fatalf("observe: %+v %v", projection, e)
			}
			observed := projection.Members[0]
			if observed.StateRoot != state || observed.DevelopmentState.Posture != projectstate.DevelopmentStateNotEnabled || observed.ObservationPosture != projects.ProjectRepositoryObservationNotObserved {
				t.Fatalf("absent state/Git should remain unobserved: %+v", observed)
			}
			assessment, e := f.assess(t, nil)
			if e != nil || assessment.State != "already_declaration" {
				t.Fatalf("already v05: %+v %v", assessment, e)
			}
			if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
				t.Fatal("observation/assessment wrote data")
			}
			for _, path := range []string{"workspace/.git", "workspace/.repo", "workspace/development"} {
				if _, e := os.Lstat(filepath.Join(f.root, path)); !os.IsNotExist(e) {
					t.Fatal("implicit development directory created")
				}
			}
			if state == ".repo" {
				for _, malformed := range []bool{false, true} {
					identity := "repository: {id: repo_other, project_id: " + f.id + "}\n"
					want := projectstate.DevelopmentStateMismatch
					if malformed {
						identity, want = "repository: [unterminated\n", projectstate.DevelopmentStateInvalid
					}
					migrationWrite(t, f.root, "workspace/.repo/repo.yaml", identity)
					before := migrationPureSnapshot(t, f.db, f.root)
					observed, e := (projectstate.Service{Reader: service, LocalNode: "main"}).ObserveProject(t.Context(), f.id)
					if e != nil || len(observed.Members) != 1 || observed.Members[0].DevelopmentState.Posture != want {
						t.Fatalf("existing state handling: %+v %v", observed, e)
					}
					if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
						t.Fatal("observer rewrote malformed/mismatched state")
					}
				}
				// Remove precisely the two synthetic paths created above.
				if e := os.Remove(filepath.Join(f.root, "workspace/.repo/repo.yaml")); e != nil {
					t.Fatal(e)
				}
				if e := os.Remove(filepath.Join(f.root, "workspace/.repo")); e != nil {
					t.Fatal(e)
				}
			}
			prior = current
		})
		if t.Failed() {
			return
		}
	}
}

func TestDeclarationCompatibilityRegisteredConversionStatePostgres(t *testing.T) {
	for _, mode := range []string{"state_root", "custom_state_root"} {
		t.Run(mode, func(t *testing.T) {
			f := migrationFixturePG(t, mode)
			state := ".repo"
			if mode == "custom_state_root" {
				state = "development/state"
			}
			before := migrationPureSnapshot(t, f.db, f.root)
			a, e := f.assess(t, nil)
			if e != nil || a.State != "eligible" || a.Preview.Candidate == nil || len(a.Preview.Identity.Members) != 1 || a.Preview.Identity.Members[0].StateRoot != state || !strings.Contains(a.Preview.Candidate.YAML, "state_root: "+state) {
				t.Fatalf("registered state conversion: %v %+v %+v", e, a.Issues, a.Preview)
			}
			if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
				t.Fatal("conversion observation wrote state")
			}
			// Corrupt only this disposable membership row. Exact source/owner
			// binding must fail, never silently erase the registered state intent.
			migrationSQL(t, f.db, `UPDATE projects.project_repository_memberships SET state_root='' WHERE project_id=$1`, f.id)
			bad, e := f.assess(t, nil)
			if e == nil && (bad.State == "eligible" || bad.Preview.Candidate != nil) {
				t.Fatal("registered state mismatch became a candidate")
			}
		})
	}
}

func TestDeclarationCompatibilityRegisteredStateTamperPostgres(t *testing.T) {
	f := migrationFixturePG(t, "plain")
	migrationWrite(t, f.root, "workspace/preserved.txt", "unchanged")
	migrationWrite(t, f.root, pc.CanonicalRootContractPath, fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: %s, slug: migration-fixture, name: Migration fixture, owner_node: main, status: active}\nresources:\n  api:\n    kind: repository\n    repository: {path: workspace, role: primary, state_root: .repo}\n", f.id))
	input, e := projectregistration.BuildInput(pc.Analyze(f.root), "compatibility-state-tamper")
	if e != nil {
		t.Fatal(e)
	}
	mutate := func(raw []byte) []byte {
		var v map[string]any
		if e := json.Unmarshal(raw, &v); e != nil {
			t.Fatal(e)
		}
		v["declaration"].(map[string]any)["repositories"].([]any)[0].(map[string]any)["state_root"] = "other-state"
		out, _ := json.Marshal(v)
		return out
	}
	input.ValidationReport = mutate(input.ValidationReport)
	input.RegistrationPlan = mutate(input.RegistrationPlan)
	before := migrationPureSnapshot(t, f.db, f.root)
	if _, e = projects.NewService(f.db).RegisterProjectContract(t.Context(), f.req, input); e == nil {
		t.Fatal("matching forged compiler sets overrode source intent")
	}
	if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
		t.Fatal("refused registration wrote state")
	}
}

func TestDeclarationLegacyReferenceBeforeEffectPostgres(t *testing.T) {
	for _, scenario := range []string{"plan", "apply", "direct", "direct_nil_source", "direct_misleading_schema", "registered_plan", "registered_apply", "registered_direct_misleading_schema"} {
		t.Run(scenario, func(t *testing.T) {
			service, _, principal, root, database := realOwnerFixture(t, 0)
			ordinary := pc.Analyze(root)
			input, err := projectregistration.BuildInput(ordinary, "legacy-reference-fixture")
			if err != nil {
				t.Fatal(err)
			}
			plan, err := service.Plan(t.Context(), principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
			if err != nil {
				t.Fatal(err)
			}
			var source map[string]any
			if err = json.Unmarshal(ordinary.Loaded.Raw, &source); err != nil {
				t.Fatal(err)
			}
			old := map[string]any{"kind": pc.ProjectKind, "schema_version": pc.ProjectSchemaV04, "project": source["project"], "facets": map[string]bool{}}
			oldRaw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			retained := ".loom/contracts/retained-project-v04.yaml"
			migrationWrite(t, root, retained, string(oldRaw))
			if strings.HasPrefix(scenario, "registered_") {
				migrationWrite(t, root, pc.CanonicalRootContractPath, string(oldRaw))
				legacyInput, e := projectregistration.BuildInput(pc.Analyze(root), "legacy-registration-control")
				if e != nil {
					t.Fatal(e)
				}
				if _, e = projects.NewService(database).RegisterProjectContract(t.Context(), declarationRequest(principal), legacyInput); e != nil {
					t.Fatal(e)
				}
			}
			source["legacy_contracts"] = map[string]any{"project": map[string]any{"ref": retained, "schema_version": pc.ProjectSchemaV04, "digest": hashBytes(oldRaw)}}
			raw, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			migrationWrite(t, root, pc.CanonicalRootContractPath, string(raw))
			current := pc.Analyze(root)
			if !current.Report.OK || current.Report.Registerable || current.Plan.Registerable {
				t.Fatalf("representation should be valid but not registerable: %+v", current.Report)
			}
			before := migrationPureSnapshot(t, database, root)
			operation := strings.TrimPrefix(scenario, "registered_")
			switch operation {
			case "plan":
				_, err = service.Plan(t.Context(), principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
			case "apply":
				_, err = service.Apply(t.Context(), principal, realApplyRequest(plan, root))
			default:
				// Bypass the public mapper with otherwise valid ordinary input and forged
				// registerable flags. The domain must inspect the actual contract first.
				input.Contract = raw
				input.ContractHash = hashBytes(raw)
				if operation != "direct" {
					input.RepositorySource = nil
				}
				if operation == "direct_misleading_schema" {
					input.ContractSchemaVersion = pc.ProjectSchemaV04
				}
				_, err = projects.NewService(database).RegisterProjectContract(t.Context(), declarationRequest(principal), input)
			}
			if err == nil || !strings.Contains(err.Error(), "legacy_owner_transition_required") {
				t.Fatalf("missing pre-effect compatibility refusal: %v", err)
			}
			if !reflect.DeepEqual(before, migrationPureSnapshot(t, database, root)) {
				t.Fatal("refusal changed database or filesystem")
			}
		})
	}
}

type legacyBeforeOwner struct {
	Owner
	before func(ActionCall)
}

func (o legacyBeforeOwner) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	if o.before != nil {
		o.before(call)
	}
	return o.Owner.Apply(ctx, call)
}

func TestDeclarationLegacyOwnerPredecessorCASPostgres(t *testing.T) {
	cases := map[string]string{
		"added_member":        `INSERT INTO projects.project_watched_root_registrations SELECT (jsonb_populate_record(NULL::projects.project_watched_root_registrations,to_jsonb(r)||jsonb_build_object('project_watched_root_registration_id',project_watched_root_registration_id||'_extra','backend_root_key',backend_root_key||'_extra','local_root_key',local_root_key||'_extra'))).* FROM projects.project_watched_root_registrations r LIMIT 1`,
		"removed_member":      `DELETE FROM projects.project_watched_root_registrations WHERE project_watched_root_registration_id=(SELECT project_watched_root_registration_id FROM projects.project_watched_root_registrations ORDER BY backend_root_key LIMIT 1)`,
		"substituted_member":  `UPDATE projects.project_watched_root_registrations SET project_watched_root_registration_id=project_watched_root_registration_id||'_replacement'`,
		"policy_order":        `UPDATE projects.project_watched_root_registrations SET config_json=jsonb_set(config_json,'{include}', '["z/**","a/**"]'::jsonb)`,
		"precise_limit":       `UPDATE projects.project_watched_root_registrations SET config_json=jsonb_set(config_json,'{backup_policy,max_pending_bytes}','9007199254740992'::jsonb)`,
		"activation":          `UPDATE projects.project_watched_root_registrations SET activation_status='disabled'`,
		"report_status":       `UPDATE watched_roots.roots SET status='degraded'`,
		"report_config":       `UPDATE watched_roots.roots SET config_json=config_json||'{"unknown":"changed"}'::jsonb`,
		"report_worker":       `UPDATE watched_roots.roots SET worker_key=worker_key||'_other'`,
		"report_reachability": `UPDATE watched_roots.roots SET metadata=metadata||'{"root_reachable":false}'::jsonb`,
		"report_generation":   `UPDATE watched_roots.roots SET last_reported_at='2001-01-01'::timestamptz`,
		"row_generation":      `UPDATE projects.project_watched_root_registrations SET last_applied_at=last_applied_at+interval '1 second'`,
		"source_report":       `UPDATE projects.project_contract_registrations SET validation_report_json=jsonb_set(validation_report_json,'{watched_roots}','[]'::jsonb)`,
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			f := legacyOwnerFixture(t)
			plan, err := f.service.Plan(t.Context(), f.principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.root})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			f.service.owners[pc.DeclarationOwnerProjects] = legacyBeforeOwner{f.service.owners[pc.DeclarationOwnerProjects], func(call ActionCall) {
				if called {
					t.Fatal("owner called twice")
				}
				called = true
				if _, err := f.db.Exec(mutation); err != nil {
					t.Fatal(err)
				}
			}}
			result, err := f.service.Apply(t.Context(), f.principal, realApplyRequest(plan, f.root))
			if err == nil || !called || result.State == pc.DeclarationOperationSucceeded {
				t.Fatalf("changed predecessor accepted: %+v %v", result, err)
			}
			var schema string
			var count int
			if err = f.db.QueryRow(`SELECT contract_schema_version FROM projects.project_contract_registrations WHERE project_id=$1`, f.id).Scan(&schema); err != nil || schema != pc.ProjectSchemaV04 {
				t.Fatalf("registration escaped CAS: %s %v", schema, err)
			}
			if err = f.db.QueryRow(`SELECT count(*) FROM projects.declaration_owner_receipts`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed CAS wrote owner receipt")
			}
			if err = f.db.QueryRow(`SELECT count(*) FROM projects.declaration_watch_deliveries`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed CAS queued delivery")
			}
		})
	}
}

func TestDeclarationLegacyOwnerHeartbeatStablePostgres(t *testing.T) {
	f := legacyOwnerFixture(t)
	plan, err := f.service.Plan(t.Context(), f.principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.root})
	if err != nil {
		t.Fatal(err)
	}
	f.service.owners[pc.DeclarationOwnerProjects] = legacyBeforeOwner{f.service.owners[pc.DeclarationOwnerProjects], func(call ActionCall) {
		if _, err := f.db.Exec(`UPDATE watched_roots.roots SET last_reported_at=now()+interval '1 second',updated_at=now(),summary_json='{"scan_count":42,"bytes":9007199254740993}'::jsonb`); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := f.service.Apply(t.Context(), f.principal, realApplyRequest(plan, f.root))
	if failureCause(err) != "owner_pending" || result.State != pc.DeclarationOperationPartial {
		t.Fatalf("ordinary heartbeat invalidated predecessor: %+v %v", result, err)
	}
}

func TestDeclarationLegacyOwnerSetLockPostgres(t *testing.T) {
	f := legacyOwnerFixture(t)
	plan, err := f.service.Plan(t.Context(), f.principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.root})
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	f.service.owners[pc.DeclarationOwnerProjects] = realOwnerIntercept{Owner: f.service.owners[pc.DeclarationOwnerProjects], before: func(call ActionCall) ActionCall {
		call.Fence = callbackFence{Fence: call.Fence, before: func() {
			checked = true
			tx, err := f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`SET LOCAL lock_timeout='120ms'`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(`INSERT INTO projects.project_watched_root_registrations SELECT (jsonb_populate_record(NULL::projects.project_watched_root_registrations,to_jsonb(r)||jsonb_build_object('project_watched_root_registration_id',project_watched_root_registration_id||'_phantom','backend_root_key',backend_root_key||'_phantom','local_root_key',local_root_key||'_phantom'))).* FROM projects.project_watched_root_registrations r LIMIT 1`)
			if err == nil || !strings.Contains(err.Error(), "lock timeout") {
				t.Fatalf("project FK did not fence concurrent member insertion: %v", err)
			}
		}}
		return call
	}}
	result, err := f.service.Apply(t.Context(), f.principal, realApplyRequest(plan, f.root))
	if !checked || failureCause(err) != "owner_pending" || result.State != pc.DeclarationOperationPartial {
		t.Fatalf("set lock transition: %+v %v", result, err)
	}
	var count int
	if err = f.db.QueryRow(`SELECT count(*) FROM projects.project_watched_root_registrations`).Scan(&count); err != nil || count != 3 {
		t.Fatal("phantom member escaped")
	}
}
