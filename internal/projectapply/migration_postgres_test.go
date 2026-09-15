package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge/migrationread"
	aw "loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

type migrationFixture struct {
	db            *sql.DB
	url           string
	service       *Service
	resolver      *LocalResolver
	req           requestctx.Context
	principal     Principal
	id, box, root string
	raw           []byte
	analysis      pc.Analysis
	registration  projects.RegisterProjectContractResult
}

func migrationFixturePG(t *testing.T, mode string) migrationFixture {
	t.Helper()
	f := migrationFixture{}
	f.db, f.url = operationDatabase(t)
	if _, e := bootstrap.NewService(f.db).EnsureDevBootstrap(t.Context()); e != nil {
		t.Fatal(e)
	}
	var e error
	f.req, e = requestctx.ResolveBootstrap(t.Context(), f.db, "migration-fixture")
	if e != nil {
		t.Fatal(e)
	}
	f.principal = Principal{ActorID: f.req.ActorID, OriginNodeID: f.req.OriginNodeID}
	f.id = ids.NewProjectID()
	f.box = t.TempDir()
	f.root = filepath.Join(f.box, "project")
	migrationWrite(t, f.root, ".loom/project.yaml", fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject:\n  id: %s\n  slug: migration-fixture\n  name: Migration fixture\n  owner_node: main\n  status: active\n", f.id))
	rootFile := filepath.Join(f.root, pc.CanonicalRootContractPath)
	f.raw, _ = os.ReadFile(rootFile)
	switch mode {
	case "member", "state_root", "custom_state_root":
		f.raw = append(f.raw, []byte("facets: {repos: true}\n")...)
		tail := ""
		if mode == "state_root" {
			tail = "      state_root: .repo\n"
		}
		if mode == "custom_state_root" {
			tail = "      state_root: development/state\n"
		}
		migrationWrite(t, f.root, ".loom/contracts/repos.yaml", fmt.Sprintf("kind: loom.repos\nschema_version: repos.contract.v0.4\nrepos:\n  members:\n    - id: %s\n      key: api\n      path: api\n      role: primary\n%s", "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", tail))
		migrationWrite(t, f.root, "repos/api/.repo/preserved", "PRIVATE_PAYLOAD")
	case "backup", "disabled_backup", "dot", "custom_false":
		path := ".loom/contracts/backup.yaml"
		enabled := "true"
		if mode == "disabled_backup" || mode == "custom_false" {
			enabled = "false"
		}
		if mode == "custom_false" {
			path = "custom.yaml"
			f.raw = append(f.raw, []byte("facets: {backup_policy: false}\npolicies: {backup: custom.yaml}\n")...)
		} else {
			f.raw = append(f.raw, []byte("facets: {backup_policy: true}\n")...)
		}
		roots := "  roots:\n    - key: documents\n      path: data/docs\n"
		if mode == "dot" {
			roots = "  roots:\n    - key: notes\n      path: notes\n    - key: repos\n      path: repos\n    - key: controls\n      path: .\n      include: [loom.project.yaml, .loom/project.yaml]\n"
		}
		migrationWrite(t, f.root, path, "kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup:\n  enabled: "+enabled+"\n  defaults: {max_file_bytes: 4096, max_batch_bytes: 8192, max_pending_bytes: 16384}\n"+roots)
		for _, d := range []string{"data/docs", "notes", "repos"} {
			if e := os.MkdirAll(filepath.Join(f.root, d), 0700); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = os.WriteFile(rootFile, f.raw, 0600); e != nil {
		t.Fatal(e)
	}
	f.analysis = pc.Analyze(f.root)
	input, e := projectregistration.BuildInput(f.analysis, "migration-fixture")
	if e != nil {
		t.Fatalf("seed %s: %v %+v", mode, e, f.analysis.Report.Diagnostics)
	}
	f.registration, e = projects.NewService(f.db).RegisterProjectContract(t.Context(), f.req, input)
	if e != nil {
		t.Fatal(e)
	}
	f.resolver = NewLocalResolver(f.db, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: f.box}, nil })
	f.service = NewService(f.db, f.resolver, NewCurrentAuthority(f.db), nil)
	return f
}
func migrationWrite(t *testing.T, root, ref, raw string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(ref))
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
}
func migrationSQL(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, e := db.ExecContext(t.Context(), q, args...); e != nil {
		t.Fatal(e)
	}
}
func (f migrationFixture) assess(t *testing.T, hook func()) (pc.DeclarationMigrationAssessment, error) {
	t.Helper()
	return f.service.conversionAssessment(t.Context(), f.principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.id}, hook)
}
func migrationPureSnapshot(t *testing.T, db *sql.DB, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, e := db.Query(`SELECT table_schema,table_name FROM information_schema.tables WHERE table_type='BASE TABLE' AND table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1,2`)
	if e != nil {
		t.Fatal(e)
	}
	var tables [][2]string
	for rows.Next() {
		var v [2]string
		if e = rows.Scan(&v[0], &v[1]); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, v)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	for _, v := range tables {
		var hash string
		if e = db.QueryRow(fmt.Sprintf(`SELECT md5(COALESCE(string_agg(to_jsonb(r)::text,',' ORDER BY to_jsonb(r)::text),'')) FROM %q.%q r`, v[0], v[1])).Scan(&hash); e != nil {
			t.Fatal(e)
		}
		out[v[0]+"."+v[1]] = hash
	}
	if e = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		value := i.Mode().String()
		if i.Mode().IsRegular() {
			raw, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			value += hashBytes(raw)
		}
		out["file:"+p] = value
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return out
}
func migrationHasIssue(a pc.DeclarationMigrationAssessment, code string) bool {
	for _, v := range a.Issues {
		if v.Code == code {
			return true
		}
	}
	for _, v := range a.Preview.Issues {
		if v.CauseCode == code {
			return true
		}
	}
	return false
}
func migrationSeedWatches(t *testing.T, f migrationFixture) {
	t.Helper()
	for _, w := range f.analysis.Plan.WatchedRoots {
		kinds, _ := json.Marshal(w.SourceKinds)
		row, e := projects.NewService(f.db).UpsertProjectWatchedRootRegistration(t.Context(), f.req, projects.UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: f.registration.Detail.Registration.ProjectContractRegistrationID, ProjectID: f.id, NodeID: f.req.OriginNodeID, OwnerNodeKey: "main", LocalRootKey: w.Key, BackendRootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, SourceKinds: kinds, SafeRootKey: w.SafeRootKey, RootRelativePath: w.RootRelativePath, DisplayName: w.DisplayName, SyncMode: w.SyncMode, BackupMode: w.BackupMode, IndexMode: w.IndexMode, DeleteMode: w.DeleteMode, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CommandJSON: json.RawMessage(`[]`), ActivationStatus: "applied", Metadata: json.RawMessage(`{}`)})
		if e != nil {
			t.Fatal(e)
		}
		rid := "watched_root_migration-" + w.Key
		migrationSQL(t, f.db, `INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key,worker_key,display_name,safe_root_key,status,config_hash,config_json,last_reported_at) VALUES($1,$2,$3,$4,$5,$6,'active',$7,$8,clock_timestamp())`, rid, f.req.OriginNodeID, w.BackendRootKey, w.WorkerKey, w.DisplayName, w.SafeRootKey, w.ConfigHash, w.ConfigJSON)
		migrationSQL(t, f.db, `UPDATE projects.project_watched_root_registrations SET watched_root_id=$1 WHERE project_watched_root_registration_id=$2`, rid, row.ProjectWatchedRootRegistrationID)
	}
}
func TestDeclarationMigrationOwnersPostgres(t *testing.T) {
	for _, mode := range []string{"plain", "member", "state_root", "backup", "disabled_backup", "dot", "custom_false"} {
		t.Run(mode, func(t *testing.T) {
			f := migrationFixturePG(t, mode)
			migrationSeedWatches(t, f)
			before := migrationPureSnapshot(t, f.db, f.root)
			a, e := f.assess(t, nil)
			if e != nil {
				t.Fatal(e)
			}
			want := "eligible"
			if mode == "custom_false" {
				want = "refused"
			}
			if a.State != want {
				t.Fatalf("state %s want %s: %+v %+v", a.State, want, a.Issues, a.Preview.Issues)
			}
			if mode == "state_root" && (a.Preview.Candidate == nil || len(a.Preview.Identity.Members) != 1 || a.Preview.Identity.Members[0].StateRoot != ".repo") {
				t.Fatal("matching registered state_root intent lost")
			}
			if mode == "backup" || mode == "disabled_backup" || mode == "dot" {
				if len(a.Preview.BeforeRoots) != len(f.analysis.Plan.WatchedRoots) || len(a.Preview.AfterRoots) != len(a.Preview.BeforeRoots) {
					t.Fatal("root set changed")
				}
				for i, w := range a.Preview.BeforeRoots {
					if !reflect.DeepEqual(w, a.Preview.AfterRoots[i]) {
						t.Fatal("full watch parity changed")
					}
				}
			}
			if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
				t.Fatal("assessment mutated state")
			}
		})
	}
}
func TestDeclarationMigrationDriftPostgres(t *testing.T) {
	cases := []string{"source", "alternative", "same_size_inode", "parent", "registry", "node", "owner", "location", "actor", "origin", "member", "notes", "service", "external", "row_error", "invalid_claim", "equal_dual", "conflict_dual", "mixed_dual"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			mode := "plain"
			if name == "member" {
				mode = "member"
			}
			f := migrationFixturePG(t, mode)
			hook := func() {}
			writeRoot := func(raw string) { migrationWrite(t, f.root, pc.CanonicalRootContractPath, raw) }
			switch name {
			case "source":
				hook = func() { writeRoot(strings.Replace(string(f.raw), "Migration fixture", "Migration changed", 1)) }
			case "alternative":
				hook = func() { migrationWrite(t, f.root, pc.LegacyRootContractPath, string(f.raw)) }
			case "same_size_inode":
				hook = func() {
					p := filepath.Join(f.root, pc.CanonicalRootContractPath)
					if e := os.Rename(p, p+".old"); e != nil {
						t.Fatal(e)
					}
					writeRoot(string(f.raw))
				}
			case "parent":
				hook = func() {
					if e := os.Rename(filepath.Join(f.root, ".loom"), filepath.Join(f.root, "old")); e != nil {
						t.Fatal(e)
					}
					writeRoot(string(f.raw))
				}
			case "registry":
				hook = func() {
					migrationSQL(t, f.db, `UPDATE projects.project_contract_registrations SET metadata=metadata||'{"drift":true}' WHERE project_id=$1`, f.id)
				}
			case "node":
				hook = func() {
					migrationSQL(t, f.db, `UPDATE nodes.nodes SET node_key='changed' WHERE node_id=$1`, f.req.OriginNodeID)
				}
			case "owner":
				hook = func() {
					migrationSQL(t, f.db, `UPDATE projects.projects SET home_node_id=NULL WHERE project_id=$1`, f.id)
				}
			case "location":
				hook = func() {
					f.resolver.Config = func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: t.TempDir()}, nil }
				}
			case "actor":
				hook = func() {
					migrationSQL(t, f.db, `UPDATE identity.actors SET status='disabled' WHERE actor_id=$1`, f.req.ActorID)
				}
			case "origin":
				f.principal.OriginNodeID = ids.NewNodeID()
			case "member":
				hook = func() {
					migrationSQL(t, f.db, `UPDATE projects.project_repository_memberships SET member_path='changed' WHERE project_id=$1`, f.id)
				}
			case "notes":
				migrationSQL(t, f.db, `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,node_id,node_key,project_id,backend_root_key,status) VALUES('notes_source_root_migration','project_notes',$1,'main',$2,'private-root','disabled')`, f.req.OriginNodeID, f.id)
			case "service":
				migrationSQL(t, f.db, `INSERT INTO capabilities.providers(provider_id,provider_key,compact_address,provider_type,display_name,node_id,scope_id,status,created_by_actor_id) SELECT 'prov_migration-service','migration-service','migration-service@main','service','PRIVATE_SERVICE',$1,project_scope_id,'disabled',created_by_actor_id FROM projects.projects WHERE project_id=$2`, f.req.OriginNodeID, f.id)
			case "external":
				migrationSQL(t, f.db, `INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key,worker_key,display_name,safe_root_key,status,config_hash,config_json,last_reported_at) VALUES('watched_root_external',$1,'PRIVATE_OTHER_PROJECT','external','PRIVATE_DISPLAY','box','active','hash','{}',now())`, f.req.OriginNodeID)
			case "row_error":
				migrationSQL(t, f.db, `ALTER TABLE knowledge.notes_source_roots RENAME COLUMN status TO status_fixture_unavailable`)
			case "invalid_claim":
				writeRoot(strings.Replace(string(f.raw), "kind: loom.project", "kind: /Users/SECRET_MARKER", 1))
			case "equal_dual":
				migrationWrite(t, f.root, pc.LegacyRootContractPath, "# second byte identity\n"+string(f.raw))
			case "conflict_dual":
				migrationWrite(t, f.root, pc.LegacyRootContractPath, strings.Replace(string(f.raw), "Migration fixture", "Other fixture", 1))
			case "mixed_dual":
				migrationWrite(t, f.root, pc.LegacyRootContractPath, strings.Replace(string(f.raw), "v0.4", "v0.5", 1)+"resources: {}\n")
			}
			a, e := f.assess(t, hook)
			if name == "equal_dual" {
				if e != nil || a.State != "eligible" {
					t.Fatalf("equal dual %s %v %+v %+v", a.State, e, a.Issues, a.Preview.Issues)
				}
				if len(a.Preview.Retention.Sources) != 2 {
					t.Fatal("dual identity lost")
				}
			} else if e == nil && (a.State == "eligible" || a.Preview.Candidate != nil) {
				t.Fatalf("%s returned candidate", name)
			}
			raw, _ := json.Marshal(a)
			for _, marker := range []string{f.root, "SECRET_MARKER", "PRIVATE_OTHER_PROJECT", "PRIVATE_DISPLAY", "PRIVATE_SERVICE"} {
				if strings.Contains(string(raw), marker) {
					t.Fatalf("private marker escaped: %s", name)
				}
			}
		})
	}
}
func TestDeclarationMigrationReadOnlyRolePostgres(t *testing.T) {
	f := migrationFixturePG(t, "plain")
	role := "migration_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	migrationSQL(t, f.db, `CREATE ROLE "`+role+`" LOGIN`)
	t.Cleanup(func() {
		if _, e := f.db.ExecContext(context.Background(), `DROP OWNED BY "`+role+`"; DROP ROLE "`+role+`"`); e != nil {
			t.Error(e)
		}
	})
	rows, e := f.db.Query(`SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('pg_catalog','information_schema') AND schema_name NOT LIKE 'pg_%'`)
	if e != nil {
		t.Fatal(e)
	}
	var schemas []string
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			t.Fatal(e)
		}
		schemas = append(schemas, s)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	for _, s := range schemas {
		migrationSQL(t, f.db, fmt.Sprintf(`GRANT USAGE ON SCHEMA %q TO %q; GRANT SELECT ON ALL TABLES IN SCHEMA %q TO %q`, s, role, s, role))
	}
	u, _ := url.Parse(f.url)
	u.User = url.User(role)
	readDB, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer readDB.Close()
	f.resolver.DB = readDB
	f.service.auth = NewCurrentAuthority(readDB)
	if _, e = readDB.Exec(`UPDATE projects.projects SET name=name`); e == nil {
		t.Fatal("fixture role can mutate")
	}
	before := migrationPureSnapshot(t, f.db, f.root)
	a, e := f.assess(t, nil)
	if e != nil || a.State != "eligible" {
		t.Fatalf("read-only: %s %v %+v", a.State, e, a.Issues)
	}
	if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
		t.Fatal("read-only assessment wrote state")
	}
}

