package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

// Inspect the actual contract before any registration mutation. Caller-supplied
// schema, repository-source and registerable flags cannot authorize adoption.
func rejectLegacyDeclarationRegistration(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		if _, present := fields["legacy_contracts"]; present {
			return fmt.Errorf("%w: legacy_owner_transition_required", ErrInvalidProjectRepositorySourceInput)
		}
	}
	return nil
}

// These projections are domain inputs, not another declaration compiler. The
// adapter must use projectregistration.BuildInput (including D1/D2 validation)
// before calling this boundary, just as legacy contract registration does.
type declarationDocument struct {
	Kind            string                       `json:"kind"`
	SchemaVersion   string                       `json:"schema_version"`
	Project         ProjectContractProjectInput  `json:"project"`
	LegacyContracts *declarationLegacyReferences `json:"legacy_contracts,omitempty"`
	Resources       map[string]struct {
		Kind       string `json:"kind"`
		Repository *struct {
			ID        string `json:"id"`
			Path      string `json:"path"`
			Role      string `json:"role"`
			StateRoot string `json:"state_root,omitempty"`
		} `json:"repository"`
	} `json:"resources"`
}
type declarationCompilation struct {
	Document     json.RawMessage                    `json:"document"`
	Repositories []projectRepositoryValidatorMember `json:"repositories"`
	Errors       []json.RawMessage                  `json:"errors"`
	Sources      []struct {
		Ref           string `json:"ref"`
		Hash          string `json:"hash"`
		Revision      string `json:"revision"`
		SchemaVersion string `json:"schema_version"`
		Raw           []byte `json:"raw"`
	} `json:"sources"`
}
type declarationEvidence struct {
	projectRepositoryValidatorDocument
	Declaration  *declarationCompilation `json:"declaration"`
	WatchedRoots []json.RawMessage       `json:"watched_roots"`
}

type declarationLegacyReferences struct {
	Project struct {
		Ref           string `json:"ref"`
		SchemaVersion string `json:"schema_version"`
		Digest        string `json:"digest"`
	} `json:"project"`
}

// Omitted IDs stay omitted until the existing domain transaction has acquired
// its project lock. Complete-set evidence is checked before any row is changed.
func buildDeclarationRepositorySource(input RegisterProjectContractInput, projectID string) (ProjectRepositoryValidatedSourceInput, error) {
	var report, plan declarationEvidence
	if !declarationDecode(input.ValidationReport, &report) || !declarationDecode(input.RegistrationPlan, &plan) || !report.Registerable || !plan.Registerable {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("%w: declaration validation evidence", ErrInvalidProjectRepositorySourceInput)
	}
	return buildDeclarationRepositoryEvidence(input, projectID)
}

// Pure evidence projection. Mutation callers must carry a privately prepared
// registration; public/direct callers additionally require registerable flags.
func buildDeclarationRepositoryEvidence(input RegisterProjectContractInput, projectID string) (ProjectRepositoryValidatedSourceInput, error) {
	invalid := func(reason string) (ProjectRepositoryValidatedSourceInput, error) {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("%w: declaration %s", ErrInvalidProjectRepositorySourceInput, reason)
	}
	if input.RepositorySource == nil || input.Project.ID != projectID || len(input.Facets) != 0 {
		return invalid("identity or legacy facets")
	}
	if input.RepositorySource.ContractSchemaVersion != ProjectRepositoryProjectSchemaV05 || input.RepositorySource.ContractPath != input.ContractPath || input.RepositorySource.ContractHash != input.ContractHash || input.ContractPath != filepath.Join(input.ProjectRoot, ".loom", "project.yaml") {
		return invalid("single source coordinates")
	}
	var d declarationDocument
	var report, plan declarationEvidence
	if !declarationDecode(input.Contract, &d) || d.Kind != "loom.project" || d.SchemaVersion != ProjectRepositoryProjectSchemaV05 || d.Resources == nil || !declarationDecode(input.ValidationReport, &report) || !declarationDecode(input.RegistrationPlan, &plan) {
		return invalid("evidence JSON")
	}
	if !report.OK || report.SchemaVersion != projectRepositoryValidationReportSchemaV03 || plan.SchemaVersion != projectRepositoryRegistrationPlanSchemaV03 || report.Declaration == nil || plan.Declaration == nil {
		return invalid("validation evidence")
	}
	if !declarationEqual(report.Declaration, plan.Declaration) || !declarationEqual(input.Contract, report.Declaration.Document) || len(report.Declaration.Errors) != 0 {
		return invalid("source/compiler mismatch")
	}
	if report.ProjectRoot != input.ProjectRoot || plan.ProjectRoot != input.ProjectRoot || report.ContractPath != input.ContractPath || plan.ContractPath != input.ContractPath {
		return invalid("source location mismatch")
	}
	expected := normalizeProjectRepositoryValidatorProject(d.Project)
	if !reflect.DeepEqual(expected, input.Project) || report.Project != expected || plan.Project != expected {
		return invalid("project identity mismatch")
	}
	seen := map[string]bool{}
	rootBound := false
	for _, source := range report.Declaration.Sources {
		if seen[source.Ref] || source.Hash != fmt.Sprintf("sha256:%x", sha256.Sum256(source.Raw)) || source.Revision != source.Hash {
			return invalid("source snapshot")
		}
		seen[source.Ref] = true
		if source.Ref == ".loom/project.yaml" {
			rootBound = source.Hash == input.ContractHash && source.SchemaVersion == ProjectRepositoryProjectSchemaV05
		}
	}
	if !rootBound {
		return invalid("root snapshot missing or mismatched")
	}
	keys := []string{}
	for k, res := range d.Resources {
		if res.Kind == "repository" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > ProjectRepositoryReadMemberLimit {
		return invalid("repository member limit exceeded")
	}
	expectedMembers := []projectRepositoryValidatorMember{}
	members := []ProjectRepositoryValidatedMember{}
	for _, key := range keys {
		repo := d.Resources[key].Repository
		if repo == nil {
			return invalid("repository payload missing")
		}
		expectedMembers = append(expectedMembers, projectRepositoryValidatorMember{RepositoryID: repo.ID, Key: key, Path: repo.Path, Role: repo.Role, StateRoot: repo.StateRoot})
		members = append(members, ProjectRepositoryValidatedMember{RepositoryID: repo.ID, Key: key, Path: repo.Path, Role: ProjectRepositoryRole(repo.Role), StateRoot: repo.StateRoot})
	}
	if !reflect.DeepEqual(expectedMembers, report.Declaration.Repositories) {
		return invalid("repository set differs from source")
	}
	return ProjectRepositoryValidatedSourceInput{ProjectID: projectID, ProjectRoot: input.ProjectRoot, ProjectContractPath: input.ContractPath, ReposContractPath: input.ContractPath, OwnerNode: input.Project.OwnerNode, Versions: projectRepositorySourceV05V05, ProjectContractDigest: input.ContractHash, ReposContractDigest: input.ContractHash, RegistrationPlan: append(json.RawMessage(nil), input.RegistrationPlan...), Members: members}, nil
}
func declarationDecode(raw []byte, target any) bool {
	_, err := canonicalStoredRegistrationPlan(raw)
	return err == nil && json.Unmarshal(raw, target) == nil
}
func declarationEqual(a, b any) bool {
	ar, ea := json.Marshal(a)
	br, eb := json.Marshal(b)
	if ea != nil || eb != nil {
		return false
	}
	ar, ea = canonicalStoredRegistrationPlan(ar)
	br, eb = canonicalStoredRegistrationPlan(br)
	return ea == nil && eb == nil && string(ar) == string(br)
}
func resolveDeclarationRepositoryIDsTx(ctx context.Context, tx *sql.Tx, input ProjectRepositoryValidatedSourceInput) (ProjectRepositoryValidatedSourceInput, error) {
	rows, err := tx.QueryContext(ctx, `SELECT member_key,repository_id FROM projects.project_repository_memberships WHERE project_id=$1 ORDER BY member_key FOR UPDATE`, input.ProjectID)
	if err != nil {
		return input, err
	}
	existing := map[string]string{}
	for rows.Next() {
		var key, id string
		if err = rows.Scan(&key, &id); err != nil {
			rows.Close()
			return input, err
		}
		existing[key] = id
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return input, err
	}
	// Keep a removed key's last accepted identity in its existing source history.
	// This is not a second registry and never derives identity from path spelling.
	history, err := tx.QueryContext(ctx, `SELECT to_jsonb(h)-'source_snapshot_json'||jsonb_build_object('source_snapshot',source_snapshot_json) FROM projects.project_repository_source_history h WHERE project_id=$1 ORDER BY source_revision DESC`, input.ProjectID)
	if err != nil {
		return input, err
	}
	for history.Next() {
		var raw []byte
		if err = history.Scan(&raw); err != nil {
			history.Close()
			return input, err
		}
		var source ProjectRepositorySource
		if !declarationDecode(raw, &source) {
			history.Close()
			return input, fmt.Errorf("%w: historical source", ErrProjectRepositoryPersistenceCorrupt)
		}
		canonical, err := decodeAndValidateProjectRepositorySourceRow(source)
		if err != nil {
			history.Close()
			return input, err
		}
		for _, m := range canonical.Snapshot.Members {
			if _, ok := existing[m.Key]; !ok {
				existing[m.Key] = m.RepositoryID
			}
		}
	}
	err = history.Err()
	history.Close()
	if err != nil {
		return input, err
	}
	input.Members = append([]ProjectRepositoryValidatedMember{}, input.Members...)
	for i := range input.Members {
		m := &input.Members[i]
		if id, ok := existing[m.Key]; ok {
			if m.RepositoryID != "" && m.RepositoryID != id {
				return input, fmt.Errorf("%w: established declaration key %s cannot replace its repository ID", ErrProjectRepositoryOwnershipConflict, m.Key)
			}
			m.RepositoryID = id
		}
		if m.RepositoryID == "" {
			if m.Role == ProjectRepositoryRoleReference {
				return input, fmt.Errorf("%w: reference requires existing repository", ErrProjectRepositoryOwnershipConflict)
			}
			m.RepositoryID = "repo_" + strings.TrimPrefix(ids.NewProjectID(), "project_")
		}
	}
	return input, nil
}

// DeclarationAuthority is a read-only view of existing authorization owners.
// Revision covers only current relevant authority, never mutable display metadata.
// Non-default policy shapes require a future typed evaluator, never Allow=true.
type DeclarationAuthority struct {
	Revision     string `json:"revision"`
	Level        int    `json:"level"`
	CanRead      bool   `json:"can_read"`
	CanWrite     bool   `json:"can_write"`
	Prerequisite string `json:"prerequisite,omitempty"`
}

func (s Service) GetDeclarationAuthority(ctx context.Context, req requestctx.Context, projectID, targetNodeID string) (DeclarationAuthority, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DeclarationAuthority{}, err
	}
	defer tx.Rollback()
	return declarationAuthorityTx(ctx, tx, req, projectID, targetNodeID, false)
}

