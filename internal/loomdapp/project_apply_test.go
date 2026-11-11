package loomdapp

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func TestProjectDeclarationCompositionFailsClosedAndReloads(t *testing.T) {
	if newProjectDeclarationService(nil, func() (config.Config, error) { return config.Config{}, nil }) != nil || newProjectDeclarationService(&sql.DB{}, nil) != nil {
		t.Fatal("incomplete composition accepted")
	}
	calls := 0
	service := newProjectDeclarationService(&sql.DB{}, func() (config.Config, error) { calls++; return config.Config{}, errors.New("private config failure") })
	p := projectapply.Principal{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID()}
	for range 2 {
		_, err := service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "/tmp/fixture"})
		var f *projectapply.Failure
		if !errors.As(err, &f) || f.Cause != "local_config_unavailable" {
			t.Fatalf("configuration did not fail closed: %v", err)
		}
	}
	if calls != 2 {
		t.Fatal("configuration snapshot cached")
	}
}

func TestProjectDeclarationProjectsCatalogBindingFailsClosed(t *testing.T) {
	db := &sql.DB{}
	if _, err := projectDeclarationProjects(db, config.Config{BoxesFile: "/missing/boxes.json"}); err == nil {
		t.Fatal("broken configured catalog fell back to global service")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "box")
	if err := os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	descriptor, _ := json.Marshal(map[string]string{"schema_version": "loom.box.v0.5", "box_id": "box_primary", "root_path": root, "owner_node": "main"})
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), descriptor, 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "boxes.json")
	raw, _ := json.Marshal(boxregistry.Document{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal", Boxes: []boxregistry.Entry{{Key: "personal", Label: "Personal", Node: "main", RootPath: root}}})
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	service, err := projectDeclarationProjects(db, config.Config{NodeID: "main", BoxPath: root, BoxesFile: file, DataDir: filepath.Join(base, "data"), StorageRoot: filepath.Join(base, "storage"), BoxStateRoot: filepath.Join(base, "state")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReconcileBoxMembership(t.Context(), projects.BoxMembershipReconcileInput{ProjectIDs: []string{ids.NewProjectID()}, Apply: true, CatalogDigest: "stale"})
	if err == nil || !strings.Contains(err.Error(), "Box catalog changed") {
		t.Fatalf("registration service did not retain catalog binding: %v", err)
	}
}
