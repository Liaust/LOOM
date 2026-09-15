package repostate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParserAndValidatorPreserveEnvelopePrecedence(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		parsed := (Parser{}).Parse(t.TempDir())
		result := (Validator{}).Validate(parsed, nil, nil, nil)
		if result.Status != TrackingNotEnabled || len(result.Issues) != 0 {
			t.Fatalf("absent result = %#v", result)
		}
	})

	t.Run("malformed envelope precedes version", func(t *testing.T) {
		root := t.TempDir()
		mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", "kind: loom.repository_state\nkind: duplicate\nschema_version: repo.state.v2\n")
		parsed := (Parser{}).Parse(root)
		result := (Validator{}).Validate(parsed, nil, nil, nil)
		if result.Status != TrackingMalformed || !validationHasCode(result, "manifest_envelope_malformed") {
			t.Fatalf("malformed envelope result = %#v", result)
		}
	})

	t.Run("unsupported version ignores body", func(t *testing.T) {
		root := t.TempDir()
		mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", "kind: loom.repository_state\nschema_version: repo.state.v2\nfuture_field: any shape is not interpreted\n")
		parsed := (Parser{}).Parse(root)
		if !parsed.UnsupportedVersion || parsed.Manifest != nil {
			t.Fatalf("unsupported parse = %#v", parsed)
		}
		result := (Validator{}).Validate(parsed, nil, nil, nil)
		if result.Status != TrackingStaleVersion || !validationHasCode(result, "unsupported_schema") {
			t.Fatalf("unsupported result = %#v", result)
		}
	})

	t.Run("supported body rejects unknown fields", func(t *testing.T) {
		root := t.TempDir()
		mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", strings.Replace(validManifestFixture(), "source:\n", "unexpected: true\nsource:\n", 1))
		parsed := (Parser{}).Parse(root)
		result := (Validator{}).Validate(parsed, nil, nil, nil)
		if result.Status != TrackingMalformed || !validationHasCode(result, "manifest_body_malformed") {
			t.Fatalf("unknown field result = %#v", result)
		}
	})

	t.Run("supported body requires explicit empty lists", func(t *testing.T) {
		root := t.TempDir()
		manifest := strings.Replace(validManifestFixture(), "  aliases: [atlas, atlas-api]\n", "", 1)
		mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", manifest)
		parsed := (Parser{}).Parse(root)
		result := (Validator{}).Validate(parsed, nil, nil, nil)
		if result.Status != TrackingMalformed || !validationHasCode(result, "manifest_body_malformed") {
			t.Fatalf("missing aliases result = %#v", result)
		}
	})
}

func TestValidatorAppliesFrozenMembershipPrecedence(t *testing.T) {
	root := t.TempDir()
	writeValidRepositoryStateFixture(t, root)
	parsed := (Parser{}).Parse(root)
	observation := fixtureGitObservation(parsed, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	base := fixtureMembership()
	tests := []struct {
		name   string
		mutate func(*AuthoritativeMembership)
		want   TrackingStatus
	}{
		{name: "valid", mutate: func(*AuthoritativeMembership) {}, want: TrackingValid},
		{name: "unresolved", mutate: func(value *AuthoritativeMembership) { value.Resolved = false }, want: TrackingMembershipUnresolved},
		{name: "repository mismatch", mutate: func(value *AuthoritativeMembership) { value.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA" }, want: TrackingMismatchedRepository},
		{name: "owner mismatch", mutate: func(value *AuthoritativeMembership) { value.OwnerProjectSlug = "atlas-old" }, want: TrackingMismatchedOwner},
		{name: "role mismatch", mutate: func(value *AuthoritativeMembership) { value.OwnerRole = RepositoryRolePrimary }, want: TrackingMismatchedMembership},
		{name: "state root mismatch", mutate: func(value *AuthoritativeMembership) { value.StateRoot = ".project" }, want: TrackingMismatchedMembership},
		{name: "absolute navigation", mutate: func(value *AuthoritativeMembership) { value.NavigationPath = "/srv/atlas" }, want: TrackingMismatchedMembership},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			membership := base
			test.mutate(&membership)
			result := (Validator{}).Validate(parsed, &membership, &observation, nil)
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q; issues=%#v", result.Status, test.want, result.Issues)
			}
		})
	}
}

