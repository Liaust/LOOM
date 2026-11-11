package loomdapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
)

var ErrWorkspacePathDependency = errors.New("unsupported workspace path dependency")

// WorkspacePathDependencyError names the owner that must prove or implement a
// successor. Disabled registrations still have owners; inactivity is not proof
// that an absolute runtime pointer can be moved safely.
type WorkspacePathDependencyError struct {
	Owner  string
	Key    string
	Path   string
	Reason string
}

func (e *WorkspacePathDependencyError) Error() string {
	return fmt.Sprintf("%s: owner=%s key=%s path=%s reason=%s", ErrWorkspacePathDependency, e.Owner, e.Key, e.Path, e.Reason)
}

func (e *WorkspacePathDependencyError) Unwrap() error { return ErrWorkspacePathDependency }

func workspacePathHold(owner, key, at, reason string) error {
	return &WorkspacePathDependencyError{Owner: owner, Key: key, Path: at, Reason: reason}
}

type workspacePathProject struct {
	ID, Scope, Slug, Status, Node, NodeKey string
	Registration                           projects.ProjectContractRegistration
}

type workspacePathRoot struct {
	Owner    string `json:"owner"`
	Key      string `json:"key"`
	Status   string `json:"status,omitempty"`
	Path     string `json:"path"`
	Revision string `json:"revision,omitempty"`
}

type workspacePathDependencyFacts struct {
	Schema               string                 `json:"schema"`
	ProjectID            string                 `json:"project_id"`
	ScopeID              string                 `json:"scope_id"`
	NodeID               string                 `json:"node_id"`
	Kind                 string                 `json:"kind"`
	Source               string                 `json:"source"`
	RegistrationID       string                 `json:"registration_id"`
	RegistrationRevision int                    `json:"registration_revision"`
	ContractHash         string                 `json:"contract_hash"`
	ContractSchema       string                 `json:"contract_schema"`
	Sources              []pc.DeclarationSource `json:"sources"`
	Roots                []workspacePathRoot    `json:"roots"`
}

// NewWorkspacePathDependencies composes WorkspacePathService.Dependencies. The
// trusted caller must already authorize/fence the operation and verify the exact
// pending marker through its saved Projects snapshot. This read is NOT that
// authority check: it neither trusts nor hashes temporary project metadata, and
// never calls EnsureProjectMutable (which would reject the caller's own fence).
// physicalProjectRoot is the caller-pinned original or recovered destination;
// all facts use the old registered logical location, never that physical alias.
func NewWorkspacePathDependencies(db *sql.DB) func(context.Context, string, string, string, string) (string, error) {
	return func(ctx context.Context, projectID, kind, sourceAbsolute, physicalProjectRoot string) (string, error) {
		if db == nil {
			return "", workspacePathHold("database", projectID, "", "unavailable")
		}
		if kind != "relocate_project" && kind != "retain_child" {
			return "", workspacePathHold("operation", kind, sourceAbsolute, "unsupported_kind")
		}
		if !workspacePathExactAbsolute(sourceAbsolute) || !workspacePathExactAbsolute(physicalProjectRoot) {
			return "", workspacePathHold("custody", projectID, sourceAbsolute, "exact_absolute_paths_required")
		}
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return "", workspacePathHold("database", projectID, "", "snapshot_unavailable")
		}
		defer tx.Rollback()
		p, err := readWorkspacePathProject(ctx, tx, projectID)
		if err != nil {
			return "", err
		}
		facts, err := workspacePathDeclarationFacts(p, kind, sourceAbsolute, physicalProjectRoot)
		if err != nil {
			return "", err
		}
		if err := readWorkspacePathRuntimeHolds(ctx, tx, p, physicalProjectRoot); err != nil {
			return "", err
		}
		roots, err := readWorkspacePathRoots(ctx, tx, p, physicalProjectRoot)
		if err != nil {
			return "", err
		}
		facts.Roots = append(facts.Roots, roots...)
		if err := checkWorkspacePathRoots(&facts); err != nil {
			return "", err
		}
		digest, err := workspacePathDependencyDigest(facts)
		if err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", workspacePathHold("database", projectID, "", "snapshot_incomplete")
		}
		return digest, nil
	}
}