// GetDeclarationAuthorityForRepositories includes every referenced project's
// existing read authority. Owned/new repositories add no synthetic grants.
func (s Service) GetDeclarationAuthorityForRepositories(ctx context.Context, req requestctx.Context, projectID, targetNodeID string, repositoryIDs []string) (DeclarationAuthority, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DeclarationAuthority{}, err
	}
	defer tx.Rollback()
	return declarationRepositoryAuthorityTx(ctx, tx, req, projectID, targetNodeID, repositoryIDs, false)
}
func declarationRepositoryAuthorityTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, projectID, targetNodeID string, repositoryIDs []string, lock bool) (DeclarationAuthority, error) {
	own, err := declarationAuthorityTx(ctx, tx, req, projectID, targetNodeID, lock)
	if err != nil {
		return own, err
	}
	facts := map[string]string{projectID: own.Revision}
	seen := map[string]bool{}
	sorted := sortedUniqueProjectRepositoryLockKeys(repositoryIDs)
	for _, id := range sorted {
		var owner, node, status string
		err := tx.QueryRowContext(ctx, `SELECT r.owning_project_id,coalesce(p.home_node_id,''),r.lifecycle_status FROM projects.repositories r JOIN projects.projects p ON p.project_id=r.owning_project_id WHERE repository_id=$1`, id).Scan(&owner, &node, &status)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return own, err
		}
		if owner == projectID || seen[owner] {
			continue
		}
		seen[owner] = true
		other, err := declarationAuthorityTx(ctx, tx, req, owner, node, lock)
		if err != nil {
			return own, err
		}
		facts[owner] = other.Revision
		if !other.CanRead || status != "active" {
			own.CanWrite = false
			own.Prerequisite = "referenced_project_read_access_required"
		}
	}
	// No foreign references preserves the base authority fingerprint, including
	// across creation of the existing transaction's default owner membership.
	if len(facts) > 1 {
		raw, err := json.Marshal(facts)
		if err != nil {
			return own, err
		}
		own.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	}
	return own, nil
}

func declarationAuthorityTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, projectID, targetNodeID string, lock bool) (DeclarationAuthority, error) {
	result := DeclarationAuthority{Level: 5}
	facts := map[string]any{}
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	denied := func(code string) { result.Prerequisite = code; result.CanRead = false; result.CanWrite = false }
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM identity.actors WHERE actor_id=$1`+suffix, req.ActorID).Scan(&status); err != nil {
		return result, err
	}
	facts["actor_status"] = status
	active := status == "active"
	nodeIDs := []string{req.OriginNodeID, targetNodeID}
	sort.Strings(nodeIDs)
	previous := ""
	for _, nodeID := range nodeIDs {
		if nodeID == previous {
			continue
		}
		previous = nodeID
		if err := tx.QueryRowContext(ctx, `SELECT status FROM nodes.nodes WHERE node_id=$1`+suffix, nodeID).Scan(&status); err != nil {
			return result, err
		}
		facts["node:"+nodeID] = status
		active = active && status == "active"
		var raw []byte
		var level int
		var valid bool
		err := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('id',authorization_id,'status',status,'level',authorization_level,'expires_at',expires_at),authorization_level,status='active' AND (expires_at IS NULL OR expires_at>clock_timestamp()) FROM identity.actor_node_authorizations WHERE actor_id=$1 AND node_id=$2`+suffix, req.ActorID, nodeID).Scan(&raw, &level, &valid)
		if err == sql.ErrNoRows {
			active = false
			level = 0
			raw = []byte(`null`)
		} else if err != nil {
			return result, err
		}
		facts["grant:"+nodeID] = json.RawMessage(raw)
		active = active && valid
		if level < result.Level {
			result.Level = level
		}
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects.projects WHERE project_id=$1)`, projectID).Scan(&exists); err != nil {
		return result, err
	}
	// The existing create transaction creates exactly these owner defaults. Before
	// creation they describe eligibility under actor-node authority, not a grant.
	role, memberStatus, hint, memberValid := "owner", "active", 5, true
	categories := []byte(`{}`)
	var expires sql.NullTime
	if exists {
		err := tx.QueryRowContext(ctx, `SELECT role,status,coalesce(authorization_hint,5),allowed_action_categories,expires_at,status='active' AND (expires_at IS NULL OR expires_at>clock_timestamp()) FROM projects.project_memberships WHERE project_id=$1 AND actor_id=$2`+suffix, projectID, req.ActorID).Scan(&role, &memberStatus, &hint, &categories, &expires, &memberValid)
		if err == sql.ErrNoRows {
			memberValid = false
			role = "absent"
		} else if err != nil {
			return result, err
		}
	}
	facts["membership"] = map[string]any{"role": role, "status": memberStatus, "hint": hint, "categories": json.RawMessage(categories), "expires_at": expires}
	if hint < result.Level {
		result.Level = hint
	}
	policy := []byte(`{"allowed_actors":{},"allowed_nodes":{},"approval_requirements":{}}`)
	if exists {
		err := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('allowed_actors',allowed_actors,'allowed_nodes',allowed_nodes,'approval_requirements',approval_requirements) FROM projects.project_policy_profiles WHERE project_id=$1`+suffix, projectID).Scan(&policy)
		if err != nil {
			return result, err
		}
	}
	facts["policy"] = json.RawMessage(policy)
	result.CanRead = active && memberValid && result.Level >= 1
	result.CanWrite = result.CanRead && result.Level >= 3 && (role == "owner" || role == "maintainer")
	if !result.CanRead {
		denied("actor_node_or_project_access_required")
	} else if !result.CanWrite {
		result.Prerequisite = "project_maintainer_level_3_required"
	}
	var relevant map[string]json.RawMessage
	if !declarationDecode(policy, &relevant) {
		return result, fmt.Errorf("invalid relevant project policy")
	}
	for _, v := range relevant {
		if string(v) != "{}" && string(v) != "[]" {
			denied("typed_declaration_policy_evaluator_required")
		}
	}
	if string(categories) != "{}" && string(categories) != "[]" {
		denied("typed_declaration_action_category_evaluator_required")
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return result, err
	}
	raw, err = canonicalStoredRegistrationPlan(raw)
	if err != nil {
		return result, err
	}
	result.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return result, nil
}

// DeclarationProjectState contains reference-only facts, including full current
// membership identity. Hashes cover existing registry/lifecycle rows and source
// revisions; a caller cannot turn an old operation into a drift exemption.
type DeclarationProjectState struct {
	RegistryRevision  string            `json:"registry_revision"`
	LifecycleRevision string            `json:"lifecycle_revision"`
	Repositories      map[string]string `json:"repositories"`
}

func (s Service) GetDeclarationProjectState(ctx context.Context, projectID string) (DeclarationProjectState, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DeclarationProjectState{}, err
	}
	defer tx.Rollback()
	return declarationProjectStateTx(ctx, tx, projectID, false)
}
func declarationProjectStateTx(ctx context.Context, tx *sql.Tx, projectID string, lock bool) (DeclarationProjectState, error) {
	out := DeclarationProjectState{Repositories: map[string]string{}}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	registry := map[string]any{}
	queries := map[string]string{
		"project":      `SELECT to_jsonb(p)-'indexing_policy'-'backup_policy' FROM projects.projects p WHERE project_id=$1`,
		"registration": `SELECT to_jsonb(r)-'contract_json'-'validation_report_json'-'registration_plan_json' FROM projects.project_contract_registrations r WHERE project_id=$1`,
		"source":       `SELECT to_jsonb(s)-'source_snapshot_json' FROM projects.project_repository_sources s WHERE project_id=$1`,
	}
	for _, key := range []string{"project", "registration", "source"} {
		query := queries[key]
		var raw []byte
		err := tx.QueryRowContext(ctx, query+suffix, projectID).Scan(&raw)
		if err == sql.ErrNoRows {
			raw = []byte(`null`)
		} else if err != nil {
			return out, err
		}
		registry[key] = json.RawMessage(raw)
	}
	rows, err := tx.QueryContext(ctx, `SELECT member_key,m.repository_id,jsonb_build_object('membership',to_jsonb(m),'repository',to_jsonb(r)) FROM projects.project_repository_memberships m JOIN projects.repositories r USING(repository_id) WHERE project_id=$1 ORDER BY member_key`+suffix, projectID)
	if err != nil {
		return out, err
	}
	members := []json.RawMessage{}
	for rows.Next() {
		var key, id string
		var raw []byte
		if err = rows.Scan(&key, &id, &raw); err != nil {
			rows.Close()
			return out, err
		}
		out.Repositories[key] = id
		members = append(members, json.RawMessage(raw))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	registry["members"] = members
	var lifecycle []byte
	err = tx.QueryRowContext(ctx, `SELECT jsonb_build_object('status',status,'archive_state',archive_state,'home_node_id',home_node_id,'owner_actor_id',owner_actor_id) FROM projects.projects WHERE project_id=$1`, projectID).Scan(&lifecycle)
	if err == sql.ErrNoRows {
		lifecycle = []byte(`{"exists":false}`)
	} else if err != nil {
		return out, err
	}
	raw, err := json.Marshal(registry)
	if err != nil {
		return out, err
	}
	raw, err = canonicalStoredRegistrationPlan(raw)
	if err != nil {
		return out, err
	}
	out.RegistryRevision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	lifecycle, err = canonicalStoredRegistrationPlan(lifecycle)
	if err != nil {
		return out, err
	}
	out.LifecycleRevision = fmt.Sprintf("sha256:%x", sha256.Sum256(lifecycle))
	return out, nil
}

// DeclarationRegistrationPayload is the complete registration intent carried
// by the coordinator. It contains paths, source digests and declarations only;
// source bytes/validator inventories stay outside the operation journal.
type DeclarationRegistrationPayload struct {
	ProjectRoot  string                             `json:"project_root"`
	ContractPath string                             `json:"contract_path"`
	ContractHash string                             `json:"contract_hash"`
	Project      ProjectContractProjectInput        `json:"project"`
	Repositories []ProjectRepositoryValidatedMember `json:"repositories"`
	IntentHash   string                             `json:"intent_hash,omitempty"`
	Declaration  json.RawMessage                    `json:"declaration,omitempty"`
	Predecessor  *DeclarationLegacyPredecessor      `json:"predecessor,omitempty"`
}

func BuildDeclarationRegistrationPayload(input RegisterProjectContractInput) (DeclarationRegistrationPayload, error) {
	normalized, err := normalizeRegisterProjectContractInput(input)
	if err != nil {
		return DeclarationRegistrationPayload{}, err
	}
	source, err := buildDeclarationRepositorySource(normalized, normalized.Project.ID)
	if err != nil {
		return DeclarationRegistrationPayload{}, err
	}
	return DeclarationRegistrationPayload{ProjectRoot: normalized.ProjectRoot, ContractPath: normalized.ContractPath, ContractHash: normalized.ContractHash, Project: normalized.Project, Repositories: source.Members}, nil
}

type DeclarationOwnerRequest struct {
	OperationID       string
	ActionID          string
	Token             string
	InputHash         string
	TargetNodeID      string
	Expected          DeclarationProjectState
	AuthorityRevision string
	Predecessor       *DeclarationLegacyPredecessor
}
type DeclarationOwnerResult struct {
	EffectRef string                  `json:"effect_ref"`
	Before    DeclarationProjectState `json:"before"`
	After     DeclarationProjectState `json:"after"`
}
type DeclarationCommitFence interface{ Check(context.Context) error }

// ObserveDeclarationRegistration recovers only the exact authenticated effect
// token. No source or lifecycle drift is erased by the presence of a receipt.
func (s Service) ObserveDeclarationRegistration(ctx context.Context, req requestctx.Context, projectID string, call DeclarationOwnerRequest) (*DeclarationOwnerResult, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT result FROM projects.declaration_owner_receipts WHERE project_id=$1 AND owner='projects' AND token=$2 AND operation_id=$3 AND action_id=$4 AND input_hash=$5 AND actor_id=$6 AND origin_node_id=$7 AND target_node_id=$8`, projectID, call.Token, call.OperationID, call.ActionID, call.InputHash, req.ActorID, req.OriginNodeID, call.TargetNodeID).Scan(&raw)
	if err == sql.ErrNoRows {
		var exists bool
		if err = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects.declaration_owner_receipts WHERE project_id=$1 AND owner='projects' AND token=$2)`, projectID, call.Token).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("declaration owner token identity conflict")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result DeclarationOwnerResult
	if !declarationDecode(raw, &result) {
		return nil, fmt.Errorf("invalid declaration owner evidence")
	}
	return &result, nil
}

