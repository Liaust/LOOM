package loomcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportBundleDryRunDoesNotWriteArchive(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	stdout, stderr, err := executeSupportCommand("support", "bundle", "create", "--dry-run", "--output", output)
	if err != nil {
		t.Fatalf("support bundle dry-run returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOOM support bundle plan") || !strings.Contains(stdout, "No archive written.") {
		t.Fatalf("unexpected dry-run output:\n%s", stdout)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write archive, stat err=%v", err)
	}
}

func TestSupportBundleCreateWritesArchive(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	stdout, stderr, err := executeSupportCommand("support", "bundle", "create", "--output", output)
	if err != nil {
		t.Fatalf("support bundle create returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOOM support bundle: created") || !strings.Contains(stdout, "Review before sharing.") {
		t.Fatalf("unexpected create output:\n%s", stdout)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("archive was not written: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("archive should not be empty")
	}
}

func TestSupportBundleCarriesSafeFilesystemConfigWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	serviceRoot := filepath.Join(root, "service")
	dataRoot := filepath.Join(root, "data")
	legacyExport := filepath.Join(root, "legacy-export")
	legacyDocuments := filepath.Join(root, "legacy-documents")
	t.Setenv("LOOM_SERVICE_ROOT", serviceRoot)
	t.Setenv("LOOM_DATA_DIR", dataRoot)
	t.Setenv("LOOM_STORAGE_ROOT", filepath.Join(serviceRoot, "storage"))
	t.Setenv("LOOM_IMPORTS_ROOT", filepath.Join(serviceRoot, "storage", "imports"))
	t.Setenv("LOOM_USER_BACKUPS_ROOT", filepath.Join(serviceRoot, "storage", "backups"))
	t.Setenv("LOOM_ARCHIVE_ROOT", filepath.Join(serviceRoot, "storage", "archive"))
	t.Setenv("LOOM_GENERATED_ROOT", filepath.Join(dataRoot, "generated"))
	t.Setenv("LOOM_STORAGE_EXPORT_ROOT", legacyExport)
	t.Setenv("LOOM_MAIN_DOCUMENTS_ROOT", legacyDocuments)
	t.Setenv("LOOM_DB_URL", "postgres://operator:unsafe-password@127.0.0.1/loom")
	configPath := filepath.Join(t.TempDir(), "support.env")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("write isolated support config: %v", err)
	}

	output := filepath.Join(t.TempDir(), "support.tar.gz")
	_, stderr, err := executeSupportCommand("--config", configPath, "support", "bundle", "create", "--profile", "minimal", "--output", output)
	if err != nil {
		t.Fatalf("support bundle create returned error: %v stderr=%s", err, stderr)
	}
	summary := readSupportArchiveText(t, output, "loom-support/summaries/config.json")
	for _, want := range []string{
		`"service_root": "$LOOM_SERVICE_ROOT"`,
		`"box_root": "$LOOM_BOX"`,
		`"imports_root": "$LOOM_IMPORTS_ROOT"`,
		`"box_state_root": "$LOOM_BOX_STATE_ROOT"`,
		`"deprecated_storage_export_root": "$LOOM_DEPRECATED_STORAGE_EXPORT_ROOT"`,
		`"deprecated_main_documents_root": "$LOOM_DEPRECATED_MAIN_DOCUMENTS_ROOT"`,
		`"storage_export_migration_input_only": "true"`,
		`"main_documents_migration_input_only": "true"`,
		`"legacy_box_state_compatibility_active": "false"`,
		`"db_configured": "true"`,
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("config summary missing %s:\n%s", want, summary)
		}
	}
	for _, forbidden := range []string{root, "operator", "unsafe-password", "postgres://"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("config summary leaked %q:\n%s", forbidden, summary)
		}
	}
}

func TestSupportBundleJSONDryRunPlan(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	stdout, stderr, err := executeSupportCommand("--json", "support", "bundle", "create", "--dry-run", "--profile", "minimal", "--output", output)
	if err != nil {
		t.Fatalf("support bundle JSON dry-run returned error: %v stderr=%s", err, stderr)
	}
	var result supportBundleJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON output did not unmarshal: %v output=%s", err, stdout)
	}
	if result.Status != "planned" || !result.DryRun || result.OutputPath != output {
		t.Fatalf("unexpected JSON result: %#v", result)
	}
	if result.Plan.Profile != "minimal" {
		t.Fatalf("profile = %q, want minimal", result.Plan.Profile)
	}
	if !supportPlanHasCollector(result.Plan.Collectors, "version") || !supportPlanHasCollector(result.Plan.Collectors, "doctor") {
		t.Fatalf("minimal plan missing version/doctor collectors: %#v", result.Plan.Collectors)
	}
	if supportPlanHasCollector(result.Plan.Collectors, "storage") || supportPlanHasCollector(result.Plan.Collectors, "logs") {
		t.Fatalf("minimal/default plan should not include storage or logs: %#v", result.Plan.Collectors)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("JSON dry-run should not write archive, stat err=%v", err)
	}
}