func TestDeclarationMigrationOwnerBoundsPostgres(t *testing.T) {
	for _, family := range []string{"roots", "box", "notes", "services", "watches", "facets"} {
		t.Run(family, func(t *testing.T) {
			f := migrationFixturePG(t, "plain")
			switch family {
			case "roots":
				migrationSQL(t, f.db, `INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key) SELECT 'watched_root_bound_'||i,$1,'bound-'||i FROM generate_series(1,4097)i`, f.req.OriginNodeID)
			case "box":
				migrationSQL(t, f.db, `INSERT INTO box.watch_root_registrations(box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,area_key,local_root_key,backend_root_key,activation_status) SELECT 'box_watch_root_registration_bound_'||i,'box_bound_'||i,'/PRIVATE_BOX_'||i,'/PRIVATE_CONTRACT',$1,'notes','bound-'||i,'bound-'||i,'stale' FROM generate_series(1,4097)i`, f.req.OriginNodeID)
			case "notes":
				migrationSQL(t, f.db, `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,node_id,node_key,project_id,backend_root_key,status) SELECT 'notes_source_root_bound_'||i,'project_notes',$1,'main',$2,'bound-'||i,'disabled' FROM generate_series(1,4097)i`, f.req.OriginNodeID, f.id)
			case "services":
				migrationSQL(t, f.db, `INSERT INTO capabilities.providers(provider_id,provider_key,compact_address,provider_type,display_name,node_id,scope_id,status,created_by_actor_id) SELECT 'prov_bound_'||i,'bound-'||i,'bound-'||i||'@main','service','PRIVATE_SERVICE',$1,p.project_scope_id,'disabled',p.created_by_actor_id FROM projects.projects p CROSS JOIN generate_series(1,4097)i WHERE project_id=$2`, f.req.OriginNodeID, f.id)
			case "watches":
				migrationSQL(t, f.db, `INSERT INTO projects.project_watched_root_registrations(project_watched_root_registration_id,project_contract_registration_id,project_id,node_id,owner_node_key,local_root_key,backend_root_key,activation_status) SELECT 'project_watched_root_registration_bound_'||i,project_contract_registration_id,project_id,$1,'main','bound-'||i,'bound-'||i,'stale' FROM projects.project_contract_registrations CROSS JOIN generate_series(1,4097)i WHERE project_id=$2`, f.req.OriginNodeID, f.id)
			case "facets":
				migrationSQL(t, f.db, `INSERT INTO projects.project_contract_facets(project_contract_facet_id,project_contract_registration_id,project_id,facet_key,enabled,present,facet_status) SELECT 'project_contract_facet_bound_'||i,project_contract_registration_id,project_id,'bound-'||i,false,false,'disabled' FROM projects.project_contract_registrations CROSS JOIN generate_series(1,501)i WHERE project_id=$1`, f.id)
			}
			a, e := f.assess(t, nil)
			if e != nil {
				t.Fatal(e)
			}
			if a.State != "incomplete" || a.Preview.Candidate != nil {
				t.Fatalf("truncated %s escaped: %s", family, a.State)
			}
			raw, _ := json.Marshal(a)
			if strings.Contains(string(raw), "PRIVATE_") {
				t.Fatal("owner bound leaked private rows")
			}
		})
	}
}
func TestDeclarationMigrationWatchGenerationPostgres(t *testing.T) {
	for _, name := range []string{"pending", "wrong_worker", "missing_report", "config_conflict", "missing_watch", "wrong_node", "extra_watch", "box_pending"} {
		t.Run(name, func(t *testing.T) {
			f := migrationFixturePG(t, "backup")
			migrationSeedWatches(t, f)
			switch name {
			case "pending":
				migrationSQL(t, f.db, `UPDATE projects.project_watched_root_registrations SET activation_status='pending_agent_apply'`)
			case "wrong_worker":
				migrationSQL(t, f.db, `UPDATE watched_roots.roots SET worker_key='wrong'`)
			case "missing_report":
				migrationSQL(t, f.db, `UPDATE projects.project_watched_root_registrations SET watched_root_id=NULL`)
			case "config_conflict":
				migrationSQL(t, f.db, `UPDATE watched_roots.roots SET config_json=jsonb_set(config_json,'{backup_policy,max_file_bytes}','8192')`)
			case "missing_watch":
				migrationSQL(t, f.db, `DELETE FROM projects.project_watched_root_registrations`)
			case "wrong_node":
				migrationSQL(t, f.db, `UPDATE projects.project_watched_root_registrations SET owner_node_key='elsewhere'`)
			case "extra_watch":
				migrationSQL(t, f.db, `UPDATE projects.project_watched_root_registrations SET local_root_key='extra'`)
			case "box_pending":
				migrationSQL(t, f.db, `INSERT INTO box.watch_root_registrations(box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,area_key,local_root_key,backend_root_key,activation_status) VALUES('box_watch_root_registration_pending','box_fixture',$1,'PRIVATE_BOX_CONTRACT',$2,'notes','extra','extra','pending_agent_apply')`, f.box, f.req.OriginNodeID)
			}
			before := migrationPureSnapshot(t, f.db, f.root)
			a, e := f.assess(t, nil)
			if e != nil {
				t.Fatal(e)
			}
			if a.State == "eligible" || a.Preview.Candidate != nil {
				t.Fatalf("%s admitted", name)
			}
			if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.root)) {
				t.Fatal("generation check wrote state")
			}
		})
	}
}