// RegisterDeclaration commits the existing project/whole repository-set
// transaction and exact owner dedup evidence together. It exposes no SQL hook.
func (s Service) RegisterDeclaration(ctx context.Context, req requestctx.Context, input RegisterProjectContractInput, call DeclarationOwnerRequest, fence DeclarationCommitFence) (*DeclarationOwnerResult, error) {
	if call.ActionID != "register_project:project" {
		return nil, fmt.Errorf("atomic project registration action required")
	}
	if input.Project.Status == "archived" {
		return nil, fmt.Errorf("project archive owner required")
	}
	if fence == nil {
		return nil, fmt.Errorf("declaration commit fence required")
	}
	var desired DeclarationRegistrationPayload
	var err error
	if call.Predecessor != nil {
		desired, err = BuildDeclarationLegacyRegistrationIntent(input, call.Predecessor)
	} else {
		desired, err = BuildDeclarationRegistrationPayload(input)
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		return nil, err
	}
	raw, err = declarationIntentCanonical(raw)
	if err != nil {
		return nil, err
	}
	if call.InputHash != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
		return nil, fmt.Errorf("declaration registration input hash mismatch")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockProjectRepositoryRegistrationProjectTx(ctx, tx, input); err != nil {
		return nil, err
	}
	// Lock project first, matching legacy registration/archive lock ordering.
	project, err := getProjectByIDOrSlugTx(ctx, tx, input.Project.ID, input.Project.Slug, true)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		if project.ProjectID != input.Project.ID || project.Slug != input.Project.Slug || project.HomeNodeID == nil || *project.HomeNodeID != call.TargetNodeID {
			return nil, fmt.Errorf("declaration project identity/location conflict")
		}
		if err = EnsureProjectMutable(project, "project_registration", project.ProjectID); err != nil {
			return nil, err
		}
	}
	target, err := resolveNodeRefTx(ctx, tx, input.Project.OwnerNode)
	if err != nil || target != call.TargetNodeID {
		return nil, fmt.Errorf("declaration target mismatch")
	}
	repositoryIDs := []string{}
	for _, m := range desired.Repositories {
		if m.RepositoryID != "" {
			repositoryIDs = append(repositoryIDs, m.RepositoryID)
		}
	}
	currentRows, err := tx.QueryContext(ctx, `SELECT repository_id FROM projects.project_repository_memberships WHERE project_id=$1`, input.Project.ID)
	if err != nil {
		return nil, err
	}
	for currentRows.Next() {
		var id string
		if err = currentRows.Scan(&id); err != nil {
			currentRows.Close()
			return nil, err
		}
		repositoryIDs = append(repositoryIDs, id)
	}
	err = currentRows.Err()
	currentRows.Close()
	if err != nil {
		return nil, err
	}
	if err = lockProjectRepositoryRegistrationRepositoriesTx(ctx, tx, repositoryIDs); err != nil {
		return nil, err
	}
	authorityIDs := []string{}
	for _, m := range desired.Repositories {
		if m.RepositoryID != "" {
			authorityIDs = append(authorityIDs, m.RepositoryID)
		}
	}
	authority, err := declarationRepositoryAuthorityTx(ctx, tx, req, input.Project.ID, target, authorityIDs, true)
	if err != nil {
		return nil, err
	}
	if !authority.CanWrite || authority.Revision != call.AuthorityRevision {
		return nil, fmt.Errorf("declaration authority changed or refused: %s", authority.Prerequisite)
	}
	// Bind token to D3a's original typed action and authenticated target. This
	// prevents an arbitrary owner call inventing a journal action/foreign token.
	var journalValid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects.declaration_operations o JOIN projects.declaration_action_receipts a USING(operation_id) WHERE o.operation_id=$1 AND a.action_id=$2 AND a.token=$3 AND a.input_hash=$4 AND a.owner='projects' AND o.actor_id=$5 AND o.origin_node_id=$6 AND o.project_id=$7 AND o.target_node_id=$8 AND o.project_root=$9 AND o.state<>'superseded' AND o.resolution#>>'{plan,basis,revisions,projects:registry}'=$10 AND o.resolution#>>'{plan,basis,revisions,projects:lifecycle}'=$11 AND o.resolution#>>ARRAY['plan','basis','revisions','authorization:'||$5||':'||$8]=$12)`, call.OperationID, call.ActionID, call.Token, call.InputHash, req.ActorID, req.OriginNodeID, input.Project.ID, target, input.ProjectRoot, call.Expected.RegistryRevision, call.Expected.LifecycleRevision, call.AuthorityRevision).Scan(&journalValid)
	if err != nil {
		return nil, err
	}
	if !journalValid {
		return nil, fmt.Errorf("declaration journal identity conflict")
	}
	var originalPayload []byte
	if err = tx.QueryRowContext(ctx, `SELECT resolution->'payloads'->$2 FROM projects.declaration_operations WHERE operation_id=$1`, call.OperationID, call.ActionID).Scan(&originalPayload); err != nil || !declarationEqual(json.RawMessage(originalPayload), desired) {
		return nil, fmt.Errorf("declaration journal payload mismatch")
	}
	var stored []byte
	err = tx.QueryRowContext(ctx, `SELECT result FROM projects.declaration_owner_receipts WHERE project_id=$1 AND owner='projects' AND token=$2 AND operation_id=$3 AND action_id=$4 AND input_hash=$5 AND actor_id=$6 AND origin_node_id=$7 AND target_node_id=$8`, input.Project.ID, call.Token, call.OperationID, call.ActionID, call.InputHash, req.ActorID, req.OriginNodeID, target).Scan(&stored)
	if err == nil {
		var result DeclarationOwnerResult
		if !declarationDecode(stored, &result) {
			return nil, fmt.Errorf("invalid declaration owner receipt")
		}
		return &result, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	before, err := declarationProjectStateTx(ctx, tx, input.Project.ID, true)
	if err != nil {
		return nil, err
	}
	if !declarationEqual(before, call.Expected) {
		return nil, fmt.Errorf("declaration project prerequisite CAS failed")
	}
	var registered RegisterProjectContractResult
	if p := call.Predecessor; p != nil {
		// The project FOR UPDATE lock also fences insertions through the watch
		// table's project FK. Lock every existing member before comparing the set.
		actual, err := readDeclarationLegacyPredecessorTx(ctx, tx, req, input, p.Contract, target, p.PhysicalRoot, true)
		if err != nil || !declarationEqual(actual, p) {
			return nil, fmt.Errorf("legacy registration predecessor CAS failed")
		}
		normalized, source, _, err := legacyDeclarationInput(input)
		if err != nil {
			return nil, err
		}
		registered, err = registerPreparedProjectContractTx(ctx, tx, req, preparedProjectRegistration{input: normalized, source: &source})
		if err != nil {
			return nil, err
		}
		members, err := readLegacyMembersTx(ctx, tx, input.Project.ID, true)
		if err != nil || !legacyValueEqual(members, p.Members) {
			return nil, fmt.Errorf("legacy watch set changed during registration")
		}
	} else {
		registered, err = registerProjectContractTx(ctx, tx, req, input)
	}
	if err != nil {
		return nil, err
	}
	after, err := declarationProjectStateTx(ctx, tx, input.Project.ID, true)
	if err != nil {
		return nil, err
	}
	result := DeclarationOwnerResult{EffectRef: registered.Detail.Registration.ProjectContractRegistrationID, Before: before, After: after}
	raw, err = json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects.declaration_owner_receipts(project_id,owner,token,input_hash,operation_id,action_id,actor_id,origin_node_id,target_node_id,result) VALUES($1,'projects',$2,$3,$4,$5,$6,$7,$8,$9)`, input.Project.ID, call.Token, call.InputHash, call.OperationID, call.ActionID, req.ActorID, req.OriginNodeID, target, raw); err != nil {
		return nil, err
	}
	if err = fence.Check(ctx); err != nil {
		return nil, err
	}
	currentAuthority, err := declarationRepositoryAuthorityTx(ctx, tx, req, input.Project.ID, target, authorityIDs, true)
	if err != nil {
		return nil, err
	}
	if !currentAuthority.CanWrite || currentAuthority.Revision != call.AuthorityRevision {
		return nil, fmt.Errorf("declaration authority expired or changed before commit")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &result, nil
}

// DeclarationWatchGroup is one bounded combined desired snapshot. Each named
// contributor must first commit its own authorized intent. It contains source
// references and exact portable/effective configs, never source inventories.
type DeclarationWatchSource struct {
	Ref           string `json:"ref"`
	SchemaVersion string `json:"schema_version"`
	Hash          string `json:"hash"`
	Revision      string `json:"revision"`
}
type DeclarationWatchContributor struct {
	ActionID string `json:"action_id"`
	Owner    string `json:"owner"`
	Resource string `json:"resource"`
	Retire   bool   `json:"retire"`
}
type DeclarationWatchRoot struct {
	LocalRootKey       string          `json:"local_root_key"`
	BackendRootKey     string          `json:"backend_root_key"`
	WorkerKey          string          `json:"worker_key"`
	SourceKinds        []string        `json:"source_kinds"`
	Enabled            bool            `json:"enabled"`
	CompilerConfigHash string          `json:"compiler_config_hash"`
	CompilerConfigJSON []byte          `json:"compiler_config_json"`
	ConfigHash         string          `json:"config_hash"`
	ConfigJSON         json.RawMessage `json:"config_json"`
	KnowledgeSource    json.RawMessage `json:"knowledge_source,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
}
type DeclarationWatchGroup struct {
	ProjectID    string                        `json:"project_id"`
	ProjectRoot  string                        `json:"project_root"`
	NodeID       string                        `json:"node_id"`
	NodeKey      string                        `json:"node_key"`
	Sources      []DeclarationWatchSource      `json:"sources"`
	Contributors []DeclarationWatchContributor `json:"contributors"`
	Roots        []DeclarationWatchRoot        `json:"roots"`
	Predecessor  *DeclarationLegacyPredecessor `json:"predecessor,omitempty"`
}
type DeclarationWatchToken struct {
	ActionID  string `json:"action_id"`
	Owner     string `json:"owner"`
	Resource  string `json:"resource"`
	Token     string `json:"token"`
	InputHash string `json:"input_hash"`
}

