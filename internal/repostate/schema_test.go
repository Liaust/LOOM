package repostate

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/ids"
)

func TestRepositoryManifestV1Fixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema_v1/repo.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var manifest RepositoryManifest
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decode strict v1 fixture: %v", err)
	}
	if manifest.Kind != RepositoryManifestKind || manifest.SchemaVersion != RepositorySchemaVersion {
		t.Fatalf("unexpected envelope: kind=%q schema=%q", manifest.Kind, manifest.SchemaVersion)
	}
	if err := ids.Validate(RepositoryIDPrefix, manifest.Repository.ID); err != nil {
		t.Fatalf("repository ID fixture: %v", err)
	}
	if err := ids.Validate(ProjectIDPrefix, manifest.OwnerProject.ID); err != nil {
		t.Fatalf("project ID fixture: %v", err)
	}
	if manifest.Repository.Role != RepositoryRoleComponent {
		t.Fatalf("role = %q", manifest.Repository.Role)
	}
	if manifest.Source.Tracking != "git" || manifest.Source.StateRoot != StateRoot {
		t.Fatalf("source policy = %#v", manifest.Source)
	}
	assertSortedUnique(t, "aliases", manifest.Repository.Aliases)
	assertSortedUnique(t, "topics", manifest.Repository.Topics)
	if bytes.Contains(raw, []byte("/Users/")) || bytes.Contains(raw, []byte("/home/")) {
		t.Fatal("portable fixture contains a host-absolute path")
	}
}

func TestSchemaV1FreezesPortableTreeAndLifecycleManifests(t *testing.T) {
	schema := SchemaV1()
	requiredRoot := map[string]bool{
		".repo/repo.yaml":     false,
		".repo/README.md":     false,
		".repo/REPOSITORY.md": false,
		".repo/STATE.md":      false,
		".repo/ROADMAP.md":    false,
	}
	seenPaths := map[string]struct{}{}
	for _, path := range schema.RootPaths {
		if !strings.HasPrefix(path.Pattern, StateRoot+"/") {
			t.Fatalf("non-portable root path %q", path.Pattern)
		}
		if _, duplicate := seenPaths[path.Pattern]; duplicate {
			t.Fatalf("duplicate root path %q", path.Pattern)
		}
		seenPaths[path.Pattern] = struct{}{}
		if _, ok := requiredRoot[path.Pattern]; ok && path.Requirement == PathRequired {
			requiredRoot[path.Pattern] = true
		}
	}
	for path, found := range requiredRoot {
		if !found {
			t.Errorf("required root path not frozen: %s", path)
		}
	}

	wantKinds := []ObjectKind{ObjectKindFuture, ObjectKindInitiative, ObjectKindFeature, ObjectKindDecision, ObjectKindRelease}
	gotKinds := make([]ObjectKind, 0, len(schema.LifecycleSchemas))
	for _, lifecycle := range schema.LifecycleSchemas {
		gotKinds = append(gotKinds, lifecycle.Kind)
		if !strings.HasPrefix(lifecycle.SchemaVersion, "repo.") || !strings.HasSuffix(lifecycle.SchemaVersion, ".v1") {
			t.Errorf("%s schema version = %q", lifecycle.Kind, lifecycle.SchemaVersion)
		}
		if !strings.HasPrefix(lifecycle.ManifestPattern, StateRoot+"/") {
			t.Errorf("%s manifest is not beneath .repo: %q", lifecycle.Kind, lifecycle.ManifestPattern)
		}
		if len(lifecycle.RequiredFields) == 0 || len(lifecycle.RequiredFiles) == 0 || len(lifecycle.AllowedStatuses) == 0 {
			t.Errorf("%s lifecycle is not fully specified", lifecycle.Kind)
		}
		assertUnique(t, string(lifecycle.Kind)+" statuses", lifecycle.AllowedStatuses)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("lifecycle kinds = %#v, want %#v", gotKinds, wantKinds)
	}
}

