package loomdapp

import (
	"database/sql"
	"errors"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
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
