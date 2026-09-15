package repostate

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"loom.local/loom/internal/ids"
)

type ValidationIssue struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AuthoritativeMembership is a read-only input supplied by the owning LOOM
// project. The validator never creates or changes project membership.
type AuthoritativeMembership struct {
	Resolved         bool           `json:"resolved"`
	RepositoryID     string         `json:"repository_id"`
	OwnerProjectID   string         `json:"owner_project_id"`
	OwnerProjectSlug string         `json:"owner_project_slug"`
	OwnerRole        RepositoryRole `json:"owner_role"`
	StateRoot        string         `json:"state_root,omitempty"`
	NavigationPath   string         `json:"navigation_path,omitempty"`
	SourcePath       string         `json:"source_path,omitempty"`
	SourceDigest     string         `json:"source_digest,omitempty"`
	SourceCommit     string         `json:"source_commit,omitempty"`
}

type ValidationResult struct {
	Status TrackingStatus    `json:"status"`
	Issues []ValidationIssue `json:"issues"`
}

type Validator struct{}

// Validate applies the frozen membership precedence. A missing Git
// observation qualifies extraction freshness but does not invent a membership
// mismatch. When an observation is present, every portable source file must be
// a tracked regular Git entry with identical bytes.
func (Validator) Validate(parsed ParsedRepositoryState, membership *AuthoritativeMembership, observation *GitObservation, observationErr error) ValidationResult {
	result := ValidationResult{Issues: []ValidationIssue{}}
	if !parsed.Enabled && !hasParseStage(parsed.Issues, ParseIssueEnvelope) {
		result.Status = TrackingNotEnabled
		return result
	}
	if hasParseStage(parsed.Issues, ParseIssueEnvelope) {
		result.Status = TrackingMalformed
		result.addParseIssues(parsed.Issues, ParseIssueEnvelope)
		return result
	}
	if parsed.UnsupportedVersion {
		result.Status = TrackingStaleVersion
		result.Issues = append(result.Issues, validationIssue("unsupported_schema", RepositoryManifestPath, parsed.SchemaVersion))
		return result
	}
	if hasParseStage(parsed.Issues, ParseIssueBody) || parsed.Manifest == nil {
		result.Status = TrackingMalformed
		result.addParseIssues(parsed.Issues, ParseIssueBody)
		if parsed.Manifest == nil && len(result.Issues) == 0 {
			result.Issues = append(result.Issues, validationIssue("manifest_body_malformed", RepositoryManifestPath, "manifest body was not decoded"))
		}
		return result
	}

	result.Issues = append(result.Issues, validateManifest(*parsed.Manifest)...)
	result.Issues = append(result.Issues, validateRequiredRootFiles(parsed)...)
	result.Issues = append(result.Issues, validatePortablePaths(parsed)...)
	result.Issues = append(result.Issues, validateLifecycle(parsed)...)
	if observation != nil {
		result.Issues = append(result.Issues, validateTrackedSources(parsed, *observation)...)
	}
	if observationErr != nil && gitObservationInvalidatesSource(observationErr) {
		code := gitObservationErrorCode(observationErr)
		result.Issues = append(result.Issues, validationIssue(code, StateRoot, "Git reported an invalid portable source view"))
	}
	if len(result.Issues) > 0 {
		result.Status = TrackingMalformed
		sortValidationIssues(result.Issues)
		return result
	}

	if membership == nil || !membership.Resolved {
		result.Status = TrackingMembershipUnresolved
		result.Issues = append(result.Issues, validationIssue("owner_membership_unresolved", "", "authoritative owning membership is unavailable"))
		return result
	}
	manifest := parsed.Manifest
	if manifest.Repository.ID != membership.RepositoryID {
		result.Status = TrackingMismatchedRepository
		result.Issues = append(result.Issues, validationIssue("repository_id_mismatch", RepositoryManifestPath, "declared repository ID does not match authoritative membership"))
		return result
	}
	if manifest.OwnerProject.ID != membership.OwnerProjectID || manifest.OwnerProject.Slug != membership.OwnerProjectSlug {
		result.Status = TrackingMismatchedOwner
		result.Issues = append(result.Issues, validationIssue("owner_project_mismatch", RepositoryManifestPath, "declared owner backlink does not match authoritative membership"))
		return result
	}
	if manifest.Repository.Role != membership.OwnerRole || (membership.OwnerRole != RepositoryRolePrimary && membership.OwnerRole != RepositoryRoleComponent) {
		result.Status = TrackingMismatchedMembership
		result.Issues = append(result.Issues, validationIssue("owner_role_mismatch", RepositoryManifestPath, "declared repository role does not match the owning membership"))
		return result
	}
	stateRoot := strings.TrimSpace(membership.StateRoot)
	if stateRoot == "" {
		stateRoot = StateRoot
	}
	if stateRoot != StateRoot {
		result.Status = TrackingMismatchedMembership
		result.Issues = append(result.Issues, validationIssue("state_root_mismatch", RepositoryManifestPath, "authoritative membership does not resolve state_root to .repo"))
		return result
	}
	if membership.NavigationPath != "" && !validProjectRelativeNavigation(membership.NavigationPath) {
		result.Status = TrackingMismatchedMembership
		result.Issues = append(result.Issues, validationIssue("navigation_path_invalid", membershipSourcePath(membership), "authoritative navigation path must be project-relative slash form"))
		return result
	}
	if membership.SourcePath != "" && !validProjectRelativeNavigation(membership.SourcePath) {
		result.Status = TrackingMismatchedMembership
		result.Issues = append(result.Issues, validationIssue("membership_source_path_invalid", ".loom/contracts/repos.yaml", "authoritative membership source path must be project-relative slash form"))
		return result
	}
	if membership.SourceDigest != "" && !validSourceDigest(membership.SourceDigest) {
		result.Status = TrackingMismatchedMembership
		result.Issues = append(result.Issues, validationIssue("membership_source_digest_invalid", membershipSourcePath(membership), "authoritative membership source digest is malformed"))
		return result
	}
	if membership.SourceCommit != "" && !validGitObjectID(membership.SourceCommit) {
		result.Status = TrackingMismatchedMembership
		result.Issues = append(result.Issues, validationIssue("membership_source_commit_invalid", membershipSourcePath(membership), "authoritative membership source commit is malformed"))
		return result
	}
	result.Status = TrackingValid
	return result
}

