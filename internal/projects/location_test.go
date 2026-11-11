package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func locationSnapshotFixture(t *testing.T, version string, repositories bool) ProjectLocationSnapshot {
	t.Helper()
	root := "/srv/loom/box/Projects/example"
	node := "node_main"
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	contract, _ := json.Marshal(map[string]any{
		"kind": "loom.project", "schema_version": version,
		"project": map[string]string{"id": projectID, "slug": "example", "owner_node": "main", "status": "active"},
	})
	s := ProjectLocationSnapshot{
		SchemaVersion: ProjectLocationSnapshotSchemaVersion, ProjectID: projectID, Root: root,
		Project: Project{ProjectID: projectID, Slug: "example", ProjectScopeID: "scope_example", ProjectScopeKey: "project:example", Status: "active", HomeNodeID: &node,
			Metadata: json.RawMessage(`{"project_root":"` + root + `","large":9007199254740993,"nested":{"workspace_filesystem_operation":"not a fence"}}`)},
		Registration: ProjectContractRegistration{ProjectID: projectID, ProjectContractRegistrationID: "registration_example", ProjectRoot: root,
			ContractPath: root + "/custom/project.yaml", ContractHash: "sha256:" + strings.Repeat("a", 64), ContractSchemaVersion: version,
			Contract: contract, RegistrationRevision: 3, RegistrationStatus: ProjectRegistrationStatusRegistered, ActivationStatus: "inactive",
			RegistrationPlan: json.RawMessage(`{"old_request_path":"` + root + `"}`)},
		Repositories: []ProjectLocationRepository{}, Observations: map[string]ProjectRepositoryObservation{},
	}
	if isDeclarationSchemaVersion(version) {
		s.Registration.ContractPath = root + "/.loom/project.yaml"
	}
	if repositories {
		versions := ProjectRepositorySourceVersions{ProjectContract: version, ReposContract: ProjectRepositoryReposSchemaV04}
		reposPath := root + "/custom/repositories.yaml"
		if isDeclarationSchemaVersion(version) {
			versions.ReposContract, reposPath = version, s.Registration.ContractPath
		}
		member := ProjectRepositoryValidatedMember{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA", Key: "app", Path: "app", Role: ProjectRepositoryRolePrimary, StateRoot: ".repo"}
		reposHash := "sha256:" + strings.Repeat("b", 64)
		if isDeclarationSchemaVersion(version) {
			reposHash = s.Registration.ContractHash
		}
		canonical, err := BuildProjectRepositoryCanonicalSource(ProjectRepositoryValidatedSourceInput{
			ProjectID: projectID, ProjectRoot: root, ProjectContractPath: s.Registration.ContractPath, ReposContractPath: reposPath,
			OwnerNode: "main", Versions: versions, ProjectContractDigest: s.Registration.ContractHash, ReposContractDigest: reposHash,
			RegistrationPlan: json.RawMessage(`{"schema_version":"project.plan.v0.3","project_root":"` + root + `","nested":{"exact":9007199254740993}}`), Members: []ProjectRepositoryValidatedMember{member},
		})
		if err != nil {
			t.Fatal(err)
		}
		s.RepositorySource = &ProjectRepositorySource{ProjectID: projectID, ProjectContractRegistrationID: s.Registration.ProjectContractRegistrationID,
			ProjectRoot: root, ProjectContractPath: s.Registration.ContractPath, ReposContractPath: reposPath,
			ProjectContractSchemaVersion: version, ReposContractSchemaVersion: versions.ReposContract,
			ProjectContractDigest: s.Registration.ContractHash, ReposContractDigest: reposHash,
			SemanticDigest: canonical.SemanticDigest, LocationDigest: canonical.LocationDigest, SourceSnapshot: canonical.SnapshotJSON, SourceRevision: 7,
			RegisteredByActorID: "actor_registrar"}
		s.Repositories = []ProjectLocationRepository{{
			Membership: ProjectRepositoryMembership{ProjectID: projectID, RepositoryID: member.RepositoryID, RepositoryOwnerProjectID: projectID, MemberKey: member.Key,
				MemberPath: member.Path, Role: member.Role, StateRoot: member.StateRoot, LifecycleStatus: RepositoryLifecycleArchived},
			Repository: RepositoryIdentity{RepositoryID: member.RepositoryID, OwningProjectID: projectID, LifecycleStatus: RepositoryLifecycleArchived},
		}}
		at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		s.Observations[member.RepositoryID] = ProjectRepositoryObservation{ProjectID: projectID, RepositoryID: member.RepositoryID,
			SourceBindingDigest: canonical.ObservationBindings[0].Digest, ObservationPosture: ProjectRepositoryObservationObserved,
			ObservedAt: &at, Observation: json.RawMessage(`{"head":"abc"}`), ObservationRevision: 4}
	}
	if err := validateProjectLocationSnapshot(s); err != nil {
		t.Fatal(err)
	}
	var err error
	s.ExpectedDigest, err = projectLocationSnapshotDigest(s, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProjectLocationRepositorySuccessor(t *testing.T) {
	for _, version := range []string{ProjectRepositoryProjectSchemaV04, ProjectRepositoryProjectSchemaV05, ProjectRepositoryProjectSchemaV06} {
		t.Run(version, func(t *testing.T) {
			s := locationSnapshotFixture(t, version, true)
			original, _ := json.Marshal(s)
			canonical, err := decodeAndValidateProjectRepositorySourceRow(*s.RepositorySource)
			if err != nil {
				t.Fatal(err)
			}
			next, change, err := projectLocationRepositorySuccessor(canonical, "/srv/loom/box/Documents/new")
			if err != nil {
				t.Fatal(err)
			}
			if change.Classification != ProjectRepositorySourceClassificationRelocation || next.SemanticDigest != canonical.SemanticDigest || next.LocationDigest == canonical.LocationDigest || !bytes.Equal(next.Snapshot.StableRegistrationPlan, canonical.Snapshot.StableRegistrationPlan) {
				t.Fatalf("not a location-only successor: %#v", change)
			}
			if next.Snapshot.Location.ProjectContractPath != strings.Replace(s.Registration.ContractPath, s.Root, "/srv/loom/box/Documents/new", 1) || next.Snapshot.ProjectContractDigest != s.Registration.ContractHash || next.Snapshot.SourceVersions != canonical.Snapshot.SourceVersions {
				t.Fatal("lost exact path, hash or schema binding")
			}
			priorObservation := s.Observations[next.ObservationBindings[0].RepositoryID]
			transition, err := PlanProjectRepositoryObservationBinding(&priorObservation, next.ObservationBindings[0])
			if err != nil || !transition.Invalidated || transition.Observation.ObservationPosture != ProjectRepositoryObservationNotObserved || transition.Observation.ObservedAt != nil || string(transition.Observation.Observation) != "{}" || transition.Observation.ObservationRevision != 5 {
				t.Fatalf("observation was not invalidated: %#v %v", transition, err)
			}
			after, _ := json.Marshal(s)
			if !bytes.Equal(original, after) {
				t.Fatal("successor mutated predecessor evidence")
			}
		})
	}
}

func TestProjectLocationPendingChildFence(t *testing.T) {
	root := "/srv/loom/box/Projects/example"
	for _, test := range []struct {
		name, kind, source, operation, destination string
		valid                                      bool
	}{
		{"own child snapshot", "retain_child", root + "/notes/old", "own", "", true},
		{"child cannot move parent", "retain_child", root + "/notes/old", "own", "/srv/loom/box/Projects/new", false},
		{"foreign operation", "retain_child", root + "/notes/old", "other", "", false},
		{"child is parent", "retain_child", root, "own", "", false},
		{"child is sibling", "retain_child", root + "-other/notes", "own", "", false},
		{"unknown kind", "unknown", root + "/notes", "own", "", false},
		{"unclean source", "retain_child", root + "/notes/../old", "own", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"workspace_filesystem_operation": map[string]string{"id": "own", "kind": test.kind, "source": test.source, "destination": "/srv/loom/storage/Records/example"}})
			if err != nil {
				t.Fatal(err)
			}
			err = validateProjectLocationPending(raw, test.operation, root, test.destination)
			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrProjectLocationChanged) {
				t.Fatalf("valid=%v: %v", test.valid, err)
			}
		})
	}
}

