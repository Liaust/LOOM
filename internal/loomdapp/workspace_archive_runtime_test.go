package loomdapp

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	workerruntimes "loom.local/loom/internal/workers/runtimes"
)

func TestWorkspaceArchiveRuntimeIsDisabledByDefault(t *testing.T) {
	service, err := workspaceArchiveRuntime(config.Config{}, storagecatalog.Service{}, "")
	if err != nil || service != nil {
		t.Fatalf("disabled workspace archive runtime = %#v, %v", service, err)
	}
	runtime := workerruntimes.NewKnowledgeIndexerRuntime(knowledge.NewService(nil)).WithArchiveService(service, "main")
	if runtime.Custody != nil {
		t.Fatal("disabled archive service connected a custody consumer")
	}
}

func TestWorkspaceArchiveRuntimeBindsConfiguredRootsCatalogAndCredential(t *testing.T) {
	root := t.TempDir()
	boxRoot := filepath.Join(root, "box", "..", "box")
	storageRoot := filepath.Join(root, "storage", ".")
	credentialDirectory := filepath.Join(root, "credentials")
	if err := os.MkdirAll(credentialDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialDirectory, err := filepath.EvalSymlinks(credentialDirectory)
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(credentialDirectory, storagearchive.WorkspaceArchiveManifestCredentialName)
	encoded := hex.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	if err := os.WriteFile(credential, []byte(encoded), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credential, 0o400); err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.Service{}
	service, err := workspaceArchiveRuntime(config.Config{
		WorkspaceArchiveEnabled:       true,
		WorkspaceArchiveManifestKeyID: "workspace-archive-runtime-v1",
		BoxPath:                       boxRoot,
		StorageRoot:                   storageRoot,
	}, catalog, credentialDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if service == nil {
		t.Fatal("enabled workspace archive runtime was not constructed")
	}
	if service.Roots.BoxRoot != filepath.Clean(boxRoot) || service.Roots.StorageRoot != filepath.Clean(storageRoot) {
		t.Fatalf("trusted roots = %#v", service.Roots)
	}
	if service.ManifestKeyID != "workspace-archive-runtime-v1" || service.ManifestKey == nil {
		t.Fatalf("manifest binding = id:%q lookup:%v", service.ManifestKeyID, service.ManifestKey != nil)
	}
	if _, ok := service.Catalog.(storagecatalog.Service); !ok {
		t.Fatalf("catalog backend = %T, want storagecatalog.Service", service.Catalog)
	}
	if _, ok := service.Journal.(storagecatalog.Service); !ok {
		t.Fatalf("journal backend = %T, want storagecatalog.Service", service.Journal)
	}
	key, err := service.ManifestKey(context.Background(), service.ManifestKeyID)
	if err != nil || len(key) != 32 {
		t.Fatalf("manifest key lookup len=%d err=%v", len(key), err)
	}
	runtime := workerruntimes.NewKnowledgeIndexerRuntime(knowledge.NewService(nil)).WithArchiveService(service, "configured-main")
	projector, ok := runtime.Custody.(knowledge.NotesArchiveProjector)
	if !ok || projector.NodeKey != "configured-main" || projector.Workspace.Roots != service.Roots || projector.Workspace.ManifestKeyID != service.ManifestKeyID {
		t.Fatal("Notes consumer lost its exact trusted archive owner")
	}
	if key, err := projector.Workspace.ManifestKey(context.Background(), projector.Workspace.ManifestKeyID); err != nil || len(key) != 32 {
		t.Fatal("Notes consumer did not reuse the approved key provider", err)
	}
}

func TestWorkspaceArchiveRuntimeRefusesMissingCredential(t *testing.T) {
	service, err := workspaceArchiveRuntime(config.Config{
		WorkspaceArchiveEnabled:       true,
		WorkspaceArchiveManifestKeyID: config.DefaultWorkspaceArchiveManifestKeyID,
		BoxPath:                       filepath.Join(t.TempDir(), "box"),
		StorageRoot:                   filepath.Join(t.TempDir(), "storage"),
	}, storagecatalog.Service{}, "")
	if err == nil || service != nil {
		t.Fatalf("missing credential runtime = %#v, %v", service, err)
	}
}
