package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/projectcontracts"
)

const (
	SourceRootOriginBoxWatchRootRegistration       = "box.watch_root_registration"
	SourceRootOriginProjectWatchedRootRegistration = "projects.project_watched_root_registration"
)

type SourceRootReconcileInput struct {
	DryRun               bool                             `json:"dry_run,omitempty"`
	DiscoverFromStore    bool                             `json:"discover_from_store,omitempty"`
	BoxRegistrations     []BoxWatchRootRegistration       `json:"box_registrations,omitempty"`
	ProjectRegistrations []ProjectWatchedRootRegistration `json:"project_registrations,omitempty"`
}

type SourceRootReconcileResult struct {
	DryRun      bool                  `json:"dry_run"`
	Candidates  []SourceRootCandidate `json:"candidates"`
	SourceRoots []SourceRoot          `json:"source_roots"`
	Skipped     []SourceRootSkip      `json:"skipped,omitempty"`
	Applied     int                   `json:"applied"`
}

type SourceRootCandidate struct {
	Origin      string     `json:"origin"`
	OriginRef   string     `json:"origin_ref"`
	SourceKinds []string   `json:"source_kinds,omitempty"`
	Root        SourceRoot `json:"root"`
}

type SourceRootSkip struct {
	Origin    string `json:"origin"`
	OriginRef string `json:"origin_ref,omitempty"`
	Reason    string `json:"reason"`
}

type BoxWatchRootRegistration struct {
	BoxWatchRootRegistrationID string          `json:"box_watch_root_registration_id"`
	BoxID                      string          `json:"box_id"`
	BoxRootPath                string          `json:"box_root_path"`
	NodeID                     string          `json:"node_id,omitempty"`
	OwnerNodeKey               string          `json:"owner_node_key,omitempty"`
	AreaKey                    string          `json:"area_key"`
	LocalRootKey               string          `json:"local_root_key"`
	BackendRootKey             string          `json:"backend_root_key"`
	SourceKinds                json.RawMessage `json:"source_kinds"`
	RootRelativePath           string          `json:"root_relative_path"`
	DisplayName                string          `json:"display_name"`
	ActivationStatus           string          `json:"activation_status"`
	Metadata                   json.RawMessage `json:"metadata"`
}

type ProjectWatchedRootRegistration struct {
	ProjectWatchedRootRegistrationID string          `json:"project_watched_root_registration_id"`
	ProjectContractRegistrationID    string          `json:"project_contract_registration_id"`
	ProjectID                        string          `json:"project_id"`
	ProjectRoot                      string          `json:"project_root,omitempty"`
	NodeID                           string          `json:"node_id,omitempty"`
	OwnerNodeKey                     string          `json:"owner_node_key,omitempty"`
	LocalRootKey                     string          `json:"local_root_key"`
	BackendRootKey                   string          `json:"backend_root_key"`
	SourceKinds                      json.RawMessage `json:"source_kinds"`
	RootRelativePath                 string          `json:"root_relative_path"`
	DisplayName                      string          `json:"display_name"`
	ActivationStatus                 string          `json:"activation_status"`
	Metadata                         json.RawMessage `json:"metadata"`
}