func TestDeclarationMigrationNotesCustodyPostgres(t *testing.T) {
	f := migrationFixturePG(t, "plain")
	for _, status := range []string{"active", "disabled", "stale", "deleted", "blocked"} {
		migrationSQL(t, f.db, `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,node_id,node_key,project_id,backend_root_key,status,authorization_metadata,metadata,source_path) VALUES($1,'project_notes',$2,'main',$3,$4,$5,'{"private":"AUTH_MARKER"}','{"registration_metadata":{"knowledge_source":{"enabled":false}}}', '/PRIVATE_NOTES_PATH')`, "notes_source_root_"+status, f.req.OriginNodeID, f.id, "root-"+status, status)
	}
	storage := t.TempDir()
	migrationWrite(t, f.box, "Topics/migration-custody/fixture.txt", "owned fixture")
	if e := os.MkdirAll(filepath.Join(storage, "archive/topics"), 0700); e != nil {
		t.Fatal(e)
	}
	catalog := storagecatalog.NewService(f.db)
	owner := storagearchive.WorkspaceMoveService{Roots: storagearchive.TrustedWorkspaceRoots{BoxRoot: f.box, StorageRoot: storage}, Catalog: catalog, Journal: catalog, ManifestKeyID: "migration.fixture", ManifestKey: func(context.Context, string) ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil }}
	plan, e := owner.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_migration_custody", Slug: "migration-custody", ActorID: f.req.ActorID, Reason: "Disposable migration reader fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = owner.ApplyArchive(t.Context(), plan, plan.PlanDigest); e != nil {
		t.Fatal(e)
	}
	var eventID string
	if e = f.db.QueryRow(`SELECT workspace_lifecycle_event_id FROM storage.workspace_lifecycle_events WHERE workspace_archive_operation_id=$1`, plan.OperationID).Scan(&eventID); e != nil {
		t.Fatal(e)
	}
	migrationSQL(t, f.db, `INSERT INTO knowledge.knowledge_objects(knowledge_object_id,notes_source_root_id,relative_path,file_class,source_path) VALUES('knowledge_object_migration','notes_source_root_disabled','fixture.txt','text','/PRIVATE_ORIGINAL')`)
	digest := "sha256:" + strings.Repeat("a", 64)
	migrationSQL(t, f.db, `INSERT INTO knowledge.notes_custody_transitions(knowledge_object_id,workspace_lifecycle_event_id,archive_operation_id,original_path,canonical_path,workspace_relative_path,manifest_digest,transition_digest) VALUES('knowledge_object_migration',$1,$2,'/PRIVATE_ORIGINAL','/PRIVATE_CANONICAL','fixture.txt',$3,$3)`, eventID, plan.OperationID, digest)
	migrationSQL(t, f.db, `INSERT INTO knowledge.notes_current_custody VALUES('knowledge_object_migration',$1)`, eventID)
	before := migrationPureSnapshot(t, f.db, f.box)
	tx, e := f.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		t.Fatal(e)
	}
	observations, e := migrationread.ReadDeclarationMigrationKnowledgeTx(t.Context(), tx, f.id, f.req.OriginNodeID)
	tx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	if observations.Completeness != "complete" || len(observations.Roots) != 5 || len(observations.Custody) != 1 || !observations.Custody[0].Current || observations.Custody[0].ManifestDigest != digest || observations.Custody[0].CanonicalPath != "/PRIVATE_CANONICAL" {
		t.Fatalf("custody dropped: %+v", observations)
	}
	a, e := f.assess(t, nil)
	if e != nil || a.State != "incomplete" || !migrationHasIssue(a, "notes_behavior_unmapped") {
		t.Fatalf("custody qualification: %s %v %+v", a.State, e, a.Issues)
	}
	raw, _ := json.Marshal(a)
	for _, marker := range []string{"PRIVATE_", "AUTH_MARKER", eventID, plan.OperationID} {
		if strings.Contains(string(raw), marker) {
			t.Fatal("private custody leaked")
		}
	}
	if !reflect.DeepEqual(before, migrationPureSnapshot(t, f.db, f.box)) {
		t.Fatal("reader mutated custody")
	}
}

