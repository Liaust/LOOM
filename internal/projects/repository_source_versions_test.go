package projects_test

import (
	"errors"
	"reflect"
	"testing"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func TestSupportedProjectRepositorySourceVersionsMatchValidator(t *testing.T) {
	want := []projects.ProjectRepositorySourceVersions{
		{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV03},
		{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV04},
		{ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ReposSchemaV03},
		{ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ReposSchemaV04},
		{ProjectContract: projectcontracts.ProjectSchemaV05, ReposContract: projectcontracts.ProjectSchemaV05},
	}
	if got := projects.SupportedProjectRepositorySourceVersions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supported source versions = %#v, want %#v", got, want)
	}
}

func TestProjectRepositoryPersistenceModelsKeepVersionsAndLifecycleSeparate(t *testing.T) {
	for _, model := range []reflect.Type{
		reflect.TypeOf(projects.ProjectRepositorySource{}),
		reflect.TypeOf(projects.ProjectRepositorySourceHistory{}),
	} {
		for _, field := range []string{"ProjectContractSchemaVersion", "ReposContractSchemaVersion"} {
			if _, ok := model.FieldByName(field); !ok {
				t.Fatalf("%s must store %s separately", model.Name(), field)
			}
		}
	}

	history := reflect.TypeOf(projects.ProjectRepositorySourceHistory{})
	for _, forbidden := range []string{"ArchiveState", "LifecycleStatus", "ProjectStatus", "ObservationPosture"} {
		if _, ok := history.FieldByName(forbidden); ok {
			t.Fatalf("registration-source history must not encode %s", forbidden)
		}
	}
}

func TestPlanProjectRepositorySourceVersionTransitionAllowsSupportedFirstRegistration(t *testing.T) {
	for _, next := range projects.SupportedProjectRepositorySourceVersions() {
		next := next
		t.Run(next.ProjectContract+"+"+next.ReposContract, func(t *testing.T) {
			transition, err := projects.PlanProjectRepositorySourceVersionTransition(nil, next)
			if err != nil {
				t.Fatalf("first registration returned error: %v", err)
			}
			if transition.Kind != projects.ProjectRepositorySourceVersionFirstRegistration || transition.From != nil || transition.To != next {
				t.Fatalf("transition = %#v", transition)
			}
		})
	}
}

func TestPlanProjectRepositorySourceVersionTransitionAllowsSameVersionReplay(t *testing.T) {
	for _, versions := range projects.SupportedProjectRepositorySourceVersions() {
		versions := versions
		t.Run(versions.ProjectContract+"+"+versions.ReposContract, func(t *testing.T) {
			transition, err := projects.PlanProjectRepositorySourceVersionTransition(&versions, versions)
			if err != nil {
				t.Fatalf("same-version replay returned error: %v", err)
			}
			if transition.Kind != projects.ProjectRepositorySourceVersionSame || transition.From == nil || *transition.From != versions || transition.To != versions {
				t.Fatalf("transition = %#v", transition)
			}
		})
	}
}

func TestPlanProjectRepositorySourceVersionTransitionAllowsOnlyEnumeratedV03ToV04Upgrades(t *testing.T) {
	v03v03 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV03}
	v03v04 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV04}
	v04v03 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ReposSchemaV03}
	v04v04 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ReposSchemaV04}

	allowed := []struct {
		from projects.ProjectRepositorySourceVersions
		to   projects.ProjectRepositorySourceVersions
	}{
		{from: v03v03, to: v03v04},
		{from: v03v03, to: v04v03},
		{from: v03v03, to: v04v04},
		{from: v03v04, to: v04v04},
		{from: v04v03, to: v04v04},
	}
	for _, test := range allowed {
		transition, err := projects.PlanProjectRepositorySourceVersionTransition(&test.from, test.to)
		if err != nil {
			t.Fatalf("upgrade %#v -> %#v returned error: %v", test.from, test.to, err)
		}
		if transition.Kind != projects.ProjectRepositorySourceVersionV03ToV04 {
			t.Fatalf("upgrade %#v -> %#v kind = %q", test.from, test.to, transition.Kind)
		}
	}
}