func validateManifest(manifest RepositoryManifest) []ValidationIssue {
	issues := []ValidationIssue{}
	add := func(code, detail string) {
		issues = append(issues, validationIssue(code, RepositoryManifestPath, detail))
	}
	if manifest.Kind != RepositoryManifestKind || manifest.SchemaVersion != RepositorySchemaVersion {
		add("manifest_envelope_malformed", "manifest kind and schema version must match v1")
	}
	if err := ids.Validate(RepositoryIDPrefix, manifest.Repository.ID); err != nil {
		add("repository_id_invalid", "repository.id is not a typed repository ID")
	}
	if err := ids.Validate(ProjectIDPrefix, manifest.OwnerProject.ID); err != nil {
		add("owner_project_id_invalid", "owner_project.id is not a typed project ID")
	}
	if !validRequiredText(manifest.Repository.Name) {
		add("repository_name_invalid", "repository.name must be non-empty, trimmed, and free of control characters")
	}
	if !validRequiredText(manifest.Repository.Purpose) {
		add("repository_purpose_invalid", "repository.purpose must be non-empty, trimmed, and free of control characters")
	}
	if manifest.Repository.Role != RepositoryRolePrimary && manifest.Repository.Role != RepositoryRoleComponent {
		add("repository_role_invalid", "repository.role must be primary or component")
	}
	if !validSlug(manifest.OwnerProject.Slug) {
		add("owner_project_slug_invalid", "owner_project.slug must be a lowercase portable slug")
	}
	if issue := validateAliases(manifest.Repository.Aliases); issue != "" {
		add("repository_aliases_invalid", issue)
	}
	if issue := validateTopics(manifest.Repository.Topics); issue != "" {
		add("repository_topics_invalid", issue)
	}
	if manifest.Source.Tracking != "git" || manifest.Source.StateRoot != StateRoot {
		add("source_policy_invalid", "source must declare tracking=git and state_root=.repo")
	}
	for _, branch := range []string{manifest.Branches.Stable, manifest.Branches.Default} {
		if branch != "" && !validGitBranch(branch) {
			add("branch_declaration_invalid", "branch declarations must use portable Git reference syntax")
			break
		}
	}
	sortValidationIssues(issues)
	return issues
}

func validateRequiredRootFiles(parsed ParsedRepositoryState) []ValidationIssue {
	issues := []ValidationIssue{}
	for _, schema := range SchemaV1().RootPaths {
		if schema.Requirement != PathRequired {
			continue
		}
		if _, ok := parsed.Sources[schema.Pattern]; !ok {
			issues = append(issues, validationIssue("required_source_missing", schema.Pattern, "required v1 source file is absent"))
		}
	}
	return issues
}