// Match the existing daemon composition without invoking a second Projects
// guard/read owner under its already-held session fence.
func workspacePathDependencies(db *sql.DB, _ projects.Service) func(context.Context, string, string, string, string) (string, error) {
	return NewWorkspacePathDependencies(db)
}

func readWorkspacePathProject(ctx context.Context, tx *sql.Tx, id string) (workspacePathProject, error) {
	var p workspacePathProject
	r := &p.Registration
	var contract, plan []byte
	err := tx.QueryRowContext(ctx, `
		SELECT p.project_id,p.project_scope_id,p.slug,p.status,coalesce(p.home_node_id,''),n.node_key,
		 r.project_contract_registration_id,r.project_root,r.contract_path,r.contract_hash,r.contract_schema_version,
		 r.contract_json,r.registration_plan_json,r.registration_status,r.activation_status,r.registration_revision
		FROM projects.projects p JOIN nodes.nodes n ON n.node_id=p.home_node_id
		JOIN projects.project_contract_registrations r USING(project_id) WHERE p.project_id=$1
	`, id).Scan(&p.ID, &p.Scope, &p.Slug, &p.Status, &p.Node, &p.NodeKey,
		&r.ProjectContractRegistrationID, &r.ProjectRoot, &r.ContractPath, &r.ContractHash, &r.ContractSchemaVersion,
		&contract, &plan, &r.RegistrationStatus, &r.ActivationStatus, &r.RegistrationRevision)
	if err != nil {
		return p, workspacePathHold("projects.project_contract_registrations", id, "", "current_registration_unavailable")
	}
	r.ProjectID, r.Contract, r.RegistrationPlan = p.ID, contract, plan
	if p.ID != id || p.Status != "active" || p.NodeKey != "main" || r.RegistrationStatus != "registered" || r.RegistrationRevision < 1 || !workspacePathExactAbsolute(r.ProjectRoot) {
		return p, workspacePathHold("projects.project_contract_registrations", id, r.ProjectRoot, "active_Main_registration_required")
	}
	return p, nil
}

