package projects

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRepositoryProjectionSourceLocatorsStayInternal(t *testing.T) {
	source := ProjectRepositoryReadSource{ProjectRoot: "/private/project", ProjectContractPath: "/private/project/.loom/project.yaml", ReposContractPath: "/private/project/repos/loom.repos.yaml"}
	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/private", "project_root", "project_contract_path", "repos_contract_path"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("internal locator leaked: %s", payload)
		}
	}
}

func TestValidateProjectRepositoryReadModelRejectsLifecycleOwnershipAndObservationCorruption(t *testing.T) {
	validModel, canonical := validProjectRepositoryReadModelFixture()
	if err := validateProjectRepositoryReadModel(validModel, canonical); err != nil {
		t.Fatalf("valid read model rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ProjectRepositoryReadModel)
	}{
		{name: "archived project with active membership", mutate: func(model *ProjectRepositoryReadModel) { model.Project.Status = "archived" }},
		{name: "owning member points elsewhere", mutate: func(model *ProjectRepositoryReadModel) {
			model.Members[0].RepositoryOwnerProjectID = "project_elsewhere"
		}},
		{name: "not observed has timestamp", mutate: func(model *ProjectRepositoryReadModel) {
			now := time.Now().UTC()
			model.Members[0].StoredObservedAt = &now
		}},
		{name: "observed missing timestamp", mutate: func(model *ProjectRepositoryReadModel) {
			model.Members[0].StoredObservationPosture = ProjectRepositoryObservationObserved
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := validModel
			model.Members = append([]ProjectRepositoryReadMember(nil), validModel.Members...)
			test.mutate(&model)
			if err := validateProjectRepositoryReadModel(model, canonical); !errors.Is(err, ErrProjectRepositoryPersistenceCorrupt) {
				t.Fatalf("corrupt read model error = %v", err)
			}
		})
	}
}

func validProjectRepositoryReadModelFixture() (ProjectRepositoryReadModel, ProjectRepositoryCanonicalSource) {
	const (
		projectID    = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		repositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA"
		binding      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	canonical := ProjectRepositoryCanonicalSource{Snapshot: ProjectRepositorySourceSnapshot{
		ProjectID: projectID,
		Members:   []ProjectRepositorySourceMemberSnapshot{{RepositoryID: repositoryID, Key: "primary", Path: "primary", Role: ProjectRepositoryRolePrimary, ObservationBindingDigest: binding}},
	}}
	model := ProjectRepositoryReadModel{
		Project: ProjectRepositoryReadProject{ProjectID: projectID, Status: "active"},
		Source:  &ProjectRepositoryReadSource{},
		Members: []ProjectRepositoryReadMember{{
			RepositoryID: repositoryID, RepositoryOwnerProjectID: projectID, Key: "primary", Path: "primary", Role: ProjectRepositoryRolePrimary,
			MembershipLifecycle: RepositoryLifecycleActive, RepositoryLifecycle: RepositoryLifecycleActive, SourceBindingDigest: binding,
			StoredObservationPosture: ProjectRepositoryObservationNotObserved, ObservationRevision: 1,
		}},
	}
	return model, canonical
}
