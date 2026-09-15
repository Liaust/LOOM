package projects

import (
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func TestRetainedSourceSelector(t *testing.T) {
	t.Run("schema_path_meaning", func(t *testing.T) {
		for _, pair := range SupportedProjectRepositorySourceVersions() {
			for _, member := range []string{"app", "repos/app", "code/app", "."} {
				t.Run(pair.ProjectContract+"/"+pair.ReposContract+"/"+member, func(t *testing.T) {
					want := "repos/" + member
					if member == "." {
						want = "repos"
					}
					if pair == projectRepositorySourceV05V05 {
						want = member
					}
					got, err := retainedRepositoryPath(pair, member)
					if err != nil || got != want {
						t.Fatalf("path=%q err=%v want=%q", got, err, want)
					}
				})
			}
		}
		for _, pair := range []ProjectRepositorySourceVersions{{}, {ProjectContract: ProjectRepositoryProjectSchemaV05, ReposContract: ProjectRepositoryReposSchemaV04}, {ProjectContract: ProjectRepositoryProjectSchemaV04, ReposContract: ProjectRepositoryProjectSchemaV05}, {ProjectContract: "project.contract.v0.6", ReposContract: "repos.contract.v0.6"}, {ProjectContract: ProjectRepositoryProjectSchemaV05, ReposContract: "repos.contract.v0.5"}} {
			t.Run("refuse_"+pair.ProjectContract+"/"+pair.ReposContract, func(t *testing.T) {
				if _, err := retainedRepositoryPath(pair, "repos/app"); err == nil {
					t.Fatal("unknown/mixed source schema pair accepted")
				}
			})
		}
	})
	digest := "sha256:" + strings.Repeat("1", 64)
	makeFacts := func() RetainedSourceFacts {
		return RetainedSourceFacts{RetainedSourceSelector: RetainedSourceSelector{ProjectID: ids.NewProjectID(), RepositoryID: strings.Replace(ids.NewProjectID(), "project_", "repo_", 1), SourceBindingDigest: digest, LocationDigest: digest, SourceRevision: 1, Selection: []string{"."}}, ProjectScopeID: ids.NewScopeID(), OwnerNodeID: ids.NewNodeID(), MemberPath: "repository", RegistrationID: ids.NewProjectContractRegistrationID(), SemanticDigest: digest}
	}
	t.Run("singleton_dot", func(t *testing.T) {
		if e := ValidateRetainedSourceFacts(makeFacts()); e != nil {
			t.Fatal(e)
		}
	})
	for _, kind := range []string{"project", "repository", "scope", "node", "registration", "revision", "binding", "location", "semantic", "mixed_dot", "overlap", "empty", "order", "member_escape"} {
		t.Run(kind, func(t *testing.T) {
			f := makeFacts()
			switch kind {
			case "project":
				f.ProjectID = "slug"
			case "repository":
				f.RepositoryID = "name"
			case "scope":
				f.ProjectScopeID = "scope"
			case "node":
				f.OwnerNodeID = "main"
			case "registration":
				f.RegistrationID = "registration"
			case "revision":
				f.SourceRevision = 0
			case "binding":
				f.SourceBindingDigest = "bad"
			case "location":
				f.LocationDigest = "bad"
			case "semantic":
				f.SemanticDigest = "bad"
			case "mixed_dot":
				f.Selection = []string{".", "a"}
			case "overlap":
				f.Selection = []string{"a", "a/b"}
			case "empty":
				f.Selection = nil
			case "order":
				f.Selection = []string{"b", "a"}
			case "member_escape":
				f.MemberPath = "../out"
			}
			if e := ValidateRetainedSourceFacts(f); e == nil {
				t.Fatal("invalid source facts accepted")
			}
		})
	}
	if _, e := (Service{}).ResolveRetainedSource(t.Context(), requestctx.Context{}, RetainedSourceLocal{}, RetainedSourceSelector{}); e == nil {
		t.Fatal("unconfigured source accepted")
	}
}
func TestRetainedPackageSourceSelector(t *testing.T) { TestRetainedSourceSelector(t) }