const DeclarationWatchControlSchemaVersion = "project.watch.control.v1"

type DeclarationWatchIntentPayload struct {
	Group     DeclarationWatchGroup `json:"group"`
	GroupHash string                `json:"group_hash"`
	Owner     string                `json:"owner"`
	Resource  string                `json:"resource"`
	Retire    bool                  `json:"retire"`
}
type DeclarationWatchControlPayload struct {
	SchemaVersion string                             `json:"schema_version"`
	OperationID   string                             `json:"operation_id"`
	GroupHash     string                             `json:"group_hash"`
	Evidence      communication.DesiredStateEvidence `json:"evidence"`
	Group         DeclarationWatchGroup              `json:"group"`
	Tokens        []DeclarationWatchToken            `json:"tokens"`
}
type DeclarationWatchAcknowledgement struct {
	SchemaVersion string                             `json:"schema_version"`
	OperationID   string                             `json:"operation_id"`
	ProjectID     string                             `json:"project_id"`
	NodeID        string                             `json:"node_id"`
	GroupHash     string                             `json:"group_hash"`
	Evidence      communication.AppliedStateEvidence `json:"evidence"`
	ErrorCode     string                             `json:"error_code,omitempty"`
}
type DeclarationWatchIntentRequest struct {
	DeclarationOwnerRequest
	Payload          DeclarationWatchIntentPayload
	ExpectedRevision string
}
type DeclarationWatchIntentObservation struct {
	EffectRef       string
	BeforeRevision  string
	AfterRevision   string
	Stage           string
	Final           bool
	Queued          bool
	Applied         bool
	MessageID       string
	DesiredRevision int64
}
type DeclarationWatchResourceState struct {
	Owner      string
	Resource   string
	Revision   string
	Retire     bool
	Registered bool
	Queued     bool
	Applied    bool
	Pending    bool
}

// D3 input hashes use integer decimal JSON. The existing repository storage
// canonicalizer validates duplicates but has a different numeric spelling.
func declarationIntentCanonical(raw []byte) ([]byte, error) {
	if _, err := canonicalStoredRegistrationPlan(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var valid func(any) bool
	valid = func(v any) bool {
		switch x := v.(type) {
		case json.Number:
			return !strings.ContainsAny(x.String(), ".eE")
		case map[string]any:
			for _, v := range x {
				if !valid(v) {
					return false
				}
			}
		case []any:
			for _, v := range x {
				if !valid(v) {
					return false
				}
			}
		}
		return true
	}
	if !valid(value) {
		return nil, fmt.Errorf("declaration intent requires exact integer JSON")
	}
	return json.Marshal(value)
}
func declarationValueDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	raw, err = declarationIntentCanonical(raw)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

// ReadDeclarationWatchState is a pure current owner view. Pending final intents
// are visible as pending but do not replace the last completed prerequisite.
func (s Service) ReadDeclarationWatchState(ctx context.Context, projectID, nodeID string) ([]DeclarationWatchResourceState, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return readDeclarationWatchStateTx(ctx, tx, projectID, nodeID)
}
func readDeclarationWatchStateTx(ctx context.Context, tx *sql.Tx, projectID, nodeID string) ([]DeclarationWatchResourceState, error) {
	rows, err := tx.QueryContext(ctx, `WITH latest AS (
  SELECT DISTINCT ON(owner,resource_key) * FROM projects.declaration_watch_intents WHERE project_id=$1 AND node_id=$2 ORDER BY owner,resource_key,created_at DESC,operation_id DESC,action_id
 ), completed AS (
  SELECT DISTINCT ON(owner,resource_key) owner,resource_key,after_revision FROM projects.declaration_watch_intents WHERE project_id=$1 AND node_id=$2 AND stage<>'pending' ORDER BY owner,resource_key,created_at DESC,operation_id DESC,action_id
 ) SELECT i.owner,i.resource_key,coalesce(c.after_revision,'absent'),i.retire,
 EXISTS(SELECT 1 FROM projects.declaration_watch_deliveries d WHERE d.operation_id=i.operation_id AND d.project_id=i.project_id AND d.node_id=i.node_id AND d.group_hash=i.group_hash),
 EXISTS(SELECT 1 FROM projects.declaration_watch_intents f WHERE f.operation_id=i.operation_id AND f.group_hash=i.group_hash AND f.final_intent AND f.stage='applied')
 FROM latest i LEFT JOIN completed c USING(owner,resource_key) ORDER BY i.owner,i.resource_key LIMIT 501`, projectID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeclarationWatchResourceState{}
	for rows.Next() {
		var state DeclarationWatchResourceState
		if err = rows.Scan(&state.Owner, &state.Resource, &state.Revision, &state.Retire, &state.Queued, &state.Applied); err != nil {
			return nil, err
		}
		state.Registered = true
		state.Pending = !state.Applied
		out = append(out, state)
		if len(out) > 500 {
			return nil, fmt.Errorf("project watch resource bound exceeded")
		}
	}
	return out, rows.Err()
}
func currentDeclarationWatchRevision(states []DeclarationWatchResourceState, owner, resource string) string {
	for _, state := range states {
		if state.Owner == owner && state.Resource == resource {
			return state.Revision
		}
	}
	return "absent"
}

// ObserveDeclarationWatchIntent may consume only an already-authenticated exact
// node ACK during Apply/resume. Plan/status use ReadDeclarationWatchState and
// never run this acknowledgement-consumption transaction.
func (s Service) ObserveDeclarationWatchIntent(ctx context.Context, req requestctx.Context, call DeclarationWatchIntentRequest) (*DeclarationWatchIntentObservation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = acquireProjectRepositoryAdvisoryLocksTx(ctx, tx, "project", []string{"id:" + call.Payload.Group.ProjectID}); err != nil {
		return nil, err
	}
	var originalIDs []byte
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(b.value->>'repository_id') FILTER (WHERE coalesce(b.value->>'repository_id','')<>''),'[]') FROM projects.declaration_operations o CROSS JOIN LATERAL jsonb_each(o.resolution#>'{plan,basis,bindings}') b WHERE o.operation_id=$1 AND o.project_id=$2 AND o.target_node_id=$3 AND o.actor_id=$4 AND o.origin_node_id=$5`, call.OperationID, call.Payload.Group.ProjectID, call.TargetNodeID, req.ActorID, req.OriginNodeID).Scan(&originalIDs); err != nil {
		return nil, err
	}
	var repositoryIDs []string
	if json.Unmarshal(originalIDs, &repositoryIDs) != nil {
		return nil, fmt.Errorf("invalid watch authorization evidence")
	}
	if err = lockProjectRepositoryRegistrationRepositoriesTx(ctx, tx, repositoryIDs); err != nil {
		return nil, err
	}
	authority, err := declarationRepositoryAuthorityTx(ctx, tx, req, call.Payload.Group.ProjectID, call.TargetNodeID, repositoryIDs, true)
	if err != nil {
		return nil, err
	}
	if !authority.CanWrite || authority.Revision != call.AuthorityRevision {
		return nil, fmt.Errorf("current watch observation authority required")
	}
	observation, err := observeDeclarationWatchIntentTx(ctx, tx, req, call, true)
	if err != nil || observation == nil {
		return observation, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return observation, nil
}
func observeDeclarationWatchIntentTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, call DeclarationWatchIntentRequest, consumeAck bool) (*DeclarationWatchIntentObservation, error) {
	observation := &DeclarationWatchIntentObservation{EffectRef: "project-watch-intent:" + call.Token}
	var project, node, actor, origin, owner, resource, inputHash, groupHash, token string
	err := tx.QueryRowContext(ctx, `SELECT project_id,node_id,actor_id,origin_node_id,owner,resource_key,input_hash,group_hash,token,before_revision,after_revision,stage,final_intent FROM projects.declaration_watch_intents WHERE operation_id=$1 AND action_id=$2 FOR UPDATE`, call.OperationID, call.ActionID).Scan(&project, &node, &actor, &origin, &owner, &resource, &inputHash, &groupHash, &token, &observation.BeforeRevision, &observation.AfterRevision, &observation.Stage, &observation.Final)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if project != call.Payload.Group.ProjectID || node != call.TargetNodeID || actor != req.ActorID || origin != req.OriginNodeID || owner != call.Payload.Owner || resource != call.Payload.Resource || inputHash != call.InputHash || groupHash != call.Payload.GroupHash || token != call.Token {
		return nil, fmt.Errorf("project watch token identity conflict")
	}
	var status string
	var recordedAck bool
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT d.message_id,d.desired_revision,m.status,m.result_json,EXISTS(SELECT 1 FROM communication.message_acks a WHERE a.communication_message_id=m.communication_message_id AND a.node_id=m.node_id AND a.ack_status='completed' AND a.result_json=m.result_json) FROM projects.declaration_watch_deliveries d JOIN communication.messages m ON m.communication_message_id=d.message_id AND m.node_id=d.node_id WHERE d.operation_id=$1 AND d.project_id=$2 AND d.node_id=$3 AND d.group_hash=$4 AND m.kind=$5`, call.OperationID, project, node, groupHash, communication.KindProjectWatchReconcile).Scan(&observation.MessageID, &observation.DesiredRevision, &status, &raw, &recordedAck)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	observation.Queued = err == nil
	var ack DeclarationWatchAcknowledgement
	observed := err == nil && recordedAck && status == communication.StatusAcked && declarationDecode(raw, &ack) && ack.SchemaVersion == DeclarationWatchControlSchemaVersion && ack.OperationID == call.OperationID && ack.ProjectID == project && ack.NodeID == node && ack.GroupHash == groupHash && ack.Evidence.SchemaVersion == communication.ControlEvidenceSchemaVersion && ack.Evidence.DesiredRevision == observation.DesiredRevision && ack.Evidence.AppliedRevision == observation.DesiredRevision && ack.Evidence.ConfigHash == groupHash && ack.Evidence.Outcome == communication.ControlOutcomeCompleted && ack.ErrorCode == ""
	if observation.Final && observation.Stage == "pending" && observed && consumeAck {
		// Recording exact ACK evidence does not activate a worker. Update only this
		// generation's matching desired rows; never rewrite a newer source's facts.
		if _, err = tx.ExecContext(ctx, `UPDATE projects.declaration_watch_intents SET stage='applied' WHERE operation_id=$1 AND action_id=$2 AND stage='pending'`, call.OperationID, call.ActionID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projects.project_watched_root_registrations SET activation_status=CASE WHEN activation_status='pending_agent_apply' THEN 'applied' ELSE activation_status END,last_applied_by_actor_id=$4,last_applied_at=now() WHERE project_id=$1 AND node_id=$2 AND metadata#>>'{declaration_adapter,group_hash}'=$3`, project, node, groupHash, req.ActorID); err != nil {
			return nil, err
		}
		observation.Stage = "applied"
	}
	observation.Applied = observed
	return observation, nil
}

