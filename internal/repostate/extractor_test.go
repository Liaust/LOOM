package repostate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type staticRepositoryGitObserver struct {
	observation GitObservation
	err         error
	calls       int
}

func (observer *staticRepositoryGitObserver) Observe(context.Context, string) (GitObservation, error) {
	observer.calls++
	return observer.observation, observer.err
}

func TestExtractorProjectsDeterministicFieldsAndEvidenceWithSourcePosture(t *testing.T) {
	root := t.TempDir()
	writeValidRepositoryStateFixture(t, root)
	parsed := (Parser{}).Parse(root)
	observedAt := time.Date(2026, 8, 30, 12, 0, 0, 123, time.UTC)
	observer := &staticRepositoryGitObserver{observation: fixtureGitObservation(parsed, observedAt)}
	membership := fixtureMembership()

	extraction := (Extractor{Git: observer}).Extract(context.Background(), ExtractInput{RepositoryRoot: root, Membership: &membership})
	if extraction.TrackingStatus != TrackingValid {
		t.Fatalf("tracking status = %q, issues=%#v", extraction.TrackingStatus, extraction.Validation.Issues)
	}
	if observer.calls != 1 {
		t.Fatalf("Git observer calls = %d, want 1", observer.calls)
	}
	if extraction.AcceptedContext == nil || len(extraction.AcceptedContext) != 0 {
		t.Fatalf("accepted_context = %#v, want an explicit empty collection", extraction.AcceptedContext)
	}
	if len(extraction.DiscoveryFields) != len(SchemaV1().DiscoveryFields) {
		t.Fatalf("discovery fields = %d, want %d", len(extraction.DiscoveryFields), len(SchemaV1().DiscoveryFields))
	}
	for index, schema := range SchemaV1().DiscoveryFields {
		field := extraction.DiscoveryFields[index]
		if field.Name != schema.Name {
			t.Fatalf("field %d = %q, want %q", index, field.Name, schema.Name)
		}
		if field.Source.Path == "" || field.Source.Digest == "" || field.Source.Commit == "" || field.Source.ObservedAt == nil {
			t.Errorf("field %s lacks complete source metadata: %#v", field.Name, field.Source)
		}
	}
	navigation, _ := fieldByName(extraction.DiscoveryFields, "navigation_path")
	if navigation.Value != "repos/atlas-api" || navigation.Source.Posture != PostureDerived || strings.HasPrefix(fmt.Sprint(navigation.Value), "/") {
		t.Fatalf("navigation field = %#v", navigation)
	}
	if navigation.Source.Commit != membership.SourceCommit {
		t.Fatalf("navigation source commit = %q, want membership commit %q", navigation.Source.Commit, membership.SourceCommit)
	}
	currentState, _ := fieldByName(extraction.DiscoveryFields, "current_state")
	if currentState.Value != "The repository API is stable and the search slice is under review." || currentState.Source.Posture != PostureDeclared {
		t.Fatalf("current_state = %#v", currentState)
	}
	priorities, _ := fieldByName(extraction.DiscoveryFields, "next_priorities")
	if !reflect.DeepEqual(priorities.Value, []string{"Integrate deterministic repository search.", "Publish bounded acceptance evidence."}) {
		t.Fatalf("next_priorities = %#v", priorities.Value)
	}
	if len(extraction.Features) != 1 || len(extraction.Decisions) != 1 || len(extraction.Releases) != 1 || len(extraction.Integrations) != 1 || len(extraction.Handoffs) != 1 || len(extraction.Acceptance) != 1 {
		t.Fatalf("extracted collections: features=%d decisions=%d releases=%d integrations=%d handoffs=%d acceptance=%d", len(extraction.Features), len(extraction.Decisions), len(extraction.Releases), len(extraction.Integrations), len(extraction.Handoffs), len(extraction.Acceptance))
	}
	if extraction.Freshness.TrackedState != GitTrackedClean || len(extraction.Freshness.Reasons) != 0 || extraction.Freshness.AgeSeconds == 0 {
		t.Fatalf("freshness = %#v", extraction.Freshness)
	}
	payload, err := json.Marshal(extraction)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(root)) || !bytes.Contains(payload, []byte(`"accepted_context":[]`)) {
		t.Fatalf("projection leaked a node-local path or omitted explicit empty accepted_context: %s", payload)
	}
}

