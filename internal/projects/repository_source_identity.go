package projects

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	ProjectRepositorySourceSnapshotSchemaVersion    = "project_repository_source_snapshot.v1"
	ProjectRepositoryChangeSummarySchemaVersion     = "project_repository_change_summary.v1"
	ProjectRepositoryChangeSummaryMemberLimit       = 100
	projectRepositoryJSONNumberMaxLexemeBytes       = 8192
	projectRepositoryJSONNumberMaxSignificantDigits = 4096
	projectRepositoryJSONNumberMaxExpandedDigits    = 4096

	ProjectRepositoryObservationReasonSourceBindingChanged ProjectRepositoryObservationReasonCode = "source_binding_changed"
)

var (
	projectRepositoryDigestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	projectRepositoryProjectIDPattern = regexp.MustCompile(`^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	projectRepositoryIDPattern        = regexp.MustCompile(`^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	projectRepositoryMemberKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

	ErrInvalidProjectRepositorySourceInput = errors.New("invalid project repository source input")
	ErrProjectRepositorySourceMismatch     = errors.New("project repository source mismatch")
)

// ProjectRepositoryValidatedSourceInput is a pure persistence boundary. Its
// values come from the project-contract validator; this package does not read
// or re-parse project files.
type ProjectRepositoryValidatedSourceInput struct {
	ProjectID             string                             `json:"project_id"`
	ProjectRoot           string                             `json:"project_root"`
	ProjectContractPath   string                             `json:"project_contract_path"`
	ReposContractPath     string                             `json:"repos_contract_path"`
	OwnerNode             string                             `json:"owner_node"`
	Versions              ProjectRepositorySourceVersions    `json:"source_versions"`
	ProjectContractDigest string                             `json:"project_contract_digest"`
	ReposContractDigest   string                             `json:"repos_contract_digest"`
	RegistrationPlan      json.RawMessage                    `json:"registration_plan"`
	Members               []ProjectRepositoryValidatedMember `json:"members"`
}

type ProjectRepositoryValidatedMember struct {
	RepositoryID string                `json:"repository_id"`
	Key          string                `json:"key"`
	Path         string                `json:"path"`
	Role         ProjectRepositoryRole `json:"role"`
	StateRoot    string                `json:"state_root,omitempty"`
}

type ProjectRepositorySourceLocationSnapshot struct {
	ProjectRoot         string `json:"project_root"`
	ProjectContractPath string `json:"project_contract_path"`
	ReposContractPath   string `json:"repos_contract_path"`
	OwnerNode           string `json:"owner_node"`
}

type ProjectRepositorySourceMemberSnapshot struct {
	RepositoryID             string                `json:"repository_id"`
	Key                      string                `json:"key"`
	Path                     string                `json:"path"`
	Role                     ProjectRepositoryRole `json:"role"`
	StateRoot                string                `json:"state_root,omitempty"`
	ObservationBindingDigest string                `json:"observation_binding_digest"`
}

// ProjectRepositorySourceSnapshot is the canonical, complete source document
// stored by the later transaction slice. Arrays are always present and sorted;
// StableRegistrationPlan has deterministic object ordering and no generated
// timestamps or project-root-dependent absolute paths.
type ProjectRepositorySourceSnapshot struct {
	SchemaVersion          string                                  `json:"schema_version"`
	ProjectID              string                                  `json:"project_id"`
	SourceVersions         ProjectRepositorySourceVersions         `json:"source_versions"`
	ProjectContractDigest  string                                  `json:"project_contract_digest"`
	ReposContractDigest    string                                  `json:"repos_contract_digest"`
	StableRegistrationPlan json.RawMessage                         `json:"stable_registration_plan"`
	Members                []ProjectRepositorySourceMemberSnapshot `json:"members"`
	Location               ProjectRepositorySourceLocationSnapshot `json:"location"`
}

type ProjectRepositoryObservationBinding struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	Digest       string `json:"digest"`
}

type ProjectRepositoryCanonicalSource struct {
	Snapshot            ProjectRepositorySourceSnapshot       `json:"snapshot"`
	SnapshotJSON        json.RawMessage                       `json:"snapshot_json"`
	SemanticDigest      string                                `json:"semantic_digest"`
	LocationDigest      string                                `json:"location_digest"`
	ObservationBindings []ProjectRepositoryObservationBinding `json:"observation_bindings"`
}

type ProjectRepositorySourceClassification string

const (
	ProjectRepositorySourceClassificationFirstRegistration           ProjectRepositorySourceClassification = "first_registration"
	ProjectRepositorySourceClassificationIdenticalReplay             ProjectRepositorySourceClassification = "identical_replay"
	ProjectRepositorySourceClassificationSemanticChange              ProjectRepositorySourceClassification = "semantic_change"
	ProjectRepositorySourceClassificationRelocation                  ProjectRepositorySourceClassification = "source_relocation"
	ProjectRepositorySourceClassificationSemanticChangeAndRelocation ProjectRepositorySourceClassification = "semantic_change_and_relocation"
)

type ProjectRepositoryMemberFieldChange struct {
	RepositoryID string   `json:"repository_id"`
	Fields       []string `json:"fields"`
}

type ProjectRepositoryMembershipChangeSummary struct {
	AddedRepositoryIDs   []string                             `json:"added_repository_ids"`
	RemovedRepositoryIDs []string                             `json:"removed_repository_ids"`
	ChangedMembers       []ProjectRepositoryMemberFieldChange `json:"changed_members"`
	AddedCount           int                                  `json:"added_count"`
	RemovedCount         int                                  `json:"removed_count"`
	ChangedCount         int                                  `json:"changed_count"`
	OmittedCount         int                                  `json:"omitted_count"`
	Truncated            bool                                 `json:"truncated"`
}

type ProjectRepositoryChangeSummary struct {
	SchemaVersion     string                                   `json:"schema_version"`
	ContentChanges    []string                                 `json:"content_changes"`
	MembershipChanges ProjectRepositoryMembershipChangeSummary `json:"membership_changes"`
	LocationChanges   []string                                 `json:"location_changes"`
	VersionChanges    []string                                 `json:"version_changes"`
}

type ProjectRepositorySourceChange struct {
	Classification    ProjectRepositorySourceClassification `json:"classification"`
	HistoryChangeKind ProjectRepositorySourceChangeKind     `json:"history_change_kind,omitempty"`
	Summary           ProjectRepositoryChangeSummary        `json:"summary"`
}

type ProjectRepositoryObservationReasonCode string

type ProjectRepositoryObservationBindingTransition struct {
	Observation ProjectRepositoryObservation `json:"observation"`
	Changed     bool                         `json:"changed"`
	Invalidated bool                         `json:"invalidated"`
}