// CommitDeclarationWatchIntent registers only this owner's intent. The final
// contributor atomically persists the exact complete desired rows and stable
// communication message after revalidating every contributing authorization.
func (s Service) CommitDeclarationWatchIntent(ctx context.Context, req requestctx.Context, call DeclarationWatchIntentRequest, fence DeclarationCommitFence) (*DeclarationWatchIntentObservation, error) {
	group := call.Payload.Group
	if fence == nil || group.ProjectID == "" || group.NodeID != call.TargetNodeID || len(group.Contributors) == 0 || len(group.Contributors) > 500 || group.Roots == nil {
		return nil, fmt.Errorf("invalid declaration watch intent")
	}
	inputHash, err := declarationValueDigest(call.Payload)
	if err != nil || inputHash != call.InputHash {
		return nil, fmt.Errorf("watch intent input hash mismatch")
	}
	groupHash, err := declarationValueDigest(group)
	if err != nil || groupHash != call.Payload.GroupHash {
		return nil, fmt.Errorf("watch intent group hash mismatch")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = acquireProjectRepositoryAdvisoryLocksTx(ctx, tx, "project", []string{"id:" + group.ProjectID}); err != nil {
		return nil, err
	}
	project, err := getProjectByIDOrSlugTx(ctx, tx, group.ProjectID, "", true)
	if err != nil {
		return nil, err
	}
	if project.HomeNodeID == nil || *project.HomeNodeID != group.NodeID {
		return nil, fmt.Errorf("watch intent node ownership conflict")
	}
	if err = EnsureProjectMutable(project, "project_watch", group.ProjectID); err != nil {
		return nil, err
	}
	registration, err := getContractRegistrationByProjectTx(ctx, tx, group.ProjectID)
	if err != nil {
		return nil, err
	}
	if registration.ProjectRoot != group.ProjectRoot || registration.ContractSchemaVersion != ProjectRepositoryProjectSchemaV05 {
		return nil, fmt.Errorf("watch intent registered source conflict")
	}
	rootBound := false
	for _, source := range group.Sources {
		if source.Ref == ".loom/project.yaml" && source.Hash == registration.ContractHash && source.SchemaVersion == ProjectRepositoryProjectSchemaV05 {
			rootBound = true
		}
	}
	if !rootBound {
		return nil, fmt.Errorf("watch intent root source mismatch")
	}
	// Read the immutable D3a basis and exact typed payload for every contributor.
	var journal []byte
	err = tx.QueryRowContext(ctx, `SELECT resolution FROM projects.declaration_operations WHERE operation_id=$1 AND project_id=$2 AND target_node_id=$3 AND actor_id=$4 AND origin_node_id=$5 AND state<>'superseded'`, call.OperationID, group.ProjectID, group.NodeID, req.ActorID, req.OriginNodeID).Scan(&journal)
	if err != nil {
		return nil, err
	}
	var original struct {
		Plan struct {
			Basis struct {
				Revisions map[string]string `json:"revisions"`
				Bindings  map[string]struct {
					RepositoryID string `json:"repository_id"`
				} `json:"bindings"`
				Actions []struct {
					ID            string `json:"id"`
					Kind          string `json:"kind"`
					Owner         string `json:"owner"`
					Resource      string `json:"resource"`
					TargetRef     string `json:"target_ref"`
					InputHash     string `json:"input_hash"`
					Authorization struct {
						ActorID          string `json:"actor_id"`
						NodeID           string `json:"node_id"`
						Level            int    `json:"level"`
						PolicyRevision   string `json:"policy_revision"`
						ApprovalRequired bool   `json:"approval_required"`
					} `json:"authorization"`
				} `json:"actions"`
			} `json:"basis"`
		} `json:"plan"`
		Payloads map[string]json.RawMessage `json:"payloads"`
	}
	if !declarationDecode(journal, &original) {
		return nil, fmt.Errorf("invalid watch operation evidence")
	}
	repositoryIDs := []string{}
	for _, binding := range original.Plan.Basis.Bindings {
		if binding.RepositoryID != "" {
			repositoryIDs = append(repositoryIDs, binding.RepositoryID)
		}
	}
	if err = lockProjectRepositoryRegistrationRepositoriesTx(ctx, tx, repositoryIDs); err != nil {
		return nil, err
	}
	authority, err := declarationRepositoryAuthorityTx(ctx, tx, req, group.ProjectID, group.NodeID, repositoryIDs, true)
	if err != nil {
		return nil, err
	}
	if !authority.CanWrite || authority.Revision != call.AuthorityRevision {
		return nil, fmt.Errorf("watch intent authority changed or refused")
	}
	participants := map[string]DeclarationWatchContributor{}
	for _, c := range group.Contributors {
		if _, exists := participants[c.ActionID]; exists {
			return nil, fmt.Errorf("duplicate watch contributor")
		}
		participants[c.ActionID] = c
	}
	matched := 0
	selected := false
	for _, action := range original.Plan.Basis.Actions {
		if action.Owner != "knowledge" && action.Owner != "backupcontracts" {
			continue
		}
		contributor, exists := participants[action.ID]
		if !exists {
			return nil, fmt.Errorf("watch group omitted an original owner")
		}
		if contributor.Owner != action.Owner || contributor.Resource != action.Resource || action.TargetRef != group.ProjectID+"/"+contributor.Resource || action.Authorization.ActorID != req.ActorID || action.Authorization.NodeID != group.NodeID || action.Authorization.Level != 3 || action.Authorization.PolicyRevision != authority.Revision || action.Authorization.ApprovalRequired {
			return nil, fmt.Errorf("watch contributor authorization/identity conflict")
		}
		if contributor.Retire != (action.Kind == "retire_resource") {
			return nil, fmt.Errorf("watch retirement action mismatch")
		}
		var payload DeclarationWatchIntentPayload
		if !declarationDecode(original.Payloads[action.ID], &payload) || payload.Owner != contributor.Owner || payload.Resource != contributor.Resource || payload.Retire != contributor.Retire || payload.GroupHash != groupHash || !declarationEqual(payload.Group, group) {
			return nil, fmt.Errorf("watch contributor desired group mismatch")
		}
		hash, err := declarationValueDigest(payload)
		if err != nil || hash != action.InputHash {
			return nil, fmt.Errorf("watch contributor input hash mismatch")
		}
		if action.ID == call.ActionID {
			selected = payload.Owner == call.Payload.Owner && payload.Resource == call.Payload.Resource && payload.Retire == call.Payload.Retire && action.InputHash == call.InputHash
		}
		matched++
	}
	if !selected || matched != len(participants) {
		return nil, fmt.Errorf("exact watch contributor set required")
	}
	var validToken bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects.declaration_action_receipts WHERE operation_id=$1 AND action_id=$2 AND owner=$3 AND token=$4 AND input_hash=$5)`, call.OperationID, call.ActionID, call.Payload.Owner, call.Token, call.InputHash).Scan(&validToken); err != nil {
		return nil, err
	}
	if !validToken {
		return nil, fmt.Errorf("watch action token conflict")
	}
	if existing, err := observeDeclarationWatchIntentTx(ctx, tx, req, call, true); err != nil {
		return nil, err
	} else if existing != nil {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	}
	current, err := declarationProjectStateTx(ctx, tx, group.ProjectID, true)
	if err != nil {
		return nil, err
	}
	if !declarationEqual(current, call.Expected) {
		return nil, fmt.Errorf("watch project prerequisite CAS failed")
	}
	expectedRegistry := original.Plan.Basis.Revisions["projects:registry"]
	expectedLifecycle := original.Plan.Basis.Revisions["projects:lifecycle"]
	var registrationEvidence []byte
	err = tx.QueryRowContext(ctx, `SELECT result FROM projects.declaration_owner_receipts WHERE operation_id=$1 AND project_id=$2 AND owner='projects' AND action_id='register_project:project'`, call.OperationID, group.ProjectID).Scan(&registrationEvidence)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		var r DeclarationOwnerResult
		if !declarationDecode(registrationEvidence, &r) {
			return nil, fmt.Errorf("invalid registration owner evidence")
		}
		expectedRegistry = r.After.RegistryRevision
		expectedLifecycle = r.After.LifecycleRevision
	}
	if call.Expected.RegistryRevision != expectedRegistry || call.Expected.LifecycleRevision != expectedLifecycle {
		return nil, fmt.Errorf("watch registry prerequisite is not original owner evidence")
	}
	if p := group.Predecessor; p != nil {
		if err != nil || ValidateDeclarationLegacyPredecessor(p) != nil || p.RegistrationID != registration.ProjectContractRegistrationID || p.ProjectID != group.ProjectID || p.NodeID != group.NodeID || p.ProjectRoot != group.ProjectRoot {
			return nil, fmt.Errorf("legacy watch requires its registration receipt")
		}
		var accepted DeclarationRegistrationPayload
		if !declarationDecode(original.Payloads["register_project:project"], &accepted) || !declarationEqual(accepted.Predecessor, p) {
			return nil, fmt.Errorf("legacy watch predecessor differs from original registration")
		}
		actual, e := registeredLegacyIntent(registration, p)
		if e != nil || !declarationEqual(actual, accepted) {
			return nil, fmt.Errorf("legacy registered successor evidence changed")
		}
		hash, e := declarationValueDigest(accepted)
		matched := false
		for _, a := range original.Plan.Basis.Actions {
			if a.ID == "register_project:project" && a.Owner == "projects" && a.InputHash == hash {
				matched = true
			}
		}
		if e != nil || !matched {
			return nil, fmt.Errorf("legacy registration journal input mismatch")
		}
		members, e := readLegacyMembersTx(ctx, tx, group.ProjectID, true)
		if e != nil || !legacyValueEqual(members, p.Members) {
			return nil, fmt.Errorf("legacy watch predecessor CAS failed")
		}
	}
	states, err := readDeclarationWatchStateTx(ctx, tx, group.ProjectID, group.NodeID)
	if err != nil {
		return nil, err
	}
	if currentDeclarationWatchRevision(states, call.Payload.Owner, call.Payload.Resource) != call.ExpectedRevision {
		return nil, fmt.Errorf("watch owner prerequisite CAS failed")
	}
	if original.Plan.Basis.Revisions[call.Payload.Owner+":"+call.Payload.Resource] != call.ExpectedRevision {
		return nil, fmt.Errorf("watch owner prerequisite differs from original basis")
	}
	actionIDs := []string{}
	for id := range participants {
		actionIDs = append(actionIDs, id)
	}
	sort.Strings(actionIDs)
	final := call.ActionID == actionIDs[len(actionIDs)-1]
	after, err := declarationValueDigest(map[string]string{"token": call.Token, "input_hash": call.InputHash, "group_hash": groupHash, "owner": call.Payload.Owner, "resource": call.Payload.Resource})
	if err != nil {
		return nil, err
	}
	stage := "registered"
	if final {
		stage = "pending"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects.declaration_watch_intents(operation_id,action_id,project_id,node_id,actor_id,origin_node_id,owner,resource_key,token,input_hash,group_hash,authority_revision,before_revision,after_revision,retire,final_intent,stage) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, call.OperationID, call.ActionID, group.ProjectID, group.NodeID, req.ActorID, req.OriginNodeID, call.Payload.Owner, call.Payload.Resource, call.Token, call.InputHash, groupHash, authority.Revision, call.ExpectedRevision, after, call.Payload.Retire, final, stage); err != nil {
		return nil, err
	}
	if final {
		tokens := []DeclarationWatchToken{}
		for _, id := range actionIDs {
			c := participants[id]
			var token, inputHash, authRevision, stage string
			err = tx.QueryRowContext(ctx, `SELECT token,input_hash,authority_revision,stage FROM projects.declaration_watch_intents WHERE operation_id=$1 AND action_id=$2 AND project_id=$3 AND node_id=$4 AND group_hash=$5 AND owner=$6 AND resource_key=$7 AND actor_id=$8 AND origin_node_id=$9`, call.OperationID, id, group.ProjectID, group.NodeID, groupHash, c.Owner, c.Resource, req.ActorID, req.OriginNodeID).Scan(&token, &inputHash, &authRevision, &stage)
			if err != nil {
				return nil, fmt.Errorf("all exact watch intents must be committed: %w", err)
			}
			if authRevision != authority.Revision || (id != call.ActionID && stage != "registered") {
				return nil, fmt.Errorf("watch contributor prerequisite changed")
			}
			tokens = append(tokens, DeclarationWatchToken{ActionID: id, Owner: c.Owner, Resource: c.Resource, Token: token, InputHash: inputHash})
		}
		if err = persistDeclarationWatchRowsTx(ctx, tx, req, registration, call.Payload); err != nil {
			return nil, err
		}
		var revision int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(desired_revision),0) FROM projects.declaration_watch_deliveries WHERE project_id=$1 AND node_id=$2`, group.ProjectID, group.NodeID).Scan(&revision); err != nil {
			return nil, err
		}
		if revision == math.MaxInt64 {
			return nil, fmt.Errorf("watch desired revision exhausted")
		}
		revision++
		payload := DeclarationWatchControlPayload{SchemaVersion: DeclarationWatchControlSchemaVersion, OperationID: call.OperationID, GroupHash: groupHash, Evidence: communication.NewDesiredStateEvidence(group.NodeID, revision, groupHash), Group: group, Tokens: tokens}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		message, err := communication.EnqueueProjectWatchTx(ctx, tx, req, group.NodeID, "declaration-watch:"+call.Token, raw)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO projects.declaration_watch_deliveries(project_id,node_id,desired_revision,operation_id,group_hash,message_id) VALUES($1,$2,$3,$4,$5,$6)`, group.ProjectID, group.NodeID, revision, call.OperationID, groupHash, message.CommunicationMessageID); err != nil {
			return nil, err
		}
		// A newer explicit desired source owns this project's queue only. No remote
		// payload is deleted; the node also rejects stale/source-mismatched delivery.
		if _, err = tx.ExecContext(ctx, `UPDATE communication.messages m SET status='cancelled',updated_at=now() FROM projects.declaration_watch_deliveries d WHERE m.communication_message_id=d.message_id AND d.project_id=$1 AND d.node_id=$2 AND d.desired_revision<$3 AND m.status IN ('available','pending','claimed','delivered','failed_retryable')`, group.ProjectID, group.NodeID, revision); err != nil {
			return nil, err
		}
	}
	if err = fence.Check(ctx); err != nil {
		return nil, err
	}
	latest, err := declarationRepositoryAuthorityTx(ctx, tx, req, group.ProjectID, group.NodeID, repositoryIDs, true)
	if err != nil {
		return nil, err
	}
	if !latest.CanWrite || latest.Revision != authority.Revision {
		return nil, fmt.Errorf("watch contributor authority expired before commit")
	}
	observation, err := observeDeclarationWatchIntentTx(ctx, tx, req, call, false)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return observation, nil
}
func persistDeclarationWatchRowsTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, registration ProjectContractRegistration, payload DeclarationWatchIntentPayload) error {
	group := payload.Group
	keys := []string{}
	for _, root := range group.Roots {
		var config struct {
			RootKey          string `json:"root_key"`
			SafeRootKey      string `json:"safe_root_key"`
			RootRelativePath string `json:"root_relative_path"`
			DisplayName      string `json:"display_name"`
			Sync             struct {
				Mode string `json:"mode"`
			} `json:"sync_policy"`
			Backup struct {
				Mode string `json:"mode"`
			} `json:"backup_policy"`
			Index struct {
				Mode string `json:"mode"`
			} `json:"index_policy"`
			Delete struct {
				Mode string `json:"mode"`
			} `json:"delete_policy"`
		}
		if !declarationDecode(root.ConfigJSON, &config) || config.RootKey != root.BackendRootKey {
			return fmt.Errorf("invalid effective watch root")
		}
		var node string
		var metadata []byte
		err := tx.QueryRowContext(ctx, `SELECT node_id,metadata FROM projects.project_watched_root_registrations WHERE project_id=$1 AND backend_root_key=$2 FOR UPDATE`, group.ProjectID, root.BackendRootKey).Scan(&node, &metadata)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			var prior map[string]json.RawMessage
			legacy := false
			if group.Predecessor != nil {
				for _, member := range group.Predecessor.Members {
					r := member.Registration
					if r.BackendRootKey == root.BackendRootKey && r.NodeID == node && declarationEqual(r.Metadata, json.RawMessage(metadata)) {
						legacy = true
					}
				}
			}
			if !declarationDecode(metadata, &prior) || !declarationWatchOwnedMetadata(prior, group.ProjectID) && !legacy || node != group.NodeID {
				return fmt.Errorf("existing watched root belongs to another source owner")
			}
		} else if group.Predecessor != nil {
			return fmt.Errorf("legacy watched root disappeared before adoption")
		}
		var meta map[string]json.RawMessage
		if !declarationDecode(root.Metadata, &meta) || meta == nil {
			return fmt.Errorf("watch compiler metadata required")
		}
		if _, exists := meta["declaration_adapter"]; exists {
			return fmt.Errorf("compiler metadata cannot supply adapter evidence")
		}
		meta["declaration_adapter"], err = json.Marshal(map[string]any{"schema_version": "project.watch.binding.v1", "project_id": group.ProjectID, "node_id": group.NodeID, "group_hash": payload.GroupHash, "safe_root_key": config.SafeRootKey, "compiler_config_json": root.CompilerConfigJSON, "compiler_config_hash": root.CompilerConfigHash, "effective_config_hash": root.ConfigHash})
		if err != nil {
			return err
		}
		metadata, err = json.Marshal(meta)
		if err != nil {
			return err
		}
		sourceKinds, err := json.Marshal(root.SourceKinds)
		if err != nil {
			return err
		}
		stage := ProjectWatchedRootRegistrationStatusPendingAgentApply
		if !root.Enabled {
			stage = ProjectWatchedRootRegistrationStatusDisabled
		}
		if _, err = upsertProjectWatchedRootRegistration(ctx, tx, req, UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: registration.ProjectContractRegistrationID, ProjectID: group.ProjectID, NodeID: group.NodeID, OwnerNodeKey: group.NodeKey, LocalRootKey: root.LocalRootKey, BackendRootKey: root.BackendRootKey, WorkerKey: root.WorkerKey, SourceKinds: sourceKinds, SafeRootKey: config.SafeRootKey, RootRelativePath: config.RootRelativePath, DisplayName: config.DisplayName, SyncMode: config.Sync.Mode, BackupMode: config.Backup.Mode, IndexMode: config.Index.Mode, DeleteMode: config.Delete.Mode, ConfigHash: root.ConfigHash, ConfigJSON: root.ConfigJSON, CommandJSON: json.RawMessage(`[]`), ActivationStatus: stage, Metadata: metadata}, false); err != nil {
			return err
		}
		keys = append(keys, root.BackendRootKey)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects.project_watched_root_registrations SET activation_status='stale',last_applied_at=NULL,last_applied_by_actor_id=NULL,metadata=jsonb_set(metadata,'{declaration_adapter,group_hash}',to_jsonb($4::text)),updated_at=now() WHERE project_id=$1 AND node_id=$2 AND metadata#>>'{declaration_adapter,schema_version}'='project.watch.binding.v1' AND NOT(backend_root_key=ANY($3::text[]))`, group.ProjectID, group.NodeID, keys, payload.GroupHash); err != nil {
		return err
	}
	return nil
}

