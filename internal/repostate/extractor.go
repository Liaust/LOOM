package repostate

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ExtractInput struct {
	RepositoryRoot string                   `json:"repository_root"`
	Membership     *AuthoritativeMembership `json:"membership,omitempty"`
}

type SourceMetadata struct {
	Posture    SourcePosture `json:"posture"`
	Path       string        `json:"path,omitempty"`
	Digest     string        `json:"digest,omitempty"`
	Commit     string        `json:"commit,omitempty"`
	ObservedAt *time.Time    `json:"observed_at,omitempty"`
	Reasons    []string      `json:"reasons"`
}

type ExtractedField struct {
	Name   string         `json:"name"`
	Value  any            `json:"value,omitempty"`
	Source SourceMetadata `json:"source"`
}

type ExtractedDocument struct {
	Title    string                   `json:"title,omitempty"`
	Sections map[string]ParsedSection `json:"sections"`
	Source   SourceMetadata           `json:"source"`
}

type ExtractedLifecycleObject struct {
	Kind      ObjectKind     `json:"kind"`
	Directory string         `json:"directory"`
	Fields    map[string]any `json:"fields"`
	Source    SourceMetadata `json:"source"`
}

type Freshness struct {
	ObservedAt         time.Time       `json:"observed_at"`
	HeadCommit         string          `json:"head_commit,omitempty"`
	SourceCommit       string          `json:"source_commit,omitempty"`
	LatestRepoCommitAt *time.Time      `json:"latest_repo_commit_at,omitempty"`
	AgeSeconds         uint64          `json:"age_seconds"`
	TrackedState       GitTrackedState `json:"tracked_state"`
	Reasons            []string        `json:"reasons"`
}

type Extraction struct {
	TrackingStatus  TrackingStatus             `json:"tracking_status"`
	Validation      ValidationResult           `json:"validation"`
	Manifest        *RepositoryManifest        `json:"manifest,omitempty"`
	ManifestSource  SourceMetadata             `json:"manifest_source"`
	Summary         *ExtractedDocument         `json:"summary,omitempty"`
	State           *ExtractedDocument         `json:"state,omitempty"`
	Roadmap         *ExtractedDocument         `json:"roadmap,omitempty"`
	Features        []ExtractedLifecycleObject `json:"features"`
	Decisions       []ExtractedLifecycleObject `json:"decisions"`
	Releases        []ExtractedLifecycleObject `json:"releases"`
	Integrations    []ExtractedDocument        `json:"integrations"`
	Handoffs        []ExtractedDocument        `json:"handoffs"`
	Acceptance      []ExtractedDocument        `json:"acceptance"`
	DiscoveryFields []ExtractedField           `json:"discovery_fields"`
	Freshness       Freshness                  `json:"freshness"`
	AcceptedContext []string                   `json:"accepted_context"`
}

type Extractor struct {
	Parser    Parser
	Validator Validator
	Git       GitObserver
}

// Extract composes the read-only parser, validator, and bounded Git observer.
// It deliberately cannot join or manufacture accepted semantic context.
func (extractor Extractor) Extract(ctx context.Context, input ExtractInput) Extraction {
	parsed := extractor.Parser.Parse(input.RepositoryRoot)
	var observation *GitObservation
	var observationErr error
	if parsed.gitObservationSafe {
		gitObserver := extractor.Git
		if gitObserver == nil {
			gitObserver = OSGitObserver{}
		}
		observed, err := gitObserver.Observe(ctx, input.RepositoryRoot)
		if err != nil {
			observationErr = err
		} else {
			observation = &observed
		}
	}
	validation := extractor.Validator.Validate(parsed, input.Membership, observation, observationErr)
	extraction := Extraction{
		TrackingStatus:  validation.Status,
		Validation:      validation,
		Manifest:        parsed.Manifest,
		Features:        []ExtractedLifecycleObject{},
		Decisions:       []ExtractedLifecycleObject{},
		Releases:        []ExtractedLifecycleObject{},
		Integrations:    []ExtractedDocument{},
		Handoffs:        []ExtractedDocument{},
		Acceptance:      []ExtractedDocument{},
		DiscoveryFields: []ExtractedField{},
		AcceptedContext: []string{},
	}
	sourceCommit, observedAt := observationIdentity(observation)
	manifestSource := parsed.Sources[RepositoryManifestPath]
	extraction.ManifestSource = metadataForSource(manifestSource, declaredManifestPosture(parsed, validation.Status), sourceCommit, observedAt)
	extraction.Summary = projectDocument(parsed.RepositorySummary, validation.Status, sourceCommit, observedAt)
	extraction.State = projectDocument(parsed.State, validation.Status, sourceCommit, observedAt)
	extraction.Roadmap = projectDocument(parsed.Roadmap, validation.Status, sourceCommit, observedAt)
	for _, object := range parsed.Lifecycle {
		projected := ExtractedLifecycleObject{
			Kind:      object.Kind,
			Directory: object.Directory,
			Fields:    cloneAnyMap(object.Fields),
			Source:    metadataForSource(object.Source, lifecyclePosture(object, validation.Status), sourceCommit, observedAt),
		}
		switch object.Kind {
		case ObjectKindFeature:
			extraction.Features = append(extraction.Features, projected)
		case ObjectKindDecision:
			extraction.Decisions = append(extraction.Decisions, projected)
		case ObjectKindRelease:
			extraction.Releases = append(extraction.Releases, projected)
		}
	}
	for _, document := range parsed.Integrations {
		extraction.Integrations = append(extraction.Integrations, *projectDocument(&document, validation.Status, sourceCommit, observedAt))
	}
	for _, document := range parsed.Handoffs {
		extraction.Handoffs = append(extraction.Handoffs, *projectDocument(&document, validation.Status, sourceCommit, observedAt))
	}
	for _, document := range parsed.Acceptance {
		extraction.Acceptance = append(extraction.Acceptance, *projectDocument(&document, validation.Status, sourceCommit, observedAt))
	}

	sourceDigest := ""
	if observation != nil && len(observation.Entries) > 0 {
		if digest, err := CanonicalSourceDigest(observation.Entries); err == nil {
			sourceDigest = digest
		}
	}
	extraction.Freshness = deriveFreshness(parsed, observation, observationErr)
	extraction.DiscoveryFields = discoveryProjection(parsed, input.Membership, validation, observation, sourceDigest, extraction.Freshness)
	return extraction
}