func TestSupportBundleDefaultOutputWritesUnderTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	stdout, stderr, err := executeSupportCommand("--json", "support", "bundle", "create", "--profile", "minimal")
	if err != nil {
		t.Fatalf("support bundle default output returned error: %v stderr=%s", err, stderr)
	}
	var result supportBundleJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON output did not unmarshal: %v output=%s", err, stdout)
	}
	if result.Status != "created" || result.OutputPath == "" {
		t.Fatalf("unexpected create result: %#v", result)
	}
	if !strings.HasPrefix(result.OutputPath, tmp+string(filepath.Separator)) {
		t.Fatalf("default output path = %q, want under %q", result.OutputPath, tmp)
	}
	if _, err := os.Stat(result.OutputPath); err != nil {
		t.Fatalf("default archive was not written: %v", err)
	}
	if result.Manifest.Privacy.LogsIncluded {
		t.Fatalf("logs should not be included by default: %#v", result.Manifest.Privacy)
	}
	if supportPlanHasCollector(result.Plan.Collectors, "logs") {
		t.Fatalf("logs collector should not be planned by default: %#v", result.Plan.Collectors)
	}
}

func TestSupportBundleJSONDryRunPrivacyFlags(t *testing.T) {
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	stdout, stderr, err := executeSupportCommand("--json", "support", "bundle", "create", "--dry-run", "--include-logs", "--include-live", "--output", output)
	if err != nil {
		t.Fatalf("support bundle privacy dry-run returned error: %v stderr=%s", err, stderr)
	}
	var result supportBundleJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON output did not unmarshal: %v output=%s", err, stdout)
	}
	if !supportPlanHasCollector(result.Plan.Collectors, "logs") || !supportPlanHasCollector(result.Plan.Collectors, "live") {
		t.Fatalf("privacy opt-in collectors missing from plan: %#v", result.Plan.Collectors)
	}
	if !result.Manifest.Privacy.LogsIncluded || !result.Manifest.Privacy.LiveProbesAllowed {
		t.Fatalf("manifest privacy flags missing: %#v", result.Manifest.Privacy)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("privacy dry-run should not write archive, stat err=%v", err)
	}
}

func TestSupportBundleBadProfileFails(t *testing.T) {
	stdout, stderr, err := executeSupportCommand("support", "bundle", "create", "--dry-run", "--profile", "unsafe")
	if err == nil {
		t.Fatal("bad profile should fail")
	}
	if stdout != "" {
		t.Fatalf("bad profile should not write stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "unsupported support bundle profile") {
		t.Fatalf("bad profile error missing context:\n%s", stderr)
	}
}

func TestSupportBundleBadOutputPathFailsCleanly(t *testing.T) {
	output := filepath.Join(t.TempDir(), "missing", "support.tar.gz")
	stdout, stderr, err := executeSupportCommand("support", "bundle", "create", "--output", output)
	if err == nil {
		t.Fatal("bad output path should fail")
	}
	if stdout != "" {
		t.Fatalf("bad output path should not write stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, "support.tar.gz") {
		t.Fatalf("bad output path error missing context:\n%s", stderr)
	}
}

type supportBundleJSONResult struct {
	Status     string `json:"status"`
	OutputPath string `json:"output_path"`
	DryRun     bool   `json:"dry_run"`
	Plan       struct {
		Profile    string `json:"profile"`
		Collectors []struct {
			Key string `json:"key"`
		} `json:"collectors"`
	} `json:"plan"`
	Manifest struct {
		Privacy struct {
			LogsIncluded      bool     `json:"logs_included"`
			LiveProbesAllowed bool     `json:"live_probes_allowed"`
			LiveSections      []string `json:"live_sections"`
		} `json:"privacy"`
	} `json:"manifest"`
}

func executeSupportCommand(args ...string) (string, string, error) {
	cmd := NewRootCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	fullArgs := append([]string{"--socket", filepath.Join(os.TempDir(), "loom-support-test-missing.sock")}, args...)
	cmd.SetArgs(fullArgs)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func supportPlanHasCollector(collectors []struct {
	Key string `json:"key"`
}, key string) bool {
	for _, collector := range collectors {
		if collector.Key == key {
			return true
		}
	}
	return false
}

func readSupportArchiveText(t *testing.T, archivePath, wantName string) string {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open support archive: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open support gzip: %v", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read support archive: %v", err)
		}
		if header.Name != wantName {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %s: %v", wantName, err)
		}
		return string(data)
	}
	t.Fatalf("support archive missing %s", wantName)
	return ""
}