func declarationWatchOwnedMetadata(metadata map[string]json.RawMessage, projectID string) bool {
	var adapter struct {
		SchemaVersion string `json:"schema_version"`
		ProjectID     string `json:"project_id"`
	}
	return declarationDecode(metadata["declaration_adapter"], &adapter) && adapter.SchemaVersion == "project.watch.binding.v1" && adapter.ProjectID == projectID
}

const DeclarationLegacyPredecessorSchema = "project.declaration_legacy_predecessor.v1"

// Historical source and ownership are immutable action data. Current source,
// authority and node-local observations remain separate validation boundaries.
type DeclarationLegacyPredecessor struct {
	SchemaVersion        string                    `json:"schema_version"`
	ProjectID            string                    `json:"project_id"`
	RegistrationID       string                    `json:"registration_id"`
	RegistrationRevision int                       `json:"registration_revision"`
	NodeID               string                    `json:"node_id"`
	NodeKey              string                    `json:"node_key"`
	ProjectRoot          string                    `json:"project_root"`
	PhysicalRoot         string                    `json:"physical_root"`
	ContractPath         string                    `json:"contract_path"`
	ContractHash         string                    `json:"contract_hash"`
	ContractSchema       string                    `json:"contract_schema_version"`
	Contract             json.RawMessage           `json:"contract"`
	Members              []DeclarationLegacyMember `json:"members"`
	Digest               string                    `json:"digest"`
}

