package projectcontracts

import (
	"encoding/json"
	"testing"
)

func TestApplicationBrowseDeclaration(t *testing.T) {
	for _, subpath := range []string{"notebooks", "", "../secret", "/etc", "a\\b"} {
		doc, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, resource := range doc.Resources {
			if resource.Application != nil {
				resource.Application.Data = map[ResourceKey]ApplicationData{"files": {BindingRef: "webdav.files", Browse: &ApplicationDataBrowse{Subpath: subpath}}}
			}
		}
		raw, _ := json.Marshal(doc)
		_, err = ParseProjectDeclaration(raw)
		if (err == nil) != (subpath == "" || subpath == "notebooks") {
			t.Fatalf("%q: %v", subpath, err)
		}
	}
}
