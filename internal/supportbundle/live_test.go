package supportbundle

import (
	"context"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/cloudstorage"
)

func TestLiveRequiresExplicitFlagAcrossProfiles(t *testing.T) {
	defaultPlan, err := BuildPlan(Options{Profile: ProfileDefault}, nil)
	if err != nil {
		t.Fatalf("BuildPlan default returned error: %v", err)
	}
	if plannedCollectorExists(defaultPlan.Collectors, "live") {
		t.Fatalf("default plan should exclude live: %#v", defaultPlan.Collectors)
	}
	fullPlan, err := BuildPlan(Options{Profile: ProfileFull}, nil)
	if err != nil {
		t.Fatalf("BuildPlan full returned error: %v", err)
	}
	if plannedCollectorExists(fullPlan.Collectors, "live") {
		t.Fatalf("full profile without --include-live should exclude live: %#v", fullPlan.Collectors)
	}
	withLive, err := BuildPlan(Options{Profile: ProfileDefault, IncludeLive: true}, nil)
	if err != nil {
		t.Fatalf("BuildPlan with live returned error: %v", err)
	}
	if !plannedCollectorExists(withLive.Collectors, "live") {
		t.Fatalf("--include-live should include live collector: %#v", withLive.Collectors)
	}
}

func TestLiveProviderRequiresFlagAndManifestRecordsChoice(t *testing.T) {
	called := false
	collector := Collector{
		Key:          "live",
		Title:        "Live Diagnostics",
		Profiles:     []Profile{ProfileMinimal, ProfileDefault, ProfileFull},
		PrivacyClass: PrivacyDiagnosticSummary,
		RequiresLive: true,
		Collect:      collectLive,
	}
	withoutLive, err := Create(context.Background(), Options{
		OutputPath: filepath.Join(t.TempDir(), "support.tar.gz"),
		Profile:    ProfileDefault,
		Now:        fixedTestTime(),
		LiveDiagnosticsProvider: func(context.Context, Options) (LiveDiagnosticsSummary, error) {
			called = true
			return LiveDiagnosticsSummary{}, nil
		},
	}, []Collector{collector})
	if err != nil {
		t.Fatalf("Create without live returned error: %v", err)
	}
	if called {
		t.Fatal("live provider should not be called without IncludeLive")
	}
	section := findSectionResult(withoutLive.Sections, "live")
	if section == nil || section.Status != SectionStatusSkipped || section.Reason != "live_probes_not_requested" {
		t.Fatalf("live should be skipped without explicit flag: %#v", withoutLive.Sections)
	}
	if withoutLive.Manifest.Privacy.LiveProbesAllowed {
		t.Fatalf("manifest should record live disabled: %#v", withoutLive.Manifest.Privacy)
	}

	withLive, err := Create(context.Background(), Options{
		OutputPath:  filepath.Join(t.TempDir(), "support.tar.gz"),
		Profile:     ProfileDefault,
		IncludeLive: true,
		Now:         fixedTestTime(),
		LiveDiagnosticsProvider: func(context.Context, Options) (LiveDiagnosticsSummary, error) {
			called = true
			return LiveDiagnosticsSummary{
				Status: "collected",
				Cloud:  &cloudstorage.StatusReport{Status: "ok", Mode: string(cloudstorage.StatusModeLive)},
			}, nil
		},
	}, []Collector{collector})
	if err != nil {
		t.Fatalf("Create with live returned error: %v", err)
	}
	if !called {
		t.Fatal("live provider should be called when IncludeLive is set")
	}
	if !sectionStatusExists(withLive.Sections, "live", SectionStatusIncluded) {
		t.Fatalf("live should be included with explicit flag: %#v", withLive.Sections)
	}
	if !withLive.Manifest.Privacy.LiveProbesAllowed || len(withLive.Manifest.Privacy.LiveSections) != 1 || withLive.Manifest.Privacy.LiveSections[0] != "cloud" {
		t.Fatalf("manifest should record live section choice: %#v", withLive.Manifest.Privacy)
	}
}

func TestLiveWithoutProviderIsSkipped(t *testing.T) {
	result, err := Create(context.Background(), Options{
		OutputPath:  filepath.Join(t.TempDir(), "support.tar.gz"),
		Profile:     ProfileDefault,
		IncludeLive: true,
		Now:         fixedTestTime(),
	}, []Collector{{
		Key:          "live",
		Title:        "Live Diagnostics",
		Profiles:     []Profile{ProfileMinimal, ProfileDefault, ProfileFull},
		PrivacyClass: PrivacyDiagnosticSummary,
		RequiresLive: true,
		Collect:      collectLive,
	}})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	section := findSectionResult(result.Sections, "live")
	if section == nil || section.Status != SectionStatusSkipped || section.Reason != "live_provider_unavailable" {
		t.Fatalf("live should be skipped with provider reason: %#v", result.Sections)
	}
}