func TestDeclarationMigrationAdversarialPostgres(t *testing.T) {
	t.Run("facet_metadata_drift", func(t *testing.T) {
		f := migrationFixturePG(t, "disabled_backup")
		migrationSeedWatches(t, f)
		before, e := f.assess(t, nil)
		if e != nil || before.State != "eligible" {
			t.Fatalf("control %s %v", before.State, e)
		}
		a, e := f.assess(t, func() {
			migrationSQL(t, f.db, `UPDATE projects.project_contract_facets SET metadata=metadata||'{"pending_owner_generation":2}' WHERE project_id=$1`, f.id)
		})
		if e == nil && a.State == "eligible" {
			t.Fatal("facet owner metadata drift omitted from observation digest")
		}
	})
	t.Run("box_claimed_anchor_differs_from_configured_anchor", func(t *testing.T) {
		f := migrationFixturePG(t, "plain")
		other := t.TempDir()
		cfg := aw.NormalizeRootConfig(aw.RootConfig{RootKey: "box-root", SafeRootKey: "box", RootRelativePath: "."})
		cfg, e := aw.ValidatePortableRootConfig(cfg)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(cfg)
		hash := aw.ConfigHash(cfg)
		migrationSQL(t, f.db, `INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key,worker_key,safe_root_key,status,config_hash,config_json,last_reported_at) VALUES('watched_root_box_anchor',$1,'box-root','worker','box','active',$2,$3,clock_timestamp())`, f.req.OriginNodeID, hash, raw)
		migrationSQL(t, f.db, `INSERT INTO box.watch_root_registrations(box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,owner_node_key,area_key,local_root_key,backend_root_key,worker_key,safe_root_key,root_relative_path,config_hash,config_json,watched_root_id,activation_status,last_applied_at,desired_revision,applied_revision,desired_config_hash,applied_config_hash) VALUES('box_watch_root_registration_anchor','box_anchor',$1,'PRIVATE_BOX_CONTRACT',$2,'main','notes','box-root','box-root','worker','box','.',$3,$4,'watched_root_box_anchor','applied',now()-interval '1 second',1,1,$3,$3)`, other, f.req.OriginNodeID, hash, raw)
		a, e := f.assess(t, nil)
		if e != nil {
			t.Fatal(e)
		}
		if a.State == "eligible" {
			t.Fatal("forged physical Box anchor hid overlap with configured Box")
		}
	})
}

