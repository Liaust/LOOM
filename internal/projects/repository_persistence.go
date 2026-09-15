package projects

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// These are the exact project/repositories versions emitted by the accepted
	// Slice 1 validator. The external contract test locks them to that package.
	ProjectRepositoryProjectSchemaV03 = "project.contract.v0.3"
	ProjectRepositoryProjectSchemaV04 = "project.contract.v0.4"
	ProjectRepositoryProjectSchemaV05 = "project.contract.v0.5"
	ProjectRepositoryReposSchemaV03   = "repos.contract.v0.3"
	ProjectRepositoryReposSchemaV04   = "repos.contract.v0.4"
)

type RepositoryLifecycleStatus string

const (
	RepositoryLifecycleActive   RepositoryLifecycleStatus = "active"
	RepositoryLifecycleArchived RepositoryLifecycleStatus = "archived"
)

type ProjectRepositoryRole string

const (
	ProjectRepositoryRolePrimary   ProjectRepositoryRole = "primary"
	ProjectRepositoryRoleComponent ProjectRepositoryRole = "component"
	ProjectRepositoryRoleReference ProjectRepositoryRole = "reference"
)

type ProjectRepositoryObservationPosture string

const (
	ProjectRepositoryObservationNotObserved       ProjectRepositoryObservationPosture = "not_observed"
	ProjectRepositoryObservationObserved          ProjectRepositoryObservationPosture = "observed"
	ProjectRepositoryObservationRemoteUnavailable ProjectRepositoryObservationPosture = "remote_unavailable"
)

type ProjectRepositorySourceChangeKind string

const (
	ProjectRepositorySourceFirstRegistration           ProjectRepositorySourceChangeKind = "first_registration"
	ProjectRepositorySourceSemanticChange              ProjectRepositorySourceChangeKind = "semantic_change"
	ProjectRepositorySourceRelocation                  ProjectRepositorySourceChangeKind = "source_relocation"
	ProjectRepositorySourceSemanticChangeAndRelocation ProjectRepositorySourceChangeKind = "semantic_change_and_relocation"
)

