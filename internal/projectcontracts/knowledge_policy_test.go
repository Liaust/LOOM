package projectcontracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgeDeclarationPolicy(t *testing.T) {
	base := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", "resources:\n  reading:\n    kind: knowledge\n    knowledge:\n      path: journal\n      category: notes\n", 1)
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"legacy", "", true},
		{"defaults", "      refresh: {}\n", true},
		{"settings", "      refresh: {quiet_for: 10m, max_wait: 30m}\n      processing: {ocr: auto, embeddings: false, image_descriptions: true}\n", true},
		{"zero quiet", "      refresh: {quiet_for: 0s, max_wait: 1s}\n", true},
		{"short maximum", "      refresh: {quiet_for: 10m, max_wait: 5m}\n", false},
		{"fraction", "      refresh: {quiet_for: 0.5s}\n", false},
		{"negative", "      refresh: {quiet_for: -1s}\n", false},
		{"too long", "      refresh: {max_wait: 25h}\n", false},
		{"overflow", "      refresh: {max_wait: 999999999999999999h}\n", false},
		{"unknown", "      processing: {model: arbitrary}\n", false},
		{"ocr typo", "      processing: {ocr: force}\n", false},
		{"string bool", "      processing: {embeddings: 'false'}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseProjectDeclaration([]byte(base + tc.fields))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if !tc.valid {
				if tc.name == "short maximum" && !strings.Contains(err.Error(), "resources.reading.knowledge.refresh.max_wait") {
					t.Fatalf("missing actionable policy field: %v", err)
				}
				return
			}
			policy, err := NormalizeKnowledgeSourcePolicy(*d.Resources["reading"].Knowledge)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "legacy" && policy != nil {
				t.Fatal("legacy policy encoding changed")
			}
			if tc.name == "defaults" && (policy.Refresh.QuietForSeconds != 600 || policy.Refresh.MaxWaitSeconds != 1800) {
				t.Fatalf("defaults: %+v", policy)
			}
			if tc.name == "settings" && (policy.Processing.Embeddings == nil || *policy.Processing.Embeddings) {
				t.Fatal("explicit false lost")
			}
		})
	}
}

func TestKnowledgePolicyCompilerEvidence(t *testing.T) {
	root := declarationEnrollmentFixture(t, "  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes, refresh: {quiet_for: 600s}, processing: {embeddings: false}}\n", "")
	a := Analyze(root)
	if !a.Report.OK || len(a.Report.WatchedRoots) != 1 {
		t.Fatalf("analysis: %+v", a.Report.Diagnostics)
	}
	source := a.Report.WatchedRoots[0].Metadata["knowledge_source"].(map[string]any)
	raw, _ := json.Marshal(source["policy"])
	p, err := DecodeKnowledgeSourcePolicy(raw)
	if err != nil || p.Refresh.QuietForSeconds != 600 || p.Refresh.MaxWaitSeconds != 1800 || p.Processing.Embeddings == nil || *p.Processing.Embeddings {
		t.Fatalf("policy=%s err=%v", raw, err)
	}
}

func TestKnowledgePolicyNormalizedEvidence(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{"refresh":null}`, `{"processing":null}`,
		`{"refresh":{"max_wait_seconds":1800}}`,
		`{"refresh":{"quiet_for_seconds":null,"max_wait_seconds":1800}}`,
		`{"refresh":{"quiet_for_seconds":0.5,"max_wait_seconds":1800}}`,
		`{"refresh":{"quiet_for_seconds":0,"max_wait_seconds":86401}}`,
		`{"processing":{"model":"other"}}`,
		`{"processing":{},"processing":{}}`,
	} {
		if _, err := DecodeKnowledgeSourcePolicy([]byte(raw)); err == nil {
			t.Errorf("accepted malformed policy %s", raw)
		}
	}
}

func TestKnowledgePoliciesRemainPerFolder(t *testing.T) {
	root := declarationEnrollmentFixture(t, "  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes, refresh: {quiet_for: 1m, max_wait: 3m}, processing: {embeddings: false}}\n  published:\n    kind: knowledge\n    knowledge: {path: notes, category: research, refresh: {quiet_for: 5m, max_wait: 15m}, processing: {embeddings: true}}\n", "")
	a := Analyze(root)
	if !a.Report.OK || len(a.Report.WatchedRoots) != 2 {
		t.Fatalf("analysis: %+v", a.Report.Diagnostics)
	}
	for _, item := range a.Report.WatchedRoots {
		raw, _ := json.Marshal(item.Metadata["knowledge_source"].(map[string]any)["policy"])
		p, err := DecodeKnowledgeSourcePolicy(raw)
		if err != nil || p.Refresh == nil || p.Processing == nil || p.Processing.Embeddings == nil {
			t.Fatalf("%s: %s %v", item.Key, raw, err)
		}
		wantQuiet, wantMaximum, wantEmbeddings := int64(60), int64(180), false
		if item.Key == "published" {
			wantQuiet, wantMaximum, wantEmbeddings = 300, 900, true
		}
		if p.Refresh.QuietForSeconds != wantQuiet || p.Refresh.MaxWaitSeconds != wantMaximum || *p.Processing.Embeddings != wantEmbeddings {
			t.Fatalf("cross-folder policy leak at %s: %s", item.Key, raw)
		}
	}
}