type DeclarationLegacyMember struct {
	Registration ProjectWatchedRootRegistration `json:"registration"`
	Report       DeclarationLegacyReport        `json:"report"`
}

// Heartbeat time and counters do not identify a configuration generation.
type DeclarationLegacyReport struct {
	WatchedRootID string                     `json:"watched_root_id"`
	NodeID        string                     `json:"node_id"`
	RootKey       string                     `json:"root_key"`
	WorkerKey     string                     `json:"worker_key"`
	DisplayName   string                     `json:"display_name"`
	SafeRootKey   string                     `json:"safe_root_key"`
	Status        string                     `json:"status"`
	ConfigHash    string                     `json:"config_hash"`
	ConfigJSON    json.RawMessage            `json:"config_json"`
	CreatedAt     time.Time                  `json:"created_at"`
	Metadata      map[string]json.RawMessage `json:"metadata"`
}

func frozenLegacyWatch(row ProjectWatchedRootRegistration) ProjectWatchedRootRegistration {
	row.LastReportedAt = nil
	row.UpdatedAt = time.Time{}
	return row
}

func legacyPredecessorDigest(p DeclarationLegacyPredecessor) (string, error) {
	p.Digest = ""
	return declarationValueDigest(p)
}

func legacyValueEqual(a, b any) bool {
	return declarationEqual(map[string]any{"value": a}, map[string]any{"value": b})
}

// ValidateDeclarationLegacyPredecessor checks representation only. It never
// establishes that these rows exist or grants permission to adopt a worker.
func ValidateDeclarationLegacyPredecessor(p *DeclarationLegacyPredecessor) error {
	if p == nil || p.SchemaVersion != DeclarationLegacyPredecessorSchema || ids.Validate(ids.ProjectPrefix, p.ProjectID) != nil || ids.Validate(ids.ProjectContractRegistrationPrefix, p.RegistrationID) != nil || p.RegistrationRevision < 1 || ids.Validate(ids.NodePrefix, p.NodeID) != nil || p.NodeKey == "" || !filepath.IsAbs(p.ProjectRoot) || filepath.Clean(p.ProjectRoot) != p.ProjectRoot || !filepath.IsAbs(p.PhysicalRoot) || filepath.Clean(p.PhysicalRoot) != p.PhysicalRoot || p.ContractPath != filepath.Join(p.ProjectRoot, ".loom", "project.yaml") && p.ContractPath != filepath.Join(p.ProjectRoot, "loom.project.yaml") || !contractHashPattern.MatchString(p.ContractHash) || p.ContractSchema != "project.contract.v0.3" && p.ContractSchema != "project.contract.v0.4" || p.Members == nil || len(p.Members) > 500 {
		return fmt.Errorf("invalid legacy predecessor identity")
	}
	var old projectRepositoryContractDocument
	if !declarationDecode(p.Contract, &old) || old.Kind != "loom.project" || old.SchemaVersion != p.ContractSchema || old.Project.ID != p.ProjectID || old.Project.OwnerNode != p.NodeKey {
		return fmt.Errorf("invalid legacy predecessor source")
	}
	priorKey := ""
	seenIDs := map[string]bool{}
	for _, member := range p.Members {
		r, report := member.Registration, member.Report
		if r.ProjectID != p.ProjectID || r.ProjectContractRegistrationID != p.RegistrationID || r.NodeID != p.NodeID || r.OwnerNodeKey != p.NodeKey || ids.Validate(ids.ProjectWatchedRootRegistrationPrefix, r.ProjectWatchedRootRegistrationID) != nil || seenIDs[r.ProjectWatchedRootRegistrationID] || r.BackendRootKey <= priorKey || r.WorkerKey != "node-agent.watched_root."+r.BackendRootKey || r.LocalRootKey == "" || r.SafeRootKey != "project" || r.WatchedRootID == nil || ids.Validate(ids.WatchedRootPrefix, *r.WatchedRootID) != nil || r.LastAppliedAt == nil || r.LastAppliedAt.IsZero() || r.CreatedAt.IsZero() || r.ActivationStatus != "applied" && r.ActivationStatus != "reported" || !declarationEqual(r, frozenLegacyWatch(r)) || !contractHashPattern.MatchString(r.ConfigHash) {
			return fmt.Errorf("invalid legacy predecessor member")
		}
		seenIDs[r.ProjectWatchedRootRegistrationID], priorKey = true, r.BackendRootKey
		if report.WatchedRootID != *r.WatchedRootID || report.NodeID != r.NodeID || report.RootKey != r.BackendRootKey || report.WorkerKey != r.WorkerKey || report.DisplayName != r.DisplayName || report.SafeRootKey != r.SafeRootKey || report.Status != "active" && report.Status != "healthy" || report.ConfigHash != r.ConfigHash || !declarationEqual(report.ConfigJSON, r.ConfigJSON) || report.CreatedAt.IsZero() || report.Metadata == nil {
			return fmt.Errorf("invalid legacy predecessor report")
		}
		expected := map[string]any{"source": "loom-node-agent", "runtime_worker": r.WorkerKey, "root_reachable": true, "safe_root_key": r.SafeRootKey, "root_relative_path": r.RootRelativePath}
		for key, raw := range report.Metadata {
			value, exists := expected[key]
			if !exists || !legacyValueEqual(raw, value) {
				return fmt.Errorf("legacy report metadata disagrees with owner")
			}
		}
	}
	hash, err := legacyPredecessorDigest(*p)
	if err != nil || hash != p.Digest {
		return fmt.Errorf("legacy predecessor digest mismatch")
	}
	return nil
}

func legacyDeclarationInput(input RegisterProjectContractInput) (RegisterProjectContractInput, ProjectRepositoryValidatedSourceInput, declarationDocument, error) {
	var d declarationDocument
	var report, plan declarationEvidence
	var source ProjectRepositoryValidatedSourceInput
	if !declarationDecode(input.Contract, &d) || d.LegacyContracts == nil || input.ContractSchemaVersion != ProjectRepositoryProjectSchemaV05 || !declarationDecode(input.ValidationReport, &report) || !declarationDecode(input.RegistrationPlan, &plan) || report.Registerable || plan.Registerable {
		return input, source, d, fmt.Errorf("legacy declaration intent evidence required")
	}
	input, err := normalizeRegisterProjectContractFields(input)
	if err != nil {
		return input, source, d, err
	}
	source, err = buildDeclarationRepositoryEvidence(input, input.Project.ID)
	if err != nil || len(source.Members) != 0 {
		return input, source, d, fmt.Errorf("unsupported legacy declaration membership")
	}
	return input, source, d, nil
}

func registeredLegacyIntent(r ProjectContractRegistration, p *DeclarationLegacyPredecessor) (DeclarationRegistrationPayload, error) {
	var d declarationDocument
	if !declarationDecode(r.Contract, &d) {
		return DeclarationRegistrationPayload{}, fmt.Errorf("invalid registered declaration")
	}
	input := RegisterProjectContractInput{ProjectRoot: r.ProjectRoot, ContractPath: r.ContractPath, ContractHash: r.ContractHash, ContractSchemaVersion: r.ContractSchemaVersion, Contract: r.Contract, ValidationReport: r.ValidationReport, RegistrationPlan: r.RegistrationPlan, DerivedProviders: r.DerivedProviders, PolicyRefs: r.PolicyRefs, Metadata: r.Metadata, Project: d.Project}
	return BuildDeclarationLegacyRegistrationIntent(input, p)
}

