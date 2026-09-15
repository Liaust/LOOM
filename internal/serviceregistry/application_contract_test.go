package serviceregistry

import (
	"os"
	"strings"
	"testing"
)

func applicationManifestFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("application_testdata/webdav.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplicationContract(t *testing.T) {
	b := applicationManifestFixture(t)
	m, err := ParseApplicationManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Listener.Address != "127.0.0.1" || m.Config.Schema != "webdav.config.v1" {
		t.Fatalf("defaults/config: %+v", m)
	}
	cases := map[string]string{
		"unknown":         string(b) + "privileged: true\n",
		"nested-unknown":  strings.Replace(string(b), "  manager: systemd", "  manager: systemd\n  privileged: true", 1),
		"duplicate":       string(b) + "kind: loom.application\n",
		"secret":          strings.Replace(string(b), "  schema: webdav.config.v1", "  schema: webdav.config.v1\n  password: synthetic-secret", 1),
		"environment":     strings.Replace(string(b), "  manager: systemd", "  manager: systemd\n  environment: {TOKEN: synthetic-secret}", 1),
		"null":            strings.Replace(string(b), "boolean: false", "boolean: null", 1),
		"alias":           strings.Replace(string(b), "  schema: webdav.config.v1", "  schema: &p webdav.config.v1", 1),
		"trailing":        string(b) + "---\nkind: loom.application\n",
		"root-user":       strings.Replace(string(b), "user: webdav-app", "user: root", 1),
		"privileged-port": strings.Replace(string(b), "port: 8080", "port: 80", 1),
		"public-listener": strings.Replace(string(b), "  port: 8080", "  port: 8080\n  address: 0.0.0.0", 1),
		"unpinned":        strings.Replace(string(b), "sha256:"+strings.Repeat("a", 64), "latest", 1),
		"shell":           strings.Replace(string(b), "read_only: {type: boolean, boolean: false}", "read_only: {type: string, string: \"$(uname)\"}", 1),
		"unsafe-health":   strings.Replace(string(b), "path: /", "path: //evil.example/", 1),
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseApplicationManifest([]byte(source))
			if err == nil {
				t.Fatal("accepted invalid source")
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("secret in error")
			}
		})
	}
}

func TestApplicationContractAdversarial(t *testing.T) {
	cases := []string{
		`{"kind":"loom.application","\u006bind":"loom.application"}`,
		`{"artifact":{"ref":"synthetic-secret"},"unknown_secret":"synthetic-secret"}`,
	}
	for _, source := range cases {
		m, err := ParseApplicationManifest([]byte(source))
		if err == nil {
			t.Fatal("accepted malformed JSON")
		}
		if m.Artifact.Ref != "" {
			t.Fatal("rejected parser returned partial supplied data")
		}
	}
}

func TestApplicationContractParameterTypes(t *testing.T) {
	base := string(applicationManifestFixture(t))
	old := "read_only: {type: boolean, boolean: false}"
	for name, value := range map[string]string{
		"string":         "{type: string, string: ready}",
		"empty-string":   "{type: string, string: ''}",
		"integer-zero":   "{type: integer, integer: 0}",
		"integer-min":    "{type: integer, integer: -9223372036854775808}",
		"integer-max":    "{type: integer, integer: 9223372036854775807}",
		"boolean-false":  "{type: boolean, boolean: false}",
		"data-ref":       "{type: data_ref, data_ref: library}",
		"credential-ref": "{type: credential_ref, credential_ref: webdav.auth}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseApplicationManifest([]byte(strings.Replace(base, old, "read_only: "+value, 1))); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, value := range map[string]string{
		"wrong-boolean-tag":      "{type: boolean, boolean: 'false'}",
		"coerced-string":         "{type: string, string: 42}",
		"coerced-bool-string":    "{type: string, string: true}",
		"coerced-reference":      "{type: data_ref, data_ref: 42}",
		"mixed-union":            "{type: string, string: ready, integer: 1}",
		"no-branch":              "{type: string}",
		"wrong-branch":           "{type: string, boolean: false}",
		"unknown-branch":         "{type: secret, secret: synthetic-secret}",
		"inline-credential":      "{type: credential_ref, string: synthetic-secret}",
		"credential-value-field": "{type: credential_ref, credential_ref: webdav.auth, value: synthetic-secret}",
		"integer-overflow":       "{type: integer, integer: 9223372036854775808}",
		"floating-number":        "{type: integer, integer: 1.25}",
		"interpolation":          "{type: string, string: '${TOKEN}'}",
		"duplicate-type":         "{type: string, type: string, string: ready}",
		"null-branch":            "{type: string, string: null}",
	} {
		t.Run(name, func(t *testing.T) {
			m, err := ParseApplicationManifest([]byte(strings.Replace(base, old, "read_only: "+value, 1)))
			if err == nil {
				t.Fatal("malformed union accepted")
			}
			if m.Kind != "" || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("rejection retained supplied content")
			}
		})
	}
}