func TestExtractorReturnsNotEnabledWithoutCallingGit(t *testing.T) {
	observer := &staticRepositoryGitObserver{}
	extraction := (Extractor{Git: observer}).Extract(context.Background(), ExtractInput{RepositoryRoot: t.TempDir()})
	if extraction.TrackingStatus != TrackingNotEnabled {
		t.Fatalf("tracking status = %q", extraction.TrackingStatus)
	}
	if observer.calls != 0 {
		t.Fatalf("Git observer was called %d times for absent .repo", observer.calls)
	}
	if extraction.AcceptedContext == nil || len(extraction.AcceptedContext) != 0 {
		t.Fatalf("accepted_context = %#v", extraction.AcceptedContext)
	}
	tracking, _ := fieldByName(extraction.DiscoveryFields, "tracking_status")
	if tracking.Value != TrackingNotEnabled || tracking.Source.Posture != PostureDerived {
		t.Fatalf("tracking field = %#v", tracking)
	}
	if extraction.Freshness.TrackedState != GitTrackedUnavailable || len(extraction.Freshness.Reasons) != 0 {
		t.Fatalf("not-enabled freshness = %#v", extraction.Freshness)
	}
}

func TestExtractorStopsBeforeTreeAndGitForUnusableManifestEnvelope(t *testing.T) {
	tests := []struct {
		name           string
		manifest       string
		wantStatus     TrackingStatus
		wantOpenSource []string
	}{
		{
			name:           "manifest absent",
			wantStatus:     TrackingMalformed,
			wantOpenSource: []string{},
		},
		{
			name:           "malformed envelope",
			manifest:       "kind: loom.repository_state\nkind: duplicate\nschema_version: repo.state.v2\n",
			wantStatus:     TrackingMalformed,
			wantOpenSource: []string{RepositoryManifestPath},
		},
		{
			name:           "malformed supported body",
			manifest:       "kind: loom.repository_state\nschema_version: repo.state.v1\nunexpected: true\n",
			wantStatus:     TrackingMalformed,
			wantOpenSource: []string{RepositoryManifestPath},
		},
		{
			name:           "unsupported envelope",
			manifest:       "kind: loom.repository_state\nschema_version: repo.state.v2\nfuture_field: &future [uninterpreted]\nfuture_field: *future\n",
			wantStatus:     TrackingStaleVersion,
			wantOpenSource: []string{RepositoryManifestPath},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.manifest != "" {
				mustWriteRepositoryFixtureFile(t, root, RepositoryManifestPath, test.manifest)
			} else if err := os.MkdirAll(filepath.Join(root, StateRoot), 0o755); err != nil {
				t.Fatal(err)
			}
			blockedPath := filepath.Join(root, StateRoot, "blocked.md")
			if err := os.WriteFile(blockedPath, []byte("must not be read\n"), 0o000); err != nil {
				t.Fatal(err)
			}
			opened := []string{}
			observer := &staticRepositoryGitObserver{}
			extraction := (Extractor{
				Parser: Parser{beforeSourceOpen: func(path string) { opened = append(opened, path) }},
				Git:    observer,
			}).Extract(context.Background(), ExtractInput{RepositoryRoot: root})
			if extraction.TrackingStatus != test.wantStatus {
				t.Fatalf("tracking status = %q, want %q; issues=%#v", extraction.TrackingStatus, test.wantStatus, extraction.Validation.Issues)
			}
			if !reflect.DeepEqual(opened, test.wantOpenSource) {
				t.Fatalf("opened sources = %#v, want %#v", opened, test.wantOpenSource)
			}
			if observer.calls != 0 {
				t.Fatalf("Git observer calls = %d, want 0", observer.calls)
			}
		})
	}
}

func TestCanonicalSourceDigestIsOrderIndependentAndContentSensitive(t *testing.T) {
	entries := []GitEntry{
		{Path: ".repo/STATE.md", Mode: "100644", Content: []byte("state\n")},
		{Path: ".repo/repo.yaml", Mode: "100644", Content: []byte("manifest\n")},
	}
	first, err := CanonicalSourceDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalSourceDigest([]GitEntry{entries[1], entries[0]})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("digests = %q and %q", first, second)
	}
	entries[0].Content = []byte("changed\n")
	changed, err := CanonicalSourceDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("digest did not change with exact source bytes")
	}
	if _, err := CanonicalSourceDigest([]GitEntry{{Path: ".repo/link", Mode: "120000", Content: []byte("target")}}); err == nil {
		t.Fatal("non-regular Git mode was accepted")
	}
}