func TestPlanProjectRepositorySourceVersionTransitionRejectsDowngradesBeforeMutation(t *testing.T) {
	v03v03 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV03}
	v03v04 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV04}
	v04v03 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ReposSchemaV03}
	v04v04 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ReposSchemaV04}

	rejected := []struct {
		from projects.ProjectRepositorySourceVersions
		to   projects.ProjectRepositorySourceVersions
	}{
		{from: v03v04, to: v03v03},
		{from: v04v03, to: v03v03},
		{from: v04v04, to: v03v03},
		{from: v04v04, to: v03v04},
		{from: v04v04, to: v04v03},
		{from: v03v04, to: v04v03},
		{from: v04v03, to: v03v04},
	}
	for _, test := range rejected {
		beforeFrom, beforeTo := test.from, test.to
		_, err := projects.PlanProjectRepositorySourceVersionTransition(&test.from, test.to)
		if !errors.Is(err, projects.ErrUnsupportedProjectRepositorySourceVersionTransition) {
			t.Fatalf("transition %#v -> %#v error = %v, want unsupported transition", test.from, test.to, err)
		}
		if test.from != beforeFrom || test.to != beforeTo {
			t.Fatalf("rejected transition mutated input: from=%#v to=%#v", test.from, test.to)
		}
	}
}

func TestPlanProjectRepositorySourceVersionTransitionRejectsUnknownVersions(t *testing.T) {
	supported := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: projectcontracts.ReposSchemaV03}
	unknown := []projects.ProjectRepositorySourceVersions{
		{},
		{ProjectContract: "project.contract.v0.2", ReposContract: projectcontracts.ReposSchemaV03},
		{ProjectContract: projectcontracts.ProjectSchemaV03, ReposContract: "repos.contract.v0.5"},
		{ProjectContract: "project.contract.v0.4 ", ReposContract: projectcontracts.ReposSchemaV04},
	}
	for _, versions := range unknown {
		if _, err := projects.PlanProjectRepositorySourceVersionTransition(nil, versions); !errors.Is(err, projects.ErrUnsupportedProjectRepositorySourceVersions) {
			t.Fatalf("first registration %#v error = %v, want unsupported versions", versions, err)
		}
		if _, err := projects.PlanProjectRepositorySourceVersionTransition(&versions, supported); !errors.Is(err, projects.ErrUnsupportedProjectRepositorySourceVersions) {
			t.Fatalf("existing source %#v error = %v, want unsupported versions", versions, err)
		}
	}
}

func TestDeclarationRepositorySourceV05UpgradeAndDowngrade(t *testing.T) {
	v05 := projects.ProjectRepositorySourceVersions{ProjectContract: projectcontracts.ProjectSchemaV05, ReposContract: projectcontracts.ProjectSchemaV05}
	for _, legacy := range projects.SupportedProjectRepositorySourceVersions() {
		if legacy == v05 {
			continue
		}
		transition, err := projects.PlanProjectRepositorySourceVersionTransition(&legacy, v05)
		if err != nil || transition.Kind != projects.ProjectRepositorySourceVersionToV05 {
			t.Fatalf("explicit upgrade %v: %+v %v", legacy, transition, err)
		}
		if _, err = projects.PlanProjectRepositorySourceVersionTransition(&v05, legacy); err == nil {
			t.Fatalf("implicit downgrade allowed %v", legacy)
		}
	}
	for _, mixed := range []projects.ProjectRepositorySourceVersions{{ProjectContract: projectcontracts.ProjectSchemaV05, ReposContract: projectcontracts.ReposSchemaV04}, {ProjectContract: projectcontracts.ProjectSchemaV04, ReposContract: projectcontracts.ProjectSchemaV05}, {ProjectContract: projectcontracts.ProjectSchemaV05, ReposContract: "repos.contract.v0.5"}} {
		if _, err := projects.PlanProjectRepositorySourceVersionTransition(nil, mixed); err == nil {
			t.Fatalf("fictitious/mixed source accepted %+v", mixed)
		}
	}
}
