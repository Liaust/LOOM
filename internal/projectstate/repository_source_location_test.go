package projectstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/projects"
)

func TestRepositorySourceLocationUnsupportedVersionDoesNotReadGit(t *testing.T) {
	for _, version := range []string{"", "project.contract.v999", projects.ProjectRepositoryProjectSchemaV05} {
		t.Run(version, func(t *testing.T) {
			model := localReadModel("/registered/project", "project_test", []projects.ProjectRepositoryReadMember{localReadMember("repo_test", "api", "api", "")})
			model.Source.ProjectContractSchemaVersion = version
			paths := &staticPathResolver{byRelative: map[string]ResolvedPath{"repos/api": {Path: "/registered/project/repos/api", Exists: true, Directory: true}}}
			git := &staticGitObserver{}
			_, err := (Service{Reader: staticProjectReader{model: model}, LocalNode: "main", Paths: paths, Git: git}).ObserveProject(context.Background(), "project_test")
			if err == nil || len(paths.calls) != 0 || len(git.calls) != 0 {
				t.Fatalf("unsupported source version must refuse before path/Git observation: err=%v paths=%v Git=%v", err, paths.calls, git.calls)
			}
		})
	}
}

func TestRepositorySourceLocationSupportedVersionsAndAttribution(t *testing.T) {
	for _, versions := range projects.SupportedProjectRepositorySourceVersions() {
		for _, ref := range []string{".loom/contracts/repos.yaml", "repos/loom.repos.yaml"} {
			t.Run(versions.ProjectContract+"/"+versions.ReposContract+"/"+ref, func(t *testing.T) {
				model := localReadModel("/registered/project", "project_test", nil)
				source := model.Source
				source.ProjectContractSchemaVersion, source.ReposContractSchemaVersion = versions.ProjectContract, versions.ReposContract
				source.ReposContractPath = filepath.Join(source.ProjectRoot, ref)
				want := "repos/custom-code"
				if versions.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
					source.ReposContractPath = source.ProjectContractPath
					ref, want = ".loom/project.yaml", "custom-code"
				}
				got, err := repositorySourceLocation(source, "custom-code")
				if err != nil || got != want {
					t.Fatalf("location=%q want=%q err=%v", got, want, err)
				}
				attribution, err := repositoryMembershipSource(source)
				if err != nil || attribution != ref {
					t.Fatalf("attribution=%q want=%q err=%v", attribution, ref, err)
				}
			})
		}
	}
}