// CanonicalSourceDigest implements the exact v1 framing frozen by SchemaV1.
// Input order is ignored; source paths are sorted by raw UTF-8 bytes.
func CanonicalSourceDigest(entries []GitEntry) (string, error) {
	ordered := append([]GitEntry(nil), entries...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Path < ordered[right].Path })
	digest := sha256.New()
	_, _ = digest.Write([]byte("loom.repo.source.v1\x00"))
	previous := ""
	for _, entry := range ordered {
		if !validRepoSourcePath(entry.Path) || !regularGitMode(entry.Mode) {
			return "", fmt.Errorf("invalid source entry %q", entry.Path)
		}
		if entry.Path == previous {
			return "", fmt.Errorf("duplicate source entry %q", entry.Path)
		}
		previous = entry.Path
		writeDigestFrame(digest.Write, []byte(entry.Path))
		writeDigestFrame(digest.Write, []byte(entry.Mode))
		writeDigestFrame(digest.Write, entry.Content)
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
}

func writeDigestFrame(write func([]byte) (int, error), payload []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
	_, _ = write(length[:])
	_, _ = write(payload)
}

func deriveFreshness(parsed ParsedRepositoryState, observation *GitObservation, observationErr error) Freshness {
	freshness := Freshness{TrackedState: GitTrackedUnavailable, Reasons: []string{}}
	if observation == nil {
		if !parsed.Enabled {
			return freshness
		}
		freshness.Reasons = append(freshness.Reasons, "git_observation_unavailable")
		if parsed.UnsupportedVersion {
			freshness.Reasons = append(freshness.Reasons, "unsupported_schema")
		}
		sort.Strings(freshness.Reasons)
		return freshness
	}
	freshness.ObservedAt = observation.ObservedAt
	freshness.HeadCommit = observation.HeadCommit
	freshness.SourceCommit = observation.SourceCommit
	freshness.TrackedState = observation.TrackedState
	if !observation.LatestRepoCommitAt.IsZero() {
		latest := observation.LatestRepoCommitAt
		freshness.LatestRepoCommitAt = &latest
		if observation.ObservedAt.After(latest) {
			freshness.AgeSeconds = uint64(observation.ObservedAt.Sub(latest) / time.Second)
		}
	}
	if observation.TrackedState == GitTrackedDirty {
		freshness.Reasons = append(freshness.Reasons, "repo_tree_dirty")
	}
	if observation.SourceCommit != "" && observation.SourceCommit != observation.HeadCommit {
		freshness.Reasons = append(freshness.Reasons, "source_commit_not_head")
	}
	if declaredDateAfter(parsed, observation.ObservedAt) {
		freshness.Reasons = append(freshness.Reasons, "declared_date_after_observation")
	}
	if parsed.UnsupportedVersion {
		freshness.Reasons = append(freshness.Reasons, "unsupported_schema")
	}
	if observationErr != nil {
		freshness.Reasons = append(freshness.Reasons, "git_observation_unavailable")
	}
	freshness.Reasons = sortedUniqueStrings(freshness.Reasons)
	return freshness
}

