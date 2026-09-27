package projectcontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeApplicationDataDeclaration(t *testing.T) {
	for _, tc := range []string{"subfolder", "whole_allocation", "both", "missing_application", "missing_data", "traversal", "absolute", "protection"} {
		t.Run(tc, func(t *testing.T) {
			root := declarationWebDAVRoot(t)
			d, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			delete(d.Resources, "retained")
			app := d.Resources["webdav"].Application
			app.Data["library"] = ApplicationData{BindingRef: "webdav.library"}
			k := d.Resources["reading"].Knowledge
			k.Path, k.Protection = "", ""
			k.ApplicationData = &KnowledgeApplicationData{Application: "webdav", Data: "library", Subpath: "papers"}
			switch tc {
			case "whole_allocation":
				k.ApplicationData.Subpath = ""
			case "both":
				k.Path = "notes"
			case "missing_application":
				k.ApplicationData.Application = "other"
			case "missing_data":
				k.ApplicationData.Data = "other"
			case "traversal":
				k.ApplicationData.Subpath = "../private"
			case "absolute":
				k.ApplicationData.Subpath = "/private"
			case "protection":
				k.Protection = "retained"
			}
			raw, _ := json.Marshal(d)
			if err := os.WriteFile(filepath.Join(root, CanonicalRootContractPath), raw, 0600); err != nil {
				t.Fatal(err)
			}
			a := Analyze(root)
			valid := tc == "subfolder" || tc == "whole_allocation"
			if a.Report.OK != valid {
				t.Fatalf("valid=%v: %+v", valid, a.Report.Diagnostics)
			}
			if !valid {
				return
			}
			if len(a.Plan.WatchedRoots) != 1 || a.Plan.WatchedRoots[0].RootRelativePath != "application-data/reading" || a.Plan.WatchedRoots[0].BackupMode != "none" {
				t.Fatalf("unexpected enrollment: %+v", a.Plan.WatchedRoots)
			}
			if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "application-data")); !os.IsNotExist(err) {
				t.Fatal("compiler needs or creates synthetic source directory")
			}
		})
	}
}
