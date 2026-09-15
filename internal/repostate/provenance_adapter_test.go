package repostate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

func TestProvenanceAdapterUsesAuthoritativeProjectMembership(t *testing.T) {
	root := t.TempDir()
	writeValidRepositoryStateFixture(t, root)
	parsed := (Parser{}).Parse(root)
	observedAt := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	git := &staticRepositoryGitObserver{observation: fixtureGitObservation(parsed, observedAt)}
	source := projectstate.ProvenanceRepositorySource{
		Projection: projectstate.RepositoryProjection{
			RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", RepositoryOwnerProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			Key: "atlas-api", Role: projects.ProjectRepositoryRoleComponent, StateRoot: StateRoot,
			RelativeSource: "atlas-api", SourceBindingDigest: "sha256:" + strings.Repeat("d", 64),
		},
		NavigationPath: "repos/atlas-api", MembershipSourcePath: ".loom/contracts/repos.yaml",
		ProjectSlug: "atlas", SourceVersion: 9, RepositoryRoot: root, Available: true, Owned: true,
	}
	result := (ProvenanceAdapter{Extractor: Extractor{Git: git}}).ProjectForProvenance(context.Background(), source)
	if !result.Available || result.Extraction.TrackingStatus != TrackingValid || git.calls != 1 {
		t.Fatalf("authoritative provenance extraction = %#v, git calls=%d", result, git.calls)
	}
	if result.Extraction.AcceptedContext == nil || len(result.Extraction.AcceptedContext) != 0 {
		t.Fatalf("repository adapter joined semantic context too early: %#v", result.Extraction.AcceptedContext)
	}
	navigation, found := fieldByName(result.Extraction.DiscoveryFields, "navigation_path")
	if !found || navigation.Value != "repos/atlas-api" || navigation.Source.Path != source.MembershipSourcePath || navigation.Source.Digest != source.Projection.SourceBindingDigest || strings.HasPrefix(navigation.Value.(string), "/") {
		t.Fatalf("repository adapter navigation = %#v", navigation)
	}
}

func TestProvenanceAdapterRetainsUnavailablePostureWithoutExtraction(t *testing.T) {
	source := projectstate.ProvenanceRepositorySource{
		Projection: projectstate.RepositoryProjection{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		Owned:      true, Available: false, ReasonCode: "member_path_missing",
	}
	result := (ProvenanceAdapter{}).ProjectForProvenance(context.Background(), source)
	if result.Available || result.ReasonCode != "member_path_missing" || result.Extraction.TrackingStatus != "" {
		t.Fatalf("unavailable repository was extracted or relabelled: %#v", result)
	}
}

func TestProvenanceAdapterRequiresQualifiedLocationsBeforeExtraction(t *testing.T) {
	for name, change := range map[string]func(*projectstate.ProvenanceRepositorySource){
		"missing navigation":  func(s *projectstate.ProvenanceRepositorySource) { s.NavigationPath = "" },
		"missing source":      func(s *projectstate.ProvenanceRepositorySource) { s.MembershipSourcePath = "" },
		"escaping navigation": func(s *projectstate.ProvenanceRepositorySource) { s.NavigationPath = "../outside" },
		"absolute source":     func(s *projectstate.ProvenanceRepositorySource) { s.MembershipSourcePath = "/private/source.yaml" },
		"missing revision":    func(s *projectstate.ProvenanceRepositorySource) { s.SourceVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			source := projectstate.ProvenanceRepositorySource{Owned: true, Available: true, RepositoryRoot: t.TempDir(), SourceVersion: 1, NavigationPath: "custom-code", MembershipSourcePath: ".loom/project.yaml"}
			change(&source)
			git := &staticRepositoryGitObserver{}
			result := (ProvenanceAdapter{Extractor: Extractor{Git: git}}).ProjectForProvenance(context.Background(), source)
			if result.Available || result.ReasonCode != "repository_source_binding_invalid" || git.calls != 0 || result.Extraction.TrackingStatus != "" {
				t.Fatalf("invalid source extracted: %#v calls=%d", result, git.calls)
			}
		})
	}
}

func TestProvenanceAdapterPreservesExistingStateAndExactAttribution(t *testing.T) {
	for _, item := range []struct{ navigation, source string }{
		{"repos/atlas-api", ".loom/contracts/repos.yaml"},
		{"repos/atlas-api", "repos/loom.repos.yaml"},
		{"custom-code", ".loom/project.yaml"},
		{"repos/api", ".loom/project.yaml"},
	} {
		t.Run(item.navigation+"/"+item.source, func(t *testing.T) {
			root := t.TempDir()
			writeValidRepositoryStateFixture(t, root)
			before := adapterFileSnapshot(t, root)
			git := &staticRepositoryGitObserver{observation: fixtureGitObservation((Parser{}).Parse(root), time.Now().UTC())}
			source := projectstate.ProvenanceRepositorySource{
				Projection:  projectstate.RepositoryProjection{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", RepositoryOwnerProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Key: "atlas-api", Role: projects.ProjectRepositoryRoleComponent, StateRoot: StateRoot, SourceBindingDigest: "sha256:" + strings.Repeat("d", 64)},
				ProjectSlug: "atlas", SourceVersion: 9, RepositoryRoot: root, Available: true, Owned: true,
				NavigationPath: item.navigation, MembershipSourcePath: item.source,
			}
			result := (ProvenanceAdapter{Extractor: Extractor{Git: git}}).ProjectForProvenance(context.Background(), source)
			field, found := fieldByName(result.Extraction.DiscoveryFields, "navigation_path")
			if !result.Available || result.Extraction.TrackingStatus != TrackingValid || !found || field.Value != item.navigation || field.Source.Path != item.source || field.Source.Digest != source.Projection.SourceBindingDigest {
				t.Fatalf("attribution=%#v result=%#v", field, result)
			}
			if !reflect.DeepEqual(before, adapterFileSnapshot(t, root)) {
				t.Fatal("adapter changed existing .repo source")
			}
		})
	}
}

func adapterFileSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(":%x", sha256.Sum256(raw))
		}
		result[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
