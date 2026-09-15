package communication

import (
	"strings"
	"testing"
)

func TestConfigHashIsDeterministic(t *testing.T) {
	left, err := ConfigHash(map[string]any{"node": "main", "roots": []any{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := ConfigHash(map[string]any{"roots": []any{"a", "b"}, "node": "main"})
	if err != nil {
		t.Fatal(err)
	}
	if left != right || !strings.HasPrefix(left, "sha256:") {
		t.Fatalf("hashes = %q and %q, want equal sha256 hashes", left, right)
	}
}

func TestCompareRevision(t *testing.T) {
	hashA, _ := ConfigHash(map[string]any{"root": "a"})
	hashB, _ := ConfigHash(map[string]any{"root": "b"})
	tests := []struct {
		name    string
		applied int64
		hash    string
		desired int64
		want    string
	}{
		{name: "newer", applied: 1, hash: hashA, desired: 2, want: RevisionDecisionApply},
		{name: "equal", applied: 2, hash: hashA, desired: 2, want: RevisionDecisionIdempotent},
		{name: "stale", applied: 3, hash: hashA, desired: 2, want: RevisionDecisionStale},
		{name: "conflict", applied: 2, hash: hashB, desired: 2, want: RevisionDecisionConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CompareRevision(test.applied, test.hash, NewDesiredStateEvidence("main", test.desired, hashA))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("decision = %q, want %q", got, test.want)
			}
		})
	}
}

func TestControlEvidenceValidationAndStrictJSON(t *testing.T) {
	hash, _ := ConfigHash(map[string]any{"root": "a"})
	evidence := NewDesiredStateEvidence("main", 1, hash)
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []DesiredStateEvidence{
		{},
		{SchemaVersion: ControlEvidenceSchemaVersion, TargetNode: "main", DesiredRevision: 1, ConfigHash: "sha256:nope"},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("expected validation failure for %#v", invalid)
		}
	}
	var decoded DesiredStateEvidence
	payload := `{"schema_version":"loom.control.evidence.v1","target_node":"main","desired_revision":1,"config_hash":"` + hash + `"}`
	if err := DecodeStrictJSONObject([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if err := DecodeStrictJSONObject([]byte(strings.TrimSuffix(payload, "}")+`,"unknown":true}`), &decoded); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}
