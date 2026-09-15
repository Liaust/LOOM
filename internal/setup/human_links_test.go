package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyHumanLinksSkipsInaccessibleLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics are required")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can traverse the inaccessible test directory")
	}
	root := t.TempDir()
	target := filepath.Join(root, "storage-export")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create target: %v", err)
	}
	inaccessibleHome := filepath.Join(root, "other-user")
	if err := os.MkdirAll(inaccessibleHome, 0o700); err != nil {
		t.Fatalf("create inaccessible home: %v", err)
	}
	if err := os.Chmod(inaccessibleHome, 0); err != nil {
		t.Fatalf("chmod inaccessible home: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(inaccessibleHome, 0o700)
	})

	changes, err := applyHumanLinks(SetupPlan{
		Spec: SetupSpec{NodeKind: "main"},
		Paths: PathPlan{HumanLinks: []HumanLinkPlan{{
			Key:        "main_storage_other_user",
			Kind:       humanLinkKindBox,
			Intent:     humanLinkIntentSymlink,
			Label:      "LOOM Storage",
			LinkPath:   filepath.Join(inaccessibleHome, "LOOM Storage"),
			TargetPath: target,
			Required:   true,
		}}},
	}, true)
	if err != nil {
		t.Fatalf("applyHumanLinks returned error: %v", err)
	}
	if len(changes.skipped) != 1 {
		t.Fatalf("skipped changes = %#v", changes.skipped)
	}
	if changes.skipped[0].Status != ApplyStatusSkipped || !strings.Contains(changes.skipped[0].Message, "not accessible") {
		t.Fatalf("unexpected skipped change: %#v", changes.skipped[0])
	}
}

func TestHumanLinkStatusMarksPermissionDeniedAsInaccessibleWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics are required")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can traverse the inaccessible test directory")
	}
	root := t.TempDir()
	inaccessibleHome := filepath.Join(root, "other-user")
	if err := os.MkdirAll(inaccessibleHome, 0o700); err != nil {
		t.Fatalf("create inaccessible home: %v", err)
	}
	if err := os.Chmod(inaccessibleHome, 0); err != nil {
		t.Fatalf("chmod inaccessible home: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(inaccessibleHome, 0o700)
	})

	status := humanLinkStatus(HumanLinkPlan{
		Key:        "main_storage_other_user",
		Kind:       humanLinkKindBox,
		Intent:     humanLinkIntentSymlink,
		LinkPath:   filepath.Join(inaccessibleHome, "LOOM Storage"),
		TargetPath: filepath.Join(root, "storage-export"),
		Required:   true,
	})
	if status.Status != "inaccessible" {
		t.Fatalf("status = %#v", status)
	}
	if !humanLinkNeedsRepair(status) {
		t.Fatalf("inaccessible link should still be visible as a repairable finding")
	}
	if severity := humanLinkSeverity(status); severity != DiagnosticWarning {
		t.Fatalf("severity = %q, want warning", severity)
	}
}