func discoveryProjection(parsed ParsedRepositoryState, membership *AuthoritativeMembership, validation ValidationResult, observation *GitObservation, sourceDigest string, freshness Freshness) []ExtractedField {
	fields := make([]ExtractedField, 0, len(SchemaV1().DiscoveryFields))
	manifestPath := RepositoryManifestPath
	manifestSource := parsed.Sources[manifestPath]
	manifestPosture := declaredManifestPosture(parsed, validation.Status)
	commit, observedAt := observationIdentity(observation)
	add := func(name string, value any, source ParsedSource, posture SourcePosture, reasons ...string) {
		fields = append(fields, ExtractedField{Name: name, Value: value, Source: metadataForSource(source, posture, commit, observedAt, reasons...)})
	}
	if parsed.Manifest != nil {
		add("name", parsed.Manifest.Repository.Name, manifestSource, manifestPosture)
		add("aliases", append([]string(nil), parsed.Manifest.Repository.Aliases...), manifestSource, manifestPosture)
		ownerPosture := manifestPosture
		if validationHasIssue(validation, "owner_project_mismatch") {
			ownerPosture = PostureInvalid
		}
		add("owning_project", parsed.Manifest.OwnerProject, manifestSource, ownerPosture)
		rolePosture := manifestPosture
		if validationHasIssue(validation, "owner_role_mismatch") {
			rolePosture = PostureInvalid
		}
		add("role", parsed.Manifest.Repository.Role, manifestSource, rolePosture)
		add("purpose", parsed.Manifest.Repository.Purpose, manifestSource, manifestPosture)
		add("topics", append([]string(nil), parsed.Manifest.Repository.Topics...), manifestSource, manifestPosture)
	} else {
		for _, name := range []string{"name", "aliases", "owning_project", "role", "purpose", "topics"} {
			add(name, nil, manifestSource, manifestPosture)
		}
	}
	addDocumentField := func(name string, document *ParsedDocument, section string, list bool) {
		if document == nil {
			add(name, nil, ParsedSource{Path: discoveryDocumentPath(name)}, missingOrContractPosture(parsed, validation.Status))
			return
		}
		parsedSection, ok := document.Sections[section]
		if !ok || (parsedSection.Text == "" && len(parsedSection.Items) == 0) {
			add(name, nil, document.Source, missingOrContractPosture(parsed, validation.Status))
			return
		}
		value := any(parsedSection.Text)
		if list {
			value = append([]string(nil), parsedSection.Items...)
		}
		add(name, value, document.Source, declaredDocumentPosture(validation.Status))
	}
	addDocumentField("current_state", parsed.State, "current_state", false)
	addDocumentField("active_focus", parsed.State, "active_focus", true)
	addDocumentField("recent_outcomes", parsed.State, "recent_outcomes", true)
	addDocumentField("next_priorities", parsed.Roadmap, "next_priorities", true)
	addDocumentField("blockers", parsed.State, "blockers", true)

	if membership == nil || membership.NavigationPath == "" {
		fields = append(fields, ExtractedField{Name: "navigation_path", Source: metadataForSource(ParsedSource{Path: membershipSourcePath(membership)}, PostureMissing, membershipSourceCommit(membership), observedAt)})
	} else if !validProjectRelativeNavigation(membership.NavigationPath) {
		fields = append(fields, ExtractedField{Name: "navigation_path", Value: membership.NavigationPath, Source: metadataForSource(membershipParsedSource(membership), PostureInvalid, membershipSourceCommit(membership), observedAt, "navigation_path_not_project_relative")})
	} else {
		fields = append(fields, ExtractedField{Name: "navigation_path", Value: membership.NavigationPath, Source: metadataForSource(membershipParsedSource(membership), PostureDerived, membershipSourceCommit(membership), observedAt)})
	}
	trackingPosture := PostureDerived
	if validation.Status == TrackingStaleVersion {
		trackingPosture = PostureStale
	} else if validation.Status != TrackingValid && validation.Status != TrackingNotEnabled {
		trackingPosture = PostureInvalid
	}
	add("tracking_status", validation.Status, ParsedSource{Path: StateRoot, Digest: sourceDigest}, trackingPosture)
	sourceDigestPosture := PostureDerived
	if sourceDigest == "" {
		sourceDigestPosture = PostureMissing
	} else if validation.Status == TrackingStaleVersion {
		sourceDigestPosture = PostureStale
	} else if validation.Status == TrackingMalformed {
		sourceDigestPosture = PostureInvalid
	}
	add("source_digest", sourceDigest, ParsedSource{Path: StateRoot, Digest: sourceDigest}, sourceDigestPosture)
	if observation == nil || observation.SourceCommit == "" {
		reasons := []string{}
		if parsed.Enabled {
			reasons = append(reasons, "git_observation_unavailable")
		}
		add("source_commit", nil, ParsedSource{Path: StateRoot}, PostureMissing, reasons...)
	} else {
		posture := PostureObserved
		if validation.Status == TrackingStaleVersion || observation.SourceCommit != observation.HeadCommit {
			posture = PostureStale
		}
		add("source_commit", observation.SourceCommit, ParsedSource{Path: StateRoot, Digest: sourceDigest}, posture)
	}
	freshnessPosture := PostureDerived
	if observation == nil {
		freshnessPosture = PostureMissing
	} else if len(freshness.Reasons) > 0 {
		freshnessPosture = PostureStale
	}
	add("freshness", freshness, ParsedSource{Path: StateRoot, Digest: sourceDigest}, freshnessPosture, freshness.Reasons...)
	return orderDiscoveryFields(fields)
}

