package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

func realOwnerFixture(t *testing.T, count int) (*Service, *LocalResolver, Principal, string, *sql.DB) {
	t.Helper()
	database, _ := operationDatabase(t)
	if _, err := bootstrap.NewService(database).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), database, "declaration-owner-test")
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID}
	box := t.TempDir()
	root := filepath.Join(box, "project")
	if err = os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	resources := map[string]any{}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("repo%d", i)
		if err = os.MkdirAll(filepath.Join(root, key), 0700); err != nil {
			t.Fatal(err)
		}
		resources[key] = map[string]any{"kind": "repository", "repository": map[string]any{"path": key, "role": "component"}}
	}
	id := ids.NewProjectID()
	raw, _ := json.Marshal(map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV05, "project": map[string]any{"id": id, "slug": "declaration-" + strings.ToLower(id[8:]), "name": "Declaration", "owner_node": "main", "status": "active"}, "resources": resources})
	if err = os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	resolver := NewLocalResolver(database, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: box}, nil })
	owner := ProjectsOwner{Projects: projects.NewService(database), Resolver: resolver}
	service := NewService(database, resolver, NewCurrentAuthority(database), map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: owner})
	return service, resolver, p, root, database
}
func realApplyRequest(plan pc.DeclarationPlan, root string) pc.DeclarationApplyRequest {
	return pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root, PlanID: plan.PlanID, Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, IdempotencyKey: "real-owner"}
}
func TestDeclarationRealRegistrationOwnerPostgres(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("%d_idless", count), func(t *testing.T) {
			service, resolver, p, root, database := realOwnerFixture(t, count)
			ctx := context.Background()
			request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root}
			plan, err := service.Plan(ctx, p, request)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Basis.Actions) != 1 || len(plan.Basis.Bindings) != count {
				t.Fatalf("plan=%+v", plan)
			}
			for _, b := range plan.Basis.Bindings {
				if b.RepositoryID != "" {
					t.Fatal("planned ID allocation")
				}
			}
			var rows int
			if err = database.QueryRow(`SELECT count(*) FROM projects.projects`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("plan wrote project rows: %d %v", rows, err)
			}
			again, err := service.Plan(ctx, p, request)
			if err != nil || again.PlanID != plan.PlanID {
				t.Fatalf("unstable plan: %v", err)
			}
			apply := realApplyRequest(plan, root)
			result, err := service.Apply(ctx, p, apply)
			if err != nil || result.State != pc.DeclarationOperationSucceeded {
				t.Fatalf("apply=%+v err=%v", result, err)
			}
			if err = database.QueryRow(`SELECT count(*) FROM projects.declaration_owner_receipts`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("owner receipts=%d %v", rows, err)
			}
			state, err := projects.NewService(database).GetDeclarationProjectState(ctx, plan.Basis.Target.ProjectID)
			if err != nil || len(state.Repositories) != count {
				t.Fatalf("state=%+v %v", state, err)
			}
			var events int
			if err = database.QueryRow(`SELECT count(*) FROM events.events`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			restarted := NewService(database, resolver, NewCurrentAuthority(database), map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: ProjectsOwner{Resolver: resolver, Projects: projects.NewService(database)}})
			apply.OperationID = result.OperationID
			replay, err := restarted.Apply(ctx, p, apply)
			if err != nil || replay.OperationID != result.OperationID || replay.State != pc.DeclarationOperationSucceeded {
				t.Fatalf("restart=%+v err=%v", replay, err)
			}
			if err = database.QueryRow(`SELECT count(*) FROM events.events`).Scan(&rows); err != nil || rows != events {
				t.Fatalf("replay events=%d want%d %v", rows, events, err)
			}
		})
	}
}

type realOwnerIntercept struct {
	Owner
	before func(ActionCall) ActionCall
	after  func(ActionCall)
}

func (o realOwnerIntercept) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	if o.before != nil {
		call = o.before(call)
	}
	result, err := o.Owner.Apply(ctx, call)
	if err == nil && o.after != nil {
		o.after(call)
	}
	return result, err
}

type callbackFence struct {
	Fence
	before func()
}