func validatePortablePaths(parsed ParsedRepositoryState) []ValidationIssue {
	rootFiles := map[string]struct{}{
		"repo.yaml": {}, "README.md": {}, "REPOSITORY.md": {}, "STATE.md": {}, "ROADMAP.md": {},
	}
	rootDirectories := map[string]struct{}{
		"future": {}, "initiatives": {}, "features": {}, "decisions": {}, "releases": {},
		"integrations": {}, "architecture": {}, "protocols": {}, "templates": {}, "archive": {},
	}
	issues := []ValidationIssue{}
	for sourcePath := range parsed.Sources {
		parts := strings.Split(sourcePath, "/")
		if len(parts) < 2 || parts[0] != StateRoot {
			issues = append(issues, validationIssue("portable_path_invalid", sourcePath, "source path is outside the portable .repo root"))
			continue
		}
		if len(parts) == 2 {
			if _, allowed := rootFiles[parts[1]]; !allowed {
				issues = append(issues, validationIssue("portable_path_unknown", sourcePath, "v1 does not define this root source file"))
			}
			continue
		}
		if _, allowed := rootDirectories[parts[1]]; !allowed {
			issues = append(issues, validationIssue("portable_path_unknown", sourcePath, "v1 does not define this source collection"))
		}
	}
	return issues
}

func validateLifecycle(parsed ParsedRepositoryState) []ValidationIssue {
	issues := []ValidationIssue{}
	schemas := map[ObjectKind]LifecycleSchema{}
	collections := map[string]LifecycleSchema{}
	for _, schema := range SchemaV1().LifecycleSchemas {
		schemas[schema.Kind] = schema
		collections[string(schema.Kind)+"s"] = schema
	}
	orphanChecked := map[string]struct{}{}
	for sourcePath := range parsed.Sources {
		parts := strings.Split(sourcePath, "/")
		if len(parts) < 4 || parts[0] != StateRoot {
			continue
		}
		schema, ok := collections[parts[1]]
		if !ok {
			continue
		}
		objectRoot := strings.Join(parts[:3], "/")
		if _, checked := orphanChecked[objectRoot]; checked {
			continue
		}
		orphanChecked[objectRoot] = struct{}{}
		manifestPath := objectRoot + "/" + lifecycleManifestName(schema.Kind)
		if _, present := parsed.Sources[manifestPath]; !present {
			issues = append(issues, validationIssue("lifecycle_manifest_missing", manifestPath, "lifecycle directory is missing its required manifest"))
		}
	}
	seen := map[string]struct{}{}
	for _, object := range parsed.Lifecycle {
		if object.ParseIssue != nil {
			continue
		}
		schema, ok := schemas[object.Kind]
		if !ok {
			issues = append(issues, validationIssue("lifecycle_kind_invalid", object.Source.Path, "unsupported lifecycle kind"))
			continue
		}
		if _, duplicate := seen[object.Source.Path]; duplicate {
			issues = append(issues, validationIssue("lifecycle_duplicate", object.Source.Path, "lifecycle manifest path is duplicated"))
			continue
		}
		seen[object.Source.Path] = struct{}{}
		for _, field := range schema.RequiredFields {
			value, present := object.raw[field]
			if !present || !requiredLifecycleValue(field, value) {
				issues = append(issues, validationIssue("lifecycle_required_field_invalid", object.Source.Path, fmt.Sprintf("required field %s is absent or invalid", field)))
			}
		}
		if anyString(object.raw, "kind") != string(object.Kind) {
			issues = append(issues, validationIssue("lifecycle_kind_mismatch", object.Source.Path, "kind does not match the lifecycle directory"))
		}
		if anyString(object.raw, "schema_version") != schema.SchemaVersion {
			issues = append(issues, validationIssue("lifecycle_schema_unsupported", object.Source.Path, "schema_version does not match the frozen v1 lifecycle schema"))
		}
		status := anyString(object.raw, "status")
		if stringIndex(schema.AllowedStatuses, status) < 0 {
			issues = append(issues, validationIssue("lifecycle_status_invalid", object.Source.Path, "status is not allowed by the lifecycle schema"))
		}
		issues = append(issues, validateLifecycleIdentity(object)...)
		issues = append(issues, validateLifecycleDates(object)...)
		for _, requiredFile := range schema.RequiredFiles {
			path := StateRoot + "/" + string(object.Kind) + "s/" + object.Directory + "/" + requiredFile
			if _, ok := parsed.Sources[path]; !ok {
				issues = append(issues, validationIssue("lifecycle_required_file_missing", path, "required lifecycle file is absent"))
			}
		}
		issues = append(issues, validateConditionalLifecycleFiles(parsed, object, status)...)
	}
	return issues
}