// BuildProjectRepositoryCanonicalSource constructs all source identities from
// validated values only. It is deterministic, has no clocks, and never mutates
// its input.
func BuildProjectRepositoryCanonicalSource(input ProjectRepositoryValidatedSourceInput) (ProjectRepositoryCanonicalSource, error) {
	normalized, err := normalizeProjectRepositoryValidatedSourceInput(input)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, err
	}

	location := ProjectRepositorySourceLocationSnapshot{
		ProjectRoot:         normalized.ProjectRoot,
		ProjectContractPath: normalized.ProjectContractPath,
		ReposContractPath:   normalized.ReposContractPath,
		OwnerNode:           normalized.OwnerNode,
	}
	stablePlan, err := canonicalStableRegistrationPlan(normalized.RegistrationPlan, location)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: registration plan: %v", ErrInvalidProjectRepositorySourceInput, err)
	}

	members := make([]ProjectRepositorySourceMemberSnapshot, 0, len(normalized.Members))
	bindings := make([]ProjectRepositoryObservationBinding, 0, len(normalized.Members))
	for _, member := range normalized.Members {
		bindingDigest, err := digestProjectRepositoryObservationBinding(location, normalized.Versions, member)
		if err != nil {
			return ProjectRepositoryCanonicalSource{}, err
		}
		members = append(members, ProjectRepositorySourceMemberSnapshot{
			RepositoryID:             member.RepositoryID,
			Key:                      member.Key,
			Path:                     member.Path,
			Role:                     member.Role,
			StateRoot:                member.StateRoot,
			ObservationBindingDigest: bindingDigest,
		})
		bindings = append(bindings, ProjectRepositoryObservationBinding{
			ProjectID:    normalized.ProjectID,
			RepositoryID: member.RepositoryID,
			Digest:       bindingDigest,
		})
	}

	snapshot := ProjectRepositorySourceSnapshot{
		SchemaVersion:          ProjectRepositorySourceSnapshotSchemaVersion,
		ProjectID:              normalized.ProjectID,
		SourceVersions:         normalized.Versions,
		ProjectContractDigest:  normalized.ProjectContractDigest,
		ReposContractDigest:    normalized.ReposContractDigest,
		StableRegistrationPlan: stablePlan,
		Members:                members,
		Location:               location,
	}
	semanticDigest, err := digestProjectRepositorySemanticSnapshot(snapshot)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, err
	}
	locationDigest, err := digestProjectRepositoryLocationSnapshot(snapshot)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("marshal project repository source snapshot: %w", err)
	}

	return ProjectRepositoryCanonicalSource{
		Snapshot:            snapshot,
		SnapshotJSON:        json.RawMessage(snapshotJSON),
		SemanticDigest:      semanticDigest,
		LocationDigest:      locationDigest,
		ObservationBindings: bindings,
	}, nil
}

// DecodeProjectRepositoryCanonicalSource restores a source snapshot returned
// from JSON/JSONB storage. It rejects unknown fields and stale or forged
// digests, then emits the same canonical bytes as a fresh build regardless of
// the storage engine's object-key serialization order.
func DecodeProjectRepositoryCanonicalSource(snapshotJSON json.RawMessage, semanticDigest, locationDigest string) (ProjectRepositoryCanonicalSource, error) {
	decoder := json.NewDecoder(bytes.NewReader(snapshotJSON))
	decoder.DisallowUnknownFields()
	var snapshot ProjectRepositorySourceSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: decode source snapshot: %v", ErrInvalidProjectRepositorySourceInput, err)
	}
	if err := requireProjectRepositoryJSONEOF(decoder); err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: decode source snapshot: %v", ErrInvalidProjectRepositorySourceInput, err)
	}
	stablePlan, err := canonicalStoredRegistrationPlan(snapshot.StableRegistrationPlan)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: registration plan: %v", ErrInvalidProjectRepositorySourceInput, err)
	}
	snapshot.StableRegistrationPlan = stablePlan
	canonicalJSON, err := json.Marshal(snapshot)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("marshal project repository source snapshot: %w", err)
	}
	bindings := make([]ProjectRepositoryObservationBinding, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		bindings = append(bindings, ProjectRepositoryObservationBinding{
			ProjectID:    snapshot.ProjectID,
			RepositoryID: member.RepositoryID,
			Digest:       member.ObservationBindingDigest,
		})
	}
	source := ProjectRepositoryCanonicalSource{
		Snapshot:            snapshot,
		SnapshotJSON:        json.RawMessage(canonicalJSON),
		SemanticDigest:      strings.TrimSpace(semanticDigest),
		LocationDigest:      strings.TrimSpace(locationDigest),
		ObservationBindings: bindings,
	}
	if err := validateCanonicalProjectRepositorySource(source); err != nil {
		return ProjectRepositoryCanonicalSource{}, err
	}
	return source, nil
}

// ClassifyProjectRepositorySourceChange distinguishes idempotent replay from
// semantic and location changes. Identical replay intentionally has no history
// change kind because the transaction slice must suppress its history row.
func ClassifyProjectRepositorySourceChange(current *ProjectRepositoryCanonicalSource, next ProjectRepositoryCanonicalSource) (ProjectRepositorySourceChange, error) {
	if err := validateCanonicalProjectRepositorySource(next); err != nil {
		return ProjectRepositorySourceChange{}, fmt.Errorf("next source: %w", err)
	}
	if current == nil {
		return ProjectRepositorySourceChange{
			Classification:    ProjectRepositorySourceClassificationFirstRegistration,
			HistoryChangeKind: ProjectRepositorySourceFirstRegistration,
			Summary:           summarizeProjectRepositorySourceChange(nil, next.Snapshot),
		}, nil
	}
	if err := validateCanonicalProjectRepositorySource(*current); err != nil {
		return ProjectRepositorySourceChange{}, fmt.Errorf("current source: %w", err)
	}
	if current.Snapshot.ProjectID != next.Snapshot.ProjectID {
		return ProjectRepositorySourceChange{}, fmt.Errorf("%w: project id %q != %q", ErrProjectRepositorySourceMismatch, current.Snapshot.ProjectID, next.Snapshot.ProjectID)
	}

	semanticChanged := current.SemanticDigest != next.SemanticDigest
	locationChanged := current.LocationDigest != next.LocationDigest
	result := ProjectRepositorySourceChange{Summary: summarizeProjectRepositorySourceChange(&current.Snapshot, next.Snapshot)}
	switch {
	case !semanticChanged && !locationChanged:
		result.Classification = ProjectRepositorySourceClassificationIdenticalReplay
	case semanticChanged && !locationChanged:
		result.Classification = ProjectRepositorySourceClassificationSemanticChange
		result.HistoryChangeKind = ProjectRepositorySourceSemanticChange
	case !semanticChanged && locationChanged:
		result.Classification = ProjectRepositorySourceClassificationRelocation
		result.HistoryChangeKind = ProjectRepositorySourceRelocation
	default:
		result.Classification = ProjectRepositorySourceClassificationSemanticChangeAndRelocation
		result.HistoryChangeKind = ProjectRepositorySourceSemanticChangeAndRelocation
	}
	return result, nil
}

