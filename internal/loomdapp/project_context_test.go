package loomdapp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/storagearchive"
)

type contextMembershipFixture struct {
	project   projects.Project
	err       error
	selection boxregistry.ResolvedSelection
	ref       string
}

func TestProjectContextArchiveUsesVerifiedHomeBox(t *testing.T) {
	personal := storagearchive.ProjectRuntimeService{WorkspaceRoots: storagearchive.TrustedWorkspaceRoots{BoxID: "box_personal", Primary: true}}
	company := storagearchive.ProjectRuntimeService{WorkspaceRoots: storagearchive.TrustedWorkspaceRoots{BoxID: "box_company"}}
	selected := ""
	inspector := projectContextDevelopmentInspector{PrimaryArchive: personal,
		HomeBoxID: func(_ context.Context, project string) (string, error) {
			if project != "project_exact" {
				t.Fatal(project)
			}
			return "box_company", nil
		},
		ArchiveForBox: func(_ context.Context, box string) (storagearchive.ProjectRuntimeService, error) {
			selected = box
			return company, nil
		},
	}
	archive, err := inspector.archive(t.Context(), "project_exact")
	if err != nil || selected != "box_company" || archive.WorkspaceRoots.BoxID != "box_company" {
		t.Fatal(selected, archive.WorkspaceRoots, err)
	}
	inspector.ArchiveForBox = func(context.Context, string) (storagearchive.ProjectRuntimeService, error) { return personal, nil }
	if _, err := inspector.archive(t.Context(), "project_exact"); !errors.Is(err, projectstate.ErrProjectStateUnavailable) {
		t.Fatal("Company accepted Personal owner", err)
	}
	inspector.ArchiveForBox = nil
	if _, err := inspector.archive(t.Context(), "project_exact"); !errors.Is(err, projectstate.ErrProjectStateUnavailable) {
		t.Fatal("missing resolver fell back to Personal", err)
	}
	inspector.HomeBoxID = func(context.Context, string) (string, error) { return "box_personal", nil }
	if archive, err := inspector.archive(t.Context(), "project_exact"); err != nil || archive.WorkspaceRoots.BoxID != "box_personal" {
		t.Fatal("verified Personal owner changed", err)
	}
}

func TestProjectContextArchiveLegacyIdentityFreePrimaryPreservesInspector(t *testing.T) {
	legacy := storagearchive.ProjectRuntimeService{WorkspaceRoots: storagearchive.TrustedWorkspaceRoots{BoxRoot: "/legacy/box", StorageRoot: "/legacy/storage"}}
	inspector := projectContextDevelopmentInspector{PrimaryArchive: legacy, HomeBoxID: func(context.Context, string) (string, error) {
		t.Fatal("legacy no-catalog inspector required Box membership")
		return "", nil
	}}
	archive, err := inspector.archive(t.Context(), "project_exact")
	if err != nil || !reflect.DeepEqual(archive, legacy) {
		t.Fatal(archive, err)
	}
	input := projectstate.ProjectDevelopmentInput{ProjectID: "project_exact", ProjectRoot: t.TempDir(), OwnerNode: "main", LocalNode: "main", Lifecycle: "archived", SourceRevision: 7, VerifiedArchive: true}
	want := (storagearchive.ProjectDevelopmentInspector{Archive: legacy}).Inspect(t.Context(), input)
	if got := inspector.Inspect(t.Context(), input); !reflect.DeepEqual(got, want) {
		t.Fatal("legacy archived posture changed", got, want)
	}
}

func TestProjectContextArchiveConfiguredResolverDoesNotUseLegacyException(t *testing.T) {
	for _, lookupError := range []bool{false, true} {
		calls := 0
		inspector := projectContextDevelopmentInspector{
			HomeBoxID: func(context.Context, string) (string, error) { return "box_company", nil },
			ArchiveForBox: func(_ context.Context, home string) (storagearchive.ProjectRuntimeService, error) {
				calls++
				if home != "box_company" {
					t.Fatal(home)
				}
				if lookupError {
					return storagearchive.ProjectRuntimeService{}, errors.New("owner unavailable")
				}
				return storagearchive.ProjectRuntimeService{}, nil
			},
		}
		result := inspector.Inspect(t.Context(), projectstate.ProjectDevelopmentInput{ProjectID: "project_exact", Lifecycle: "archived"})
		if calls != 1 || result.Posture != projectstate.ProjectDevelopmentUnavailable || result.ReasonCode != "archive_box_binding_unavailable" {
			t.Fatal("configured callback fell back to legacy primary", calls, result)
		}
	}
}

