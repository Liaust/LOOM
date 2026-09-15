package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ProjectRepositoryReadMemberLimit = 500
	projectRepositoryReadFacetLimit  = 200
)

var ErrProjectRepositoryReadLimitExceeded = errors.New("project repository read limit exceeded")

// ProjectRepositoryReadModel is the bounded, typed input for deterministic
// observation. It deliberately omits raw contracts, registration plans,
// observation payloads, and arbitrary metadata.
type ProjectRepositoryReadModel struct {
	Project ProjectRepositoryReadProject  `json:"project"`
	Facets  []ProjectRepositoryReadFacet  `json:"facets"`
	Source  *ProjectRepositoryReadSource  `json:"source,omitempty"`
	Members []ProjectRepositoryReadMember `json:"members"`
}

type ProjectRepositoryReadProject struct {
	ProjectID       string    `json:"project_id"`
	ProjectScopeID  string    `json:"project_scope_id"`
	ProjectScopeKey string    `json:"project_scope_key"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ProjectRepositoryReadFacet struct {
	FacetKey    string `json:"facet_key"`
	Folder      string `json:"folder,omitempty"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"`
	Placeholder bool   `json:"placeholder"`
	Status      string `json:"status"`
}

type ProjectRepositoryReadSource struct {
	ProjectContractRegistrationID string    `json:"project_contract_registration_id"`
	ProjectContractSchemaVersion  string    `json:"project_contract_schema_version"`
	ReposContractSchemaVersion    string    `json:"repos_contract_schema_version"`
	ProjectRoot                   string    `json:"-"`
	ProjectContractPath           string    `json:"-"`
	ReposContractPath             string    `json:"-"`
	OwnerNode                     string    `json:"owner_node"`
	SemanticDigest                string    `json:"semantic_digest"`
	LocationDigest                string    `json:"location_digest"`
	SourceRevision                int64     `json:"source_revision"`
	RegisteredAt                  time.Time `json:"registered_at"`
}

type ProjectRepositoryReadMember struct {
	RepositoryID             string                              `json:"repository_id"`
	RepositoryOwnerProjectID string                              `json:"repository_owner_project_id"`
	Key                      string                              `json:"key"`
	Path                     string                              `json:"path"`
	Role                     ProjectRepositoryRole               `json:"role"`
	StateRoot                string                              `json:"state_root,omitempty"`
	MembershipLifecycle      RepositoryLifecycleStatus           `json:"membership_lifecycle"`
	RepositoryLifecycle      RepositoryLifecycleStatus           `json:"repository_lifecycle"`
	SourceBindingDigest      string                              `json:"source_binding_digest"`
	StoredObservationPosture ProjectRepositoryObservationPosture `json:"stored_observation_posture"`
	StoredReasonCode         string                              `json:"stored_reason_code,omitempty"`
	StoredObservedAt         *time.Time                          `json:"stored_observed_at,omitempty"`
	ObservationRevision      int64                               `json:"observation_revision"`
}