// PlanProjectRepositoryObservationBinding returns the exact observation state
// that may be persisted with a new member binding. A changed binding always
// clears stale posture, time, reason, and payload.
func PlanProjectRepositoryObservationBinding(current *ProjectRepositoryObservation, binding ProjectRepositoryObservationBinding) (ProjectRepositoryObservationBindingTransition, error) {
	if !projectRepositoryProjectIDPattern.MatchString(binding.ProjectID) || !projectRepositoryIDPattern.MatchString(binding.RepositoryID) || !projectRepositoryDigestPattern.MatchString(binding.Digest) {
		return ProjectRepositoryObservationBindingTransition{}, fmt.Errorf("%w: invalid observation binding", ErrInvalidProjectRepositorySourceInput)
	}
	if current == nil {
		return ProjectRepositoryObservationBindingTransition{
			Observation: ProjectRepositoryObservation{
				ProjectID:           binding.ProjectID,
				RepositoryID:        binding.RepositoryID,
				SourceBindingDigest: binding.Digest,
				ObservationPosture:  ProjectRepositoryObservationNotObserved,
				Observation:         json.RawMessage(`{}`),
				ObservationRevision: 1,
			},
			Changed: true,
		}, nil
	}
	if err := validateProjectRepositoryObservation(*current); err != nil {
		return ProjectRepositoryObservationBindingTransition{}, err
	}
	if current.ProjectID != binding.ProjectID || current.RepositoryID != binding.RepositoryID {
		return ProjectRepositoryObservationBindingTransition{}, fmt.Errorf(
			"%w: observation %s/%s != binding %s/%s",
			ErrProjectRepositorySourceMismatch,
			current.ProjectID,
			current.RepositoryID,
			binding.ProjectID,
			binding.RepositoryID,
		)
	}
	if current.SourceBindingDigest == binding.Digest {
		return ProjectRepositoryObservationBindingTransition{Observation: cloneProjectRepositoryObservation(*current)}, nil
	}
	if current.ObservationRevision == math.MaxInt64 {
		return ProjectRepositoryObservationBindingTransition{}, fmt.Errorf("observation revision overflow")
	}

	invalidated := cloneProjectRepositoryObservation(*current)
	invalidated.SourceBindingDigest = binding.Digest
	invalidated.ObservationPosture = ProjectRepositoryObservationNotObserved
	invalidated.ReasonCode = string(ProjectRepositoryObservationReasonSourceBindingChanged)
	invalidated.ObservedAt = nil
	invalidated.Observation = json.RawMessage(`{}`)
	invalidated.ObservationRevision++
	return ProjectRepositoryObservationBindingTransition{
		Observation: invalidated,
		Changed:     true,
		Invalidated: true,
	}, nil
}

func normalizeProjectRepositoryValidatedSourceInput(input ProjectRepositoryValidatedSourceInput) (ProjectRepositoryValidatedSourceInput, error) {
	normalized := input
	normalized.ProjectID = strings.TrimSpace(input.ProjectID)
	normalized.OwnerNode = strings.TrimSpace(input.OwnerNode)
	normalized.ProjectContractDigest = strings.TrimSpace(input.ProjectContractDigest)
	normalized.ReposContractDigest = strings.TrimSpace(input.ReposContractDigest)
	normalized.RegistrationPlan = append(json.RawMessage(nil), input.RegistrationPlan...)
	normalized.Members = append([]ProjectRepositoryValidatedMember(nil), input.Members...)

	if !projectRepositoryProjectIDPattern.MatchString(normalized.ProjectID) {
		return normalized, fmt.Errorf("%w: project id is invalid", ErrInvalidProjectRepositorySourceInput)
	}
	if normalized.OwnerNode == "" {
		return normalized, fmt.Errorf("%w: owner node is required", ErrInvalidProjectRepositorySourceInput)
	}
	if !isSupportedProjectRepositorySourceVersions(normalized.Versions) {
		return normalized, fmt.Errorf(
			"%w: unsupported source versions project=%q repos=%q",
			ErrInvalidProjectRepositorySourceInput,
			normalized.Versions.ProjectContract,
			normalized.Versions.ReposContract,
		)
	}
	if !projectRepositoryDigestPattern.MatchString(normalized.ProjectContractDigest) {
		return normalized, fmt.Errorf("%w: project contract digest must be a sha256 URI", ErrInvalidProjectRepositorySourceInput)
	}
	if !projectRepositoryDigestPattern.MatchString(normalized.ReposContractDigest) {
		return normalized, fmt.Errorf("%w: repos contract digest must be a sha256 URI", ErrInvalidProjectRepositorySourceInput)
	}

	var err error
	normalized.ProjectRoot, err = normalizeExactProjectRepositoryPath(input.ProjectRoot, "project root", true)
	if err != nil {
		return normalized, err
	}
	normalized.ProjectContractPath, err = normalizeExactProjectRepositoryPath(input.ProjectContractPath, "project contract path", true)
	if err != nil {
		return normalized, err
	}
	normalized.ReposContractPath, err = normalizeExactProjectRepositoryPath(input.ReposContractPath, "repos contract path", true)
	if err != nil {
		return normalized, err
	}

	if err := validateSingleDeclarationSource(normalized.Versions, normalized.ProjectRoot, normalized.ProjectContractPath, normalized.ReposContractPath, normalized.ProjectContractDigest, normalized.ReposContractDigest); err != nil {
		return normalized, err
	}
	seenIDs := make(map[string]struct{}, len(normalized.Members))
	seenKeys := make(map[string]struct{}, len(normalized.Members))
	seenPaths := make(map[string]struct{}, len(normalized.Members))
	for index := range normalized.Members {
		member := &normalized.Members[index]
		member.RepositoryID = strings.TrimSpace(member.RepositoryID)
		member.Key = strings.TrimSpace(member.Key)
		member.Role = ProjectRepositoryRole(strings.ToLower(strings.TrimSpace(string(member.Role))))
		member.Path, err = normalizeProjectRepositoryMemberPath(member.Path, true)
		if err != nil {
			return normalized, fmt.Errorf("%w: member %d path: %v", ErrInvalidProjectRepositorySourceInput, index, err)
		}
		if strings.TrimSpace(member.StateRoot) != "" {
			member.StateRoot, err = normalizeProjectRepositoryMemberPath(member.StateRoot, false)
			if err != nil {
				return normalized, fmt.Errorf("%w: member %d state root: %v", ErrInvalidProjectRepositorySourceInput, index, err)
			}
		} else {
			member.StateRoot = ""
		}
		if !projectRepositoryIDPattern.MatchString(member.RepositoryID) {
			return normalized, fmt.Errorf("%w: member %d repository id is invalid", ErrInvalidProjectRepositorySourceInput, index)
		}
		if !projectRepositoryMemberKeyPattern.MatchString(member.Key) {
			return normalized, fmt.Errorf("%w: member %d key is invalid", ErrInvalidProjectRepositorySourceInput, index)
		}
		switch member.Role {
		case ProjectRepositoryRolePrimary, ProjectRepositoryRoleComponent, ProjectRepositoryRoleReference:
		default:
			return normalized, fmt.Errorf("%w: member %d role is invalid", ErrInvalidProjectRepositorySourceInput, index)
		}
		if _, exists := seenIDs[member.RepositoryID]; exists {
			return normalized, fmt.Errorf("%w: duplicate repository id %s", ErrInvalidProjectRepositorySourceInput, member.RepositoryID)
		}
		if _, exists := seenKeys[member.Key]; exists {
			return normalized, fmt.Errorf("%w: duplicate repository key %s", ErrInvalidProjectRepositorySourceInput, member.Key)
		}
		if _, exists := seenPaths[member.Path]; exists {
			return normalized, fmt.Errorf("%w: duplicate repository path %s", ErrInvalidProjectRepositorySourceInput, member.Path)
		}
		seenIDs[member.RepositoryID] = struct{}{}
		seenKeys[member.Key] = struct{}{}
		seenPaths[member.Path] = struct{}{}
	}
	sort.Slice(normalized.Members, func(i, j int) bool {
		if normalized.Members[i].RepositoryID != normalized.Members[j].RepositoryID {
			return normalized.Members[i].RepositoryID < normalized.Members[j].RepositoryID
		}
		return normalized.Members[i].Key < normalized.Members[j].Key
	})
	if normalized.Members == nil {
		normalized.Members = []ProjectRepositoryValidatedMember{}
	}
	return normalized, nil
}