func (s *Service) ReconcileSourceRoots(ctx context.Context, input SourceRootReconcileInput) (SourceRootReconcileResult, error) {
	if input.DiscoverFromStore {
		if s == nil || s.store.db == nil {
			return SourceRootReconcileResult{}, fmt.Errorf("knowledge store is not configured")
		}
		explicitBoxRegistrations := append([]BoxWatchRootRegistration{}, input.BoxRegistrations...)
		explicitProjectRegistrations := append([]ProjectWatchedRootRegistration{}, input.ProjectRegistrations...)
		boxRegistrations, err := s.store.ListBoxWatchRootRegistrations(ctx)
		if err != nil {
			return SourceRootReconcileResult{}, err
		}
		projectRegistrations, err := s.store.ListProjectWatchedRootRegistrations(ctx)
		if err != nil {
			return SourceRootReconcileResult{}, err
		}
		input.BoxRegistrations = append(boxRegistrations, explicitBoxRegistrations...)
		input.ProjectRegistrations = append(projectRegistrations, explicitProjectRegistrations...)
	}

	result := SourceRootReconcileResult{DryRun: input.DryRun}
	candidates, skipped := s.BuildSourceRootCandidates(input)
	if s != nil && s.store.db != nil {
		for i := range candidates {
			candidate := &candidates[i]
			if candidate.Origin != SourceRootOriginProjectWatchedRootRegistration || candidate.Root.Status != SourceRootStatusActive {
				continue
			}
			allowed, err := s.declarationCandidateMembership(ctx, candidate.Root)
			if err != nil {
				return result, err
			}
			if !allowed {
				candidate.Root.Status = SourceRootStatusBlocked
			}
		}
	}
	result.Candidates = candidates
	result.Skipped = skipped
	if input.DryRun {
		result.SourceRoots = rootsFromCandidates(candidates)
		return result, nil
	}
	if s == nil || s.store.db == nil {
		return result, fmt.Errorf("knowledge store is not configured")
	}
	for _, candidate := range candidates {
		root, err := s.store.UpsertSourceRoot(ctx, candidate.Root)
		if err != nil {
			return result, err
		}
		result.SourceRoots = append(result.SourceRoots, root)
		result.Applied++
	}
	return result, nil
}

