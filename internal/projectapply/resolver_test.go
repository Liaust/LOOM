package projectapply

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDeclarationRealResolverFencesPostgres(t *testing.T) {
	service, _, p, root, database := realOwnerFixture(t, 0)
	request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root, NodeRef: "remote"}
	if _, err := service.Plan(t.Context(), p, request); err == nil {
		t.Fatal("remote target used local source")
	}
	external := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(root, pc.CanonicalRootContractPath))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(external, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(external, pc.CanonicalRootContractPath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "escape")
	if err = os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}
	request.ProjectRef = alias
	request.NodeRef = ""
	if _, err = service.Plan(t.Context(), p, request); err == nil {
		t.Fatal("source escaped configured Box")
	}
	var rows int
	for _, table := range []string{"projects.projects", "projects.declaration_operations", "communication.messages"} {
		if err = database.QueryRow(`SELECT count(*) FROM ` + table).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("read wrote %s: %d %v", table, rows, err)
		}
	}
}

func TestProjectDeclarationProjectLookupErrorClassification(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code pc.DeclarationErrorCode
	}{
		{"missing", sql.ErrNoRows, pc.DeclarationReferenceMissing},
		{"wrapped_missing", fmt.Errorf("lookup: %w", sql.ErrNoRows), pc.DeclarationReferenceMissing},
		{"cancelled", context.Canceled, pc.DeclarationTargetUnavailable},
		{"deadline", context.DeadlineExceeded, pc.DeclarationTargetUnavailable},
		{"permission", errors.New("permission denied"), pc.DeclarationTargetUnavailable},
		{"database", errors.New("database unavailable"), pc.DeclarationTargetUnavailable},
		{"network", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}, pc.DeclarationTargetUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := projectReferenceLookupError(tt.err)
			var f *Failure
			if !errors.As(err, &f) || f.Code != tt.code {
				t.Fatalf("lookup classified as %v", err)
			}
		})
	}
}
func TestProjectDeclarationResolverMissingVsUnavailablePostgres(t *testing.T) {
	service, _, p, _, database := realOwnerFixture(t, 0)
	_, err := service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "missing-project"})
	var f *Failure
	if !errors.As(err, &f) || f.Code != pc.DeclarationReferenceMissing || f.Cause != "project_missing" {
		t.Fatalf("missing ref=%v", err)
	}
	registered, err := projects.NewService(database).CreateProject(t.Context(), declarationRequest(p), projects.CreateInput{Name: "Existing without location", Slug: "without-location", HomeNodeRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: registered.Project.Project.ProjectID})
	if !errors.As(err, &f) || f.Code != pc.DeclarationTargetUnavailable || f.Cause != "registered_project_location_required" {
		t.Fatalf("location absence became project absence: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = service.Plan(ctx, p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "missing-project"})
	if !errors.As(err, &f) || f.Code == pc.DeclarationReferenceMissing {
		t.Fatalf("cancelled lookup became project absence: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "missing-project"})
	if !errors.As(err, &f) || f.Code == pc.DeclarationReferenceMissing {
		t.Fatalf("database failure became project absence: %v", err)
	}
}
