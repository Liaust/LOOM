package watchedroots

import "testing"

func TestDeclarationMigrationWatchedrootsRequiresTransaction(t *testing.T) {
	out, err := ReadDeclarationMigrationWatchedrootsTx(t.Context(), nil, "project", "node")
	if err == nil || out.Completeness != "unavailable" || out.Revision != "" {
		t.Fatal("missing transaction asserted completeness")
	}
}