func (s *Service) BuildSourceRootCandidates(input SourceRootReconcileInput) ([]SourceRootCandidate, []SourceRootSkip) {
	candidates := []SourceRootCandidate{}
	skipped := []SourceRootSkip{}
	seen := map[string]struct{}{}
	for _, registration := range input.BoxRegistrations {
		candidate, ok, reason := s.boxSourceRootCandidate(registration)
		if !ok {
			skipped = append(skipped, SourceRootSkip{Origin: SourceRootOriginBoxWatchRootRegistration, OriginRef: boxRegistrationRef(registration), Reason: reason})
			continue
		}
		if _, ok := seen[sourceRootCandidateKey(candidate)]; ok {
			continue
		}
		seen[sourceRootCandidateKey(candidate)] = struct{}{}
		candidates = append(candidates, candidate)
	}
	for _, registration := range input.ProjectRegistrations {
		candidate, ok, reason := s.projectSourceRootCandidate(registration)
		if !ok {
			skipped = append(skipped, SourceRootSkip{Origin: SourceRootOriginProjectWatchedRootRegistration, OriginRef: projectRegistrationRef(registration), Reason: reason})
			continue
		}
		if _, ok := seen[sourceRootCandidateKey(candidate)]; ok {
			continue
		}
		seen[sourceRootCandidateKey(candidate)] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates, skipped
}

func BoxWatchPlanSourceRootRegistrations(plan box.WatchPlan, nodeID string) []BoxWatchRootRegistration {
	registrations := make([]BoxWatchRootRegistration, 0, len(plan.WatchedRoots))
	for _, item := range plan.WatchedRoots {
		sourceKinds, err := json.Marshal(item.SourceKinds)
		if err != nil {
			sourceKinds = []byte("[]")
		}
		metadata, err := json.Marshal(item.Metadata)
		if err != nil || len(metadata) == 0 {
			metadata = emptyJSONObject
		}
		status := strings.TrimSpace(item.ActivationStatus)
		if status == "" {
			status = box.BoxWatchStatusPendingAgentApply
		}
		registrations = append(registrations, BoxWatchRootRegistration{
			BoxID:            plan.BoxID,
			BoxRootPath:      plan.RootPath,
			NodeID:           strings.TrimSpace(nodeID),
			OwnerNodeKey:     firstNonEmpty(item.OwnerNode, plan.OwnerNode),
			AreaKey:          firstNonEmpty(item.Key, strings.TrimPrefix(item.BackendRootKey, "loom_box__")),
			LocalRootKey:     firstNonEmpty(item.Key, strings.TrimPrefix(item.BackendRootKey, "loom_box__")),
			BackendRootKey:   item.BackendRootKey,
			SourceKinds:      json.RawMessage(sourceKinds),
			RootRelativePath: item.RootRelativePath,
			DisplayName:      item.DisplayName,
			ActivationStatus: status,
			Metadata:         json.RawMessage(metadata),
		})
	}
	return registrations
}

func (s *Service) boxSourceRootCandidate(registration BoxWatchRootRegistration) (SourceRootCandidate, bool, string) {
	sourceKinds := sourceKindValues(registration.SourceKinds)
	kind := RootKindBoxNotes
	if registration.AreaKey == "topics" || registration.AreaKey == "library" {
		kind = "box_" + registration.AreaKey
	} else if !isBoxNotesRoot(registration, sourceKinds) {
		return SourceRootCandidate{}, false, "not_box_notes"
	}
	status := sourceRootStatusFromActivation(registration.ActivationStatus)
	if kind != RootKindBoxNotes {
		status = expandedSourceStatus(registration.ActivationStatus, registration.Metadata, kind, registration.RootRelativePath)
		if registration.LocalRootKey != registration.AreaKey || registration.BackendRootKey != "loom_box__"+registration.AreaKey || !containsSourceKind(sourceKinds, kind) {
			status = SourceRootStatusBlocked
		}
	}
	root := SourceRoot{
		RootKind:                   kind,
		NodeID:                     stringPtrIfNotEmpty(registration.NodeID),
		NodeKey:                    registration.OwnerNodeKey,
		BoxWatchRootRegistrationID: stringPtrIfNotEmpty(registration.BoxWatchRootRegistrationID),
		BackendRootKey:             registration.BackendRootKey,
		DisplayName:                firstNonEmpty(registration.DisplayName, "Box Notes"),
		SourcePath:                 joinSourcePath(registration.BoxRootPath, registration.RootRelativePath),
		RootRelativePath:           registration.RootRelativePath,
		Status:                     status,
		AuthorizationMetadata:      sourceRootAuthorizationMetadata(kind),
		Metadata: sourceRootMetadata(registration.Metadata, map[string]any{
			"origin":            SourceRootOriginBoxWatchRootRegistration,
			"box_id":            registration.BoxID,
			"area_key":          registration.AreaKey,
			"local_root_key":    registration.LocalRootKey,
			"activation_status": registration.ActivationStatus,
			"source_kinds":      sourceKinds,
		}),
	}
	prepared, err := s.PrepareSourceRoot(root)
	if err != nil {
		return SourceRootCandidate{}, false, "invalid_box_notes_root: " + err.Error()
	}
	return SourceRootCandidate{
		Origin:      SourceRootOriginBoxWatchRootRegistration,
		OriginRef:   boxRegistrationRef(registration),
		SourceKinds: sourceKinds,
		Root:        prepared,
	}, true, ""
}

func (s *Service) projectSourceRootCandidate(registration ProjectWatchedRootRegistration) (SourceRootCandidate, bool, string) {
	sourceKinds := sourceKindValues(registration.SourceKinds)
	kind := RootKindProjectNotes
	if containsSourceKind(sourceKinds, RootKindProjectMaterial) {
		kind = RootKindProjectMaterial
	} else if !isProjectNotesRoot(registration, sourceKinds) {
		return SourceRootCandidate{}, false, "not_project_notes"
	}
	status := sourceRootStatusFromActivation(registration.ActivationStatus)
	if kind == RootKindProjectMaterial {
		status = expandedSourceStatus(registration.ActivationStatus, registration.Metadata, kind, registration.RootRelativePath)
		if containsSourceKind(sourceKinds, "notes_contract") || containsSourceKind(sourceKinds, "repos_contract") {
			status = SourceRootStatusBlocked
		}
	}
	if source, ok := jsonObject(registration.Metadata)["knowledge_source"].(map[string]any); ok {
		if _, versioned := source["schema_version"]; versioned && (kind != RootKindProjectMaterial || !declarationRegistrationMatches(registration)) {
			status = SourceRootStatusBlocked
		}
	}
	root := SourceRoot{
		RootKind:                         kind,
		NodeID:                           stringPtrIfNotEmpty(registration.NodeID),
		NodeKey:                          registration.OwnerNodeKey,
		ProjectID:                        stringPtrIfNotEmpty(registration.ProjectID),
		ProjectWatchedRootRegistrationID: stringPtrIfNotEmpty(registration.ProjectWatchedRootRegistrationID),
		BackendRootKey:                   registration.BackendRootKey,
		DisplayName:                      firstNonEmpty(registration.DisplayName, "Project Notes"),
		SourcePath:                       joinSourcePath(registration.ProjectRoot, registration.RootRelativePath),
		RootRelativePath:                 registration.RootRelativePath,
		Status:                           status,
		AuthorizationMetadata:            sourceRootAuthorizationMetadata(kind),
		Metadata: sourceRootMetadata(registration.Metadata, map[string]any{
			"origin":                           SourceRootOriginProjectWatchedRootRegistration,
			"project_contract_registration_id": registration.ProjectContractRegistrationID,
			"local_root_key":                   registration.LocalRootKey,
			"activation_status":                registration.ActivationStatus,
			"source_kinds":                     sourceKinds,
		}),
	}
	prepared, err := s.PrepareSourceRoot(root)
	if err != nil {
		return SourceRootCandidate{}, false, "invalid_project_notes_root: " + err.Error()
	}
	return SourceRootCandidate{
		Origin:      SourceRootOriginProjectWatchedRootRegistration,
		OriginRef:   projectRegistrationRef(registration),
		SourceKinds: sourceKinds,
		Root:        prepared,
	}, true, ""
}

func expandedSourceStatus(activation string, metadata json.RawMessage, kind, relativePath string) string {
	if activation == "disabled" || activation == "stale" || activation == "blocked" {
		return sourceRootStatusFromActivation(activation)
	}
	if relativePath == "" || relativePath == "." || filepath.IsAbs(relativePath) || strings.Contains(relativePath, "\\") || filepath.ToSlash(filepath.Clean(relativePath)) != relativePath || relativePath == ".." || strings.HasPrefix(relativePath, "../") {
		return SourceRootStatusBlocked
	}
	// New categories require a compiled policy and the owner's applied report.
	// Existing Notes retain their legacy registration/identity semantics.
	declaration, ok := jsonObject(metadata)["knowledge_source"].(map[string]any)
	if !ok || declaration["enabled"] != true || declaration["root_kind"] != kind || declaration["root_relative_path"] != relativePath || activation != "reported" {
		return SourceRootStatusBlocked
	}
	if _, versioned := declaration["schema_version"]; versioned {
		if _, valid := declarationKnowledgeMetadata(metadata); valid && kind == RootKindProjectMaterial {
			return SourceRootStatusActive
		}
		return SourceRootStatusBlocked
	}
	category := strings.TrimPrefix(kind, "box_")
	if kind == RootKindProjectMaterial {
		category = "projects"
		material, _ := declaration["declaration"].(string)
		if (material != "notes" && material != "docs" && material != "research") || !(relativePath == material || strings.HasPrefix(relativePath, material+"/")) {
			return SourceRootStatusBlocked
		}
	}
	if declaration["category"] != category {
		return SourceRootStatusBlocked
	}
	return SourceRootStatusActive
}

func isBoxNotesRoot(registration BoxWatchRootRegistration, sourceKinds []string) bool {
	return strings.EqualFold(strings.TrimSpace(registration.AreaKey), "notes") ||
		strings.EqualFold(strings.TrimSpace(registration.LocalRootKey), "notes") ||
		strings.EqualFold(strings.TrimSpace(registration.BackendRootKey), "loom_box__notes") ||
		containsSourceKind(sourceKinds, "box_notes")
}

func isProjectNotesRoot(registration ProjectWatchedRootRegistration, sourceKinds []string) bool {
	return strings.EqualFold(strings.TrimSpace(registration.LocalRootKey), "notes") ||
		containsSourceKind(sourceKinds, "notes_contract")
}

func sourceRootStatusFromActivation(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "disabled":
		return SourceRootStatusDisabled
	case "stale":
		return SourceRootStatusStale
	case "blocked":
		return SourceRootStatusBlocked
	default:
		return SourceRootStatusActive
	}
}

