package capabilities

import "testing"

func TestDeclarationMigrationCapabilitiesRequiresTransaction(t *testing.T) {
	out, err := ReadDeclarationMigrationCapabilitiesTx(t.Context(), nil, "project", "node")
	if err == nil || out.Completeness != "unavailable" || out.Revision != "" {
		t.Fatal("missing transaction asserted completeness")
	}
}