func validateLifecycleIdentity(object ParsedLifecycleObject) []ValidationIssue {
	issues := []ValidationIssue{}
	wantID := ""
	switch object.Kind {
	case ObjectKindFuture, ObjectKindInitiative, ObjectKindFeature:
		slug := anyString(object.raw, "slug")
		if slug != object.Directory || !validSlug(slug) {
			issues = append(issues, validationIssue("lifecycle_slug_mismatch", object.Source.Path, "slug must match its portable directory"))
		}
		wantID = string(object.Kind) + "/" + object.Directory
	case ObjectKindDecision:
		decisionID := anyString(object.raw, "decision_id")
		if decisionID != object.Directory || decisionID == "" {
			issues = append(issues, validationIssue("decision_id_mismatch", object.Source.Path, "decision_id must match its directory"))
		}
		wantID = "decision/" + object.Directory
	case ObjectKindRelease:
		version := anyString(object.raw, "version")
		if version != object.Directory || version == "" || strings.ContainsAny(version, `/\\`) {
			issues = append(issues, validationIssue("release_version_mismatch", object.Source.Path, "version must match its portable directory"))
		}
		wantID = "release/" + object.Directory
	}
	if anyString(object.raw, "id") != wantID {
		issues = append(issues, validationIssue("lifecycle_id_mismatch", object.Source.Path, "id does not match the deterministic lifecycle identity"))
	}
	return issues
}

func validateLifecycleDates(object ParsedLifecycleObject) []ValidationIssue {
	issues := []ValidationIssue{}
	for _, field := range []string{"date", "created_at", "updated_at", "target_date"} {
		if _, present := object.raw[field]; !present {
			continue
		}
		if _, ok := sourceDate(object.raw, field); !ok {
			issues = append(issues, validationIssue("lifecycle_date_invalid", object.Source.Path, fmt.Sprintf("%s must use YYYY-MM-DD", field)))
		}
	}
	return issues
}

func validateConditionalLifecycleFiles(parsed ParsedRepositoryState, object ParsedLifecycleObject, status string) []ValidationIssue {
	issues := []ValidationIssue{}
	base := StateRoot + "/" + string(object.Kind) + "s/" + object.Directory + "/"
	if object.Kind == ObjectKindFeature {
		statuses := []string{"discovery", "planned", "ready", "in_progress", "review", "integrated", "validated", "deferred", "cancelled"}
		position := stringIndex(statuses, status)
		if position >= stringIndex(statuses, "review") {
			if _, ok := parsed.Sources[base+"handoff.md"]; !ok {
				issues = append(issues, validationIssue("feature_handoff_missing", base+"handoff.md", "feature status requires handoff.md"))
			}
		}
		if status == "integrated" || status == "validated" {
			if !hasAcceptanceSource(parsed.Sources, base) {
				issues = append(issues, validationIssue("feature_acceptance_missing", base+"acceptance.md", "feature status requires acceptance evidence"))
			}
		}
	}
	if object.Kind == ObjectKindRelease && (status == "acceptance" || status == "released") && !hasAcceptanceSource(parsed.Sources, base) {
		issues = append(issues, validationIssue("release_acceptance_missing", base+"acceptance.md", "release status requires acceptance evidence"))
	}
	return issues
}

func validateTrackedSources(parsed ParsedRepositoryState, observation GitObservation) []ValidationIssue {
	issues := []ValidationIssue{}
	tracked := make(map[string]GitEntry, len(observation.Entries))
	for _, entry := range observation.Entries {
		if _, duplicate := tracked[entry.Path]; duplicate {
			issues = append(issues, validationIssue("tracked_source_duplicate", entry.Path, "Git observation returned a duplicate source path"))
			continue
		}
		tracked[entry.Path] = entry
		if !regularGitMode(entry.Mode) {
			issues = append(issues, validationIssue("non_regular_source", entry.Path, "only regular Git entries are allowed beneath .repo"))
		}
	}
	for path, source := range parsed.Sources {
		entry, ok := tracked[path]
		if !ok {
			issues = append(issues, validationIssue("untracked_source", path, "every portable .repo file must be Git-tracked"))
			continue
		}
		if !bytes.Equal(entry.Content, source.data) {
			issues = append(issues, validationIssue("source_observation_changed", path, "source changed during bounded observation"))
		}
	}
	for path := range tracked {
		if _, ok := parsed.Sources[path]; !ok {
			issues = append(issues, validationIssue("tracked_source_missing", path, "tracked source is absent from the extracted filesystem view"))
		}
	}
	for _, path := range observation.UntrackedPaths {
		issues = append(issues, validationIssue("untracked_source", path, "every portable .repo file must be Git-tracked"))
	}
	return issues
}