// ReadProjectRepositoryState takes one repeatable-read snapshot and refuses to
// return a partial member or facet set. Later paginated surfaces are a separate
// contract; deterministic observation must never silently omit canonical
// members.
func (s Service) ReadProjectRepositoryState(ctx context.Context, ref string) (ProjectRepositoryReadModel, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ProjectRepositoryReadModel{}, fmt.Errorf("project ref is required")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ProjectRepositoryReadModel{}, err
	}
	defer tx.Rollback()

	project, err := resolveProjectRepositoryReadProjectTx(ctx, tx, ref)
	if err != nil {
		return ProjectRepositoryReadModel{}, err
	}
	model := ProjectRepositoryReadModel{
		Project: ProjectRepositoryReadProject{
			ProjectID:       project.ProjectID,
			ProjectScopeID:  project.ProjectScopeID,
			ProjectScopeKey: project.ProjectScopeKey,
			Slug:            project.Slug,
			Name:            project.Name,
			Status:          project.Status,
			UpdatedAt:       project.UpdatedAt,
		},
		Facets:  []ProjectRepositoryReadFacet{},
		Members: []ProjectRepositoryReadMember{},
	}

	model.Facets, err = readProjectRepositoryFacetsTx(ctx, tx, project.ProjectID)
	if err != nil {
		return ProjectRepositoryReadModel{}, err
	}
	source, canonical, err := readProjectRepositorySourceTx(ctx, tx, project.ProjectID)
	if err != nil && err != sql.ErrNoRows {
		return ProjectRepositoryReadModel{}, err
	}
	if err == nil {
		model.Source = &ProjectRepositoryReadSource{
			ProjectContractRegistrationID: source.ProjectContractRegistrationID,
			ProjectContractSchemaVersion:  source.ProjectContractSchemaVersion,
			ReposContractSchemaVersion:    source.ReposContractSchemaVersion,
			ProjectRoot:                   source.ProjectRoot,
			ProjectContractPath:           source.ProjectContractPath,
			ReposContractPath:             source.ReposContractPath,
			OwnerNode:                     canonical.Snapshot.Location.OwnerNode,
			SemanticDigest:                source.SemanticDigest,
			LocationDigest:                source.LocationDigest,
			SourceRevision:                source.SourceRevision,
			RegisteredAt:                  source.RegisteredAt.UTC(),
		}
	}

	model.Members, err = readProjectRepositoryMembersTx(ctx, tx, project.ProjectID)
	if err != nil {
		return ProjectRepositoryReadModel{}, err
	}
	if model.Source == nil {
		var declaration bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects.project_contract_registrations WHERE project_id=$1 AND contract_schema_version=$2)`, project.ProjectID, ProjectRepositoryProjectSchemaV05).Scan(&declaration); err != nil {
			return ProjectRepositoryReadModel{}, err
		}
		if declaration {
			return ProjectRepositoryReadModel{}, fmt.Errorf("%w: declaration registration requires its source row", ErrProjectRepositoryPersistenceCorrupt)
		}
		if len(model.Members) != 0 {
			return ProjectRepositoryReadModel{}, fmt.Errorf("%w: members exist without a current source", ErrProjectRepositoryPersistenceCorrupt)
		}
	} else if err := validateProjectRepositoryReadModel(model, canonical); err != nil {
		return ProjectRepositoryReadModel{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectRepositoryReadModel{}, err
	}
	return model, nil
}

func resolveProjectRepositoryReadProjectTx(ctx context.Context, tx *sql.Tx, ref string) (Project, error) {
	row := tx.QueryRowContext(ctx, projectSelectSQL()+`
		WHERE p.project_id = $1
		   OR p.slug = $1
		   OR scope.scope_key = $1
		   OR scope.slug = $1
		ORDER BY
			CASE
				WHEN p.project_id = $1 THEN 0
				WHEN p.slug = $1 THEN 1
				WHEN scope.scope_key = $1 THEN 2
				ELSE 3
			END
		LIMIT 1
	`, ref)
	return scanProject(row)
}

func readProjectRepositoryFacetsTx(ctx context.Context, tx *sql.Tx, projectID string) ([]ProjectRepositoryReadFacet, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT facet_key, folder, enabled, present, placeholder, facet_status
		FROM projects.project_contract_facets
		WHERE project_id = $1
		ORDER BY facet_key
		LIMIT $2
	`, projectID, projectRepositoryReadFacetLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectRepositoryReadFacet{}
	for rows.Next() {
		var facet ProjectRepositoryReadFacet
		if err := rows.Scan(&facet.FacetKey, &facet.Folder, &facet.Enabled, &facet.Present, &facet.Placeholder, &facet.Status); err != nil {
			return nil, err
		}
		result = append(result, facet)
		if len(result) > projectRepositoryReadFacetLimit {
			return nil, fmt.Errorf("%w: more than %d project facets", ErrProjectRepositoryReadLimitExceeded, projectRepositoryReadFacetLimit)
		}
	}
	return result, rows.Err()
}