func sourceRootAuthorizationMetadata(rootKind string) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"authority":             "source_root",
		"root_kind":             rootKind,
		"writable_projection":   false,
		"mutation_route":        "capability",
		"raw_projection_writes": "ignored",
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func sourceRootMetadata(registrationMetadata json.RawMessage, values map[string]any) json.RawMessage {
	metadata := map[string]any{}
	if existing := jsonObject(registrationMetadata); existing != nil {
		metadata["registration_metadata"] = existing
	}
	for key, value := range values {
		metadata[key] = value
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func sourceKindValues(raw json.RawMessage) []string {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func containsSourceKind(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func joinSourcePath(root, relative string) string {
	root = strings.TrimSpace(root)
	relative = strings.TrimSpace(relative)
	if relative == "." {
		relative = ""
	}
	if root == "" {
		if relative == "" {
			return ""
		}
		return filepath.Clean(filepath.FromSlash(relative))
	}
	if relative == "" {
		return filepath.Clean(root)
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func jsonObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

func rootsFromCandidates(candidates []SourceRootCandidate) []SourceRoot {
	roots := make([]SourceRoot, 0, len(candidates))
	for _, candidate := range candidates {
		roots = append(roots, candidate.Root)
	}
	return roots
}

func sourceRootCandidateKey(candidate SourceRootCandidate) string {
	root := candidate.Root
	return strings.Join([]string{
		root.RootKind,
		firstNonEmpty(root.NodeKey, derefString(root.NodeID)),
		derefString(root.ProjectID),
		root.BackendRootKey,
		filepath.Clean(root.SourcePath),
		filepath.Clean(filepath.FromSlash(root.RootRelativePath)),
	}, "\x00")
}

func boxRegistrationRef(registration BoxWatchRootRegistration) string {
	return firstNonEmpty(registration.BoxWatchRootRegistrationID, registration.BackendRootKey, registration.AreaKey)
}

func projectRegistrationRef(registration ProjectWatchedRootRegistration) string {
	return firstNonEmpty(registration.ProjectWatchedRootRegistrationID, registration.BackendRootKey, registration.LocalRootKey)
}

func stringPtrIfNotEmpty(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// The versioned metadata binds semantic intent to the exact captured project
// source. Legacy prefix admission never consults or inherits this exception.
func declarationKnowledgeMetadata(metadata json.RawMessage) (map[string]any, bool) {
	decoded, err := projectcontracts.DecodeDeclarationEvidenceJSON(metadata)
	outer, ok := decoded.(map[string]any)
	if err != nil || !ok {
		return nil, false
	}
	source, ok := outer["knowledge_source"].(map[string]any)
	if !ok || source["schema_version"] != "project.contract.v0.5" {
		return nil, false
	}
	allowed := []string{"schema_version", "root_kind", "category", "declaration", "enabled", "root_relative_path", "include", "exclude", "project_id", "project_root", "owner_node", "resource_key", "local_root_key", "backend_root_key", "source_ref", "source_hash", "config_hash"}
	if len(source) != len(allowed) {
		return nil, false
	}
	for _, key := range allowed {
		if _, ok := source[key]; !ok {
			return nil, false
		}
	}
	if source["enabled"] != true || source["root_kind"] != RootKindProjectMaterial || source["category"] != "projects" {
		return nil, false
	}
	var snapshots []projectcontracts.DeclarationSourceSnapshot
	raw, err := json.Marshal(outer["declaration_sources"])
	if err != nil || json.Unmarshal(raw, &snapshots) != nil {
		return nil, false
	}
	var document *projectcontracts.ProjectDeclaration
	seen := map[string]bool{}
	for _, snapshot := range snapshots {
		if seen[snapshot.Ref] || snapshot.Hash != fmt.Sprintf("sha256:%x", sha256.Sum256(snapshot.Raw)) || snapshot.Hash != snapshot.Revision {
			return nil, false
		}
		seen[snapshot.Ref] = true
		if snapshot.Ref == source["source_ref"] && snapshot.Hash == source["source_hash"] && snapshot.SchemaVersion == projectcontracts.ProjectSchemaV05 {
			d, err := projectcontracts.ParseProjectDeclaration(snapshot.Raw)
			if err != nil {
				return nil, false
			}
			document = &d
		}
	}
	if document == nil {
		return nil, false
	}
	key, _ := source["resource_key"].(string)
	resource := document.Resources[projectcontracts.ResourceKey(key)].Knowledge
	projectRoot, _ := source["project_root"].(string)
	localKey, _ := source["local_root_key"].(string)
	configHash, _ := source["config_hash"].(string)
	if resource == nil || source["project_id"] != document.Project.ID || source["owner_node"] != document.Project.OwnerNode ||
		source["declaration"] != string(resource.Category) || source["root_relative_path"] != resource.Path ||
		!filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot || localKey == "" ||
		source["backend_root_key"] != projectcontracts.ProjectWatchedRootKey(document.Project.Slug, localKey) ||
		!declarationConfigHash.MatchString(configHash) {
		return nil, false
	}
	if resource.Protection == "" && localKey != key {
		return nil, false
	}
	for _, field := range []string{"include", "exclude"} {
		values, ok := source[field].([]any)
		if !ok {
			return nil, false
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return nil, false
			}
		}
	}
	return source, true
}

var declarationConfigHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func declarationRegistrationMatches(reg ProjectWatchedRootRegistration) bool {
	source, ok := declarationKnowledgeMetadata(reg.Metadata)
	return ok && source["project_id"] == reg.ProjectID && source["project_root"] == reg.ProjectRoot &&
		source["owner_node"] == reg.OwnerNodeKey && source["local_root_key"] == reg.LocalRootKey &&
		source["backend_root_key"] == reg.BackendRootKey && source["root_relative_path"] == reg.RootRelativePath
}

// Persisted candidates are checked against current accepted source evidence and
// the exact node report. Reuse the same fence for reads, closing the interval
// between a registration change and the next reconciliation pass.
func (s *Service) declarationCandidateMembership(ctx context.Context, root SourceRoot) (bool, error) {
	var allowed bool
	err := s.store.db.QueryRowContext(ctx, `SELECT `+declarationEnrollmentMembershipSQL("candidate")+` FROM
	 (SELECT $1::text AS project_watched_root_registration_id,$2::text AS node_id,$3::text AS node_key,
	 $4::text AS project_id,$5::text AS backend_root_key,$6::text AS root_relative_path,$7::text AS source_path,$8::jsonb AS metadata,$9::text AS root_kind,$10::text AS status) candidate`,
		root.ProjectWatchedRootRegistrationID, root.NodeID, root.NodeKey, root.ProjectID, root.BackendRootKey, root.RootRelativePath, root.SourcePath, root.Metadata, root.RootKind, root.Status).Scan(&allowed)
	return allowed, err
}
