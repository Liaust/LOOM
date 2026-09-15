package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPackageValidatesAndNormalizesManifest(t *testing.T) {
	root := writeTestPackage(t, validManifestJSON("loom.project-cockpit", "0.1.0", "usage-documents/status-read.md"))

	loaded, err := LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage returned error: %v", err)
	}
	if loaded.Manifest.Module.ID != "loom.project-cockpit" {
		t.Fatalf("module id = %q", loaded.Manifest.Module.ID)
	}
	if loaded.ManifestHash == "" || !strings.HasPrefix(loaded.ManifestHash, "sha256:") {
		t.Fatalf("manifest hash = %q", loaded.ManifestHash)
	}
	if loaded.ContentHash == "" || !strings.HasPrefix(loaded.ContentHash, "sha256:") {
		t.Fatalf("content hash = %q", loaded.ContentHash)
	}
	if len(loaded.Manifest.Provides.Providers) != 1 {
		t.Fatalf("provider declarations = %d, want 1", len(loaded.Manifest.Provides.Providers))
	}
	if len(loaded.Manifest.Provides.Capabilities) != 1 {
		t.Fatalf("capability declarations = %d, want 1", len(loaded.Manifest.Provides.Capabilities))
	}
	if got := loaded.Manifest.Provides.UsageDocuments[0].ContentHash; !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("usage document content hash = %q", got)
	}
}

func TestLoadPackageRejectsInvalidModuleID(t *testing.T) {
	root := writeTestPackage(t, validManifestJSON("obsidian", "0.1.0", "usage-documents/status-read.md"))

	_, err := LoadPackage(root)
	if err == nil {
		t.Fatal("LoadPackage accepted connector-like module id")
	}
	if !strings.Contains(err.Error(), "loom.<slug>") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadPackageRejectsPathTraversal(t *testing.T) {
	root := writeTestPackage(t, validManifestJSON("loom.project-cockpit", "0.1.0", "../secret.md"))

	_, err := LoadPackage(root)
	if err == nil {
		t.Fatal("LoadPackage accepted traversal path")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadPackageRejectsInvalidJSON(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "module.json"), []byte(`{"module":`))

	_, err := LoadPackage(root)
	if err == nil {
		t.Fatal("LoadPackage accepted invalid JSON")
	}
}

func writeTestPackage(t *testing.T, manifest string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "module.json"), []byte(manifest))
	writeFile(t, filepath.Join(root, "usage-documents", "status-read.md"), []byte("# Usage\n\nUse this capability for status reads.\n"))
	return root
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func validManifestJSON(moduleID, version, usagePath string) string {
	return `{
  "module": {
    "id": "` + moduleID + `",
    "name": "LOOM Project Cockpit",
    "version": "` + version + `",
    "kind": "native",
    "description": "Minimal native module."
  },
  "requires": {
    "core_version_min": "0.0.0-dev",
    "runtime_features": ["postgres", "event_log"]
  },
  "storage": {
    "database": { "required": false },
    "filesystem": { "required": false }
  },
  "provides": {
    "object_types": [],
    "providers": [
      {
        "provider_key": "loom-project-cockpit",
        "display_name": "LOOM Project Cockpit"
      }
    ],
    "capabilities": [
      {
        "provider_key": "loom-project-cockpit",
        "endpoint_name": "status.read",
        "capability_class_namespace": "module.project_cockpit",
        "capability_class_name": "status.read",
        "display_name": "Read Project Cockpit Status",
        "form": "query",
        "execution_authorization_level": 1,
        "risk_level": "low"
      }
    ],
    "usage_documents": [
      {
        "target_kind": "capability",
        "target_ref": "loom-project-cockpit.status.read",
        "title": "Status Usage",
        "path": "` + usagePath + `"
      }
    ],
    "backup_hooks": [
      {
        "hook_key": "manifest-export",
        "hook_kind": "manifest"
      }
    ]
  }
}`
}