func TestSchemaV1InventoriesCurrentProjectObjects(t *testing.T) {
	schema := SchemaV1()
	wantSources := []string{
		".project/project.yaml#project",
		".project/PROJECT.md",
		".project/README.md",
		".project/STATE.md",
		".project/ROADMAP.md",
		".project/future/INDEX.md",
		".project/future/<slug>/outline.md",
		".project/initiatives/README.md",
		".project/initiatives/<slug>/overview.md",
		".project/initiatives/<slug>/<other-files>",
		".project/features/README.md",
		".project/features/<slug>/feature.yaml",
		".project/features/<slug>/implementation_slices.md",
		".project/features/<slug>/worktree_progress.md",
		".project/features/<slug>/handoff.md",
		".project/features/<slug>/acceptance*.md",
		".project/features/<slug>/<other-files>",
		".project/decisions/README.md",
		".project/decisions/ADR-NNNN-<slug>.md",
		".project/releases/README.md",
		".project/releases/<version>/release.yaml",
		".project/integration/STATUS.md",
		".project/architecture/README.md",
		".project/architecture/*.md",
		".project/architecture/external-constraints/**",
		".project/protocols/PROJECT_STATE_PROTOCOL.md",
		".project/protocols/AI_WORKTREE_WORKFLOW.md",
		".project/protocols/SLICE_AUTOPILOT_PROTOCOL.md",
		".project/protocols/CODE_REVIEW_PROTOCOL.md",
		".project/protocols/DOCS_WRITING_PROTOCOL.md",
		".project/templates/decision.md",
		".project/templates/docs/**",
		".project/templates/future/**",
		".project/templates/initiative/**",
		".project/templates/feature/**",
		".project/templates/release/**",
		".project/archive/{README.md,PATH_MIGRATION.md}",
		".project/archive/<imported-trees>/**",
	}
	bySource := make(map[string]CompatibilityEntry, len(schema.CompatibilityMap))
	dispositions := map[CompatibilityDisposition]bool{}
	for _, entry := range schema.CompatibilityMap {
		if _, duplicate := bySource[entry.ProjectSource]; duplicate {
			t.Fatalf("duplicate compatibility source %q", entry.ProjectSource)
		}
		bySource[entry.ProjectSource] = entry
		dispositions[entry.Disposition] = true
		if entry.Rule == "" || entry.RepoTarget == "" {
			t.Errorf("incomplete compatibility entry for %q", entry.ProjectSource)
		}
	}
	for _, source := range wantSources {
		if _, ok := bySource[source]; !ok {
			t.Errorf("current .project object is not classified: %s", source)
		}
	}
	for _, disposition := range []CompatibilityDisposition{DispositionPreserve, DispositionRename, DispositionSplit, DispositionArchive, DispositionDrop} {
		if !dispositions[disposition] {
			t.Errorf("compatibility disposition %q is not represented", disposition)
		}
	}
}

