package supportbundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsRequireExplicitFlagAcrossProfiles(t *testing.T) {
	defaultPlan, err := BuildPlan(Options{Profile: ProfileDefault}, nil)
	if err != nil {
		t.Fatalf("BuildPlan default returned error: %v", err)
	}
	if plannedCollectorExists(defaultPlan.Collectors, "logs") {
		t.Fatalf("default plan should exclude logs: %#v", defaultPlan.Collectors)
	}
	fullPlan, err := BuildPlan(Options{Profile: ProfileFull}, nil)
	if err != nil {
		t.Fatalf("BuildPlan full returned error: %v", err)
	}
	if plannedCollectorExists(fullPlan.Collectors, "logs") {
		t.Fatalf("full profile without --include-logs should exclude logs: %#v", fullPlan.Collectors)
	}
	withLogs, err := BuildPlan(Options{Profile: ProfileDefault, IncludeLogs: true}, nil)
	if err != nil {
		t.Fatalf("BuildPlan with logs returned error: %v", err)
	}
	if !plannedCollectorExists(withLogs.Collectors, "logs") {
		t.Fatalf("--include-logs should include logs collector: %#v", withLogs.Collectors)
	}
}

func TestLogsAreRedactedAndTruncated(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	logPath := filepath.Join(t.TempDir(), "loomd.log")
	content := strings.Repeat("prefix\n", 20) +
		"Authorization: Bearer abc123\n" +
		"password=secret\n" +
		"path=" + filepath.Join(root, "logs", "loomd.log") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	result, err := Create(context.Background(), Options{
		OutputPath:  output,
		Profile:     ProfileDefault,
		IncludeLogs: true,
		LogPaths:    []string{logPath},
		MaxBytes:    120,
		Now:         fixedTestTime(),
		PathAliases: []PathAlias{{Label: "$LOOM_BOX", Root: root}},
	}, []Collector{{
		Key:          "logs",
		Title:        "Logs",
		Profiles:     []Profile{ProfileMinimal, ProfileDefault, ProfileFull},
		PrivacyClass: PrivacyLogExcerpt,
		RequiresLogs: true,
		Collect:      collectLogs,
	}})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !sectionStatusExists(result.Sections, "logs", SectionStatusTruncated) {
		t.Fatalf("logs should be marked truncated: %#v", result.Sections)
	}
	text := readArchiveText(t, output, "loom-support/logs/01-loomd.log.txt")
	for _, leaked := range []string{"abc123", "secret", root} {
		if strings.Contains(text, leaked) {
			t.Fatalf("log leaked %q:\n%s", leaked, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "$LOOM_BOX/logs/loomd.log") {
		t.Fatalf("log missing redaction/alias markers:\n%s", text)
	}
	if result.Manifest.Counts.Truncated == 0 {
		t.Fatalf("manifest should record truncation: %#v", result.Manifest.Counts)
	}
	if !result.Manifest.Privacy.LogsIncluded {
		t.Fatalf("manifest should record logs included: %#v", result.Manifest.Privacy)
	}
}

func TestLogsWithoutKnownPathAreSkipped(t *testing.T) {
	result, err := Create(context.Background(), Options{
		OutputPath:  filepath.Join(t.TempDir(), "support.tar.gz"),
		Profile:     ProfileDefault,
		IncludeLogs: true,
		Now:         fixedTestTime(),
	}, []Collector{{
		Key:          "logs",
		Title:        "Logs",
		Profiles:     []Profile{ProfileMinimal, ProfileDefault, ProfileFull},
		PrivacyClass: PrivacyLogExcerpt,
		RequiresLogs: true,
		Collect:      collectLogs,
	}})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	section := findSectionResult(result.Sections, "logs")
	if section == nil || section.Status != SectionStatusSkipped || section.Reason != "log_path_unavailable" {
		t.Fatalf("logs should be skipped with explicit reason: %#v", result.Sections)
	}
}