func normalizeExactProjectRepositoryPath(value, field string, requireAbsolute bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalidProjectRepositorySourceInput, field)
	}
	if requireAbsolute && !filepath.IsAbs(value) {
		return "", fmt.Errorf("%w: %s must be absolute", ErrInvalidProjectRepositorySourceInput, field)
	}
	return filepath.Clean(value), nil
}

func normalizeProjectRepositoryMemberPath(value string, allowDot bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, `\`) || path.IsAbs(value) || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("must be a portable relative path")
	}
	if len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' {
		return "", fmt.Errorf("must not use a drive prefix")
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") || (!allowDot && clean == ".") {
		return "", fmt.Errorf("escapes its source root")
	}
	return clean, nil
}

func digestProjectRepositorySemanticSnapshot(snapshot ProjectRepositorySourceSnapshot) (string, error) {
	type semanticMember struct {
		RepositoryID string                `json:"repository_id"`
		Key          string                `json:"key"`
		Path         string                `json:"path"`
		Role         ProjectRepositoryRole `json:"role"`
		StateRoot    string                `json:"state_root,omitempty"`
	}
	members := make([]semanticMember, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		members = append(members, semanticMember{
			RepositoryID: member.RepositoryID,
			Key:          member.Key,
			Path:         member.Path,
			Role:         member.Role,
			StateRoot:    member.StateRoot,
		})
	}
	payload := struct {
		Domain                 string                          `json:"domain"`
		ProjectID              string                          `json:"project_id"`
		SourceVersions         ProjectRepositorySourceVersions `json:"source_versions"`
		ProjectContractDigest  string                          `json:"project_contract_digest"`
		ReposContractDigest    string                          `json:"repos_contract_digest"`
		StableRegistrationPlan json.RawMessage                 `json:"stable_registration_plan"`
		Members                []semanticMember                `json:"members"`
	}{
		Domain:                 "loom.project_repository.semantic.v1",
		ProjectID:              snapshot.ProjectID,
		SourceVersions:         snapshot.SourceVersions,
		ProjectContractDigest:  snapshot.ProjectContractDigest,
		ReposContractDigest:    snapshot.ReposContractDigest,
		StableRegistrationPlan: snapshot.StableRegistrationPlan,
		Members:                members,
	}
	return digestTypedProjectRepositoryValue(payload)
}

func digestProjectRepositoryLocationSnapshot(snapshot ProjectRepositorySourceSnapshot) (string, error) {
	payload := struct {
		Domain    string                                  `json:"domain"`
		ProjectID string                                  `json:"project_id"`
		Location  ProjectRepositorySourceLocationSnapshot `json:"location"`
	}{
		Domain:    "loom.project_repository.location.v1",
		ProjectID: snapshot.ProjectID,
		Location:  snapshot.Location,
	}
	return digestTypedProjectRepositoryValue(payload)
}

func digestProjectRepositoryObservationBinding(location ProjectRepositorySourceLocationSnapshot, versions ProjectRepositorySourceVersions, member ProjectRepositoryValidatedMember) (string, error) {
	payload := struct {
		Domain         string                          `json:"domain"`
		ProjectRoot    string                          `json:"project_root"`
		OwnerNode      string                          `json:"owner_node"`
		RepositoryID   string                          `json:"repository_id"`
		MemberPath     string                          `json:"member_path"`
		StateRoot      string                          `json:"state_root,omitempty"`
		Role           ProjectRepositoryRole           `json:"role"`
		SourceVersions ProjectRepositorySourceVersions `json:"source_versions"`
	}{
		Domain:         "loom.project_repository.observation_binding.v1",
		ProjectRoot:    location.ProjectRoot,
		OwnerNode:      location.OwnerNode,
		RepositoryID:   member.RepositoryID,
		MemberPath:     member.Path,
		StateRoot:      member.StateRoot,
		Role:           member.Role,
		SourceVersions: versions,
	}
	return digestTypedProjectRepositoryValue(payload)
}

func digestTypedProjectRepositoryValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal project repository digest input: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateCanonicalProjectRepositorySource(source ProjectRepositoryCanonicalSource) error {
	if source.Snapshot.SchemaVersion != ProjectRepositorySourceSnapshotSchemaVersion {
		return fmt.Errorf("%w: source snapshot schema version %q", ErrInvalidProjectRepositorySourceInput, source.Snapshot.SchemaVersion)
	}
	if !projectRepositoryProjectIDPattern.MatchString(source.Snapshot.ProjectID) || !isSupportedProjectRepositorySourceVersions(source.Snapshot.SourceVersions) {
		return fmt.Errorf("%w: invalid project identity or source versions", ErrInvalidProjectRepositorySourceInput)
	}
	if !projectRepositoryDigestPattern.MatchString(source.Snapshot.ProjectContractDigest) || !projectRepositoryDigestPattern.MatchString(source.Snapshot.ReposContractDigest) {
		return fmt.Errorf("%w: invalid contract digest", ErrInvalidProjectRepositorySourceInput)
	}
	stablePlan, err := canonicalStableRegistrationPlan(source.Snapshot.StableRegistrationPlan, source.Snapshot.Location)
	if err != nil || !bytes.Equal(stablePlan, source.Snapshot.StableRegistrationPlan) {
		return fmt.Errorf("%w: registration plan is not canonical", ErrInvalidProjectRepositorySourceInput)
	}
	projectRoot, err := normalizeExactProjectRepositoryPath(source.Snapshot.Location.ProjectRoot, "project root", true)
	if err != nil || projectRoot != source.Snapshot.Location.ProjectRoot {
		return fmt.Errorf("%w: project root is not canonical", ErrInvalidProjectRepositorySourceInput)
	}
	projectContractPath, err := normalizeExactProjectRepositoryPath(source.Snapshot.Location.ProjectContractPath, "project contract path", true)
	if err != nil || projectContractPath != source.Snapshot.Location.ProjectContractPath {
		return fmt.Errorf("%w: project contract path is not canonical", ErrInvalidProjectRepositorySourceInput)
	}
	reposContractPath, err := normalizeExactProjectRepositoryPath(source.Snapshot.Location.ReposContractPath, "repos contract path", true)
	if err != nil || reposContractPath != source.Snapshot.Location.ReposContractPath {
		return fmt.Errorf("%w: repos contract path is not canonical", ErrInvalidProjectRepositorySourceInput)
	}
	if err := validateSingleDeclarationSource(source.Snapshot.SourceVersions, source.Snapshot.Location.ProjectRoot, source.Snapshot.Location.ProjectContractPath, source.Snapshot.Location.ReposContractPath, source.Snapshot.ProjectContractDigest, source.Snapshot.ReposContractDigest); err != nil {
		return err
	}
	if strings.TrimSpace(source.Snapshot.Location.OwnerNode) == "" || strings.TrimSpace(source.Snapshot.Location.OwnerNode) != source.Snapshot.Location.OwnerNode {
		return fmt.Errorf("%w: owner node is required", ErrInvalidProjectRepositorySourceInput)
	}
	if source.Snapshot.Members == nil {
		return fmt.Errorf("%w: source members must be an explicit array", ErrInvalidProjectRepositorySourceInput)
	}
	seenRepositories := make(map[string]struct{}, len(source.Snapshot.Members))
	seenKeys := make(map[string]struct{}, len(source.Snapshot.Members))
	seenPaths := make(map[string]struct{}, len(source.Snapshot.Members))
	previousRepositoryID := ""
	for index, member := range source.Snapshot.Members {
		normalizedPath, pathErr := normalizeProjectRepositoryMemberPath(member.Path, true)
		if pathErr != nil || normalizedPath != member.Path {
			return fmt.Errorf("%w: member %d path is not canonical", ErrInvalidProjectRepositorySourceInput, index)
		}
		if member.StateRoot != "" {
			normalizedStateRoot, stateErr := normalizeProjectRepositoryMemberPath(member.StateRoot, false)
			if stateErr != nil || normalizedStateRoot != member.StateRoot {
				return fmt.Errorf("%w: member %d state root is not canonical", ErrInvalidProjectRepositorySourceInput, index)
			}
		}
		if !projectRepositoryIDPattern.MatchString(member.RepositoryID) || !projectRepositoryMemberKeyPattern.MatchString(member.Key) {
			return fmt.Errorf("%w: member %d identity is invalid", ErrInvalidProjectRepositorySourceInput, index)
		}
		switch member.Role {
		case ProjectRepositoryRolePrimary, ProjectRepositoryRoleComponent, ProjectRepositoryRoleReference:
		default:
			return fmt.Errorf("%w: member %d role is invalid", ErrInvalidProjectRepositorySourceInput, index)
		}
		if _, exists := seenRepositories[member.RepositoryID]; exists || (previousRepositoryID != "" && previousRepositoryID > member.RepositoryID) {
			return fmt.Errorf("%w: members are not uniquely sorted", ErrInvalidProjectRepositorySourceInput)
		}
		if _, exists := seenKeys[member.Key]; exists {
			return fmt.Errorf("%w: members contain duplicate key %s", ErrInvalidProjectRepositorySourceInput, member.Key)
		}
		if _, exists := seenPaths[member.Path]; exists {
			return fmt.Errorf("%w: members contain duplicate path %s", ErrInvalidProjectRepositorySourceInput, member.Path)
		}
		seenRepositories[member.RepositoryID] = struct{}{}
		seenKeys[member.Key] = struct{}{}
		seenPaths[member.Path] = struct{}{}
		previousRepositoryID = member.RepositoryID
		expectedBinding, bindingErr := digestProjectRepositoryObservationBinding(source.Snapshot.Location, source.Snapshot.SourceVersions, ProjectRepositoryValidatedMember{
			RepositoryID: member.RepositoryID,
			Key:          member.Key,
			Path:         member.Path,
			Role:         member.Role,
			StateRoot:    member.StateRoot,
		})
		if bindingErr != nil || expectedBinding != member.ObservationBindingDigest {
			return fmt.Errorf("%w: member %d observation binding does not match source inputs", ErrInvalidProjectRepositorySourceInput, index)
		}
	}
	semantic, err := digestProjectRepositorySemanticSnapshot(source.Snapshot)
	if err != nil {
		return err
	}
	location, err := digestProjectRepositoryLocationSnapshot(source.Snapshot)
	if err != nil {
		return err
	}
	if semantic != source.SemanticDigest || location != source.LocationDigest {
		return fmt.Errorf("%w: source digest does not match canonical snapshot", ErrInvalidProjectRepositorySourceInput)
	}
	raw, err := json.Marshal(source.Snapshot)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, source.SnapshotJSON) {
		return fmt.Errorf("%w: source snapshot JSON is not canonical", ErrInvalidProjectRepositorySourceInput)
	}
	if len(source.ObservationBindings) != len(source.Snapshot.Members) {
		return fmt.Errorf("%w: observation binding count does not match member count", ErrInvalidProjectRepositorySourceInput)
	}
	for index, binding := range source.ObservationBindings {
		member := source.Snapshot.Members[index]
		if binding.ProjectID != source.Snapshot.ProjectID || binding.RepositoryID != member.RepositoryID || binding.Digest != member.ObservationBindingDigest {
			return fmt.Errorf("%w: observation binding %d does not match member snapshot", ErrInvalidProjectRepositorySourceInput, index)
		}
	}
	return nil
}

func summarizeProjectRepositorySourceChange(current *ProjectRepositorySourceSnapshot, next ProjectRepositorySourceSnapshot) ProjectRepositoryChangeSummary {
	summary := ProjectRepositoryChangeSummary{
		SchemaVersion:     ProjectRepositoryChangeSummarySchemaVersion,
		ContentChanges:    []string{},
		MembershipChanges: emptyProjectRepositoryMembershipChangeSummary(),
		LocationChanges:   []string{},
		VersionChanges:    []string{},
	}
	if current == nil {
		summary.ContentChanges = []string{"project_contract_digest", "registration_plan", "repos_contract_digest"}
		summary.LocationChanges = []string{"owner_node", "project_contract_path", "project_root", "repos_contract_path"}
		summary.VersionChanges = []string{"project_contract_schema_version", "repos_contract_schema_version"}
		nextMembers := make([]string, 0, len(next.Members))
		for _, member := range next.Members {
			nextMembers = append(nextMembers, member.RepositoryID)
		}
		summary.MembershipChanges = boundProjectRepositoryMembershipChanges(nextMembers, nil, nil)
		return summary
	}

	if current.ProjectContractDigest != next.ProjectContractDigest {
		summary.ContentChanges = append(summary.ContentChanges, "project_contract_digest")
	}
	if !bytes.Equal(current.StableRegistrationPlan, next.StableRegistrationPlan) {
		summary.ContentChanges = append(summary.ContentChanges, "registration_plan")
	}
	if current.ReposContractDigest != next.ReposContractDigest {
		summary.ContentChanges = append(summary.ContentChanges, "repos_contract_digest")
	}
	if current.Location.OwnerNode != next.Location.OwnerNode {
		summary.LocationChanges = append(summary.LocationChanges, "owner_node")
	}
	if current.Location.ProjectContractPath != next.Location.ProjectContractPath {
		summary.LocationChanges = append(summary.LocationChanges, "project_contract_path")
	}
	if current.Location.ProjectRoot != next.Location.ProjectRoot {
		summary.LocationChanges = append(summary.LocationChanges, "project_root")
	}
	if current.Location.ReposContractPath != next.Location.ReposContractPath {
		summary.LocationChanges = append(summary.LocationChanges, "repos_contract_path")
	}
	if current.SourceVersions.ProjectContract != next.SourceVersions.ProjectContract {
		summary.VersionChanges = append(summary.VersionChanges, "project_contract_schema_version")
	}
	if current.SourceVersions.ReposContract != next.SourceVersions.ReposContract {
		summary.VersionChanges = append(summary.VersionChanges, "repos_contract_schema_version")
	}
	summary.MembershipChanges = summarizeProjectRepositoryMembershipChanges(current.Members, next.Members)
	return summary
}

func emptyProjectRepositoryMembershipChangeSummary() ProjectRepositoryMembershipChangeSummary {
	return ProjectRepositoryMembershipChangeSummary{
		AddedRepositoryIDs:   []string{},
		RemovedRepositoryIDs: []string{},
		ChangedMembers:       []ProjectRepositoryMemberFieldChange{},
	}
}

func summarizeProjectRepositoryMembershipChanges(current, next []ProjectRepositorySourceMemberSnapshot) ProjectRepositoryMembershipChangeSummary {
	currentByID := make(map[string]ProjectRepositorySourceMemberSnapshot, len(current))
	nextByID := make(map[string]ProjectRepositorySourceMemberSnapshot, len(next))
	for _, member := range current {
		currentByID[member.RepositoryID] = member
	}
	for _, member := range next {
		nextByID[member.RepositoryID] = member
	}
	added := []string{}
	removed := []string{}
	changed := []ProjectRepositoryMemberFieldChange{}
	for id, member := range nextByID {
		previous, exists := currentByID[id]
		if !exists {
			added = append(added, id)
			continue
		}
		fields := []string{}
		if previous.Key != member.Key {
			fields = append(fields, "key")
		}
		if previous.Path != member.Path {
			fields = append(fields, "path")
		}
		if previous.Role != member.Role {
			fields = append(fields, "role")
		}
		if previous.StateRoot != member.StateRoot {
			fields = append(fields, "state_root")
		}
		if len(fields) > 0 {
			changed = append(changed, ProjectRepositoryMemberFieldChange{RepositoryID: id, Fields: fields})
		}
	}
	for id := range currentByID {
		if _, exists := nextByID[id]; !exists {
			removed = append(removed, id)
		}
	}
	return boundProjectRepositoryMembershipChanges(added, removed, changed)
}

func boundProjectRepositoryMembershipChanges(added, removed []string, changed []ProjectRepositoryMemberFieldChange) ProjectRepositoryMembershipChangeSummary {
	sort.Strings(added)
	sort.Strings(removed)
	sort.Slice(changed, func(i, j int) bool { return changed[i].RepositoryID < changed[j].RepositoryID })
	result := emptyProjectRepositoryMembershipChangeSummary()
	result.AddedCount = len(added)
	result.RemovedCount = len(removed)
	result.ChangedCount = len(changed)
	remaining := ProjectRepositoryChangeSummaryMemberLimit
	addedCount := min(len(added), remaining)
	result.AddedRepositoryIDs = append(result.AddedRepositoryIDs, added[:addedCount]...)
	remaining -= addedCount
	removedCount := min(len(removed), remaining)
	result.RemovedRepositoryIDs = append(result.RemovedRepositoryIDs, removed[:removedCount]...)
	remaining -= removedCount
	changedCount := min(len(changed), remaining)
	for _, item := range changed[:changedCount] {
		result.ChangedMembers = append(result.ChangedMembers, ProjectRepositoryMemberFieldChange{
			RepositoryID: item.RepositoryID,
			Fields:       append([]string(nil), item.Fields...),
		})
	}
	total := len(added) + len(removed) + len(changed)
	result.OmittedCount = total - (addedCount + removedCount + changedCount)
	result.Truncated = result.OmittedCount > 0
	return result
}

func validateProjectRepositoryObservation(observation ProjectRepositoryObservation) error {
	if !projectRepositoryProjectIDPattern.MatchString(observation.ProjectID) || !projectRepositoryIDPattern.MatchString(observation.RepositoryID) || !projectRepositoryDigestPattern.MatchString(observation.SourceBindingDigest) {
		return fmt.Errorf("invalid project repository observation identity")
	}
	if observation.ObservationRevision <= 0 {
		return fmt.Errorf("project repository observation revision must be positive")
	}
	if len(observation.ReasonCode) > 128 {
		return fmt.Errorf("project repository observation reason is too long")
	}
	switch observation.ObservationPosture {
	case ProjectRepositoryObservationNotObserved:
		if observation.ObservedAt != nil {
			return fmt.Errorf("not_observed project repository observation must not have a timestamp")
		}
	case ProjectRepositoryObservationObserved, ProjectRepositoryObservationRemoteUnavailable:
		if observation.ObservedAt == nil {
			return fmt.Errorf("%s project repository observation requires a timestamp", observation.ObservationPosture)
		}
	default:
		return fmt.Errorf("unsupported project repository observation posture %q", observation.ObservationPosture)
	}
	if !isJSONObject(observation.Observation) {
		return fmt.Errorf("project repository observation payload must be a JSON object")
	}
	return nil
}

func cloneProjectRepositoryObservation(observation ProjectRepositoryObservation) ProjectRepositoryObservation {
	cloned := observation
	cloned.Observation = append(json.RawMessage(nil), observation.Observation...)
	if observation.ObservedAt != nil {
		observedAt := *observation.ObservedAt
		cloned.ObservedAt = &observedAt
	}
	return cloned
}

func isJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != nil
}

type canonicalJSONKind uint8

const (
	canonicalJSONNull canonicalJSONKind = iota
	canonicalJSONBool
	canonicalJSONNumber
	canonicalJSONString
	canonicalJSONArray
	canonicalJSONObject
)

type canonicalJSONField struct {
	key   string
	value canonicalJSONValue
}

type canonicalJSONValue struct {
	kind        canonicalJSONKind
	boolValue   bool
	textValue   string
	arrayValue  []canonicalJSONValue
	objectValue []canonicalJSONField
}

func canonicalStableRegistrationPlan(raw json.RawMessage, location ProjectRepositorySourceLocationSnapshot) (json.RawMessage, error) {
	context := canonicalProjectRepositoryJSONContext{
		normalizeBuildSemantics: true,
		projectRoot:             location.ProjectRoot,
		projectContractPath:     stableRegistrationPlanString(location.ProjectContractPath, location.ProjectRoot),
		reposContractPath:       stableRegistrationPlanString(location.ReposContractPath, location.ProjectRoot),
	}
	return canonicalProjectRepositoryRegistrationPlan(raw, context)
}

// canonicalStoredRegistrationPlan normalizes only JSON representation details
// that storage engines may change. It must not apply build-time semantic
// normalization because doing so could erase stored source mutations before
// digest validation.
func canonicalStoredRegistrationPlan(raw json.RawMessage) (json.RawMessage, error) {
	return canonicalProjectRepositoryRegistrationPlan(raw, canonicalProjectRepositoryJSONContext{})
}

func canonicalProjectRepositoryRegistrationPlan(raw json.RawMessage, context canonicalProjectRepositoryJSONContext) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeCanonicalJSONValue(decoder, context, nil)
	if err != nil {
		return nil, err
	}
	if value.kind != canonicalJSONObject {
		return nil, fmt.Errorf("must be a JSON object")
	}
	if err := requireProjectRepositoryJSONEOF(decoder); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := writeCanonicalJSONValue(&buffer, value); err != nil {
		return nil, err
	}
	return json.RawMessage(buffer.Bytes()), nil
}

func requireProjectRepositoryJSONEOF(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("has trailing token %v", token)
	}
	return err
}

type canonicalProjectRepositoryJSONContext struct {
	normalizeBuildSemantics bool
	projectRoot             string
	projectContractPath     string
	reposContractPath       string
}

func decodeCanonicalJSONValue(decoder *json.Decoder, context canonicalProjectRepositoryJSONContext, jsonPath []string) (canonicalJSONValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return canonicalJSONValue{}, err
	}
	switch typed := token.(type) {
	case nil:
		return canonicalJSONValue{kind: canonicalJSONNull}, nil
	case bool:
		return canonicalJSONValue{kind: canonicalJSONBool, boolValue: typed}, nil
	case json.Number:
		canonical, err := canonicalProjectRepositoryJSONNumber(typed.String())
		if err != nil {
			return canonicalJSONValue{}, err
		}
		return canonicalJSONValue{kind: canonicalJSONNumber, textValue: canonical}, nil
	case string:
		if context.normalizeBuildSemantics && !isProjectRepositoryWatchedRootCommandArgumentPath(jsonPath) {
			typed = stableRegistrationPlanString(typed, context.projectRoot)
		}
		return canonicalJSONValue{kind: canonicalJSONString, textValue: typed}, nil
	case json.Delim:
		switch typed {
		case '[':
			items := []canonicalJSONValue{}
			for decoder.More() {
				item, err := decodeCanonicalJSONValue(decoder, context, appendCanonicalProjectRepositoryJSONPath(jsonPath, "[]"))
				if err != nil {
					return canonicalJSONValue{}, err
				}
				items = append(items, item)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return canonicalJSONValue{}, fmt.Errorf("invalid JSON array terminator")
			}
			return canonicalJSONValue{kind: canonicalJSONArray, arrayValue: items}, nil
		case '{':
			fields := []canonicalJSONField{}
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return canonicalJSONValue{}, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return canonicalJSONValue{}, fmt.Errorf("JSON object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return canonicalJSONValue{}, fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				value, err := decodeCanonicalJSONValue(decoder, context, appendCanonicalProjectRepositoryJSONPath(jsonPath, key))
				if err != nil {
					return canonicalJSONValue{}, err
				}
				if context.normalizeBuildSemantics && unstableProjectRepositoryRegistrationPlanField(key) {
					continue
				}
				if context.normalizeBuildSemantics {
					canonicalizeProjectRepositoryRegistrationPlanSourcePath(key, &value, context)
				}
				fields = append(fields, canonicalJSONField{key: key, value: value})
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return canonicalJSONValue{}, fmt.Errorf("invalid JSON object terminator")
			}
			if context.normalizeBuildSemantics && isProjectRepositoryWatchedRootCommandObjectPath(jsonPath) {
				if err := canonicalizeProjectRepositoryWatchedRootCommand(fields, context.projectRoot); err != nil {
					return canonicalJSONValue{}, err
				}
			}
			sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })
			return canonicalJSONValue{kind: canonicalJSONObject, objectValue: fields}, nil
		default:
			return canonicalJSONValue{}, fmt.Errorf("unexpected JSON delimiter %q", typed)
		}
	default:
		return canonicalJSONValue{}, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func appendCanonicalProjectRepositoryJSONPath(jsonPath []string, segment string) []string {
	next := make([]string, len(jsonPath)+1)
	copy(next, jsonPath)
	next[len(jsonPath)] = segment
	return next
}

func isProjectRepositoryWatchedRootCommandObjectPath(jsonPath []string) bool {
	return len(jsonPath) == 4 &&
		jsonPath[0] == "watched_roots" && jsonPath[1] == "[]" &&
		jsonPath[2] == "agent_commands" && jsonPath[3] == "[]"
}

func isProjectRepositoryWatchedRootCommandArgumentPath(jsonPath []string) bool {
	return len(jsonPath) == 6 && isProjectRepositoryWatchedRootCommandObjectPath(jsonPath[:4]) &&
		jsonPath[4] == "command" && jsonPath[5] == "[]"
}

// canonicalizeProjectRepositoryWatchedRootCommand handles only the generated
// projectcontracts watched-root command projection. The structured command is
// semantic truth. Its original shell must be the exact deterministic rendering
// of that command before any project-local argument is normalized; this keeps a
// mismatched or independently edited shell from being erased as representation.
func canonicalizeProjectRepositoryWatchedRootCommand(fields []canonicalJSONField, projectRoot string) error {
	commandIndex := -1
	shellIndex := -1
	for index := range fields {
		switch fields[index].key {
		case "command":
			commandIndex = index
		case "shell":
			shellIndex = index
		}
	}
	if commandIndex < 0 || shellIndex < 0 {
		return fmt.Errorf("watched-root agent command requires command and shell")
	}
	commandValue := &fields[commandIndex].value
	shellValue := &fields[shellIndex].value
	if commandValue.kind != canonicalJSONArray || shellValue.kind != canonicalJSONString {
		return fmt.Errorf("watched-root agent command has invalid command or shell type")
	}

	original := make([]string, len(commandValue.arrayValue))
	for index := range commandValue.arrayValue {
		argument := &commandValue.arrayValue[index]
		if argument.kind != canonicalJSONString {
			return fmt.Errorf("watched-root agent command argument %d is not a string", index)
		}
		original[index] = argument.textValue
	}
	if expected := canonicalProjectRepositoryWatchedRootShell(original); shellValue.textValue != expected {
		return fmt.Errorf("watched-root agent command shell does not exactly represent command")
	}

	normalized := make([]string, len(original))
	for index, argument := range original {
		normalized[index] = stableRegistrationPlanString(argument, projectRoot)
		commandValue.arrayValue[index].textValue = normalized[index]
	}
	shellValue.textValue = canonicalProjectRepositoryWatchedRootShell(normalized)
	return nil
}

// Keep this representation identical to projectcontracts.shellJoin. The build
// boundary validates the original generated projection before deriving the
// relocated stable projection; live projectcontracts plans are never changed.
func canonicalProjectRepositoryWatchedRootShell(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@", r))
		}) == -1 {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
	}
	return strings.Join(quoted, " ")
}

// canonicalProjectRepositoryJSONNumber returns one exact representation for a
// bounded JSON number without converting through a binary floating-point
// value. The result is an integer coefficient with trailing zeroes removed and
// an optional base-10 exponent. This keeps persistence round trips stable when
// JSON/JSONB changes only the original decimal or exponent spelling.
func canonicalProjectRepositoryJSONNumber(value string) (string, error) {
	if value == "" || len(value) > projectRepositoryJSONNumberMaxLexemeBytes {
		return "", fmt.Errorf("JSON number exceeds the bounded input size")
	}

	index := 0
	negative := false
	if value[index] == '-' {
		negative = true
		index++
		if index == len(value) {
			return "", fmt.Errorf("invalid JSON number %q", value)
		}
	}

	integerStart := index
	switch {
	case value[index] == '0':
		index++
		if index < len(value) && value[index] >= '0' && value[index] <= '9' {
			return "", fmt.Errorf("invalid JSON number %q", value)
		}
	case value[index] >= '1' && value[index] <= '9':
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
	default:
		return "", fmt.Errorf("invalid JSON number %q", value)
	}
	integerEnd := index

	fractionStart := index
	fractionEnd := index
	if index < len(value) && value[index] == '.' {
		index++
		fractionStart = index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == fractionStart {
			return "", fmt.Errorf("invalid JSON number %q", value)
		}
		fractionEnd = index
	}

	exponent := 0
	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		index++
		exponentNegative := false
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			exponentNegative = value[index] == '-'
			index++
		}
		exponentStart := index
		const maxParsedExponent = projectRepositoryJSONNumberMaxLexemeBytes + projectRepositoryJSONNumberMaxExpandedDigits
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			digit := int(value[index] - '0')
			if exponent > (maxParsedExponent-digit)/10 {
				return "", fmt.Errorf("JSON number exponent exceeds the bounded range")
			}
			exponent = exponent*10 + digit
			index++
		}
		if index == exponentStart {
			return "", fmt.Errorf("invalid JSON number %q", value)
		}
		if exponentNegative {
			exponent = -exponent
		}
	}
	if index != len(value) {
		return "", fmt.Errorf("invalid JSON number %q", value)
	}

	coefficient := value[integerStart:integerEnd]
	if fractionEnd > fractionStart {
		coefficient += value[fractionStart:fractionEnd]
	}
	coefficient = strings.TrimLeft(coefficient, "0")
	if coefficient == "" {
		return "0", nil
	}

	scale := exponent - (fractionEnd - fractionStart)
	trimmedCoefficient := strings.TrimRight(coefficient, "0")
	scale += len(coefficient) - len(trimmedCoefficient)
	coefficient = trimmedCoefficient
	if len(coefficient) > projectRepositoryJSONNumberMaxSignificantDigits {
		return "", fmt.Errorf("JSON number exceeds %d significant digits", projectRepositoryJSONNumberMaxSignificantDigits)
	}
	if scale >= 0 {
		if len(coefficient)+scale > projectRepositoryJSONNumberMaxExpandedDigits {
			return "", fmt.Errorf("JSON number exceeds the bounded expanded range")
		}
	} else if -scale > projectRepositoryJSONNumberMaxExpandedDigits {
		return "", fmt.Errorf("JSON number exceeds the bounded decimal scale")
	}

	var canonical strings.Builder
	if negative {
		canonical.WriteByte('-')
	}
	canonical.WriteString(coefficient)
	if scale != 0 {
		canonical.WriteByte('e')
		canonical.WriteString(strconv.Itoa(scale))
	}
	return canonical.String(), nil
}

func unstableProjectRepositoryRegistrationPlanField(key string) bool {
	switch key {
	case "generated_at", "project_root":
		return true
	default:
		return false
	}
}

func canonicalizeProjectRepositoryRegistrationPlanSourcePath(key string, value *canonicalJSONValue, context canonicalProjectRepositoryJSONContext) {
	if value.kind != canonicalJSONString {
		return
	}
	switch key {
	case "contract_path", "project_contract_path", "repos_contract_path":
	default:
		return
	}
	switch value.textValue {
	case context.projectContractPath:
		value.textValue = "$project_contract"
	case context.reposContractPath:
		value.textValue = "$repos_contract"
	}
}

func stableRegistrationPlanString(value, projectRoot string) string {
	if !filepath.IsAbs(value) {
		return value
	}
	clean := filepath.Clean(value)
	relative, err := filepath.Rel(projectRoot, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return value
	}
	return filepath.ToSlash(relative)
}

func writeCanonicalJSONValue(buffer *bytes.Buffer, value canonicalJSONValue) error {
	switch value.kind {
	case canonicalJSONNull:
		buffer.WriteString("null")
	case canonicalJSONBool:
		if value.boolValue {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case canonicalJSONNumber:
		buffer.WriteString(value.textValue)
	case canonicalJSONString:
		raw, err := json.Marshal(value.textValue)
		if err != nil {
			return err
		}
		buffer.Write(raw)
	case canonicalJSONArray:
		buffer.WriteByte('[')
		for index, item := range value.arrayValue {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalJSONValue(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case canonicalJSONObject:
		buffer.WriteByte('{')
		for index, field := range value.objectValue {
			if index > 0 {
				buffer.WriteByte(',')
			}
			key, err := json.Marshal(field.key)
			if err != nil {
				return err
			}
			buffer.Write(key)
			buffer.WriteByte(':')
			if err := writeCanonicalJSONValue(buffer, field.value); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON kind %d", value.kind)
	}
	return nil
}

// v0.5 declares the complete repository set in the actual project source. The
// legacy column names remain compatible; no repos.yaml document is invented.
func validateSingleDeclarationSource(v ProjectRepositorySourceVersions, root, projectPath, reposPath, projectHash, reposHash string) error {
	if v != projectRepositorySourceV05V05 {
		return nil
	}
	if projectPath != filepath.Join(root, ".loom", "project.yaml") || reposPath != projectPath || reposHash != projectHash {
		return fmt.Errorf("%w: v0.5 requires one exact .loom/project.yaml path and digest", ErrInvalidProjectRepositorySourceInput)
	}
	return nil
}