func readProjectRepositorySourceTx(ctx context.Context, tx *sql.Tx, projectID string) (ProjectRepositorySource, ProjectRepositoryCanonicalSource, error) {
	var source ProjectRepositorySource
	var snapshot []byte
	err := tx.QueryRowContext(ctx, `
		SELECT project_id, project_contract_registration_id,
		       project_contract_schema_version, repos_contract_schema_version,
		       project_root, project_contract_path, repos_contract_path,
		       project_contract_digest, repos_contract_digest, semantic_digest,
		       location_digest, source_snapshot_json, source_revision,
		       registered_by_actor_id, registered_at, created_at, updated_at
		FROM projects.project_repository_sources
		WHERE project_id = $1
	`, projectID).Scan(
		&source.ProjectID,
		&source.ProjectContractRegistrationID,
		&source.ProjectContractSchemaVersion,
		&source.ReposContractSchemaVersion,
		&source.ProjectRoot,
		&source.ProjectContractPath,
		&source.ReposContractPath,
		&source.ProjectContractDigest,
		&source.ReposContractDigest,
		&source.SemanticDigest,
		&source.LocationDigest,
		&snapshot,
		&source.SourceRevision,
		&source.RegisteredByActorID,
		&source.RegisteredAt,
		&source.CreatedAt,
		&source.UpdatedAt,
	)
	if err != nil {
		return ProjectRepositorySource{}, ProjectRepositoryCanonicalSource{}, err
	}
	source.SourceSnapshot = append(json.RawMessage(nil), snapshot...)
	canonical, err := decodeAndValidateProjectRepositorySourceRow(source)
	if err != nil {
		return ProjectRepositorySource{}, ProjectRepositoryCanonicalSource{}, err
	}
	if source.ProjectContractSchemaVersion == ProjectRepositoryProjectSchemaV05 {
		if err = validateSingleDeclarationSource(canonical.Snapshot.SourceVersions, source.ProjectRoot, source.ProjectContractPath, source.ReposContractPath, source.ProjectContractDigest, source.ReposContractDigest); err != nil {
			return ProjectRepositorySource{}, ProjectRepositoryCanonicalSource{}, err
		}
		var registration ProjectContractRegistration
		err = tx.QueryRowContext(ctx, `SELECT project_contract_registration_id,project_id,project_root,contract_path,contract_hash,contract_schema_version FROM projects.project_contract_registrations WHERE project_contract_registration_id=$1`, source.ProjectContractRegistrationID).Scan(&registration.ProjectContractRegistrationID, &registration.ProjectID, &registration.ProjectRoot, &registration.ContractPath, &registration.ContractHash, &registration.ContractSchemaVersion)
		if err != nil {
			return ProjectRepositorySource{}, ProjectRepositoryCanonicalSource{}, err
		}
		if registration.ProjectID != source.ProjectID || registration.ProjectRoot != source.ProjectRoot || registration.ContractPath != source.ProjectContractPath || registration.ContractHash != source.ProjectContractDigest || registration.ContractSchemaVersion != source.ProjectContractSchemaVersion {
			return ProjectRepositorySource{}, ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: declaration repository source differs from its referenced registration", ErrProjectRepositoryPersistenceCorrupt)
		}
	}
	return source, canonical, nil
}

