package box

import "testing"

func TestDeclarationMigrationBoxRequiresTransaction(t *testing.T) {
	out, err := ReadDeclarationMigrationBoxTx(t.Context(), nil, "project", "node")
	if err == nil || out.Completeness != "unavailable" || out.Revision != "" {
		t.Fatal("missing transaction asserted completeness")
	}
}