// BuildDeclarationLegacyRegistrationIntent is a pure, untrusted payload builder.
// Only RegisterDeclaration authenticates the predecessor inside its transaction.
func BuildDeclarationLegacyRegistrationIntent(input RegisterProjectContractInput, predecessor *DeclarationLegacyPredecessor) (DeclarationRegistrationPayload, error) {
	normalized, source, d, err := legacyDeclarationInput(input)
	if err != nil {
		return DeclarationRegistrationPayload{}, err
	}
	if err = ValidateDeclarationLegacyPredecessor(predecessor); err != nil {
		return DeclarationRegistrationPayload{}, err
	}
	if predecessor.ProjectID != normalized.Project.ID || predecessor.NodeKey != normalized.Project.OwnerNode || predecessor.ProjectRoot != normalized.ProjectRoot || predecessor.ContractHash != d.LegacyContracts.Project.Digest || predecessor.ContractSchema != d.LegacyContracts.Project.SchemaVersion {
		return DeclarationRegistrationPayload{}, fmt.Errorf("legacy source/predecessor mismatch")
	}
	stable := normalized
	for _, target := range []*json.RawMessage{&stable.ValidationReport, &stable.RegistrationPlan} {
		var fields map[string]json.RawMessage
		if !declarationDecode(*target, &fields) || fields == nil {
			return DeclarationRegistrationPayload{}, fmt.Errorf("invalid mapped legacy evidence")
		}
		delete(fields, "generated_at")
		*target, err = json.Marshal(fields)
		if err != nil {
			return DeclarationRegistrationPayload{}, err
		}
	}
	hash, err := declarationValueDigest(stable)
	if err != nil {
		return DeclarationRegistrationPayload{}, err
	}
	return DeclarationRegistrationPayload{ProjectRoot: normalized.ProjectRoot, ContractPath: normalized.ContractPath, ContractHash: normalized.ContractHash, Project: normalized.Project, Repositories: source.Members, IntentHash: hash, Declaration: normalized.Contract, Predecessor: predecessor}, nil
}

func legacyPortableRows(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var plan declarationEvidence
	if !declarationDecode(raw, &plan) || plan.WatchedRoots == nil {
		return nil, fmt.Errorf("complete legacy watch plan required")
	}
	out := map[string]json.RawMessage{}
	for _, item := range plan.WatchedRoots {
		var fields map[string]json.RawMessage
		var key string
		if !declarationDecode(item, &fields) || json.Unmarshal(fields["backend_root_key"], &key) != nil || key == "" || out[key] != nil {
			return nil, fmt.Errorf("invalid legacy watch plan member")
		}
		out[key] = item
	}
	return out, nil
}

func legacyPortableProjection(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	var metadata map[string]json.RawMessage
	if !declarationDecode(raw, &fields) || !declarationDecode(fields["metadata"], &metadata) {
		return nil, fmt.Errorf("invalid legacy compiler metadata")
	}
	delete(metadata, "declaration_sources")
	fields["metadata"], _ = json.Marshal(metadata)
	return json.Marshal(fields)
}

func legacyWatchMatchesPortable(row ProjectWatchedRootRegistration, raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if !declarationDecode(raw, &fields) {
		return false
	}
	expected := map[string]any{"key": row.LocalRootKey, "backend_root_key": row.BackendRootKey, "worker_key": row.WorkerKey, "owner_node": row.OwnerNodeKey, "source_kinds": row.SourceKinds, "safe_root_key": row.SafeRootKey, "root_relative_path": row.RootRelativePath, "display_name": row.DisplayName, "sync_mode": row.SyncMode, "backup_mode": row.BackupMode, "index_mode": row.IndexMode, "delete_mode": row.DeleteMode, "config_hash": row.ConfigHash, "config_json": row.ConfigJSON, "metadata": row.Metadata}
	for key, value := range expected {
		if !legacyValueEqual(fields[key], value) {
			return false
		}
	}
	return true
}

func readLegacyMembersTx(ctx context.Context, tx *sql.Tx, projectID string, lock bool) ([]DeclarationLegacyMember, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, projectWatchedRootRegistrationSelectSQL()+` WHERE project_id=$1 ORDER BY backend_root_key LIMIT 501`+suffix, projectID)
	if err != nil {
		return nil, err
	}
	watches := []ProjectWatchedRootRegistration{}
	for rows.Next() {
		row, e := scanProjectWatchedRootRegistration(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		watches = append(watches, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(watches) > 500 {
		return nil, fmt.Errorf("legacy watch set bound")
	}
	out := []DeclarationLegacyMember{}
	for _, row := range watches {
		if row.WatchedRootID == nil || row.LastAppliedAt == nil {
			return nil, fmt.Errorf("legacy applied generation required")
		}
		var report DeclarationLegacyReport
		var metadata json.RawMessage
		var reported time.Time
		suffix := ""
		if lock {
			suffix = " FOR SHARE"
		}
		err = tx.QueryRowContext(ctx, `SELECT watched_root_id,node_id,root_key,worker_key,display_name,safe_root_key,status,config_hash,config_json,created_at,metadata,last_reported_at FROM watched_roots.roots WHERE watched_root_id=$1`+suffix, *row.WatchedRootID).Scan(&report.WatchedRootID, &report.NodeID, &report.RootKey, &report.WorkerKey, &report.DisplayName, &report.SafeRootKey, &report.Status, &report.ConfigHash, &report.ConfigJSON, &report.CreatedAt, &metadata, &reported)
		if err != nil {
			return nil, err
		}
		if reported.Before(*row.LastAppliedAt) {
			return nil, fmt.Errorf("legacy owner report predates applied generation")
		}
		var all map[string]json.RawMessage
		if !declarationDecode(metadata, &all) || all == nil {
			return nil, fmt.Errorf("legacy owner report metadata required")
		}
		report.Metadata = map[string]json.RawMessage{}
		for _, key := range []string{"source", "runtime_worker", "root_reachable", "safe_root_key", "root_relative_path"} {
			if raw, exists := all[key]; exists {
				report.Metadata[key] = raw
			}
		}
		out = append(out, DeclarationLegacyMember{Registration: frozenLegacyWatch(row), Report: report})
	}
	return out, nil
}

// ReadDeclarationLegacyPredecessor freezes eligibility only before conversion.
// It reads authoritative old rows independently from current v0.5 source bytes.
func (s Service) ReadDeclarationLegacyPredecessor(ctx context.Context, req requestctx.Context, input RegisterProjectContractInput, oldContract json.RawMessage, nodeID, physicalRoot string) (*DeclarationLegacyPredecessor, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return readDeclarationLegacyPredecessorTx(ctx, tx, req, input, oldContract, nodeID, physicalRoot, false)
}

func readDeclarationLegacyPredecessorTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input RegisterProjectContractInput, oldContract json.RawMessage, nodeID, physicalRoot string, lock bool) (*DeclarationLegacyPredecessor, error) {
	_, _, d, err := legacyDeclarationInput(input)
	if err != nil {
		return nil, err
	}
	observation, err := ReadDeclarationMigrationProjectTx(ctx, tx, req, input.Project.ID, nodeID)
	if err != nil {
		return nil, err
	}
	r := observation.Registration
	if observation.Completeness != "complete" || observation.Unsupported.Count() != 0 || len(observation.Members) != 0 || len(observation.WatchStates) != 0 || observation.Project.HomeNodeID == nil || *observation.Project.HomeNodeID != nodeID || EnsureProjectMutable(observation.Project, "project_declaration", input.Project.ID) != nil || r.ProjectRoot != input.ProjectRoot || r.ContractHash != d.LegacyContracts.Project.Digest || r.ContractSchemaVersion != d.LegacyContracts.Project.SchemaVersion || r.RegistrationStatus != "registered" || !declarationEqual(r.Contract, oldContract) {
		return nil, fmt.Errorf("legacy predecessor source/owner eligibility required")
	}
	old, err := legacyPortableRows(r.RegistrationPlan)
	if err != nil {
		return nil, err
	}
	var oldReport, oldPlan declarationEvidence
	if !declarationDecode(r.ValidationReport, &oldReport) || !declarationDecode(r.RegistrationPlan, &oldPlan) || !oldReport.OK || !oldReport.Registerable || !oldPlan.Registerable || !legacyValueEqual(oldReport.WatchedRoots, oldPlan.WatchedRoots) {
		return nil, fmt.Errorf("legacy validation and compiler evidence disagree")
	}
	next, err := legacyPortableRows(input.RegistrationPlan)
	if err != nil || len(old) != len(next) {
		return nil, fmt.Errorf("legacy compiler watch set changed")
	}
	for key, raw := range old {
		before, e := legacyPortableProjection(raw)
		after, e2 := legacyPortableProjection(next[key])
		if e != nil || e2 != nil || !declarationEqual(before, after) {
			return nil, fmt.Errorf("legacy complete watch intent changed")
		}
	}
	members, err := readLegacyMembersTx(ctx, tx, input.Project.ID, lock)
	if err != nil {
		return nil, err
	}
	if len(members) != len(old) {
		return nil, fmt.Errorf("legacy registered watch set differs from source")
	}
	for _, member := range members {
		if !legacyWatchMatchesPortable(member.Registration, old[member.Registration.BackendRootKey]) {
			return nil, fmt.Errorf("legacy registered watch differs from compiler")
		}
	}
	p := &DeclarationLegacyPredecessor{SchemaVersion: DeclarationLegacyPredecessorSchema, ProjectID: input.Project.ID, RegistrationID: r.ProjectContractRegistrationID, RegistrationRevision: r.RegistrationRevision, NodeID: nodeID, NodeKey: input.Project.OwnerNode, ProjectRoot: input.ProjectRoot, PhysicalRoot: physicalRoot, ContractPath: r.ContractPath, ContractHash: r.ContractHash, ContractSchema: r.ContractSchemaVersion, Contract: r.Contract, Members: members}
	p.Digest, err = legacyPredecessorDigest(*p)
	if err != nil {
		return nil, err
	}
	return p, ValidateDeclarationLegacyPredecessor(p)
}
