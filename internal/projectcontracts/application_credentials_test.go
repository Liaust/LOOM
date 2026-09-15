package projectcontracts

import (
	"encoding/json"
	"testing"
)

func TestApplicationProtonReferences(t *testing.T) {
	for _, ref := range []string{"pass://share-id/item_ID/password", "pass://share/item/section.api_key", "pass://opaque-share==/opaque-item=/password"} {
		if _, _, _, ok := ParseApplicationProtonReference(ref); !ok {
			t.Fatal("valid reference refused", ref)
		}
	}
	for _, ref := range []string{"literal-password", "pass://share/item", "pass://share/item/password/extra", "pass://share name/item/password", "pass://share/item/totp", "pass://share/item/password?x=y", "pass://share/item/password\n", "pass://../item/password", "pass://share/item/%70assword"} {
		if _, _, _, ok := ParseApplicationProtonReference(ref); ok {
			t.Fatal("unsafe/ambiguous reference accepted", ref)
		}
	}
}

func TestApplicationCredentialGenerationSources(t *testing.T) {
	for _, source := range []string{"pass+generate://share-id/password", "pass+generate://opaque-share==/password"} {
		share, generated, ok := ApplicationCredentialSourceShare(source)
		if !ok || !generated || share == "" {
			t.Fatal("generation source refused", source)
		}
	}
	for _, source := range []string{"pass+generate://share/item/password", "pass+generate://share/token", "pass+generate://share=/password?x=y", "pass+generate://a=b/password", "pass+generate://share===/password", "pass://a=b/item/password"} {
		if _, _, ok := ApplicationCredentialSourceShare(source); ok {
			t.Fatal("invalid generation accepted", source)
		}
	}
}

func TestDeclarationCredentialSources(t *testing.T) {
	for _, name := range []string{"valid", "generated", "missing", "extra", "inline", "legacy"} {
		t.Run(name, func(t *testing.T) {
			doc, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var app *ApplicationDeclaration
			for _, r := range doc.Resources {
				if r.Application != nil {
					app = r.Application
					break
				}
			}
			if app == nil {
				t.Fatal("fixture has no application")
			}
			app.ArtifactDescriptor = ".loom/applications/artifact.json"
			app.Credentials = []string{"webdav.auth"}
			app.CredentialSources = map[string]string{"webdav.auth": "pass://share-id/item-id/password"}
			switch name {
			case "generated":
				app.CredentialSources["webdav.auth"] = "pass+generate://share-id/password"
			case "missing":
				app.Credentials = append(app.Credentials, "other.auth")
			case "extra":
				app.CredentialSources["other.auth"] = "pass://share-id/item-id/password"
			case "inline":
				app.CredentialSources["webdav.auth"] = "literal-secret"
			case "legacy":
				app.ArtifactDescriptor = ""
			}
			raw, _ := json.Marshal(doc)
			parsed, err := ParseProjectDeclaration(raw)
			if name == "valid" || name == "generated" {
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, _ := json.Marshal(parsed)
				if string(roundTrip) != string(raw) {
					t.Fatal("credential reference lost in source")
				}
			} else if err == nil {
				t.Fatal("invalid credential declaration accepted")
			}
		})
	}
}