func TestValidatorRejectsUntrackedAndOrphanedPortableSource(t *testing.T) {
	root := t.TempDir()
	writeValidRepositoryStateFixture(t, root)
	mustWriteRepositoryFixtureFile(t, root, ".repo/features/orphan/overview.md", "# Orphan\n")
	parsed := (Parser{}).Parse(root)
	observation := fixtureGitObservation(parsed, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	for index, entry := range observation.Entries {
		if entry.Path == ".repo/STATE.md" {
			observation.Entries = append(observation.Entries[:index], observation.Entries[index+1:]...)
			break
		}
	}
	membership := fixtureMembership()
	result := (Validator{}).Validate(parsed, &membership, &observation, nil)
	if result.Status != TrackingMalformed || !validationHasCode(result, "untracked_source") || !validationHasCode(result, "lifecycle_manifest_missing") {
		t.Fatalf("validation result = %#v", result)
	}
}

func TestParserIsReadOnlyAndRejectsStateRootSymlink(t *testing.T) {
	root := t.TempDir()
	writeValidRepositoryStateFixture(t, root)
	manifestPath := filepath.Join(root, ".repo", "repo.yaml")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed := (Parser{}).Parse(root)
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !parsed.Enabled {
		t.Fatal("parser changed source bytes")
	}

	outside := t.TempDir()
	linkRoot := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linkRoot, StateRoot)); err != nil {
		t.Fatal(err)
	}
	symlinked := (Parser{}).Parse(linkRoot)
	result := (Validator{}).Validate(symlinked, nil, nil, nil)
	if result.Status != TrackingMalformed || !validationHasCode(result, "state_root_not_directory") {
		t.Fatalf("symlinked state result = %#v", result)
	}
}

func TestParserRejectsStateRootAndFileReplacementWithoutReadingOutside(t *testing.T) {
	t.Run("state root swap", func(t *testing.T) {
		root := t.TempDir()
		writeValidRepositoryStateFixture(t, root)
		outside := t.TempDir()
		mustWriteRepositoryFixtureFile(t, outside, "repo.yaml", "outside root sentinel\n")
		var swapErr error
		parser := Parser{afterStateRootOpen: func() {
			if err := os.Rename(filepath.Join(root, StateRoot), filepath.Join(root, StateRoot+"-held")); err != nil {
				swapErr = err
				return
			}
			swapErr = os.Symlink(outside, filepath.Join(root, StateRoot))
		}}
		parsed := parser.Parse(root)
		if swapErr != nil {
			t.Fatal(swapErr)
		}
		if !parseIssuesHaveCode(parsed.Issues, "state_root_replaced") || len(parsed.Sources) != 0 {
			t.Fatalf("root-swap parse = %#v", parsed)
		}
	})

	t.Run("tracked file swap", func(t *testing.T) {
		root := t.TempDir()
		writeValidRepositoryStateFixture(t, root)
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("outside file sentinel\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		swapped := false
		var swapErr error
		parser := Parser{beforeSourceOpen: func(path string) {
			if path != ".repo/STATE.md" || swapped {
				return
			}
			swapped = true
			statePath := filepath.Join(root, StateRoot, "STATE.md")
			if err := os.Rename(statePath, statePath+".held"); err != nil {
				swapErr = err
				return
			}
			swapErr = os.Symlink(outside, statePath)
		}}
		parsed := parser.Parse(root)
		if swapErr != nil {
			t.Fatal(swapErr)
		}
		if !swapped || !parseIssuesHaveCode(parsed.Issues, "source_replaced") {
			t.Fatalf("file-swap parse issues = %#v", parsed.Issues)
		}
		for _, source := range parsed.Sources {
			if strings.Contains(string(source.data), "outside file sentinel") {
				t.Fatalf("parser consumed outside bytes through %s", source.Path)
			}
		}
	})
}

func TestPortableSourceAbsolutePathDetection(t *testing.T) {
	positive := []string{
		"path: /srv/loom/state.yaml\n",
		"temporary: /tmp/loom\n",
		"config: /etc/loom/config.yaml\n",
		"private: `/private/var/db/loom`\n",
		`worktree: C:\LOOM\repo`,
		"worktree: D:/work/loom\n",
		`share: \\server\share\loom`,
		"share: //server/share/loom\n",
		"source: file:///srv/loom\n",
	}
	for _, value := range positive {
		if !containsHostAbsolutePath([]byte(value)) {
			t.Errorf("absolute path was accepted: %q", value)
		}
	}

	negative := []string{
		"url: https://example.com/srv/loom\n",
		"[reference](https://example.com/etc/loom)\n",
		"source: HTTP://example.com/C:/work/loom\n",
		"state_root: .repo\n",
		"path: repos/atlas-api\n",
		"document: docs/reference/state.md\n",
		"drive_relative: C:repo\\state\n",
		"prose: input/output values\n",
	}
	for _, value := range negative {
		if containsHostAbsolutePath([]byte(value)) {
			t.Errorf("portable value was rejected: %q", value)
		}
	}

	root := t.TempDir()
	writeValidRepositoryStateFixture(t, root)
	mustWriteRepositoryFixtureFile(t, root, ".repo/STATE.md", "# State\n\nHost path: /srv/loom\n")
	parsed := (Parser{}).Parse(root)
	result := (Validator{}).Validate(parsed, nil, nil, nil)
	if result.Status != TrackingMalformed || !validationHasCode(result, "absolute_path_forbidden") {
		t.Fatalf("absolute-path validation = %#v", result)
	}
}

func parseIssuesHaveCode(issues []ParseIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func validationHasCode(result ValidationResult, code string) bool {
	for _, issue := range result.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func validManifestFixture() string {
	return `kind: loom.repository_state
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
`
}