func workspacePathDeclarationFacts(p workspacePathProject, kind, sourceAbsolute, physicalRoot string) (workspacePathDependencyFacts, error) {
	r := p.Registration
	f := workspacePathDependencyFacts{Schema: "workspace.path_dependencies.v1", ProjectID: p.ID, ScopeID: p.Scope, NodeID: p.Node, Kind: kind,
		RegistrationID: r.ProjectContractRegistrationID, RegistrationRevision: r.RegistrationRevision, ContractHash: r.ContractHash, ContractSchema: r.ContractSchemaVersion,
		Sources: []pc.DeclarationSource{}, Roots: []workspacePathRoot{}}
	if !pc.IsDeclarationSchemaVersion(r.ContractSchemaVersion) {
		return f, workspacePathHold("declaration", p.ID, r.ContractPath, "modern_declaration_required")
	}
	source, err := workspacePathRelative(r.ProjectRoot, sourceAbsolute)
	if err != nil || kind == "relocate_project" && source != "." || kind == "retain_child" && source == "." {
		return f, workspacePathHold("custody", p.ID, sourceAbsolute, "source_does_not_match_registered_project")
	}
	f.Source = source
	contractRef, err := workspacePathRelative(r.ProjectRoot, r.ContractPath)
	if err != nil || contractRef != pc.CanonicalRootContractPath {
		return f, workspacePathHold("declaration", p.ID, r.ContractPath, "canonical_modern_contract_required")
	}
	if err := workspacePathNoAlias(physicalRoot, contractRef); err != nil {
		return f, err
	}
	analysis := pc.Analyze(physicalRoot)
	input, err := projectregistration.BuildInput(analysis, "workspace.path_dependencies")
	if err != nil || analysis.Loaded == nil || analysis.Loaded.Declaration == nil || analysis.Plan.Declaration == nil || !analysis.Report.OK || !analysis.Report.Registerable {
		return f, workspacePathHold("declaration", p.ID, contractRef, "valid_current_declaration_required")
	}
	d := analysis.Loaded.Declaration
	if input.Project.ID != p.ID || input.Project.Slug != p.Slug || input.Project.OwnerNode != "main" || input.ContractSchemaVersion != r.ContractSchemaVersion || input.ContractHash != r.ContractHash || !workspacePathJSONEqual(input.Contract, r.Contract) {
		return f, workspacePathHold("declaration", p.ID, contractRef, "registered_contract_changed")
	}
	if d.LegacyContracts != nil || analysis.Loaded.Layout != pc.ProjectLayoutCanonical {
		return f, workspacePathHold("declaration", p.ID, contractRef, "legacy_source_alias_requires_successor")
	}
	f.Roots = append(f.Roots, workspacePathRoot{Owner: "declaration", Key: "control_directory", Path: ".loom"})
	for _, s := range analysis.Plan.Declaration.Sources {
		if err := workspacePathNoAlias(physicalRoot, s.Ref); err != nil {
			return f, err
		}
		f.Sources = append(f.Sources, s.DeclarationSource)
		f.Roots = append(f.Roots, workspacePathRoot{Owner: "declaration", Key: s.Ref, Path: s.Ref, Revision: s.Hash})
	}
	// Referenced manifests/policies/instructions must still be the source set
	// accepted by the registration, not just parse successfully today.
	var saved struct {
		Declaration *struct {
			Sources []pc.DeclarationSource `json:"sources"`
		} `json:"declaration"`
	}
	if json.Unmarshal(r.RegistrationPlan, &saved) != nil || saved.Declaration == nil || !workspacePathSourcesEqual(f.Sources, saved.Declaration.Sources) {
		return f, workspacePathHold("declaration", p.ID, contractRef, "registered_declaration_sources_changed")
	}
	keys := make([]string, 0, len(d.Resources))
	for key := range d.Resources {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	for _, key := range keys {
		res := d.Resources[pc.ResourceKey(key)]
		if kind == "relocate_project" && res.Kind != pc.DeclarationRepository {
			return f, workspacePathHold("declaration.resource", key, "", "location_successor_unsupported_for_"+string(res.Kind))
		}
		add := func(at string) {
			f.Roots = append(f.Roots, workspacePathRoot{Owner: "declaration." + string(res.Kind), Key: key, Path: at})
		}
		switch res.Kind {
		case pc.DeclarationRepository:
			add(res.Repository.Path)
			if err := workspacePathNoAlias(physicalRoot, res.Repository.Path); err != nil {
				return f, err
			}
			if err := workspacePathRepositoryPointers(physicalRoot, res.Repository.Path); err != nil {
				return f, err
			}
		case pc.DeclarationKnowledge:
			if res.Knowledge.ApplicationData != nil {
				return f, workspacePathHold("declaration.knowledge", key, "", "application_data_alias_requires_owner")
			}
			add(res.Knowledge.Path)
			if res.Knowledge.Workspace != nil && res.Knowledge.Workspace.Path != "" {
				add(res.Knowledge.Workspace.Path)
			}
		case pc.DeclarationProtection:
			add(res.Protection.PolicyRef)
			for _, selection := range analysis.Plan.Declaration.Protection {
				if string(selection.Resource) == key {
					for _, root := range selection.Roots {
						add(root.Path)
					}
				}
			}
		case pc.DeclarationApplication:
			a := res.Application
			if a.Source != nil {
				at := a.Source.Path
				if at == "" {
					at = "."
				}
				add(at)
			} else if repo := d.Resources[a.Repository].Repository; repo != nil {
				add(repo.Path)
			} else {
				return f, workspacePathHold("declaration.application", key, "", "repository_root_unresolved")
			}
			for dataKey, data := range a.Data {
				if data.BindingRef != "" {
					return f, workspacePathHold("declaration.application", key+"/"+string(dataKey), data.BindingRef, "runtime_data_alias_requires_owner")
				}
				add(data.Path)
			}
		case pc.DeclarationAgent:
			workdir := res.Agent.Workdir
			if workdir == "" {
				workdir = "."
			}
			add(workdir)
		case pc.DeclarationSchedule, pc.DeclarationHermesSchedule:
			return f, workspacePathHold("declaration.resource", key, "", "runtime_target_path_closure_unavailable_for_"+string(res.Kind))
		default:
			return f, workspacePathHold("declaration.resource", key, "", "unsupported_resource_kind")
		}
	}
	return f, nil
}

// Every query uses typed project/scope ownership, not arbitrary metadata keys.
// The EXISTS proof is negative: any remaining row names an unsupported owner.
// Keep paused/disabled registrations; filter only terminal execution histories.
var workspacePathRuntimeQueries = []struct{ owner, query string }{
	{"projects.project_contract_facets", `SELECT facet_key,facet_status FROM projects.project_contract_facets r,target t WHERE r.project_id=t.project_id AND facet_key<>'repos'`},
	{"projects.project_script_exposures", `SELECT script_key,activation_status FROM projects.project_script_exposures r,target t WHERE r.project_id=t.project_id`},
	{"projects.project_schedule_registrations", `SELECT schedule_key,activation_status FROM projects.project_schedule_registrations r,target t WHERE r.project_id=t.project_id`},
	{"projects.project_direct_event_registrations", `SELECT event_key,activation_status FROM projects.project_direct_event_registrations r,target t WHERE r.project_id=t.project_id`},
	{"projects.project_connector_registrations", `SELECT connector_key,activation_status FROM projects.project_connector_registrations r,target t WHERE r.project_id=t.project_id`},
	{"projects.project_module_registrations", `SELECT module_key,activation_status FROM projects.project_module_registrations r,target t WHERE r.project_id=t.project_id`},
	{"projects.project_workflow_registrations", `SELECT workflow_key,activation_status FROM projects.project_workflow_registrations r,target t WHERE r.project_id=t.project_id`},
	{"automation.automations", `SELECT automation_id,status FROM automation.automations r,target t WHERE r.project_id=t.project_id OR r.scope_id=t.scope_id`},
	{"automation.schedules", `SELECT schedule_id,status FROM automation.schedules r,target t WHERE r.project_id=t.project_id OR r.scope_id=t.scope_id`},
	{"capabilities.providers", `SELECT provider_id,status FROM capabilities.providers r,target t WHERE r.scope_id=t.scope_id`},
	{"workers.worker_instances", `SELECT worker_instance_id,lifecycle_status FROM workers.worker_instances r,target t WHERE r.project_id=t.project_id OR r.scope_id=t.scope_id`},
	{"agents.agent_access_sessions", `SELECT agent_access_session_id,status FROM agents.agent_access_sessions r,target t WHERE (r.active_project_id=t.project_id OR r.current_scope_id=t.scope_id) AND status='active'`},
	{"agents.agent_work_contexts", `SELECT agent_work_context_id,status FROM agents.agent_work_contexts r,target t WHERE (r.active_project_id=t.project_id OR r.current_scope_id=t.scope_id) AND status='active'`},
	{"jobs.jobs", `SELECT j.job_id,j.status FROM jobs.jobs j,target t WHERE j.status NOT IN ('completed','failed','cancelled','timed_out') AND (j.scope_id=t.scope_id OR j.target_id=t.project_id OR j.workdir_path=t.root OR starts_with(j.workdir_path,t.root||'/') OR j.workdir_path=t.physical_root OR starts_with(j.workdir_path,t.physical_root||'/') OR EXISTS(SELECT 1 FROM packages.scripts s WHERE s.script_id=j.script_id AND s.owner_scope_id=t.scope_id))`},
	{"projects.declaration_operations", `SELECT operation_id,state FROM projects.declaration_operations r,target t WHERE r.project_id=t.project_id AND state NOT IN ('succeeded','superseded')`},
	{"projects.declaration_action_receipts", `SELECT a.operation_id||'/'||a.action_id,a.owner||':'||a.state FROM projects.declaration_action_receipts a JOIN projects.declaration_operations o USING(operation_id),target t WHERE o.project_id=t.project_id AND a.owner IN ('serviceregistry','hermesschedules') AND o.state<>'superseded'`},
}

func readWorkspacePathRuntimeHolds(ctx context.Context, tx *sql.Tx, p workspacePathProject, physical string) error {
	for _, owner := range workspacePathRuntimeQueries {
		query := `WITH target AS (SELECT $1::text AS project_id,$2::text AS scope_id,$3::text AS root,$4::text AS physical_root) ` + owner.query + ` ORDER BY 1 LIMIT 1`
		var key, state string
		err := tx.QueryRowContext(ctx, query, p.ID, p.Scope, p.Registration.ProjectRoot, physical).Scan(&key, &state)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return workspacePathHold(owner.owner, p.ID, "", "owner_read_failed")
		}
		return workspacePathHold(owner.owner, key, "", "remaining_runtime_registration:"+state)
	}
	return nil
}

