package projectapply

import (
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
)

func TestDeclarationReaderPrerequisiteRootVersions(t *testing.T) {
	for _, version := range []string{pc.ProjectSchemaV05, pc.ProjectSchemaV06, pc.ProjectSchemaV04, "project.contract.v0.7"} {
		t.Run(version, func(t *testing.T) {
			basis := testResolution(testPrincipal(), 0).Plan.Basis
			basis.Sources[0].SchemaVersion = version
			err := validPrerequisites(prerequisites(basis))
			want := version == pc.ProjectSchemaV05 || version == pc.ProjectSchemaV06
			if (err == nil) != want {
				t.Fatalf("supported=%t err=%v", want, err)
			}
		})
	}
	for _, mode := range []string{"duplicate", "noncanonical_path"} {
		t.Run(mode, func(t *testing.T) {
			basis := testResolution(testPrincipal(), 0).Plan.Basis
			basis.Sources[0].SchemaVersion = pc.ProjectSchemaV06
			if mode == "duplicate" {
				basis.Sources = append(basis.Sources, basis.Sources[0])
				basis.Sources[1].SchemaVersion = pc.ProjectSchemaV05
			} else {
				basis.Sources[0].Ref = "project.yaml"
			}
			if err := validPrerequisites(prerequisites(basis)); err == nil {
				t.Fatal("invalid root source accepted")
			}
		})
	}
}
