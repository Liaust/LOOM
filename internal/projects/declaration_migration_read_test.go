package projects

import (
	"loom.local/loom/internal/requestctx"
	"testing"
)

func TestDeclarationMigrationProjectsRequiresTransaction(t *testing.T) {
	if _, e := ReadDeclarationMigrationTargetTx(t.Context(), nil, "project"); e == nil {
		t.Fatal("target without transaction")
	}
	out, e := ReadDeclarationMigrationProjectTx(t.Context(), nil, requestctx.Context{}, "project", "node")
	if e == nil || out.Completeness != "unavailable" || out.Authority.CanRead {
		t.Fatal("asserted authority without caller transaction")
	}
}
