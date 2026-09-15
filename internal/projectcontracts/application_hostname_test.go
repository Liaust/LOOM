package projectcontracts

import (
	"encoding/json"
	"testing"
)

func TestApplicationHostname(t *testing.T) {
	for _, host := range []string{"webdav.apps.example.com", "files-2.apps.example.com"} {
		if !ValidApplicationHostname(host) {
			t.Fatal(host)
		}
	}
	for _, host := range []string{"https://webdav.apps.example.com", "*.apps.example.com", "apps.example.com.", "UPPER.apps.example.com", "-a.apps.example.com", "a..apps.example.com", "x.apps.example.com:8080", "x.apps.example.com\nimport x"} {
		if ValidApplicationHostname(host) {
			t.Fatal(host)
		}
	}
}

func TestApplicationHostnameDeclaration(t *testing.T) {
	d, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Resources["webdav"].Application
	a.ArtifactDescriptor = "deploy/artifact.json"
	a.Endpoint = &ApplicationEndpoint{Exposure: ApplicationPublicHTTPS, Hostname: "webdav.apps.example.com"}
	raw, _ := json.Marshal(d)
	if _, err := ParseProjectDeclaration(raw); err != nil {
		t.Fatal(err)
	}
	a.Endpoint.EndpointRef = "ambiguous.ref"
	raw, _ = json.Marshal(d)
	if _, err := ParseProjectDeclaration(raw); err == nil {
		t.Fatal("two endpoint authorities accepted")
	}
}