func TestProjectLocationDigestFenceAndRoundTrip(t *testing.T) {
	s := locationSnapshotFixture(t, ProjectRepositoryProjectSchemaV04, true)
	original := s.ExpectedDigest
	s.Project.Metadata = json.RawMessage(strings.TrimSuffix(string(s.Project.Metadata), "}") + `,"workspace_filesystem_operation":{"id":"own","kind":"relocate_project","source":"` + s.Root + `","destination":"/srv/loom/box/Documents/new"}}`)
	if err := validateProjectLocationPending(s.Project.Metadata, "own", s.Root, "/srv/loom/box/Documents/new"); err != nil {
		t.Fatal(err)
	}
	got, err := projectLocationSnapshotDigest(s, "own")
	if err != nil || got != original {
		t.Fatalf("own fence invalidated predecessor: %s %v", got, err)
	}
	for _, op := range []string{"", "other"} {
		if _, err := projectLocationSnapshotDigest(s, op); !errors.Is(err, ErrProjectLocationChanged) {
			t.Fatalf("accepted foreign fence with %q: %v", op, err)
		}
	}
	if err := validateProjectLocationPending(s.Project.Metadata, "own", s.Root, "/wrong"); !errors.Is(err, ErrProjectLocationChanged) {
		t.Fatalf("accepted wrong destination: %v", err)
	}
	before, _ := json.Marshal(s)
	var saved ProjectLocationSnapshot
	if err := json.Unmarshal(before, &saved); err != nil {
		t.Fatal(err)
	}
	got, err = projectLocationSnapshotDigest(saved, "own")
	if err != nil || got != original || !bytes.Contains(saved.Project.Metadata, []byte("9007199254740993")) {
		t.Fatalf("serialization lost expected record precision: %s %v", got, err)
	}
	for name, mutate := range map[string]func(*ProjectLocationSnapshot){
		"project audit time": func(v *ProjectLocationSnapshot) { v.Project.UpdatedAt = time.Now().UTC() },
		"metadata": func(v *ProjectLocationSnapshot) {
			v.Project.Metadata = bytes.ReplaceAll(v.Project.Metadata, []byte("9007199254740993"), []byte("9007199254740992"))
		},
		"lifecycle":       func(v *ProjectLocationSnapshot) { v.Project.Status = "archived" },
		"activation":      func(v *ProjectLocationSnapshot) { v.Registration.ActivationStatus = "active" },
		"revision":        func(v *ProjectLocationSnapshot) { v.Registration.RegistrationRevision++ },
		"source revision": func(v *ProjectLocationSnapshot) { v.RepositorySource.SourceRevision++ },
		"membership": func(v *ProjectLocationSnapshot) {
			v.Repositories[0].Membership.LifecycleStatus = RepositoryLifecycleActive
		},
		"repository": func(v *ProjectLocationSnapshot) {
			v.Repositories[0].Repository.LifecycleStatus = RepositoryLifecycleActive
		},
		"old archive": func(v *ProjectLocationSnapshot) { v.Project.ArchiveState = json.RawMessage(`{"historical":true}`) },
	} {
		t.Run(name, func(t *testing.T) {
			var changed ProjectLocationSnapshot
			_ = json.Unmarshal(before, &changed)
			mutate(&changed)
			digest, err := projectLocationSnapshotDigest(changed, "own")
			if err != nil || digest == original {
				t.Fatalf("drift not captured: %v", err)
			}
		})
	}
}