func fixtureMembership() AuthoritativeMembership {
	return AuthoritativeMembership{
		Resolved:         true,
		RepositoryID:     "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OwnerProjectID:   "project_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OwnerProjectSlug: "atlas",
		OwnerRole:        RepositoryRoleComponent,
		StateRoot:        StateRoot,
		NavigationPath:   "repos/atlas-api",
		SourcePath:       ".loom/contracts/repos.yaml",
		SourceDigest:     "sha256:" + strings.Repeat("d", 64),
		SourceCommit:     strings.Repeat("b", 40),
	}
}

func fixtureGitObservation(parsed ParsedRepositoryState, observedAt time.Time) GitObservation {
	entries := make([]GitEntry, 0, len(parsed.Sources))
	for _, source := range parsed.Sources {
		entries = append(entries, GitEntry{Path: source.Path, Mode: "100644", Content: append([]byte(nil), source.data...)})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	return GitObservation{
		ObservedAt:         observedAt,
		HeadCommit:         strings.Repeat("a", 40),
		SourceCommit:       strings.Repeat("a", 40),
		LatestRepoCommitAt: observedAt.Add(-24 * time.Hour),
		TrackedState:       GitTrackedClean,
		Entries:            entries,
		UntrackedPaths:     []string{},
	}
}

func writeValidRepositoryStateFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		".repo/repo.yaml": `kind: loom.repository_state
schema_version: repo.state.v1
repository:
  id: repo_01ARZ3NDEKTSV4RRFFQ69G5FAV
  name: Atlas API
  aliases: [atlas, atlas-api]
  role: component
  purpose: Serve the Atlas product API.
  topics: [api, atlas, go]
owner_project:
  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV
  slug: atlas
branches:
  stable: main
  default: main
source:
  tracking: git
  state_root: .repo
`,
		".repo/README.md":     "# Repository State\n\nRead repo.yaml first.\n",
		".repo/REPOSITORY.md": "# Repository Definition\n\nLast reviewed: 2026-08-29\n\n## Purpose\n\nServe the bounded Atlas API.\n",
		".repo/STATE.md":      "# Repository State\n\nLast reviewed: 2026-08-29\n\n## Current State\n\nThe repository API is stable and the search slice is under review.\n\n## Active Focus\n\n- Deterministic extraction\n\n## Recent Outcomes\n\n- Froze the portable schema.\n\n## Blockers\n\n- None.\n",
		".repo/ROADMAP.md":    "# Repository Roadmap\n\nLast reviewed: 2026-08-29\n\n## Next Priorities\n\n1. Integrate deterministic repository search.\n2. Publish bounded acceptance evidence.\n",
		".repo/features/search/feature.yaml": `kind: feature
schema_version: repo.feature.v1
id: feature/search
slug: search
title: Repository search
status: review
priority: high
created_at: 2026-08-28
updated_at: 2026-08-29
dependencies: []
`,
		".repo/features/search/overview.md":              "# Repository search\n",
		".repo/features/search/implementation_slices.md": "# Slices\n",
		".repo/features/search/worktree_progress.md":     "# Progress\n",
		".repo/features/search/handoff.md":               "# Handoff\n\nReady for review.\n",
		".repo/features/search/acceptance-local.md":      "# Acceptance\n\nFocused tests passed.\n",
		".repo/decisions/ADR-0001/decision.yaml": `kind: decision
schema_version: repo.decision.v1
id: decision/ADR-0001
decision_id: ADR-0001
title: Keep extraction deterministic
status: accepted
date: 2026-08-29
`,
		".repo/decisions/ADR-0001/decision.md": "# Decision\n",
		".repo/releases/v1/release.yaml": `kind: release
schema_version: repo.release.v1
id: release/v1
version: v1
status: planning
objective: Ship repository discovery.
features: [feature/search]
created_at: 2026-08-28
updated_at: 2026-08-29
`,
		".repo/releases/v1/overview.md": "# Release v1\n",
		".repo/integrations/STATUS.md":  "# Integration Status\n\nSearch is pending.\n",
	}
	for relative, content := range files {
		mustWriteRepositoryFixtureFile(t, root, relative, content)
	}
}

func mustWriteRepositoryFixtureFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}
