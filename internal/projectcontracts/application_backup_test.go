package projectcontracts

import (
	"encoding/json"
	"testing"
)

func TestApplicationCloudHistoryDeclaration(t *testing.T) {
	for _, name := range []string{"valid", "unknown", "path", "policy_conflict", "unmanaged"} {
		t.Run(name, func(t *testing.T) {
			doc, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			for _, resource := range doc.Resources {
				app := resource.Application
				if app == nil {
					continue
				}
				app.ArtifactDescriptor = "deploy/artifact.json"
				data := ApplicationData{BindingRef: "webdav.files", Backup: "cloud_history"}
				switch name {
				case "unknown":
					data.Backup = "anything"
				case "path":
					data.Path, data.BindingRef = "data", ""
				case "policy_conflict":
					data.Protection = "retained"
				case "unmanaged":
					app.ArtifactDescriptor = ""
				}
				app.Data = map[ResourceKey]ApplicationData{"files": data}
			}
			raw, _ := json.Marshal(doc)
			_, err = ParseProjectDeclaration(raw)
			if (err == nil) != (name == "valid") {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}