func hasParseStage(issues []ParseIssue, stage ParseIssueStage) bool {
	for _, issue := range issues {
		if issue.Stage == stage {
			return true
		}
	}
	return false
}

func (result *ValidationResult) addParseIssues(issues []ParseIssue, stage ParseIssueStage) {
	for _, issue := range issues {
		if issue.Stage == stage {
			result.Issues = append(result.Issues, validationIssue(issue.Code, issue.Path, issue.Detail))
		}
	}
	sortValidationIssues(result.Issues)
}

func validationIssue(code, path, detail string) ValidationIssue {
	detail = strings.TrimSpace(detail)
	if len(detail) > 256 {
		detail = detail[:256]
	}
	return ValidationIssue{Code: code, Path: path, Detail: detail}
}

func sortValidationIssues(issues []ValidationIssue) {
	sort.Slice(issues, func(left, right int) bool {
		if issues[left].Path != issues[right].Path {
			return issues[left].Path < issues[right].Path
		}
		return issues[left].Code < issues[right].Code
	})
}

func validRequiredText(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validSlug(value string) bool {
	if value == "" || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validateAliases(values []string) string {
	if !sort.StringsAreSorted(values) {
		return "aliases must be sorted by UTF-8 bytes"
	}
	seen := []string{}
	for _, value := range values {
		if !validRequiredText(value) {
			return "aliases must be non-empty and trimmed"
		}
		for _, previous := range seen {
			if strings.EqualFold(previous, value) {
				return "aliases must be unique after case folding"
			}
		}
		seen = append(seen, value)
	}
	return ""
}

func validateTopics(values []string) string {
	if !sort.StringsAreSorted(values) {
		return "topics must be sorted by UTF-8 bytes"
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !validSlug(value) || strings.ToLower(value) != value {
			return "topics must be lowercase portable slugs"
		}
		if _, duplicate := seen[value]; duplicate {
			return "topics must be unique"
		}
		seen[value] = struct{}{}
	}
	return ""
}

func validGitBranch(value string) bool {
	if value == "" || value == "@" || strings.TrimSpace(value) != value || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, ".") || strings.HasSuffix(value, "/") {
		return false
	}
	if strings.Contains(value, "..") || strings.Contains(value, "//") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func requiredLifecycleValue(field string, value any) bool {
	switch field {
	case "dependencies", "features":
		_, ok := anyStringSlice(value)
		return ok
	case "date", "created_at", "updated_at", "target_date":
		switch value.(type) {
		case string, time.Time:
			return true
		default:
			return false
		}
	default:
		stringValue, ok := value.(string)
		return ok && validRequiredText(stringValue)
	}
}

func anyStringSlice(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		stringValues, stringsOK := value.([]string)
		return stringValues, stringsOK
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		stringValue, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, stringValue)
	}
	return result, true
}

func hasAcceptanceSource(sources map[string]ParsedSource, base string) bool {
	for sourcePath := range sources {
		if strings.HasPrefix(sourcePath, base) && !strings.Contains(strings.TrimPrefix(sourcePath, base), "/") && isAcceptanceDocument(sourcePath) {
			return true
		}
	}
	return false
}

func validProjectRelativeNavigation(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return false
	}
	return true
}

func validSourceDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func declaredDateAfter(parsed ParsedRepositoryState, observedAt time.Time) bool {
	for _, document := range []*ParsedDocument{parsed.RepositorySummary, parsed.State, parsed.Roadmap} {
		if document != nil && document.LastReviewed != nil && document.LastReviewed.After(observedAt) {
			return true
		}
	}
	for _, object := range parsed.Lifecycle {
		for _, field := range []string{"date", "created_at", "updated_at", "target_date"} {
			if date, ok := sourceDate(object.raw, field); ok && date.After(observedAt) {
				return true
			}
		}
	}
	return false
}

func gitObservationInvalidatesSource(err error) bool {
	switch gitObservationErrorCode(err) {
	case "non_regular_entry", "index_malformed", "index_path_invalid", "index_too_large", "tracked_file_missing", "tracked_file_unreadable", "tracked_sources_too_large":
		return true
	default:
		return false
	}
}