func TestDeclarationMigrationExternalBindingDriftPostgres(t *testing.T) {
	for _, name := range []string{"unchanged", "removed", "replaced"} {
		t.Run(name, func(t *testing.T) {
			f := migrationFixturePG(t, "plain")
			migrationWrite(t, f.box, "external/fixture.txt", "owned external fixture")
			cfg := aw.NormalizeRootConfig(aw.RootConfig{RootKey: "box-root", SafeRootKey: "box", RootRelativePath: "external"})
			cfg, e := aw.ValidatePortableRootConfig(cfg)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(cfg)
			hash := aw.ConfigHash(cfg)
			migrationSQL(t, f.db, `INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key,worker_key,safe_root_key,status,config_hash,config_json,last_reported_at) VALUES('watched_root_box_external',$1,'box-root','worker','box','active',$2,$3,clock_timestamp())`, f.req.OriginNodeID, hash, raw)
			migrationSQL(t, f.db, `INSERT INTO box.watch_root_registrations(box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,owner_node_key,area_key,local_root_key,backend_root_key,worker_key,safe_root_key,root_relative_path,config_hash,config_json,watched_root_id,activation_status,last_applied_at,desired_revision,applied_revision,desired_config_hash,applied_config_hash) VALUES('box_watch_root_registration_external','box_external',$1,'PRIVATE_EXTERNAL_CONTRACT',$2,'main','notes','box-root','box-root','worker','box','external',$3,$4,'watched_root_box_external','applied',now()-interval '1 second',1,1,$3,$3)`, f.box, f.req.OriginNodeID, hash, raw)
			before, e := f.assess(t, nil)
			if e != nil || before.State != "eligible" {
				t.Fatalf("disjoint control: %s %v %+v", before.State, e, before.Issues)
			}
			a, e := f.assess(t, func() {
				if name != "unchanged" {
					if e := os.Rename(filepath.Join(f.box, "external"), filepath.Join(f.box, "external-old")); e != nil {
						t.Fatal(e)
					}
				}
				if name == "replaced" {
					migrationWrite(t, f.box, "external/fixture.txt", "owned replacement")
				}
			})
			if e != nil {
				t.Fatal(e)
			}
			if name == "unchanged" {
				if a.State != "eligible" || a.SnapshotRevision != before.SnapshotRevision {
					t.Fatal("unchanged disjoint evidence unstable")
				}
			} else if a.State != "incomplete" || a.Preview.Candidate != nil {
				t.Fatalf("changed external exclusion returned %s", a.State)
			}
			data, _ := json.Marshal(a)
			if strings.Contains(string(data), "PRIVATE_EXTERNAL") || strings.Contains(string(data), f.box) {
				t.Fatal("external physical evidence leaked")
			}
		})
	}
}