func TestRepositorySourceLocationRejectsIncompleteOrEscapingBinding(t *testing.T) {
	for name, mutate := range map[string]func(*projects.ProjectRepositoryReadSource){
		"missing project source": func(s *projects.ProjectRepositoryReadSource) { s.ProjectContractPath = "" },
		"missing member source":  func(s *projects.ProjectRepositoryReadSource) { s.ReposContractPath = "" },
		"relative project root":  func(s *projects.ProjectRepositoryReadSource) { s.ProjectRoot = "relative" },
		"source outside root":    func(s *projects.ProjectRepositoryReadSource) { s.ReposContractPath = "/elsewhere/repos.yaml" },
		"project outside root":   func(s *projects.ProjectRepositoryReadSource) { s.ProjectContractPath = "/elsewhere/project.yaml" },
		"unclean source": func(s *projects.ProjectRepositoryReadSource) {
			s.ReposContractPath = s.ProjectRoot + "/../project/repos.yaml"
		},
		"source names directory root": func(s *projects.ProjectRepositoryReadSource) { s.ReposContractPath = s.ProjectRoot },
		"v05 split source": func(s *projects.ProjectRepositoryReadSource) {
			s.ProjectContractSchemaVersion = projects.ProjectRepositoryProjectSchemaV05
			s.ReposContractSchemaVersion = projects.ProjectRepositoryProjectSchemaV05
		},
	} {
		t.Run(name, func(t *testing.T) {
			model := localReadModel("/registered/project", "project_test", nil)
			mutate(model.Source)
			if _, err := repositoryMembershipSource(model.Source); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	for _, member := range []string{"", ".", "..", " api", "api ", "../outside", "/outside", "a/../outside", "a//b", "a\\b", "a\x00b"} {
		t.Run("member/"+member, func(t *testing.T) {
			model := localReadModel("/registered/project", "project_test", nil)
			if _, err := repositorySourceLocation(model.Source, member); err == nil {
				t.Fatal("unsafe member accepted")
			}
		})
	}
}

func TestRepositorySourceLocationV05ActualPathsAndPostures(t *testing.T) {
	for _, memberPath := range []string{"custom-code", "repos/api"} {
		t.Run(memberPath, func(t *testing.T) {
			root := t.TempDir()
			memberRoot := filepath.Join(root, memberPath)
			initializeCommittedRepository(t, memberRoot)
			member := localReadMember("repo_test", "api", memberPath, "")
			member.ObservationRevision = 4
			model := localReadModel(root, "project_test", []projects.ProjectRepositoryReadMember{member})
			model.Source.ProjectContractSchemaVersion, model.Source.ReposContractSchemaVersion = projects.ProjectRepositoryProjectSchemaV05, projects.ProjectRepositoryProjectSchemaV05
			model.Source.ReposContractPath = model.Source.ProjectContractPath
			service := Service{Reader: staticProjectReader{model: model}, LocalNode: "main"}
			for _, scenario := range []string{"present", "missing", "file", "symlink escape", "remote", "unowned"} {
				t.Run(scenario, func(t *testing.T) {
					m := model
					src := *model.Source
					m.Source = &src
					m.Members = append([]projects.ProjectRepositoryReadMember(nil), model.Members...)
					want := ""
					switch scenario {
					case "missing":
						m.Members[0].Path = "missing"
						want = "member_path_missing"
					case "file":
						m.Members[0].Path = "file"
						mustWriteFile(t, filepath.Join(root, "file"), "not a directory")
						want = "member_path_not_directory"
					case "symlink escape":
						m.Members[0].Path = "link"
						if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
							t.Fatal(err)
						}
						want = "path_escape"
					case "remote":
						m.Source.OwnerNode = "remote"
						want = "remote_unavailable"
					case "unowned":
						m.Members[0].RepositoryOwnerProjectID = "other"
						m.Members[0].Role = projects.ProjectRepositoryRoleReference
						want = "repository_not_owned_by_project"
					}
					service.Reader = staticProjectReader{model: m}
					got, err := service.ObserveProjectForProvenance(context.Background(), "project_test")
					if err != nil || len(got.Repositories) != 1 {
						t.Fatalf("source=%#v err=%v", got, err)
					}
					r := got.Repositories[0]
					if r.ReasonCode != want || r.Available != (want == "") {
						t.Fatalf("posture=%#v", r)
					}
					if want == "" && (r.NavigationPath != memberPath || r.MembershipSourcePath != ".loom/project.yaml" || r.Projection.RelativeSource != memberPath || r.Projection.ObservationPosture != projects.ProjectRepositoryObservationObserved) {
						t.Fatalf("path/source=%#v", r)
					}
					payload, err := json.Marshal(got)
					if err != nil || strings.Contains(string(payload), root) {
						t.Fatalf("private source leaked: %s error=%v", payload, err)
					}
				})
			}
		})
	}
}

type changingProjectionReader struct {
	model projects.ProjectRepositoryReadModel
	calls int
}

func (reader *changingProjectionReader) ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error) {
	reader.calls++
	model := reader.model
	if reader.calls > 1 {
		source := *model.Source
		source.ProjectRoot = "/different/snapshot"
		model.Source = &source
	}
	return model, nil
}

func TestObserveProjectForProvenanceUsesOneRegistrySnapshot(t *testing.T) {
	member := localReadMember("repo_test", "api", "api", "")
	member.ObservationRevision = 1
	model := localReadModel("/registered/project", "project_test", []projects.ProjectRepositoryReadMember{member})
	model.Source.SourceRevision = 1
	reader := &changingProjectionReader{model: model}
	paths := &staticPathResolver{byRelative: map[string]ResolvedPath{"repos/api": {Path: "/registered/project/repos/api", Exists: true, Directory: true}}}
	source, err := (Service{Reader: reader, LocalNode: "main", Paths: paths, Git: &staticGitObserver{}, DevelopmentState: staticDevelopmentStateInspector{}}).ObserveProjectForProvenance(context.Background(), "project_test")
	if err != nil || reader.calls != 1 || len(source.Repositories) != 1 {
		t.Fatalf("projection must reuse one registry snapshot: reads=%d err=%v source=%#v", reader.calls, err, source)
	}
	for _, root := range paths.roots {
		if root != model.Source.ProjectRoot {
			t.Fatalf("mixed source locations: %v", paths.roots)
		}
	}
}
