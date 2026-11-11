package loomdapp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
)

func workspaceDependenciesFixture(t *testing.T, resources map[string]any) (workspacePathProject, string) {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{".loom", "code", "notes", "loose", "loose/nested", "code2"} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"kind": "loom.project", "schema_version": pc.ProjectSchemaV06,
		"project":   map[string]string{"id": "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", "slug": "dep-test", "name": "Dependency Test", "owner_node": "main", "status": "active"},
		"resources": resources,
	})
	if err := os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := pc.Analyze(root)
	in, err := projectregistration.BuildInput(a, "workspace.dependencies.test")
	if err != nil {
		t.Fatalf("real compiler fixture: %v; diagnostics=%+v", err, a.Report.Diagnostics)
	}
	p := workspacePathProject{ID: in.Project.ID, Scope: "scope_project", Slug: in.Project.Slug, Status: "active", Node: "node_main", NodeKey: "main",
		Registration: projects.ProjectContractRegistration{ProjectID: in.Project.ID, ProjectContractRegistrationID: "registration_fixture", ProjectRoot: root,
			ContractPath: in.ContractPath, ContractHash: in.ContractHash, ContractSchemaVersion: in.ContractSchemaVersion, Contract: in.Contract,
			RegistrationPlan: in.RegistrationPlan, RegistrationStatus: "registered", ActivationStatus: "inactive", RegistrationRevision: 7}}
	return p, root
}

func workspaceDependenciesRepo() map[string]any {
	return map[string]any{"code": map[string]any{"kind": "repository", "repository": map[string]string{"path": "code", "role": "component"}}}
}

func TestWorkspacePathDependenciesEligibleAndRecovery(t *testing.T) {
	for _, resources := range []map[string]any{{}, workspaceDependenciesRepo()} {
		p, root := workspaceDependenciesFixture(t, resources)
		store := &workspaceDependencyDB{t: t, p: p, roots: map[string][][]driver.Value{}}
		if len(resources) > 0 {
			store.roots["projects.project_repository_memberships"] = [][]driver.Value{{"repo_saved", "archived", "code", "code"}}
		}
		database := sql.OpenDB(workspaceDependencyConnector{store})
		t.Cleanup(func() { _ = database.Close() })
		callback := workspacePathDependencies(database, projects.NewService(database))
		before, err := callback(t.Context(), p.ID, "relocate_project", root, root)
		if err != nil || !strings.HasPrefix(before, "sha256:") {
			t.Fatalf("eligible relocation: %q %v", before, err)
		}
		child, err := callback(t.Context(), p.ID, "retain_child", filepath.Join(root, "loose"), root)
		if err != nil || child == before {
			t.Fatalf("eligible child: %q %v", child, err)
		}
		// The logical registry still names the predecessor after physical rename.
		moved := root + "-recovered"
		if err := os.Rename(root, moved); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Rename(moved, root) })
		after, err := callback(t.Context(), p.ID, "relocate_project", root, moved)
		if err != nil || before != after {
			t.Fatalf("physical recovery changed logical digest: %s -> %s: %v", before, after, err)
		}
		if store.commits != 3 || store.readTransactions != 3 {
			t.Fatal("callback did not use complete read-only snapshots")
		}
	}
}