func readWorkspacePathRoots(ctx context.Context, tx *sql.Tx, p workspacePathProject, physical string) ([]workspacePathRoot, error) {
	out := []workspacePathRoot{}
	// Memberships are independent of lifecycle: an archived or disabled member
	// can still own a physical root and cannot disappear from this closure.
	queries := []struct {
		owner, query string
		relative     bool
	}{
		{"projects.project_repository_memberships", `SELECT repository_id,lifecycle_status,member_path,member_key FROM projects.project_repository_memberships WHERE project_id=$1 ORDER BY repository_id`, true},
		{"projects.project_watched_root_registrations", `SELECT backend_root_key,activation_status,CASE WHEN safe_root_key='project' AND node_id=$2 THEN root_relative_path ELSE '' END,config_hash FROM projects.project_watched_root_registrations WHERE project_id=$1 ORDER BY backend_root_key`, true},
		{"knowledge.notes_source_roots", `SELECT notes_source_root_id,status,source_path,root_relative_path FROM knowledge.notes_source_roots WHERE (project_id=$1 OR node_id=$2 OR node_key='main') AND (source_path<>'' OR project_id=$1) ORDER BY notes_source_root_id`, false},
		{"notes_workspace.source_scopes", `SELECT s.scope_id,'bound',coalesce(nullif(r.source_path,''),s.source_path),s.root_identity FROM notes_workspace.source_scopes s LEFT JOIN knowledge.notes_source_roots r ON r.notes_source_root_id=s.scope_id WHERE s.kind='notes' AND (r.project_id=$1 OR r.node_id=$2 OR s.node_key='main') AND (coalesce(nullif(r.source_path,''),s.source_path)<>'' OR r.project_id=$1) ORDER BY s.scope_id`, false},
		{"projects.declaration_watch_intents", `SELECT owner||'/'||resource_key,stage,'',after_revision FROM (SELECT DISTINCT ON(node_id,owner,resource_key) * FROM projects.declaration_watch_intents WHERE project_id=$1 ORDER BY node_id,owner,resource_key,created_at DESC,operation_id DESC,action_id) i WHERE NOT retire OR stage<>'applied' ORDER BY 1`, true},
	}
	for _, owner := range queries {
		// Supply only parameters actually referenced by each typed owner query.
		args := []any{p.ID}
		if strings.Contains(owner.query, "$2") {
			args = append(args, p.Node)
		}
		rows, err := tx.QueryContext(ctx, owner.query+` LIMIT 4097`, args...)
		if err != nil {
			return nil, workspacePathHold(owner.owner, p.ID, "", "owner_read_failed")
		}
		count := 0
		for rows.Next() {
			var fact workspacePathRoot
			fact.Owner = owner.owner
			if err := rows.Scan(&fact.Key, &fact.Status, &fact.Path, &fact.Revision); err != nil {
				rows.Close()
				return nil, workspacePathHold(owner.owner, p.ID, "", "owner_read_failed")
			}
			count++
			if count > 4096 {
				rows.Close()
				return nil, workspacePathHold(owner.owner, p.ID, "", "owner_fact_bound_exceeded")
			}
			if owner.relative {
				if !workspacePathExactRelative(fact.Path) {
					rows.Close()
					return nil, workspacePathHold(owner.owner, fact.Key, fact.Path, "runtime_root_alias_or_unresolved_path")
				}
			} else {
				logical, relevant, err := workspacePathNormalizeRoot(fact.Path, p.Registration.ProjectRoot, physical)
				if err != nil {
					rows.Close()
					return nil, workspacePathHold(owner.owner, fact.Key, fact.Path, "runtime_root_alias_or_unresolved_path")
				}
				if !relevant {
					continue
				}
				fact.Path = logical
			}
			out = append(out, fact)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, workspacePathHold(owner.owner, p.ID, "", "owner_read_failed")
		}
	}
	return out, nil
}