func (f callbackFence) Check(ctx context.Context) error { f.before(); return f.Fence.Check(ctx) }
func TestDeclarationRealRegistrationRollbackAndRecoveryPostgres(t *testing.T) {
	for _, scenario := range []string{"source_before_commit", "owner_cas", "revoke_before_owner", "lost_coordinator_after_commit", "receipt_immutable"} {
		t.Run(scenario, func(t *testing.T) {
			service, resolver, p, root, database := realOwnerFixture(t, 2)
			ctx := context.Background()
			plan, err := service.Plan(ctx, p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
			if err != nil {
				t.Fatal(err)
			}
			original := service.owners[pc.DeclarationOwnerProjects]
			wrapped := realOwnerIntercept{Owner: original}
			switch scenario {
			case "source_before_commit":
				wrapped.before = func(call ActionCall) ActionCall {
					call.Fence = callbackFence{Fence: call.Fence, before: func() {
						path := filepath.Join(root, pc.CanonicalRootContractPath)
						raw, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
							t.Fatal(err)
						}
					}}
					return call
				}
			case "owner_cas":
				wrapped.before = func(call ActionCall) ActionCall {
					x, err := resolver.load(ctx, p, root, p.OriginNodeID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = projects.NewService(database).RegisterProjectContract(ctx, declarationRequest(p), x.Input); err != nil {
						t.Fatal(err)
					}
					return call
				}
			case "revoke_before_owner":
				wrapped.before = func(call ActionCall) ActionCall {
					if _, err = database.Exec(`UPDATE identity.actor_node_authorizations SET authorization_level=2 WHERE actor_id=$1`, p.ActorID); err != nil {
						t.Fatal(err)
					}
					return call
				}
			case "lost_coordinator_after_commit":
				wrapped.after = func(call ActionCall) {
					lock, ok := call.Fence.(*projectLock)
					if !ok {
						t.Fatal("missing real project lock")
					}
					var pid int
					if err = lock.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						t.Fatal(err)
					}
					if _, err = database.Exec(`SELECT pg_terminate_backend($1)`, pid); err != nil {
						t.Fatal(err)
					}
				}
			}
			service.owners[pc.DeclarationOwnerProjects] = wrapped
			request := realApplyRequest(plan, root)
			result, applyErr := service.Apply(ctx, p, request)
			var receipts, projectsCount, repoCount int
			for query, dest := range map[string]*int{`SELECT count(*) FROM projects.declaration_owner_receipts`: &receipts, `SELECT count(*) FROM projects.projects`: &projectsCount, `SELECT count(*) FROM projects.repositories`: &repoCount} {
				if err = database.QueryRow(query).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "source_before_commit", "revoke_before_owner":
				if applyErr == nil || receipts != 0 || projectsCount != 0 || repoCount != 0 {
					t.Fatalf("partial transaction escaped: receipts=%d projects=%d repos=%d err=%v", receipts, projectsCount, repoCount, applyErr)
				}
			case "owner_cas":
				if applyErr == nil || receipts != 0 || projectsCount != 1 || repoCount != 2 {
					t.Fatalf("CAS did not preserve external registration: %d %d %d %v", receipts, projectsCount, repoCount, applyErr)
				}
			case "lost_coordinator_after_commit":
				if applyErr == nil || receipts != 1 || result.OperationID == "" {
					t.Fatalf("lost commit evidence: %+v %d %v", result, receipts, applyErr)
				}
				request.OperationID = result.OperationID
				service.owners[pc.DeclarationOwnerProjects] = original
				recovered, err := service.Apply(ctx, p, request)
				if err != nil || recovered.State != pc.DeclarationOperationSucceeded {
					t.Fatalf("recover exact owner token: %+v %v", recovered, err)
				}
				if err = database.QueryRow(`SELECT count(*) FROM projects.declaration_owner_receipts`).Scan(&receipts); err != nil || receipts != 1 {
					t.Fatalf("duplicate owner evidence: %d %v", receipts, err)
				}
			case "receipt_immutable":
				if applyErr != nil {
					t.Fatal(applyErr)
				}
				for _, query := range []string{`UPDATE projects.declaration_owner_receipts SET result='{}'`, `DELETE FROM projects.declaration_owner_receipts`} {
					if _, err = database.Exec(query); err == nil {
						t.Fatal("owner receipt changed")
					}
				}
				if _, err = database.Exec(`INSERT INTO projects.declaration_owner_receipts SELECT project_id,owner,token,input_hash,operation_id,action_id,actor_id,origin_node_id,target_node_id,jsonb_set(result,'{after,repositories,repo0}','9007199254740993'::jsonb),created_at FROM projects.declaration_owner_receipts`); err == nil || !strings.Contains(err.Error(), "invalid declaration owner repository identity") {
					t.Fatalf("numeric receipt did not reach exact identity-preserving value guard: %v", err)
				}
			}
		})
	}
}