func TestProjectContextArchiveUnknownBindingIsClearlyUnavailable(t *testing.T) {
	for _, kind := range []string{"unknown_home", "membership_error", "missing_company", "wrong_owner"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			inspector := projectContextDevelopmentInspector{
				HomeBoxID: func(context.Context, string) (string, error) {
					if kind == "unknown_home" {
						return "", nil
					}
					if kind == "membership_error" {
						return "", errors.New("registry unavailable")
					}
					return "box_company", nil
				},
				ArchiveForBox: func(context.Context, string) (storagearchive.ProjectRuntimeService, error) {
					calls++
					if kind == "missing_company" {
						return storagearchive.ProjectRuntimeService{}, errors.New("owner not ready")
					}
					return storagearchive.ProjectRuntimeService{WorkspaceRoots: storagearchive.TrustedWorkspaceRoots{BoxID: "box_personal"}}, nil
				},
			}
			result := inspector.Inspect(t.Context(), projectstate.ProjectDevelopmentInput{ProjectID: "project_exact", ProjectRoot: t.TempDir(), OwnerNode: "main", LocalNode: "main", Lifecycle: "archived", SourceRevision: 7, VerifiedArchive: true})
			if result.Posture != projectstate.ProjectDevelopmentUnavailable || result.ReasonCode != "archive_box_binding_unavailable" || result.ProjectID != "project_exact" || result.SourceRevision != 7 || len(result.Documents) != 0 || result.Archive != nil {
				t.Fatal(result)
			}
			if (kind == "unknown_home" || kind == "membership_error") && calls != 0 {
				t.Fatal("unknown ownership reached archive resolver")
			}
		})
	}
}

func TestProjectContextActiveDevelopmentDoesNotResolveArchive(t *testing.T) {
	inspector := projectContextDevelopmentInspector{HomeBoxID: func(context.Context, string) (string, error) {
		t.Fatal("active project looked up archive membership")
		return "", nil
	}}
	input := projectstate.ProjectDevelopmentInput{ProjectID: "project_exact", ProjectRoot: t.TempDir(), OwnerNode: "remote", LocalNode: "main", Lifecycle: "active"}
	result := inspector.Inspect(t.Context(), input)
	if result.Posture != projectstate.ProjectDevelopmentUnavailable || result.ReasonCode != "owner_node_not_local" {
		t.Fatal(result)
	}
}

func (f *contextMembershipFixture) ResolveProjectRef(ctx context.Context, ref string) (projects.Project, error) {
	f.selection, _ = boxregistry.FromContext(ctx)
	f.ref = ref
	return f.project, f.err
}

func TestProjectContextHomeBoxPreservesRegisteredArchiveMembership(t *testing.T) {
	ctx := boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	fixture := &contextMembershipFixture{project: projects.Project{Status: "archived", Metadata: json.RawMessage(`{"home_box_id":"box_company"}`)}}
	id, err := projectContextHomeBoxID(ctx, fixture, "project_exact")
	if err != nil || id != "box_company" || fixture.ref != "project_exact" || !fixture.selection.Contains("box_company") {
		t.Fatal(id, err, fixture)
	}
	fixture.project.Metadata = json.RawMessage(`{}`)
	if id, err := projectContextHomeBoxID(ctx, fixture, "project_exact"); err != nil || id != "" {
		t.Fatal("unassigned project became Personal", id, err)
	}
	fixture.err = errors.New("registry unavailable")
	if _, err := projectContextHomeBoxID(ctx, fixture, "project_exact"); !errors.Is(err, fixture.err) {
		t.Fatal(err)
	}
}
