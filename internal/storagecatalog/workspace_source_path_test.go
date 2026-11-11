package storagecatalog

import "testing"

func TestWorkspaceCatalogPreservesLiteralFilesystemNames(t *testing.T) {
	for _, value := range []string{"imports/batch/research ", "imports/batch/ leading", "Projects/normal"} {
		if !validWorkspaceSourceRelative(value) {
			t.Fatalf("valid literal name rejected: %q", value)
		}
	}
	for _, value := range []string{".", "../other", "imports/../other", "/absolute", "imports//other", "imports\\other", "imports/\x00"} {
		if validWorkspaceSourceRelative(value) {
			t.Fatalf("unsafe path accepted: %q", value)
		}
	}
}