func projectDocument(document *ParsedDocument, status TrackingStatus, commit string, observedAt *time.Time) *ExtractedDocument {
	if document == nil {
		return nil
	}
	return &ExtractedDocument{
		Title:    document.Title,
		Sections: document.Sections,
		Source:   metadataForSource(document.Source, declaredDocumentPosture(status), commit, observedAt),
	}
}

func metadataForSource(source ParsedSource, posture SourcePosture, commit string, observedAt *time.Time, reasons ...string) SourceMetadata {
	return SourceMetadata{
		Posture:    posture,
		Path:       source.Path,
		Digest:     source.Digest,
		Commit:     commit,
		ObservedAt: observedAt,
		Reasons:    sortedUniqueStrings(reasons),
	}
}

func observationIdentity(observation *GitObservation) (string, *time.Time) {
	if observation == nil {
		return "", nil
	}
	observedAt := observation.ObservedAt
	return observation.SourceCommit, &observedAt
}

func lifecyclePosture(object ParsedLifecycleObject, status TrackingStatus) SourcePosture {
	if object.ParseIssue != nil || (status != TrackingValid && status != TrackingMismatchedRepository && status != TrackingMismatchedOwner && status != TrackingMismatchedMembership && status != TrackingMembershipUnresolved) {
		if status == TrackingStaleVersion {
			return PostureStale
		}
		return PostureInvalid
	}
	return PostureDeclared
}

func declaredManifestPosture(parsed ParsedRepositoryState, status TrackingStatus) SourcePosture {
	if parsed.UnsupportedVersion || status == TrackingStaleVersion {
		return PostureStale
	}
	if parsed.Manifest == nil {
		if _, ok := parsed.Sources[RepositoryManifestPath]; !ok {
			return PostureMissing
		}
		return PostureInvalid
	}
	if status == TrackingMalformed {
		return PostureInvalid
	}
	return PostureDeclared
}

func declaredDocumentPosture(status TrackingStatus) SourcePosture {
	if status == TrackingStaleVersion {
		return PostureStale
	}
	if status == TrackingMalformed {
		return PostureInvalid
	}
	return PostureDeclared
}

func missingOrContractPosture(parsed ParsedRepositoryState, status TrackingStatus) SourcePosture {
	if parsed.UnsupportedVersion || status == TrackingStaleVersion {
		return PostureStale
	}
	if status == TrackingMalformed {
		return PostureInvalid
	}
	return PostureMissing
}

func discoveryDocumentPath(name string) string {
	if name == "next_priorities" {
		return ".repo/ROADMAP.md"
	}
	return ".repo/STATE.md"
}

func membershipSourcePath(membership *AuthoritativeMembership) string {
	if membership != nil && membership.SourcePath != "" {
		return membership.SourcePath
	}
	return ".loom/contracts/repos.yaml"
}

func membershipParsedSource(membership *AuthoritativeMembership) ParsedSource {
	if membership == nil {
		return ParsedSource{Path: ".loom/contracts/repos.yaml"}
	}
	return ParsedSource{Path: membershipSourcePath(membership), Digest: membership.SourceDigest}
}

func membershipSourceCommit(membership *AuthoritativeMembership) string {
	if membership == nil {
		return ""
	}
	return membership.SourceCommit
}

func orderDiscoveryFields(fields []ExtractedField) []ExtractedField {
	byName := make(map[string]ExtractedField, len(fields))
	for _, field := range fields {
		byName[field.Name] = field
	}
	ordered := make([]ExtractedField, 0, len(fields))
	for _, schema := range SchemaV1().DiscoveryFields {
		if field, ok := byName[schema.Name]; ok {
			ordered = append(ordered, field)
		}
	}
	return ordered
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func fieldByName(fields []ExtractedField, name string) (ExtractedField, bool) {
	for _, field := range fields {
		if strings.EqualFold(field.Name, name) {
			return field, true
		}
	}
	return ExtractedField{}, false
}

func validationHasIssue(validation ValidationResult, code string) bool {
	for _, issue := range validation.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