type RepositoryIdentity struct {
	RepositoryID    string                    `json:"repository_id"`
	OwningProjectID string                    `json:"owning_project_id"`
	LifecycleStatus RepositoryLifecycleStatus `json:"lifecycle_status"`
	Metadata        json.RawMessage           `json:"metadata"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
}

type ProjectRepositoryMembership struct {
	ProjectID                string                    `json:"project_id"`
	RepositoryID             string                    `json:"repository_id"`
	RepositoryOwnerProjectID string                    `json:"repository_owner_project_id"`
	MemberKey                string                    `json:"member_key"`
	MemberPath               string                    `json:"member_path"`
	Role                     ProjectRepositoryRole     `json:"role"`
	StateRoot                string                    `json:"state_root,omitempty"`
	LifecycleStatus          RepositoryLifecycleStatus `json:"lifecycle_status"`
	Metadata                 json.RawMessage           `json:"metadata"`
	CreatedAt                time.Time                 `json:"created_at"`
	UpdatedAt                time.Time                 `json:"updated_at"`
}

type ProjectRepositorySourceVersions struct {
	ProjectContract string `json:"project_contract_schema_version"`
	ReposContract   string `json:"repos_contract_schema_version"`
}

type ProjectRepositorySource struct {
	ProjectID                     string          `json:"project_id"`
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectContractSchemaVersion  string          `json:"project_contract_schema_version"`
	ReposContractSchemaVersion    string          `json:"repos_contract_schema_version"`
	ProjectRoot                   string          `json:"project_root"`
	ProjectContractPath           string          `json:"project_contract_path"`
	ReposContractPath             string          `json:"repos_contract_path"`
	ProjectContractDigest         string          `json:"project_contract_digest"`
	ReposContractDigest           string          `json:"repos_contract_digest"`
	SemanticDigest                string          `json:"semantic_digest"`
	LocationDigest                string          `json:"location_digest"`
	SourceSnapshot                json.RawMessage `json:"source_snapshot"`
	SourceRevision                int64           `json:"source_revision"`
	RegisteredByActorID           string          `json:"registered_by_actor_id"`
	RegisteredAt                  time.Time       `json:"registered_at"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type ProjectRepositorySourceHistory struct {
	ProjectID                    string                            `json:"project_id"`
	ProjectContractSchemaVersion string                            `json:"project_contract_schema_version"`
	ReposContractSchemaVersion   string                            `json:"repos_contract_schema_version"`
	ProjectRoot                  string                            `json:"project_root"`
	ProjectContractPath          string                            `json:"project_contract_path"`
	ReposContractPath            string                            `json:"repos_contract_path"`
	ProjectContractDigest        string                            `json:"project_contract_digest"`
	ReposContractDigest          string                            `json:"repos_contract_digest"`
	SemanticDigest               string                            `json:"semantic_digest"`
	LocationDigest               string                            `json:"location_digest"`
	SourceSnapshot               json.RawMessage                   `json:"source_snapshot"`
	SourceRevision               int64                             `json:"source_revision"`
	SourceChangeKind             ProjectRepositorySourceChangeKind `json:"source_change_kind"`
	ChangeSummary                json.RawMessage                   `json:"change_summary"`
	AcceptedByActorID            string                            `json:"accepted_by_actor_id"`
	AcceptedAt                   time.Time                         `json:"accepted_at"`
}

type ProjectRepositoryObservation struct {
	ProjectID           string                              `json:"project_id"`
	RepositoryID        string                              `json:"repository_id"`
	SourceBindingDigest string                              `json:"source_binding_digest"`
	ObservationPosture  ProjectRepositoryObservationPosture `json:"observation_posture"`
	ReasonCode          string                              `json:"reason_code,omitempty"`
	ObservedAt          *time.Time                          `json:"observed_at,omitempty"`
	Observation         json.RawMessage                     `json:"observation"`
	ObservationRevision int64                               `json:"observation_revision"`
	CreatedAt           time.Time                           `json:"created_at"`
	UpdatedAt           time.Time                           `json:"updated_at"`
}

type ProjectRepositorySourceVersionTransitionKind string

const (
	ProjectRepositorySourceVersionFirstRegistration ProjectRepositorySourceVersionTransitionKind = "first_registration"
	ProjectRepositorySourceVersionSame              ProjectRepositorySourceVersionTransitionKind = "same_version"
	ProjectRepositorySourceVersionV03ToV04          ProjectRepositorySourceVersionTransitionKind = "v0.3_to_v0.4"
	ProjectRepositorySourceVersionToV05             ProjectRepositorySourceVersionTransitionKind = "to_v0.5"
)

var (
	ErrUnsupportedProjectRepositorySourceVersions          = errors.New("unsupported project/repository source versions")
	ErrUnsupportedProjectRepositorySourceVersionTransition = errors.New("unsupported project/repository source version transition")
)

type ProjectRepositorySourceVersionTransition struct {
	Kind ProjectRepositorySourceVersionTransitionKind `json:"kind"`
	From *ProjectRepositorySourceVersions             `json:"from,omitempty"`
	To   ProjectRepositorySourceVersions              `json:"to"`
}

type projectRepositorySourceVersionEdge struct {
	from ProjectRepositorySourceVersions
	to   ProjectRepositorySourceVersions
}

var (
	projectRepositorySourceV03V03 = ProjectRepositorySourceVersions{ProjectContract: ProjectRepositoryProjectSchemaV03, ReposContract: ProjectRepositoryReposSchemaV03}
	projectRepositorySourceV03V04 = ProjectRepositorySourceVersions{ProjectContract: ProjectRepositoryProjectSchemaV03, ReposContract: ProjectRepositoryReposSchemaV04}
	projectRepositorySourceV04V03 = ProjectRepositorySourceVersions{ProjectContract: ProjectRepositoryProjectSchemaV04, ReposContract: ProjectRepositoryReposSchemaV03}
	projectRepositorySourceV04V04 = ProjectRepositorySourceVersions{ProjectContract: ProjectRepositoryProjectSchemaV04, ReposContract: ProjectRepositoryReposSchemaV04}
	projectRepositorySourceV05V05 = ProjectRepositorySourceVersions{ProjectContract: ProjectRepositoryProjectSchemaV05, ReposContract: ProjectRepositoryProjectSchemaV05}
)

var supportedProjectRepositorySourceVersions = []ProjectRepositorySourceVersions{
	projectRepositorySourceV03V03,
	projectRepositorySourceV03V04,
	projectRepositorySourceV04V03,
	projectRepositorySourceV04V04,
	projectRepositorySourceV05V05,
}

// Keep the graph literal: a missing edge is a rejection, including every
// downgrade and mixed upgrade/downgrade.
var supportedProjectRepositorySourceVersionTransitions = map[projectRepositorySourceVersionEdge]ProjectRepositorySourceVersionTransitionKind{
	{from: projectRepositorySourceV03V03, to: projectRepositorySourceV03V03}: ProjectRepositorySourceVersionSame,
	{from: projectRepositorySourceV03V03, to: projectRepositorySourceV03V04}: ProjectRepositorySourceVersionV03ToV04,
	{from: projectRepositorySourceV03V03, to: projectRepositorySourceV04V03}: ProjectRepositorySourceVersionV03ToV04,
	{from: projectRepositorySourceV03V03, to: projectRepositorySourceV04V04}: ProjectRepositorySourceVersionV03ToV04,
	{from: projectRepositorySourceV03V04, to: projectRepositorySourceV03V04}: ProjectRepositorySourceVersionSame,
	{from: projectRepositorySourceV03V04, to: projectRepositorySourceV04V04}: ProjectRepositorySourceVersionV03ToV04,
	{from: projectRepositorySourceV04V03, to: projectRepositorySourceV04V03}: ProjectRepositorySourceVersionSame,
	{from: projectRepositorySourceV04V03, to: projectRepositorySourceV04V04}: ProjectRepositorySourceVersionV03ToV04,
	{from: projectRepositorySourceV04V04, to: projectRepositorySourceV04V04}: ProjectRepositorySourceVersionSame,
	{from: projectRepositorySourceV03V03, to: projectRepositorySourceV05V05}: ProjectRepositorySourceVersionToV05,
	{from: projectRepositorySourceV03V04, to: projectRepositorySourceV05V05}: ProjectRepositorySourceVersionToV05,
	{from: projectRepositorySourceV04V03, to: projectRepositorySourceV05V05}: ProjectRepositorySourceVersionToV05,
	{from: projectRepositorySourceV04V04, to: projectRepositorySourceV05V05}: ProjectRepositorySourceVersionToV05,
	{from: projectRepositorySourceV05V05, to: projectRepositorySourceV05V05}: ProjectRepositorySourceVersionSame,
}

func SupportedProjectRepositorySourceVersions() []ProjectRepositorySourceVersions {
	return append([]ProjectRepositorySourceVersions(nil), supportedProjectRepositorySourceVersions...)
}

func PlanProjectRepositorySourceVersionTransition(current *ProjectRepositorySourceVersions, next ProjectRepositorySourceVersions) (ProjectRepositorySourceVersionTransition, error) {
	if !isSupportedProjectRepositorySourceVersions(next) {
		return ProjectRepositorySourceVersionTransition{}, fmt.Errorf("%w: proposed project=%q repos=%q", ErrUnsupportedProjectRepositorySourceVersions, next.ProjectContract, next.ReposContract)
	}
	if current == nil {
		return ProjectRepositorySourceVersionTransition{Kind: ProjectRepositorySourceVersionFirstRegistration, To: next}, nil
	}

	from := *current
	if !isSupportedProjectRepositorySourceVersions(from) {
		return ProjectRepositorySourceVersionTransition{}, fmt.Errorf("%w: current project=%q repos=%q", ErrUnsupportedProjectRepositorySourceVersions, from.ProjectContract, from.ReposContract)
	}
	kind, ok := supportedProjectRepositorySourceVersionTransitions[projectRepositorySourceVersionEdge{from: from, to: next}]
	if !ok {
		return ProjectRepositorySourceVersionTransition{}, fmt.Errorf(
			"%w: project %q -> %q, repos %q -> %q",
			ErrUnsupportedProjectRepositorySourceVersionTransition,
			from.ProjectContract,
			next.ProjectContract,
			from.ReposContract,
			next.ReposContract,
		)
	}
	return ProjectRepositorySourceVersionTransition{Kind: kind, From: &from, To: next}, nil
}

func isSupportedProjectRepositorySourceVersions(versions ProjectRepositorySourceVersions) bool {
	for _, supported := range supportedProjectRepositorySourceVersions {
		if versions == supported {
			return true
		}
	}
	return false
}
