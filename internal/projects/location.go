package projects

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

const ProjectLocationSnapshotSchemaVersion = "project.location_snapshot.v1"

var (
	ErrProjectLocationUnavailable = errors.New("project location database unavailable")
	ErrProjectLocationInvalid     = errors.New("invalid project location binding")
	ErrProjectLocationChanged     = errors.New("project location predecessor changed")
)

// ProjectLocationSnapshot is private, serializable review evidence, not a grant
// to move files. The caller owns same-Box custody, physical rename, runtime and
// resource/Notes dependency checks, and the durable operation/fence journal.
type ProjectLocationSnapshot struct {
	SchemaVersion    string                                  `json:"schema_version"`
	ProjectID        string                                  `json:"project_id"`
	Root             string                                  `json:"root"`
	Project          Project                                 `json:"project"`
	Registration     ProjectContractRegistration             `json:"registration"`
	RepositorySource *ProjectRepositorySource                `json:"repository_source,omitempty"`
	Repositories     []ProjectLocationRepository             `json:"repositories"`
	Observations     map[string]ProjectRepositoryObservation `json:"observations"`
	ExpectedDigest   string                                  `json:"expected_digest"`
}

type ProjectLocationRepository struct {
	Membership ProjectRepositoryMembership `json:"membership"`
	Repository RepositoryIdentity          `json:"repository"`
}

// ProjectLocationEvidence must be persisted in the caller's operation journal in
// the same transaction. Prior retains original source bytes; Next is the actual
// successor read back under the transaction, including its new revisions.
type ProjectLocationEvidence struct {
	OperationID string                  `json:"operation_id"`
	Prior       ProjectLocationSnapshot `json:"prior"`
	Next        ProjectLocationSnapshot `json:"next"`
}

type projectLocationPending struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// CurrentProjectLocationSnapshot accepts at most one operation ID for recovery
// reads while that exact operation's temporary metadata fence is installed.
// Only that top-level marker is excluded from ExpectedDigest. Installing a
// fence must preserve project.updated_at to reuse an earlier reviewed snapshot.
func (s Service) CurrentProjectLocationSnapshot(ctx context.Context, ref string, operationID ...string) (ProjectLocationSnapshot, error) {
	return s.projectLocationSnapshot(ctx, ref, true, operationID...)
}

// CurrentProjectLocationSnapshotLocked is for the trusted workspace owner that
// already holds AcquireProjectArchiveLocks for this project on another session.
// It acquires row locks only, never project or repository advisory locks. The
// caller must keep its session fence held through the complete operation.
func (s Service) CurrentProjectLocationSnapshotLocked(ctx context.Context, ref string, operationID ...string) (ProjectLocationSnapshot, error) {
	return s.projectLocationSnapshot(ctx, ref, false, operationID...)
}

