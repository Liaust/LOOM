package redaction

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapRedactsSensitiveKeys(t *testing.T) {
	input := map[string]any{
		"api_token": "secret-value",
		"profile": map[string]any{
			"name":     "LOOM",
			"password": "hidden",
		},
		"items": []any{
			map[string]any{"private_key": "key", "label": "kept"},
		},
	}

	out := Map(input, DefaultProfile())
	if out["api_token"] != Replacement {
		t.Fatalf("api_token = %v, want redacted", out["api_token"])
	}
	profile := out["profile"].(map[string]any)
	if profile["password"] != Replacement {
		t.Fatalf("password = %v, want redacted", profile["password"])
	}
	if profile["name"] != "LOOM" {
		t.Fatalf("name = %v, want preserved", profile["name"])
	}
	items := out["items"].([]any)
	first := items[0].(map[string]any)
	if first["private_key"] != Replacement {
		t.Fatalf("private_key = %v, want redacted", first["private_key"])
	}
	if first["label"] != "kept" {
		t.Fatalf("label = %v, want preserved", first["label"])
	}
}

func TestJSONPreservesInvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid`)
	if string(JSON(raw, DefaultProfile())) != string(raw) {
		t.Fatal("invalid JSON should be returned unchanged")
	}
}

func TestStringRedactsWhenCategorized(t *testing.T) {
	if String("value") != "value" {
		t.Fatal("uncategorized string should be unchanged")
	}
	if String("value", Secret) != Replacement {
		t.Fatal("categorized string should be redacted")
	}
}

func TestTextRedactsSensitivePatterns(t *testing.T) {
	input := strings.Join([]string{
		"Authorization: Bearer abc123",
		"Cookie: session=hidden",
		"PrivateKey = abc123",
		"public line",
	}, "\n")
	out, report := Text(input, DefaultProfile())
	for _, leaked := range []string{"abc123", "session=hidden"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("sensitive value %q leaked in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "public line") {
		t.Fatalf("non-sensitive line was not preserved: %q", out)
	}
	if report.Redactions != 3 {
		t.Fatalf("redactions = %d, want 3; out=%q", report.Redactions, out)
	}
}

func TestTextRedactsDatabaseURLCredentials(t *testing.T) {
	out, report := Text("postgres://loom:secret@example/loom", DefaultProfile())
	if out != "postgres://[REDACTED]@example/loom" {
		t.Fatalf("database URL redaction mismatch: %q", out)
	}
	if report.Redactions != 1 {
		t.Fatalf("redactions = %d, want 1", report.Redactions)
	}
}

func TestJSONWithReportRedactsNestedKeysAndStrings(t *testing.T) {
	raw := json.RawMessage(`{
		"profile": {"name": "LOOM", "password": "hidden"},
		"events": [
			{"message": "Authorization: Bearer abc123"},
			{"message": "postgres://loom:secret@example/loom"}
		]
	}`)
	out, report := JSONWithReport(raw, DefaultProfile())
	if strings.Contains(string(out), "hidden") || strings.Contains(string(out), "abc123") || strings.Contains(string(out), "loom:secret") {
		t.Fatalf("sensitive value leaked in redacted JSON: %s", out)
	}
	if !strings.Contains(string(out), `"name":"LOOM"`) {
		t.Fatalf("non-sensitive JSON value was not preserved: %s", out)
	}
	if report.Redactions != 3 {
		t.Fatalf("redactions = %d, want 3; out=%s", report.Redactions, out)
	}
}

func TestTextPreservesNonSensitiveValues(t *testing.T) {
	input := "status=ok node=main path=/tmp/example"
	out, report := Text(input, DefaultProfile())
	if out != input {
		t.Fatalf("non-sensitive text changed: %q", out)
	}
	if report.Redactions != 0 {
		t.Fatalf("redactions = %d, want 0", report.Redactions)
	}
}
