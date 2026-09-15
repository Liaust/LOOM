package supportbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeProfile(t *testing.T) {
	for input, want := range map[string]Profile{
		"":        ProfileDefault,
		"default": ProfileDefault,
		"DEFAULT": ProfileDefault,
		"minimal": ProfileMinimal,
		"full":    ProfileFull,
	} {
		got, err := NormalizeProfile(input)
		if err != nil {
			t.Fatalf("NormalizeProfile(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeProfile(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := NormalizeProfile("unsafe"); err == nil {
		t.Fatal("invalid profile should fail")
	}
}

func TestNormalizeOptionsDefaultsAndValidation(t *testing.T) {
	opts, err := NormalizeOptions(Options{})
	if err != nil {
		t.Fatalf("NormalizeOptions returned error: %v", err)
	}
	if opts.Profile != ProfileDefault || opts.MaxItems != DefaultMaxItems || opts.MaxBytes != DefaultMaxBytes || opts.Timeout != DefaultTimeout {
		t.Fatalf("unexpected defaults: %#v", opts)
	}
	if _, err := NormalizeOptions(Options{MaxBytes: -1}); err == nil {
		t.Fatal("negative max bytes should fail")
	}
}

func TestBuildPlanUsesProfilesAndFlags(t *testing.T) {
	plan, err := BuildPlan(Options{Profile: ProfileMinimal}, nil)
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if !plannedCollectorExists(plan.Collectors, "version") || !plannedCollectorExists(plan.Collectors, "doctor") {
		t.Fatalf("minimal plan missing expected collectors: %#v", plan.Collectors)
	}
	if plannedCollectorExists(plan.Collectors, "storage") {
		t.Fatalf("minimal plan should not include storage: %#v", plan.Collectors)
	}
	full, err := BuildPlan(Options{Profile: ProfileFull}, nil)
	if err != nil {
		t.Fatalf("BuildPlan full returned error: %v", err)
	}
	if plannedCollectorExists(full.Collectors, "logs") || plannedCollectorExists(full.Collectors, "live") {
		t.Fatalf("full plan should not include logs/live without explicit flags: %#v", full.Collectors)
	}
	withFlags, err := BuildPlan(Options{Profile: ProfileFull, IncludeLogs: true, IncludeLive: true}, nil)
	if err != nil {
		t.Fatalf("BuildPlan full with flags returned error: %v", err)
	}
	if !plannedCollectorExists(withFlags.Collectors, "logs") || !plannedCollectorExists(withFlags.Collectors, "live") {
		t.Fatalf("full plan with flags should include logs/live: %#v", withFlags.Collectors)
	}
}

func TestCreateDryRunDoesNotWriteArchive(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	called := false
	collectors := []Collector{{
		Key:          "dry_run_guard",
		Title:        "Dry Run Guard",
		Profiles:     []Profile{ProfileDefault},
		PrivacyClass: PrivacyDiagnosticSummary,
		Collect: func(context.Context, CollectionContext) (CollectorOutput, error) {
			called = true
			return CollectorOutput{}, errors.New("dry-run should not collect")
		},
	}}
	result, err := Create(context.Background(), Options{DryRun: true, OutputPath: output, Now: fixedTestTime()}, collectors)
	if err != nil {
		t.Fatalf("Create dry-run returned error: %v", err)
	}
	if result.Status != "planned" || !result.DryRun {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if called {
		t.Fatal("dry-run should build the plan without running collectors")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run should not write archive, stat err=%v", err)
	}
}

func TestCreateArchiveLayoutWithPlaceholderCollectors(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	result, err := Create(context.Background(), Options{OutputPath: output, Now: fixedTestTime()}, nil)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if result.Manifest.Counts.Skipped == 0 {
		t.Fatalf("placeholder collectors should be recorded as skipped: %#v", result.Manifest.Counts)
	}
	names := archiveNames(t, output)
	for _, want := range []string{
		"loom-support/README.txt",
		"loom-support/collection/errors.json",
		"loom-support/collection/plan.json",
		"loom-support/collection/redaction_report.json",
		"loom-support/human/collection_warnings.txt",
		"loom-support/human/summary.txt",
		"loom-support/manifest.json",
	} {
		if !slices.Contains(names, want) {
			t.Fatalf("archive missing %s; names=%v", want, names)
		}
	}
	if !slices.IsSorted(names) {
		t.Fatalf("archive names should be deterministic and sorted: %v", names)
	}
}

func TestCollectorFailureAndTruncationAreRecorded(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	collectors := []Collector{
		{
			Key:          "ok",
			Title:        "OK",
			Profiles:     []Profile{ProfileDefault},
			PrivacyClass: PrivacyDiagnosticSummary,
			Collect: func(context.Context, CollectionContext) (CollectorOutput, error) {
				return CollectorOutput{Files: []File{{
					Path:         "summaries/ok.txt",
					ContentType:  "text/plain",
					PrivacyClass: PrivacyDiagnosticSummary,
					Data:         []byte("ok"),
				}}}, nil
			},
		},
		NewFailingCollector("bad", "Bad", errors.New("boom")),
		{
			Key:          "large",
			Title:        "Large",
			Profiles:     []Profile{ProfileDefault},
			PrivacyClass: PrivacyDiagnosticSummary,
			Collect: func(context.Context, CollectionContext) (CollectorOutput, error) {
				return CollectorOutput{Files: []File{{
					Path:         "summaries/large.txt",
					ContentType:  "text/plain",
					PrivacyClass: PrivacyDiagnosticSummary,
					Data:         []byte(strings.Repeat("x", 64)),
				}}}, nil
			},
		},
	}
	result, err := Create(context.Background(), Options{OutputPath: output, MaxBytes: 8, Now: fixedTestTime()}, collectors)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if result.Manifest.Counts.Failed != 1 {
		t.Fatalf("failed count = %d, want 1; sections=%#v", result.Manifest.Counts.Failed, result.Sections)
	}
	if result.Manifest.Counts.Truncated != 1 {
		t.Fatalf("truncated count = %d, want 1; sections=%#v", result.Manifest.Counts.Truncated, result.Sections)
	}
	if !sectionStatusExists(result.Sections, "bad", SectionStatusFailed) {
		t.Fatalf("failed section not recorded: %#v", result.Sections)
	}
	if !sectionStatusExists(result.Sections, "large", SectionStatusTruncated) {
		t.Fatalf("truncated section not recorded: %#v", result.Sections)
	}
}

func TestWriteArchiveRejectsUnsafePaths(t *testing.T) {
	var buffer bytes.Buffer
	err := WriteArchiveTo(&buffer, []File{{Path: "../secret", Data: []byte("no")}})
	if err == nil {
		t.Fatal("unsafe archive path should fail")
	}
}

func plannedCollectorExists(items []PlannedCollector, key string) bool {
	for _, item := range items {
		if item.Key == key {
			return true
		}
	}
	return false
}

func sectionStatusExists(items []SectionResult, key, status string) bool {
	for _, item := range items {
		if item.Key == key && item.Status == status {
			return true
		}
	}
	return false
}

func archiveNames(t *testing.T, archivePath string) []string {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	names := []string{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, header.Name)
	}
	return names
}

func fixedTestTime() time.Time {
	return time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
}