func (s Service) projectLocationSnapshot(ctx context.Context, ref string, advisory bool, operationID ...string) (ProjectLocationSnapshot, error) {
	if s.DB == nil {
		return ProjectLocationSnapshot{}, ErrProjectLocationUnavailable
	}
	if len(operationID) > 1 || strings.TrimSpace(ref) == "" {
		return ProjectLocationSnapshot{}, ErrProjectLocationInvalid
	}
	op := ""
	if len(operationID) == 1 {
		op = operationID[0]
		if op == "" || strings.TrimSpace(op) != op {
			return ProjectLocationSnapshot{}, ErrProjectLocationInvalid
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProjectLocationSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := currentProjectLocationSnapshotTx(ctx, tx, ref, op, advisory)
	if err != nil {
		return ProjectLocationSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectLocationSnapshot{}, err
	}
	return snapshot, nil
}

// CommitProjectLocationTx changes only current location projections and appends
// repository source history. It neither commits tx nor clears the pending fence.
// The caller must already hold the project's session fence; this method acquires
// row locks only and must not reenter advisory locks on a different connection.
// On ANY error the caller must roll back tx, including a late journal failure.
// Historical contract/report/plan JSON, hashes, schemas, archive states, facets,
// resources, memberships and repository lifecycle rows are never rewritten.
func (s Service) CommitProjectLocationTx(ctx context.Context, tx *sql.Tx, snapshot ProjectLocationSnapshot, newRoot, operationID string) (ProjectLocationEvidence, error) {
	if tx == nil {
		return ProjectLocationEvidence{}, ErrProjectLocationUnavailable
	}
	if operationID == "" || strings.TrimSpace(operationID) != operationID {
		return ProjectLocationEvidence{}, ErrProjectLocationInvalid
	}
	if err := validateProjectLocationSnapshot(snapshot); err != nil {
		return ProjectLocationEvidence{}, err
	}
	digest, err := projectLocationSnapshotDigest(snapshot, operationID)
	if err != nil {
		return ProjectLocationEvidence{}, err
	}
	if digest != snapshot.ExpectedDigest {
		return ProjectLocationEvidence{}, ErrProjectLocationChanged
	}
	if !projectLocationRootValid(newRoot) || newRoot == snapshot.Root || projectLocationContains(snapshot.Root, newRoot) || projectLocationContains(newRoot, snapshot.Root) {
		return ProjectLocationEvidence{}, fmt.Errorf("%w: destination must be an exact non-overlapping absolute root", ErrProjectLocationInvalid)
	}
	if err := validateProjectLocationPending(snapshot.Project.Metadata, operationID, snapshot.Root, newRoot); err != nil {
		return ProjectLocationEvidence{}, err
	}
	current, err := readProjectLocationSnapshotTx(ctx, tx, snapshot.ProjectID, false)
	if err != nil {
		return ProjectLocationEvidence{}, err
	}
	current.ExpectedDigest, err = projectLocationSnapshotDigest(current, operationID)
	if err != nil {
		return ProjectLocationEvidence{}, err
	}
	if current.ExpectedDigest != snapshot.ExpectedDigest {
		return ProjectLocationEvidence{}, ErrProjectLocationChanged
	}
	if err := validateProjectLocationPending(current.Project.Metadata, operationID, snapshot.Root, newRoot); err != nil {
		return ProjectLocationEvidence{}, err
	}
	contractPath, err := projectLocationSuccessorPath(current.Registration.ContractPath, current.Root, newRoot)
	if err != nil {
		return ProjectLocationEvidence{}, err
	}
	if current.Registration.RegistrationRevision == math.MaxInt32 {
		return ProjectLocationEvidence{}, fmt.Errorf("%w: registration revision overflow", ErrProjectLocationInvalid)
	}
	var next *ProjectRepositoryCanonicalSource
	var change ProjectRepositorySourceChange
	if current.RepositorySource != nil {
		canonical, err := decodeAndValidateProjectRepositorySourceRow(*current.RepositorySource)
		if err != nil {
			return ProjectLocationEvidence{}, err
		}
		successor, sourceChange, err := projectLocationRepositorySuccessor(canonical, newRoot)
		if err != nil {
			return ProjectLocationEvidence{}, err
		}
		if current.RepositorySource.SourceRevision == math.MaxInt64 {
			return ProjectLocationEvidence{}, fmt.Errorf("%w: repository source revision overflow", ErrProjectLocationInvalid)
		}
		next, change = &successor, sourceChange
	}
	// Precompute every observation transition before the first write.
	transitions := []ProjectRepositoryObservationBindingTransition{}
	if next != nil {
		for _, binding := range next.ObservationBindings {
			observation := current.Observations[binding.RepositoryID]
			transition, err := PlanProjectRepositoryObservationBinding(&observation, binding)
			if err != nil {
				return ProjectLocationEvidence{}, err
			}
			transitions = append(transitions, transition)
		}
	}
	if err := projectLocationExecOne(ctx, tx, `
		UPDATE projects.project_contract_registrations
		SET project_root=$2, contract_path=$3,
		    registration_revision=registration_revision+1, updated_at=now()
		WHERE project_contract_registration_id=$1 AND registration_revision=$4
	`, current.Registration.ProjectContractRegistrationID, newRoot, contractPath, current.Registration.RegistrationRevision); err != nil {
		return ProjectLocationEvidence{}, err
	}
	// Set only the compatibility key; in particular retain the caller's fence and
	// unrelated metadata verbatim at JSONB value precision.
	if err := projectLocationExecOne(ctx, tx, `
		UPDATE projects.projects
		SET metadata=jsonb_set(metadata, '{project_root}', to_jsonb($2::text), true), updated_at=now()
		WHERE project_id=$1
	`, current.ProjectID, newRoot); err != nil {
		return ProjectLocationEvidence{}, err
	}
	if next != nil {
		if err := persistProjectLocationRepositoryTx(ctx, tx, *current.RepositorySource, *next, change, operationID); err != nil {
			return ProjectLocationEvidence{}, err
		}
		for _, transition := range transitions {
			o := transition.Observation
			if err := projectLocationExecOne(ctx, tx, `
				UPDATE projects.project_repository_observations
				SET source_binding_digest=$3, observation_posture=$4, reason_code=$5,
				    observed_at=$6, observation_json=$7, observation_revision=$8, updated_at=now()
				WHERE project_id=$1 AND repository_id=$2 AND observation_revision=$9
			`, o.ProjectID, o.RepositoryID, o.SourceBindingDigest, o.ObservationPosture,
				o.ReasonCode, o.ObservedAt, o.Observation, o.ObservationRevision, current.Observations[o.RepositoryID].ObservationRevision); err != nil {
				return ProjectLocationEvidence{}, err
			}
		}
	}
	// The marker still names the predecessor until the caller clears it. Read the
	// successor with that predecessor binding, rather than guessing a new marker.
	successor, err := readProjectLocationSnapshotTx(ctx, tx, current.ProjectID, false)
	if err != nil {
		return ProjectLocationEvidence{}, err
	}
	successor.ExpectedDigest, err = projectLocationSnapshotDigest(successor, operationID)
	if err != nil {
		return ProjectLocationEvidence{}, err
	}
	return ProjectLocationEvidence{OperationID: operationID, Prior: current, Next: successor}, nil
}

func currentProjectLocationSnapshotTx(ctx context.Context, tx *sql.Tx, ref, operationID string, advisory bool) (ProjectLocationSnapshot, error) {
	project, err := resolveProjectRepositoryReadProjectTx(ctx, tx, ref)
	if err != nil {
		return ProjectLocationSnapshot{}, err
	}
	if advisory {
		if err := lockProjectRepositoryRegistrationProjectTx(ctx, tx, RegisterProjectContractInput{Project: ProjectContractProjectInput{ID: project.ProjectID, Slug: project.Slug}}); err != nil {
			return ProjectLocationSnapshot{}, err
		}
	}
	snapshot, err := readProjectLocationSnapshotTx(ctx, tx, project.ProjectID, advisory)
	if err != nil {
		return ProjectLocationSnapshot{}, err
	}
	if snapshot.Project.Slug != project.Slug {
		return ProjectLocationSnapshot{}, ErrProjectLocationChanged
	}
	if err := validateProjectLocationPending(snapshot.Project.Metadata, operationID, snapshot.Root, ""); err != nil {
		return ProjectLocationSnapshot{}, err
	}
	snapshot.ExpectedDigest, err = projectLocationSnapshotDigest(snapshot, operationID)
	return snapshot, err
}

func readProjectLocationSnapshotTx(ctx context.Context, tx *sql.Tx, projectID string, advisory bool) (ProjectLocationSnapshot, error) {
	snapshot := ProjectLocationSnapshot{SchemaVersion: ProjectLocationSnapshotSchemaVersion, ProjectID: projectID, Repositories: []ProjectLocationRepository{}}
	var err error
	snapshot.Project, err = scanProject(tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id=$1 FOR UPDATE OF p`, projectID))
	if err != nil {
		return snapshot, err
	}
	mainID, err := resolveNodeRefTx(ctx, tx, "main")
	if err != nil {
		return snapshot, err
	}
	if snapshot.Project.HomeNodeID == nil || *snapshot.Project.HomeNodeID != mainID {
		return snapshot, fmt.Errorf("%w: project is not Main-owned", ErrProjectLocationInvalid)
	}
	snapshot.Registration, err = scanContractRegistration(tx.QueryRowContext(ctx, contractRegistrationSelectSQL()+` WHERE project_id=$1 FOR UPDATE`, projectID))
	if err != nil {
		return snapshot, err
	}
	snapshot.Root = snapshot.Registration.ProjectRoot
	source, err := getProjectRepositorySourceTx(ctx, tx, projectID)
	if err != nil && err != sql.ErrNoRows {
		return snapshot, err
	}
	if err == nil {
		snapshot.RepositorySource = &source
		canonical, err := decodeAndValidateProjectRepositorySourceRow(source)
		if err != nil {
			return snapshot, err
		}
		repositoryIDs := make([]string, 0, len(canonical.Snapshot.Members))
		for _, member := range canonical.Snapshot.Members {
			repositoryIDs = append(repositoryIDs, member.RepositoryID)
		}
		if advisory {
			if err := lockProjectRepositoryRegistrationRepositoriesTx(ctx, tx, repositoryIDs); err != nil {
				return snapshot, err
			}
		}
	}
	members, err := listProjectRepositoryPersistedMembersTx(ctx, tx, projectID)
	if err != nil {
		return snapshot, err
	}
	for _, member := range members {
		snapshot.Repositories = append(snapshot.Repositories, ProjectLocationRepository{Membership: member.Membership, Repository: member.Repository})
	}
	snapshot.Observations, err = listProjectRepositoryObservationsTx(ctx, tx, projectID)
	if err != nil {
		return snapshot, err
	}
	return snapshot, validateProjectLocationSnapshot(snapshot)
}

func validateProjectLocationSnapshot(s ProjectLocationSnapshot) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrProjectLocationInvalid, reason) }
	p, r := s.Project, s.Registration
	if s.SchemaVersion != ProjectLocationSnapshotSchemaVersion || s.ProjectID == "" || s.ProjectID != p.ProjectID || r.ProjectID != p.ProjectID || p.ProjectScopeID == "" || p.ProjectScopeKey != "project:"+p.Slug || p.Status != "active" || p.HomeNodeID == nil {
		return invalid("active project identity/scope required")
	}
	if r.ProjectContractRegistrationID == "" || r.RegistrationRevision <= 0 || r.RegistrationStatus != ProjectRegistrationStatusRegistered || r.ProjectRoot != s.Root || !projectLocationRootValid(s.Root) || !projectRepositoryDigestPattern.MatchString(r.ContractHash) {
		return invalid("current registration binding required")
	}
	if _, err := projectLocationSuccessorPath(r.ContractPath, s.Root, s.Root); err != nil {
		return err
	}
	var contract projectArchiveContractDocument
	if json.Unmarshal(r.Contract, &contract) != nil || contract.Kind != "loom.project" || contract.SchemaVersion != r.ContractSchemaVersion || contract.Project.OwnerNode != "main" || contract.Project.Slug != p.Slug || (contract.Project.ID != p.ProjectID && (contract.Project.ID != "" || r.ContractSchemaVersion != ProjectRepositoryProjectSchemaV03)) || projectStatusFromContract(contract.Project.Status) != p.Status {
		return invalid("contract identity/owner/schema differs")
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(p.Metadata, &metadata) != nil || metadata == nil {
		return invalid("project metadata must be an object")
	}
	if raw, ok := metadata["project_root"]; ok {
		var root string
		if json.Unmarshal(raw, &root) != nil || root != s.Root {
			return invalid("compatibility project_root differs")
		}
	}
	if s.RepositorySource == nil {
		if len(s.Repositories) != 0 || len(s.Observations) != 0 {
			return invalid("repository projection exists without source")
		}
		// Do not invent a repos.yaml or empty canonical source for older,
		// genuinely repository-less registrations/declarations.
		var declaration struct {
			Resources map[string]struct {
				Kind string `json:"kind"`
			} `json:"resources"`
		}
		if err := json.Unmarshal(r.Contract, &declaration); err != nil {
			return err
		}
		for _, resource := range declaration.Resources {
			if resource.Kind == "repository" {
				return invalid("declared repository has no canonical source")
			}
		}
		return nil
	}
	canonical, err := decodeAndValidateProjectRepositorySourceRow(*s.RepositorySource)
	if err != nil {
		return err
	}
	source := s.RepositorySource
	if source.ProjectID != s.ProjectID || source.ProjectContractRegistrationID != r.ProjectContractRegistrationID || source.ProjectRoot != s.Root || source.ProjectContractPath != r.ContractPath || source.ProjectContractDigest != r.ContractHash || source.ProjectContractSchemaVersion != r.ContractSchemaVersion || canonical.Snapshot.Location.OwnerNode != "main" {
		return invalid("source and registration disagree")
	}
	if _, err := projectLocationSuccessorPath(source.ReposContractPath, s.Root, s.Root); err != nil {
		return err
	}
	if len(s.Repositories) != len(canonical.Snapshot.Members) || len(s.Observations) != len(s.Repositories) {
		return invalid("incomplete repository projection")
	}
	for i, member := range canonical.Snapshot.Members {
		persisted := s.Repositories[i]
		m, repo := persisted.Membership, persisted.Repository
		if m.ProjectID != s.ProjectID || m.RepositoryID != member.RepositoryID || repo.RepositoryID != member.RepositoryID || m.RepositoryOwnerProjectID != repo.OwningProjectID || m.MemberKey != member.Key || m.MemberPath != member.Path || m.Role != member.Role || m.StateRoot != member.StateRoot {
			return invalid("repository membership differs from source")
		}
		if (m.LifecycleStatus != RepositoryLifecycleActive && m.LifecycleStatus != RepositoryLifecycleArchived) || (repo.LifecycleStatus != RepositoryLifecycleActive && repo.LifecycleStatus != RepositoryLifecycleArchived) || (m.Role == ProjectRepositoryRoleReference) == (repo.OwningProjectID == s.ProjectID) {
			return invalid("repository lifecycle/ownership is invalid")
		}
		o, ok := s.Observations[member.RepositoryID]
		if !ok || o.ProjectID != s.ProjectID || o.RepositoryID != member.RepositoryID || o.SourceBindingDigest != member.ObservationBindingDigest {
			return invalid("repository observation binding differs")
		}
		if err := validateProjectRepositoryObservation(o); err != nil {
			return err
		}
	}
	return nil
}

func projectLocationRepositorySuccessor(current ProjectRepositoryCanonicalSource, root string) (ProjectRepositoryCanonicalSource, ProjectRepositorySourceChange, error) {
	s := current.Snapshot
	projectPath, err := projectLocationSuccessorPath(s.Location.ProjectContractPath, s.Location.ProjectRoot, root)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, ProjectRepositorySourceChange{}, err
	}
	reposPath, err := projectLocationSuccessorPath(s.Location.ReposContractPath, s.Location.ProjectRoot, root)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, ProjectRepositorySourceChange{}, err
	}
	members := make([]ProjectRepositoryValidatedMember, 0, len(s.Members))
	for _, m := range s.Members {
		members = append(members, ProjectRepositoryValidatedMember{RepositoryID: m.RepositoryID, Key: m.Key, Path: m.Path, Role: m.Role, StateRoot: m.StateRoot})
	}
	next, err := BuildProjectRepositoryCanonicalSource(ProjectRepositoryValidatedSourceInput{
		ProjectID: s.ProjectID, ProjectRoot: root, ProjectContractPath: projectPath, ReposContractPath: reposPath,
		OwnerNode: s.Location.OwnerNode, Versions: s.SourceVersions, ProjectContractDigest: s.ProjectContractDigest,
		ReposContractDigest: s.ReposContractDigest, RegistrationPlan: s.StableRegistrationPlan, Members: members,
	})
	if err != nil {
		return next, ProjectRepositorySourceChange{}, err
	}
	change, err := ClassifyProjectRepositorySourceChange(&current, next)
	if err == nil && (change.Classification != ProjectRepositorySourceClassificationRelocation || next.SemanticDigest != current.SemanticDigest || next.LocationDigest == current.LocationDigest) {
		err = fmt.Errorf("%w: successor must be location-only", ErrProjectLocationInvalid)
	}
	return next, change, err
}

func persistProjectLocationRepositoryTx(ctx context.Context, tx *sql.Tx, prior ProjectRepositorySource, next ProjectRepositoryCanonicalSource, change ProjectRepositorySourceChange, operationID string) error {
	s := next.Snapshot
	if err := projectLocationExecOne(ctx, tx, `
		UPDATE projects.project_repository_sources
		SET project_root=$2, project_contract_path=$3, repos_contract_path=$4,
		    location_digest=$5, source_snapshot_json=$6, source_revision=source_revision+1, updated_at=now()
		WHERE project_id=$1 AND source_revision=$7
	`, prior.ProjectID, s.Location.ProjectRoot, s.Location.ProjectContractPath, s.Location.ReposContractPath,
		next.LocationDigest, next.SnapshotJSON, prior.SourceRevision); err != nil {
		return err
	}
	// There is no actor argument at this boundary. Retain the source registrar;
	// the authorizing actor and reviewed operation belong to the caller's journal.
	summary, err := json.Marshal(struct {
		ProjectRepositoryChangeSummary
		OperationID         string `json:"operation_id"`
		PriorLocationDigest string `json:"prior_location_digest"`
	}{change.Summary, operationID, prior.LocationDigest})
	if err != nil {
		return err
	}
	return projectLocationExecOne(ctx, tx, `
		INSERT INTO projects.project_repository_source_history (
			project_id, project_contract_schema_version, repos_contract_schema_version,
			project_root, project_contract_path, repos_contract_path,
			project_contract_digest, repos_contract_digest, semantic_digest, location_digest,
			source_snapshot_json, source_revision, source_change_kind, change_summary_json, accepted_by_actor_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`, prior.ProjectID, prior.ProjectContractSchemaVersion, prior.ReposContractSchemaVersion,
		s.Location.ProjectRoot, s.Location.ProjectContractPath, s.Location.ReposContractPath,
		prior.ProjectContractDigest, prior.ReposContractDigest, prior.SemanticDigest, next.LocationDigest,
		next.SnapshotJSON, prior.SourceRevision+1, change.HistoryChangeKind, summary, prior.RegisteredByActorID)
}

func projectLocationSnapshotDigest(snapshot ProjectLocationSnapshot, operationID string) (string, error) {
	metadata, err := projectLocationStableMetadata(snapshot.Project.Metadata, operationID)
	if err != nil {
		return "", err
	}
	snapshot.Project.Metadata = metadata
	snapshot.ExpectedDigest = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	stable, err := canonicalStoredRegistrationPlan(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(stable)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func projectLocationStableMetadata(raw json.RawMessage, operationID string) (json.RawMessage, error) {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return nil, ErrProjectLocationInvalid
	}
	if pending, ok := metadata["workspace_filesystem_operation"]; ok {
		var marker projectLocationPending
		if operationID == "" || json.Unmarshal(pending, &marker) != nil || marker.ID != operationID {
			return nil, fmt.Errorf("%w: another workspace filesystem operation is pending", ErrProjectLocationChanged)
		}
		delete(metadata, "workspace_filesystem_operation")
	}
	return json.Marshal(metadata)
}

func validateProjectLocationPending(raw json.RawMessage, operationID, source, destination string) error {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	if pending, ok := metadata["workspace_filesystem_operation"]; ok {
		var marker projectLocationPending
		if operationID == "" || json.Unmarshal(pending, &marker) != nil || marker.ID != operationID || !projectLocationRootValid(marker.Source) || !projectLocationRootValid(marker.Destination) {
			return fmt.Errorf("%w: workspace filesystem operation binding differs", ErrProjectLocationChanged)
		}
		valid := false
		switch marker.Kind {
		case "relocate_project":
			valid = marker.Source == source && (destination == "" || marker.Destination == destination)
		case "retain_child":
			// Child retention fences the enclosing project's snapshot, but can
			// never authorize CommitProjectLocationTx to move that project root.
			valid = destination == "" && projectLocationContains(source, marker.Source)
		}
		if !valid {
			return fmt.Errorf("%w: workspace filesystem operation binding differs", ErrProjectLocationChanged)
		}
	}
	return nil
}

func projectLocationRootValid(root string) bool {
	return root != "" && root != string(filepath.Separator) && filepath.IsAbs(root) && filepath.Clean(root) == root && !strings.ContainsAny(root, "\x00\r\n")
}

func projectLocationContains(root, path string) bool {
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func projectLocationSuccessorPath(path, oldRoot, newRoot string) (string, error) {
	if !projectLocationRootValid(oldRoot) || !projectLocationRootValid(newRoot) || !projectLocationRootValid(path) || !projectLocationContains(oldRoot, path) {
		return "", fmt.Errorf("%w: contract path is not beneath exact predecessor root", ErrProjectLocationInvalid)
	}
	return newRoot + strings.TrimPrefix(path, oldRoot), nil
}

func projectLocationExecOne(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		err = ErrProjectLocationChanged
	}
	return err
}