func TestWorkspacePathDependenciesRuntimeOwnersFailClosed(t *testing.T) {
	p, root := workspaceDependenciesFixture(t, workspaceDependenciesRepo())
	for _, owner := range workspacePathRuntimeQueries {
		t.Run(owner.owner, func(t *testing.T) {
			store := &workspaceDependencyDB{t: t, p: p, hold: owner.owner, roots: map[string][][]driver.Value{}}
			database := sql.OpenDB(workspaceDependencyConnector{store})
			defer database.Close()
			for _, kind := range []string{"relocate_project", "retain_child"} {
				source := root
				if kind == "retain_child" {
					source = filepath.Join(root, "loose")
				}
				_, err := NewWorkspacePathDependencies(database)(t.Context(), p.ID, kind, source, root)
				var dep *WorkspacePathDependencyError
				if !errors.As(err, &dep) || dep.Owner != owner.owner || dep.Key != "disabled-owner" || !strings.Contains(dep.Reason, "disabled") {
					t.Fatalf("missing exact persisted dependency: %#v %v", dep, err)
				}
			}
		})
	}
	store := &workspaceDependencyDB{t: t, p: p, fail: "automation.schedules", roots: map[string][][]driver.Value{}}
	database := sql.OpenDB(workspaceDependencyConnector{store})
	defer database.Close()
	if digest, err := NewWorkspacePathDependencies(database)(t.Context(), p.ID, "relocate_project", root, root); digest != "" || !errors.Is(err, ErrWorkspacePathDependency) {
		t.Fatalf("missing owner table was treated as empty: %q %v", digest, err)
	}
}

func TestWorkspacePathDependenciesChildIntersections(t *testing.T) {
	resources := workspaceDependenciesRepo()
	resources["reading"] = map[string]any{"kind": "knowledge", "knowledge": map[string]string{"path": "notes", "category": "notes"}}
	p, root := workspaceDependenciesFixture(t, resources)
	for _, source := range []string{"code", "code/descendant", "notes", "notes/descendant", ".loom", ".loom/project.yaml"} {
		facts, err := workspacePathDeclarationFacts(p, "retain_child", filepath.Join(root, source), root)
		if err == nil {
			err = checkWorkspacePathRoots(&facts)
		}
		if !errors.Is(err, ErrWorkspacePathDependency) {
			t.Fatalf("accepted intersecting child %s: %v", source, err)
		}
	}
	for _, source := range []string{"loose", "code2"} {
		facts, err := workspacePathDeclarationFacts(p, "retain_child", filepath.Join(root, source), root)
		if err == nil {
			err = checkWorkspacePathRoots(&facts)
		}
		if err != nil {
			t.Fatalf("refused disjoint child %s: %v", source, err)
		}
	}
	if _, err := workspacePathDeclarationFacts(p, "relocate_project", root, root); !errors.Is(err, ErrWorkspacePathDependency) || !strings.Contains(err.Error(), "reading") {
		t.Fatalf("knowledge-bearing relocation accepted: %v", err)
	}
	for _, owner := range []string{"knowledge.notes_source_roots", "notes_workspace.source_scopes", "projects.project_watched_root_registrations"} {
		f := workspacePathDependencyFacts{Kind: "retain_child", Source: "loose/nested", Roots: []workspacePathRoot{{Owner: owner, Key: "disabled-notes", Status: "disabled", Path: "loose"}}}
		if err := checkWorkspacePathRoots(&f); !errors.Is(err, ErrWorkspacePathDependency) || !strings.Contains(err.Error(), "disabled-notes") {
			t.Fatalf("ignored typed %s root: %v", owner, err)
		}
		f.Source = "code"
		if err := checkWorkspacePathRoots(&f); err != nil {
			t.Fatalf("disjoint root blocks child: %v", err)
		}
		f.Kind = "relocate_project"
		if err := checkWorkspacePathRoots(&f); !errors.Is(err, ErrWorkspacePathDependency) {
			t.Fatalf("accepted root migration owned by %s", owner)
		}
	}
}