func readProjectRepositoryMembersTx(ctx context.Context, tx *sql.Tx, projectID string) ([]ProjectRepositoryReadMember, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.repository_id, m.repository_owner_project_id, m.member_key,
		       m.member_path, m.role, m.state_root, m.lifecycle_status,
		       r.lifecycle_status, o.source_binding_digest,
		       o.observation_posture, o.reason_code, o.observed_at,
		       o.observation_revision
		FROM projects.project_repository_memberships m
		JOIN projects.repositories r ON r.repository_id = m.repository_id
		JOIN projects.project_repository_observations o
		  ON o.project_id = m.project_id AND o.repository_id = m.repository_id
		WHERE m.project_id = $1
		ORDER BY m.repository_id, m.member_key
		LIMIT $2
	`, projectID, ProjectRepositoryReadMemberLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectRepositoryReadMember{}
	for rows.Next() {
		var member ProjectRepositoryReadMember
		var observedAt sql.NullTime
		if err := rows.Scan(
			&member.RepositoryID,
			&member.RepositoryOwnerProjectID,
			&member.Key,
			&member.Path,
			&member.Role,
			&member.StateRoot,
			&member.MembershipLifecycle,
			&member.RepositoryLifecycle,
			&member.SourceBindingDigest,
			&member.StoredObservationPosture,
			&member.StoredReasonCode,
			&observedAt,
			&member.ObservationRevision,
		); err != nil {
			return nil, err
		}
		if observedAt.Valid {
			value := observedAt.Time.UTC()
			member.StoredObservedAt = &value
		}
		result = append(result, member)
		if len(result) > ProjectRepositoryReadMemberLimit {
			return nil, fmt.Errorf("%w: more than %d repository members", ErrProjectRepositoryReadLimitExceeded, ProjectRepositoryReadMemberLimit)
		}
	}
	return result, rows.Err()
}

func validateProjectRepositoryReadModel(model ProjectRepositoryReadModel, canonical ProjectRepositoryCanonicalSource) error {
	if model.Source == nil || model.Project.ProjectID != canonical.Snapshot.ProjectID {
		return fmt.Errorf("%w: project/source identity mismatch", ErrProjectRepositoryPersistenceCorrupt)
	}
	if len(model.Members) != len(canonical.Snapshot.Members) {
		return fmt.Errorf("%w: source and member counts differ", ErrProjectRepositoryPersistenceCorrupt)
	}
	for index, sourceMember := range canonical.Snapshot.Members {
		member := model.Members[index]
		if member.RepositoryID != sourceMember.RepositoryID ||
			member.Key != sourceMember.Key ||
			member.Path != sourceMember.Path ||
			member.Role != sourceMember.Role ||
			member.StateRoot != sourceMember.StateRoot ||
			member.SourceBindingDigest != sourceMember.ObservationBindingDigest ||
			member.ObservationRevision <= 0 {
			return fmt.Errorf("%w: source/member/observation row %d mismatch", ErrProjectRepositoryPersistenceCorrupt, index)
		}
		expectedMembershipLifecycle := RepositoryLifecycleActive
		if model.Project.Status == "archived" {
			expectedMembershipLifecycle = RepositoryLifecycleArchived
		}
		if member.MembershipLifecycle != expectedMembershipLifecycle {
			return fmt.Errorf("%w: member %s lifecycle does not match project lifecycle", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
		}
		switch member.Role {
		case ProjectRepositoryRolePrimary, ProjectRepositoryRoleComponent:
			if member.RepositoryOwnerProjectID != model.Project.ProjectID || member.RepositoryLifecycle != expectedMembershipLifecycle {
				return fmt.Errorf("%w: owning member %s has inconsistent ownership or lifecycle", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
			}
		case ProjectRepositoryRoleReference:
			if member.RepositoryOwnerProjectID == model.Project.ProjectID {
				return fmt.Errorf("%w: reference member %s claims local ownership", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
			}
		default:
			return fmt.Errorf("%w: member %s has unsupported role", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
		}
		switch member.StoredObservationPosture {
		case ProjectRepositoryObservationNotObserved:
			if member.StoredObservedAt != nil {
				return fmt.Errorf("%w: not-observed member %s has an observation time", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
			}
		case ProjectRepositoryObservationObserved, ProjectRepositoryObservationRemoteUnavailable:
			if member.StoredObservedAt == nil {
				return fmt.Errorf("%w: observed member %s has no observation time", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
			}
		default:
			return fmt.Errorf("%w: member %s has unsupported observation posture", ErrProjectRepositoryPersistenceCorrupt, member.RepositoryID)
		}
	}
	return nil
}