func TestDeclarationMigrationOwnerRowErrorPostgres(t *testing.T) {
	f := migrationFixturePG(t, "plain")
	// A fixture view uses the unchanged root column contract but its row expression
	// fails in PostgreSQL. No failed scan/rows.Err path may assert complete absence.
	migrationSQL(t, f.db, `INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key) VALUES('watched_root_row_error',$1,'error')`, f.req.OriginNodeID)
	migrationSQL(t, f.db, `ALTER TABLE watched_roots.roots RENAME TO migration_fixture_roots;
 CREATE FUNCTION watched_roots.migration_fixture_error(text) RETURNS text LANGUAGE plpgsql AS 'BEGIN RAISE EXCEPTION ''PRIVATE_SQL_MARKER''; END';
 CREATE VIEW watched_roots.roots AS SELECT watched_root_id,node_id,root_key,worker_key,display_name,safe_root_key,status,config_hash,config_json,summary_json,watched_roots.migration_fixture_error(metadata::text)::jsonb AS metadata,last_reported_at,created_at,updated_at FROM watched_roots.migration_fixture_roots`)
	a, e := f.assess(t, nil)
	if e == nil || a.Preview.Candidate != nil {
		t.Fatal("row failure asserted complete snapshot")
	}
	if strings.Contains(e.Error(), "PRIVATE_SQL_MARKER") {
		t.Fatal("raw row error escaped service")
	}
}
