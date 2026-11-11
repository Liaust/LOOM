package notesworkspacesync

import (
	pc "loom.local/loom/internal/projectcontracts"
	"testing"
)

func TestPlacementPreviewUsesDeclaredSourceVersion(t *testing.T) {
	for _, tc := range []struct{ version, path, want string }{
		{pc.ProjectSchemaV06, ".", "Projects/Workspace"},
		{pc.ProjectSchemaV06, "notes", "Projects/Workspace/notes"},
		{pc.ProjectSchemaV05, "notes", "Projects/Workspace/Notes"},
	} {
		got, err := declarationDisplayRoot(pc.ProjectDeclaration{SchemaVersion: tc.version}, "project", tc.path, "Workspace")
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %s %v", tc, got, err)
		}
	}
}