func TestWorkspacePathDependenciesCurrentContractAndPointers(t *testing.T) {
	for _, mutation := range []string{"changed-bytes", "invalid", "legacy", "alias", "gitdir", "alternates"} {
		t.Run(mutation, func(t *testing.T) {
			p, root := workspaceDependenciesFixture(t, workspaceDependenciesRepo())
			contract := filepath.Join(root, pc.CanonicalRootContractPath)
			switch mutation {
			case "changed-bytes":
				raw, _ := os.ReadFile(contract)
				if err := os.WriteFile(contract, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := os.WriteFile(contract, []byte("not a valid contract"), 0600); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				p.Registration.ContractSchemaVersion = projects.ProjectRepositoryProjectSchemaV04
			case "alias":
				if err := os.Rename(filepath.Join(root, "code"), filepath.Join(root, "real-code")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("real-code", filepath.Join(root, "code")); err != nil {
					t.Fatal(err)
				}
			case "gitdir":
				if err := os.WriteFile(filepath.Join(root, "code/.git"), []byte("gitdir: /external/worktree"), 0600); err != nil {
					t.Fatal(err)
				}
			case "alternates":
				if err := os.MkdirAll(filepath.Join(root, "code/.git/objects/info"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "code/.git/objects/info/alternates"), []byte("/external/objects"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := workspacePathDeclarationFacts(p, "relocate_project", root, root); !errors.Is(err, ErrWorkspacePathDependency) {
				t.Fatalf("accepted %s: %v", mutation, err)
			}
		})
	}
}

func TestWorkspacePathDependenciesStableFacts(t *testing.T) {
	p, root := workspaceDependenciesFixture(t, workspaceDependenciesRepo())
	facts, err := workspacePathDeclarationFacts(p, "relocate_project", root, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspacePathDependencyDigest(facts)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(facts.Roots)-1; i < j; i, j = i+1, j-1 {
		facts.Roots[i], facts.Roots[j] = facts.Roots[j], facts.Roots[i]
	}
	after, err := workspacePathDependencyDigest(facts)
	if err != nil || before != after {
		t.Fatal("query ordering changed digest")
	}
	facts.RegistrationRevision++
	after, _ = workspacePathDependencyDigest(facts)
	if after == before {
		t.Fatal("registration drift did not change digest")
	}
	for _, at := range []string{root + "/notes", "/recovered/project/notes"} {
		logical, relevant, err := workspacePathNormalizeRoot(at, root, "/recovered/project")
		if err != nil || !relevant || logical != "notes" {
			t.Fatalf("runtime root normalization: %s %t %v", logical, relevant, err)
		}
	}
	if _, _, err := workspacePathNormalizeRoot(root+"/notes/../code", root, root); err == nil {
		t.Fatal("accepted unnormalized runtime alias")
	}
	if workspacePathIntersects("loose", "loose2") || !workspacePathIntersects("loose/nested", "loose") {
		t.Fatal("path containment uses an inexact prefix")
	}
	if _, err := NewWorkspacePathDependencies(nil)(t.Context(), p.ID, "relocate_project", root, root); !errors.Is(err, ErrWorkspacePathDependency) {
		t.Fatal("missing database admitted")
	}
}

func TestWorkspacePathDependenciesTypedRootRows(t *testing.T) {
	p, root := workspaceDependenciesFixture(t, workspaceDependenciesRepo())
	for _, owner := range []string{"projects.project_repository_memberships", "projects.project_watched_root_registrations", "knowledge.notes_source_roots", "notes_workspace.source_scopes"} {
		t.Run(owner, func(t *testing.T) {
			at := "notes"
			if owner == "knowledge.notes_source_roots" || owner == "notes_workspace.source_scopes" {
				at = filepath.Join(root, "notes")
			}
			store := &workspaceDependencyDB{t: t, p: p, roots: map[string][][]driver.Value{owner: {{"retained-owner", "disabled", at, "revision-a"}}}}
			database := sql.OpenDB(workspaceDependencyConnector{store})
			defer database.Close()
			callback := NewWorkspacePathDependencies(database)
			disjoint, err := callback(t.Context(), p.ID, "retain_child", filepath.Join(root, "loose"), root)
			if err != nil {
				t.Fatalf("disjoint typed root refused: %v", err)
			}
			_, err = callback(t.Context(), p.ID, "retain_child", filepath.Join(root, "notes"), root)
			var dependency *WorkspacePathDependencyError
			if !errors.As(err, &dependency) || dependency.Owner != owner || dependency.Key != "retained-owner" {
				t.Fatalf("typed child collision not named: %v", err)
			}
			store.roots[owner][0][3] = "revision-b"
			changed, err := callback(t.Context(), p.ID, "retain_child", filepath.Join(root, "loose"), root)
			if err != nil || disjoint == changed {
				t.Fatalf("owner drift did not change digest: %v", err)
			}
		})
	}
}

// This small SQL driver exercises callback composition and makes any attempted
// write or advisory-lock command fail. PostgreSQL SQL validation remains with
// the integrator's real database acceptance.
type workspaceDependencyDB struct {
	t                         *testing.T
	p                         workspacePathProject
	hold, fail                string
	roots                     map[string][][]driver.Value
	commits, readTransactions int
}
type workspaceDependencyConnector struct{ store *workspaceDependencyDB }

func (c workspaceDependencyConnector) Connect(context.Context) (driver.Conn, error) {
	return &workspaceDependencyConn{c.store}, nil
}
func (c workspaceDependencyConnector) Driver() driver.Driver { return workspaceDependencyDriver{} }

type workspaceDependencyDriver struct{}

func (workspaceDependencyDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type workspaceDependencyConn struct{ store *workspaceDependencyDB }

func (c *workspaceDependencyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("writes/prepared statements unsupported")
}
func (c *workspaceDependencyConn) Close() error { return nil }
func (c *workspaceDependencyConn) Begin() (driver.Tx, error) {
	return nil, errors.New("read-only transaction required")
}
func (c *workspaceDependencyConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	if !o.ReadOnly || o.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
		return nil, errors.New("consistent read-only snapshot required")
	}
	c.store.readTransactions++
	return workspaceDependencyTx{c.store}, nil
}

type workspaceDependencyTx struct{ store *workspaceDependencyDB }

func (t workspaceDependencyTx) Commit() error { t.store.commits++; return nil }
func (workspaceDependencyTx) Rollback() error { return nil }
func (c *workspaceDependencyConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	p := c.store.p
	r := p.Registration
	if strings.Contains(q, "FROM projects.projects p JOIN nodes.nodes") {
		if len(args) != 1 || args[0].Value != p.ID {
			return nil, errors.New("wrong project selector")
		}
		return dependencyRows([][]driver.Value{{p.ID, p.Scope, p.Slug, p.Status, p.Node, p.NodeKey, r.ProjectContractRegistrationID, r.ProjectRoot, r.ContractPath, r.ContractHash, r.ContractSchemaVersion, []byte(r.Contract), []byte(r.RegistrationPlan), r.RegistrationStatus, r.ActivationStatus, int64(r.RegistrationRevision)}}, 16), nil
	}
	for _, owner := range workspacePathRuntimeQueries {
		if strings.Contains(q, owner.query) {
			if len(args) != 4 || args[0].Value != p.ID || args[1].Value != p.Scope {
				return nil, errors.New("wrong runtime ownership")
			}
			if c.store.fail == owner.owner {
				return nil, errors.New("owner unavailable")
			}
			if c.store.hold == owner.owner {
				return dependencyRows([][]driver.Value{{"disabled-owner", "disabled"}}, 2), nil
			}
			return dependencyRows(nil, 2), nil
		}
	}
	for _, owner := range []string{"projects.project_repository_memberships", "projects.project_watched_root_registrations", "knowledge.notes_source_roots", "notes_workspace.source_scopes", "projects.declaration_watch_intents"} {
		if strings.Contains(q, "FROM "+owner) {
			return dependencyRows(c.store.roots[owner], 4), nil
		}
	}
	return nil, fmt.Errorf("unexpected query: %s", q)
}

type workspaceDependencyRows struct {
	columns []string
	rows    [][]driver.Value
	at      int
}

func dependencyRows(rows [][]driver.Value, cols int) *workspaceDependencyRows {
	names := make([]string, cols)
	for i := range names {
		names[i] = fmt.Sprint(i)
	}
	return &workspaceDependencyRows{columns: names, rows: rows}
}
func (r *workspaceDependencyRows) Columns() []string { return r.columns }
func (r *workspaceDependencyRows) Close() error      { return nil }
func (r *workspaceDependencyRows) Next(out []driver.Value) error {
	if r.at == len(r.rows) {
		return io.EOF
	}
	copy(out, r.rows[r.at])
	r.at++
	return nil
}