func TestProjectLocationExactPrefix(t *testing.T) {
	for _, path := range []string{"/oldish/.loom/project.yaml", "/outside/project.yaml", "/old/../project.yaml", "/old", "relative/project.yaml", "/old//project.yaml"} {
		if _, err := projectLocationSuccessorPath(path, "/old", "/new"); !errors.Is(err, ErrProjectLocationInvalid) {
			t.Fatalf("accepted %q: %v", path, err)
		}
	}
	got, err := projectLocationSuccessorPath("/old/custom/old/project.yaml", "/old", "/new")
	if err != nil || got != "/new/custom/old/project.yaml" {
		t.Fatalf("exact prefix: %q %v", got, err)
	}
}

func TestProjectLocationRepositoryLessAndInvalidBindings(t *testing.T) {
	for _, version := range []string{ProjectRepositoryProjectSchemaV03, ProjectRepositoryProjectSchemaV05, ProjectRepositoryProjectSchemaV06} {
		s := locationSnapshotFixture(t, version, false)
		if s.RepositorySource != nil || len(s.Repositories) != 0 {
			t.Fatal("invented a canonical repository source")
		}
		original := s.Registration.Contract
		s.Registration.Contract = json.RawMessage(strings.TrimSuffix(string(original), "}") + `,"resources":{"app":{"kind":"repository"}}}`)
		if err := validateProjectLocationSnapshot(s); !errors.Is(err, ErrProjectLocationInvalid) {
			t.Fatalf("accepted declared repository without source: %v", err)
		}
	}
	for name, mutate := range map[string]func(*ProjectLocationSnapshot){
		"root": func(s *ProjectLocationSnapshot) { s.Root += "-changed" },
		"hash": func(s *ProjectLocationSnapshot) { s.Registration.ContractHash = "sha256:" + strings.Repeat("c", 64) },
		"schema": func(s *ProjectLocationSnapshot) {
			s.Registration.ContractSchemaVersion = ProjectRepositoryProjectSchemaV06
		},
		"path": func(s *ProjectLocationSnapshot) { s.Registration.ContractPath = "/outside/project.yaml" },
		"owner": func(s *ProjectLocationSnapshot) {
			s.RepositorySource.SourceSnapshot = bytes.ReplaceAll(s.RepositorySource.SourceSnapshot, []byte(`"main"`), []byte(`"other"`))
		},
		"projection":  func(s *ProjectLocationSnapshot) { s.Repositories = nil },
		"observation": func(s *ProjectLocationSnapshot) { s.Observations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s := locationSnapshotFixture(t, ProjectRepositoryProjectSchemaV04, true)
			mutate(&s)
			if err := validateProjectLocationSnapshot(s); err == nil {
				t.Fatal("accepted invalid location binding")
			}
		})
	}
	if _, err := NewService(nil).CurrentProjectLocationSnapshot(context.Background(), "example"); !errors.Is(err, ErrProjectLocationUnavailable) {
		t.Fatalf("nil database: %v", err)
	}
	if _, err := NewService(nil).CommitProjectLocationTx(context.Background(), nil, ProjectLocationSnapshot{}, "/new", "op"); !errors.Is(err, ErrProjectLocationUnavailable) {
		t.Fatalf("nil transaction: %v", err)
	}
	// An archived repository in an otherwise active project is not reactivated.
	s := locationSnapshotFixture(t, ProjectRepositoryProjectSchemaV04, true)
	before := s.Repositories
	if err := validateProjectLocationSnapshot(s); err != nil || !reflect.DeepEqual(before, s.Repositories) {
		t.Fatalf("repository lifecycle changed: %v", err)
	}
}