func TestSchemaV1PreservesCurrentInitiativeAttachments(t *testing.T) {
	const (
		overviewSource   = ".project/initiatives/<slug>/overview.md"
		attachmentSource = ".project/initiatives/<slug>/<other-files>"
		attachmentTarget = ".repo/initiatives/<slug>/<other-files>"
	)

	currentAttachments := []string{
		".project/initiatives/active-workspace-and-archive-lifecycle/reconciliation.md",
		".project/initiatives/performance-and-maintainability/benchmark-plan.md",
		".project/initiatives/semantic-context-and-human-control-plane/basecamp-control-plane.md",
		".project/initiatives/semantic-context-and-human-control-plane/delivery-plan.md",
		".project/initiatives/semantic-context-and-human-control-plane/information-and-search-model.md",
		".project/initiatives/semantic-context-and-human-control-plane/provenance-model.md",
		".project/initiatives/system-solidification-and-morathustra-acceptance/developer_experience_audit_20260912.md",
		".project/initiatives/system-solidification-and-morathustra-acceptance/readiness_review_20260910.md",
	}

	schema := SchemaV1()
	overviewIndex := -1
	attachmentIndex := -1
	var attachment CompatibilityEntry
	for index, entry := range schema.CompatibilityMap {
		switch entry.ProjectSource {
		case overviewSource:
			overviewIndex = index
		case attachmentSource:
			attachmentIndex = index
			attachment = entry
		}
	}
	if overviewIndex < 0 || attachmentIndex < 0 {
		t.Fatalf("initiative compatibility entries are incomplete: overview=%d attachments=%d", overviewIndex, attachmentIndex)
	}
	if attachmentIndex <= overviewIndex {
		t.Fatalf("initiative attachment rule must follow the dedicated overview rule: overview=%d attachments=%d", overviewIndex, attachmentIndex)
	}
	if attachment.Disposition != DispositionPreserve || attachment.RepoTarget != attachmentTarget {
		t.Fatalf("initiative attachment compatibility = %#v", attachment)
	}

	// Exercise the historical shapes without depending on private planning files.
	fixtureRoot := t.TempDir()
	for _, source := range currentAttachments {
		target := filepath.Join(fixtureRoot, source)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("fixture attachment\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	initiativeRoot := filepath.Join(fixtureRoot, ".project", "initiatives")
	initiativeDirs, err := os.ReadDir(initiativeRoot)
	if err != nil {
		t.Fatalf("read current initiative inventory: %v", err)
	}
	actualAttachments := make([]string, 0, len(currentAttachments))
	for _, initiative := range initiativeDirs {
		if !initiative.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(initiativeRoot, initiative.Name()))
		if err != nil {
			t.Fatalf("read initiative %q: %v", initiative.Name(), err)
		}
		for _, file := range files {
			if file.Name() == "overview.md" {
				continue
			}
			if file.IsDir() || !file.Type().IsRegular() {
				t.Fatalf("initiative attachment needs a stricter compatibility rule: %s/%s", initiative.Name(), file.Name())
			}
			actualAttachments = append(actualAttachments, ".project/initiatives/"+initiative.Name()+"/"+file.Name())
		}
	}
	sort.Strings(actualAttachments)
	if !reflect.DeepEqual(actualAttachments, currentAttachments) {
		t.Fatalf("current initiative attachments = %#v, want frozen inventory %#v", actualAttachments, currentAttachments)
	}

	for _, source := range currentAttachments {
		parts := strings.Split(source, "/")
		if len(parts) != 4 || parts[0] != ".project" || parts[1] != "initiatives" || parts[2] == "" || parts[3] == "" {
			t.Errorf("current initiative attachment has an unsupported shape: %q", source)
			continue
		}
		if parts[3] == "overview.md" || parts[3] == "initiative.yaml" {
			t.Errorf("dedicated initiative semantics leaked into attachment inventory: %q", source)
		}
	}
}

func TestSchemaV1FreezesSemanticDiscoveryPosture(t *testing.T) {
	schema := SchemaV1()
	wantNames := []string{
		"name", "aliases", "owning_project", "role", "purpose", "topics",
		"current_state", "active_focus", "recent_outcomes", "next_priorities", "blockers",
		"navigation_path", "tracking_status", "source_digest", "source_commit", "freshness",
	}
	gotNames := make([]string, 0, len(schema.DiscoveryFields))
	allowed := map[SourcePosture]bool{
		PostureDeclared: true, PostureObserved: true, PostureDerived: true,
		PostureMissing: true, PostureStale: true, PostureInvalid: true,
	}
	for _, field := range schema.DiscoveryFields {
		gotNames = append(gotNames, field.Name)
		if !allowed[field.PrimaryPosture] || field.ValueShape == "" || field.Source == "" || field.Rule == "" {
			t.Errorf("incomplete discovery field %#v", field)
		}
		for _, posture := range field.AllowedPostures {
			if !allowed[posture] {
				t.Errorf("field %s permits non-source posture %q", field.Name, posture)
			}
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("discovery fields = %#v, want %#v", gotNames, wantNames)
	}
	if !strings.Contains(schema.AcceptedContextRule, "always emits accepted_context as empty") {
		t.Fatalf("accepted-context boundary not frozen: %q", schema.AcceptedContextRule)
	}
	if schema.SourceDigestPolicy.Algorithm != "SHA-256" ||
		!strings.Contains(schema.SourceDigestPolicy.Framing, "Git mode") ||
		!strings.Contains(schema.SourceDigestPolicy.InputSet, "non-regular Git entries are malformed") {
		t.Fatalf("source digest policy is not deterministic: %#v", schema.SourceDigestPolicy)
	}
	assertSortedUnique(t, "freshness tracked states", schema.FreshnessPolicy.TrackedState)
	assertSortedUnique(t, "freshness stale reasons", schema.FreshnessPolicy.StaleReasons)
}

func TestSchemaV1MembershipFixtureFreezesPrecedenceAndReferenceRule(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema_v1/membership_cases.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fixture membershipFixture
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode strict membership fixture: %v", err)
	}
	if len(fixture.Cases) < 12 {
		t.Fatalf("membership fixture has only %d cases", len(fixture.Cases))
	}
	seen := map[string]struct{}{}
	for _, testCase := range fixture.Cases {
		if _, duplicate := seen[testCase.Name]; duplicate {
			t.Fatalf("duplicate membership case %q", testCase.Name)
		}
		seen[testCase.Name] = struct{}{}
		if got := classifyMembershipFixture(testCase); got != testCase.Expected {
			t.Errorf("%s: got %q, want %q", testCase.Name, got, testCase.Expected)
		}
	}

	rules := SchemaV1().MembershipRules
	for index, rule := range rules {
		if rule.Precedence != index+1 {
			t.Errorf("rule %s precedence = %d, want %d", rule.Code, rule.Precedence, index+1)
		}
	}
	if _, ok := seen["valid_reference_view_uses_canonical_owner"]; !ok {
		t.Fatal("reference-view compatibility case is missing")
	}
}

func TestSchemaV1IsImmutableByConventionAndNonMutating(t *testing.T) {
	first := SchemaV1()
	first.RootPaths[0].Pattern = "changed"
	first.LifecycleSchemas[0].AllowedStatuses[0] = "changed"
	second := SchemaV1()
	if second.RootPaths[0].Pattern != RepositoryManifestPath || second.LifecycleSchemas[0].AllowedStatuses[0] != "captured" {
		t.Fatal("SchemaV1 returned shared mutable slices")
	}
	if len(second.VersionPolicy.SupportedRepositoryVersions) != 1 || second.VersionPolicy.SupportedRepositoryVersions[0] != RepositorySchemaVersion {
		t.Fatalf("supported versions = %#v", second.VersionPolicy.SupportedRepositoryVersions)
	}
	for _, rule := range second.PortableTrackingRules {
		if strings.Contains(strings.ToLower(rule), "automatically commit") {
			t.Fatalf("mutating tracking rule: %q", rule)
		}
	}
}

type membershipFixture struct {
	Cases []membershipCase `yaml:"cases"`
}

type membershipCase struct {
	Name                          string         `yaml:"name"`
	StateRootPresent              bool           `yaml:"state_root_present"`
	EnvelopeMalformed             bool           `yaml:"envelope_malformed"`
	BodyMalformed                 bool           `yaml:"body_malformed"`
	SchemaVersion                 string         `yaml:"schema_version"`
	ManifestRepositoryID          string         `yaml:"manifest_repository_id"`
	ManifestOwnerProjectID        string         `yaml:"manifest_owner_project_id"`
	ManifestOwnerProjectSlug      string         `yaml:"manifest_owner_project_slug"`
	ManifestRole                  RepositoryRole `yaml:"manifest_role"`
	OwnerResolved                 bool           `yaml:"owner_resolved"`
	AuthoritativeRepositoryID     string         `yaml:"authoritative_repository_id"`
	AuthoritativeOwnerProjectID   string         `yaml:"authoritative_owner_project_id"`
	AuthoritativeOwnerProjectSlug string         `yaml:"authoritative_owner_project_slug"`
	AuthoritativeOwnerRole        RepositoryRole `yaml:"authoritative_owner_role"`
	AuthoritativeStateRoot        string         `yaml:"authoritative_state_root"`
	ViewedMembershipRole          string         `yaml:"viewed_membership_role"`
	Expected                      TrackingStatus `yaml:"expected"`
}

func classifyMembershipFixture(testCase membershipCase) TrackingStatus {
	if !testCase.StateRootPresent {
		return TrackingNotEnabled
	}
	if testCase.EnvelopeMalformed {
		return TrackingMalformed
	}
	if testCase.SchemaVersion != RepositorySchemaVersion {
		return TrackingStaleVersion
	}
	if testCase.BodyMalformed {
		return TrackingMalformed
	}
	if !testCase.OwnerResolved {
		return TrackingMembershipUnresolved
	}
	if testCase.ManifestRepositoryID != testCase.AuthoritativeRepositoryID {
		return TrackingMismatchedRepository
	}
	if testCase.ManifestOwnerProjectID != testCase.AuthoritativeOwnerProjectID ||
		testCase.ManifestOwnerProjectSlug != testCase.AuthoritativeOwnerProjectSlug {
		return TrackingMismatchedOwner
	}
	if testCase.ManifestRole != testCase.AuthoritativeOwnerRole ||
		(testCase.ManifestRole != RepositoryRolePrimary && testCase.ManifestRole != RepositoryRoleComponent) {
		return TrackingMismatchedMembership
	}
	stateRoot := testCase.AuthoritativeStateRoot
	if stateRoot == "" {
		stateRoot = StateRoot
	}
	if stateRoot != StateRoot {
		return TrackingMismatchedMembership
	}
	return TrackingValid
}

func assertSortedUnique(t *testing.T, label string, values []string) {
	t.Helper()
	if len(values) == 0 {
		t.Fatalf("%s must not be empty", label)
	}
	assertSortedUniqueNotRequired(t, label, values)
}

func assertSortedUniqueNotRequired(t *testing.T, label string, values []string) {
	t.Helper()
	want := append([]string(nil), values...)
	sort.Strings(want)
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("%s must be sorted: %#v", label, values)
	}
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			t.Fatalf("%s contains duplicate %q", label, values[index])
		}
	}
}

func assertUnique(t *testing.T, label string, values []string) {
	t.Helper()
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("%s contains duplicate %q", label, value)
		}
		seen[value] = struct{}{}
	}
}