func checkWorkspacePathRoots(f *workspacePathDependencyFacts) error {
	sort.Slice(f.Roots, func(i, j int) bool {
		a, b := f.Roots[i], f.Roots[j]
		return a.Owner+"\x00"+a.Key+"\x00"+a.Path < b.Owner+"\x00"+b.Key+"\x00"+b.Path
	})
	for _, root := range f.Roots {
		if !workspacePathExactRelative(root.Path) {
			return workspacePathHold(root.Owner, root.Key, root.Path, "unresolved_root")
		}
		if f.Kind == "relocate_project" {
			if root.Owner != "declaration" && root.Owner != "declaration.repository" && root.Owner != "projects.project_repository_memberships" {
				return workspacePathHold(root.Owner, root.Key, root.Path, "location_successor_required")
			}
		} else if workspacePathIntersects(f.Source, root.Path) {
			return workspacePathHold(root.Owner, root.Key, root.Path, "child_intersects_dependency")
		}
	}
	return nil
}

func workspacePathDependencyDigest(f workspacePathDependencyFacts) (string, error) {
	f.Sources = append([]pc.DeclarationSource{}, f.Sources...)
	f.Roots = append([]workspacePathRoot{}, f.Roots...)
	sort.Slice(f.Sources, func(i, j int) bool { return f.Sources[i].Ref < f.Sources[j].Ref })
	sort.Slice(f.Roots, func(i, j int) bool {
		a, _ := json.Marshal(f.Roots[i])
		b, _ := json.Marshal(f.Roots[j])
		return bytes.Compare(a, b) < 0
	})
	raw, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

func workspacePathSourcesEqual(a, b []pc.DeclarationSource) bool {
	a, b = append([]pc.DeclarationSource{}, a...), append([]pc.DeclarationSource{}, b...)
	sort.Slice(a, func(i, j int) bool { return a[i].Ref < a[j].Ref })
	sort.Slice(b, func(i, j int) bool { return b[i].Ref < b[j].Ref })
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func workspacePathJSONEqual(a, b []byte) bool {
	normalize := func(raw []byte) ([]byte, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return nil, errors.New("trailing JSON")
		}
		return json.Marshal(v)
	}
	x, e := normalize(a)
	y, f := normalize(b)
	return e == nil && f == nil && bytes.Equal(x, y)
}

func workspacePathExactAbsolute(p string) bool {
	return filepath.IsAbs(p) && p != "/" && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}
func workspacePathExactRelative(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n")
}
func workspacePathRelative(root, absolute string) (string, error) {
	if !workspacePathExactAbsolute(root) || !workspacePathExactAbsolute(absolute) {
		return "", errors.New("unconfined path")
	}
	rel, err := filepath.Rel(root, absolute)
	if err != nil || !workspacePathExactRelative(filepath.ToSlash(rel)) {
		return "", errors.New("unconfined path")
	}
	return filepath.ToSlash(rel), nil
}
func workspacePathIntersects(a, b string) bool {
	return a == "." || b == "." || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func workspacePathNormalizeRoot(at, logical, physical string) (string, bool, error) {
	if !workspacePathExactAbsolute(at) {
		return "", false, errors.New("invalid runtime root")
	}
	for _, base := range []string{logical, physical} {
		if rel, err := workspacePathRelative(base, at); err == nil {
			return rel, true, nil
		}
		if strings.HasPrefix(base, at+string(filepath.Separator)) {
			return ".", true, nil
		}
	}
	return "", false, nil
}

func workspacePathNoAlias(root, relative string) error {
	if !workspacePathExactRelative(relative) {
		return workspacePathHold("filesystem", relative, relative, "unconfined_dependency_path")
	}
	at := root
	parts := append([]string{""}, strings.Split(relative, "/")...)
	for _, part := range parts {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		if err != nil {
			return workspacePathHold("filesystem", relative, relative, "dependency_path_unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return workspacePathHold("filesystem", relative, relative, "runtime_pointer_alias_unsupported")
		}
	}
	return nil
}

func workspacePathRepositoryPointers(root, relative string) error {
	git := filepath.Join(root, filepath.FromSlash(relative), ".git")
	info, err := os.Lstat(git)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return workspacePathHold("repository", relative, path.Join(relative, ".git"), "git_directory_pointer_unsupported")
	}
	for _, pointer := range []string{"commondir", "objects/info/alternates"} {
		if _, err := os.Lstat(filepath.Join(git, pointer)); err == nil {
			return workspacePathHold("repository", relative, path.Join(relative, ".git", pointer), "external_git_pointer_unsupported")
		} else if !os.IsNotExist(err) {
			return workspacePathHold("repository", relative, relative, "git_pointer_unavailable")
		}
	}
	return nil
}
