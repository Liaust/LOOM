package filepolicy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFolderPolicyInheritanceAndCanonicalOrder(t *testing.T) {
	policy, err := DecodeFolderPolicy([]byte(`{"rules":[
	 {"path":"Notes","enrichment":["vision","ocr","embeddings","ocr"]},
	 {"path":"Notes/Books","enrichment":[]},
	 {"path":"Notes/Books/reviewed","enrichment":["embeddings"]},
	 {"path":"Notes/Books/reviewed/child"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want *[]string
	}{
		{"Topics/ordinary.md", nil},
		{"Notes/ordinary.md", ptrStages("embeddings", "ocr", "vision")},
		{"Notes/Books/new/deep/paper.pdf", ptrStages()},
		{"Notes/Bookshelf/note.md", ptrStages("embeddings", "ocr", "vision")},
		{"Notes/Books/reviewed/child/note.md", ptrStages("embeddings")},
	} {
		got, err := policy.ResolveEnrichment(tc.path)
		if err != nil || !reflect.DeepEqual(got.Enrichment, tc.want) {
			t.Fatalf("%s: %+v want %+v, err=%v", tc.path, got, tc.want, err)
		}
	}
	before, _ := json.Marshal(policy)
	for i, j := 0, len(policy.Rules)-1; i < j; i, j = i+1, j-1 {
		policy.Rules[i], policy.Rules[j] = policy.Rules[j], policy.Rules[i]
	}
	normalized, err := NormalizeFolderPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(normalized)
	if string(before) != string(after) || !strings.Contains(string(after), `"enrichment":[]`) {
		t.Fatalf("unstable rule encoding: %s vs %s", before, after)
	}
}

func TestFolderPolicyGlobConflictsAreOrderIndependent(t *testing.T) {
	policy, err := DecodeFolderPolicy([]byte(`{"rules":[
	 {"path":"Notes/*/paper.pdf","enrichment":[]},
	 {"path":"Notes/books/*.pdf","enrichment":["ocr"]},
	 {"path":"Notes/b?oks/paper.pdf","enrichment":["vision"]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	// Two broad patterns overlap, but the literal-prefix books rule wins.
	got, err := policy.ResolveEnrichment("Notes/books/paper.pdf")
	if err != nil || !reflect.DeepEqual(got.Enrichment, ptrStages("ocr")) {
		t.Fatalf("more specific rule did not win: %+v %v", got, err)
	}
	policy.Rules = []FolderRule{
		{Path: "Notes/*/paper.pdf", Enrichment: ptrStages()},
		{Path: "Notes/b?oks/paper.pdf", Enrichment: ptrStages("vision")},
	}
	for range 2 {
		if _, err := policy.ResolveEnrichment("Notes/books/paper.pdf"); err == nil || !strings.Contains(err.Error(), "equal-specificity") {
			t.Fatalf("expected named ambiguity, got %v", err)
		}
		policy.Rules[0], policy.Rules[1] = policy.Rules[1], policy.Rules[0]
	}
}

func TestFolderPolicyRejectsUnsupportedAndAmbiguousInput(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"rules":null}`, `{"rules":[null]}`,
		`{"Rules":[]}`, `{"rules":[{"Path":"Notes","enrichment":[]}]}`,
		`{"rules":[{"path":"Notes","Enrichment":null}]}`,
		`{"rules":[{"path":"Notes","enrichment":[],"Enrichment":["ocr"]}]}`,
		`{"rules":[{"path":true}]}`,
		`{"rules":[{"path":"../outside","enrichment":[]}]}`,
		`{"rules":[{"path":"/outside","enrichment":[]}]}`,
		`{"rules":[{"path":"Notes//Books","enrichment":[]}]}`,
		`{"rules":[{"path":"Notes/[broken","enrichment":[]}]}`,
		`{"rules":[{"path":"Notes","enrichment":null}]}`,
		`{"rules":[{"path":"Notes","enrichment":["paid-model"]}]}`,
		`{"rules":[{"path":"Notes","protect":null}]}`,
		`{"rules":[{"path":"Notes","search":"full"}]}`,
		`{"rules":[{"path":"Notes","enrichment":[]},{"path":"Notes","enrichment":["ocr"]}]}`,
		`{"rules":[{"path":"Notes","enrichment":[]},{"path":"Notes"},{"path":"Notes","enrichment":["ocr"]}]}`,
	} {
		if _, err := DecodeFolderPolicy([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestFolderPolicyFieldsInheritIndependently(t *testing.T) {
	policy, err := DecodeFolderPolicy([]byte(`{"rules":[
	 {"path":"**","search":"metadata","protect":true,"enrichment":[]},
	 {"path":"Notes","search":"text","enrichment":["embeddings"]},
	 {"path":"Notes/private","search":"none"},
	 {"path":"Notes/private","protect":false}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := policy.Resolve("Notes/private/draft.md")
	if err != nil || got.Search != "none" || got.Protect == nil || *got.Protect || !reflect.DeepEqual(got.Enrichment, ptrStages("embeddings")) {
		t.Fatalf("independent inheritance: %+v %v", got, err)
	}
}

func TestWorkspaceFolderDefaultsAvoidAutomaticBinaryEnrichment(t *testing.T) {
	policy, err := WorkspaceFolderDefaults(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, search string
		stages       *[]string
	}{
		{"notes/idea.md", "text", ptrStages("embeddings")},
		{"notes/large.pdf", "metadata", ptrStages()},
		{"assets/photo.png", "metadata", ptrStages()},
		{"repos/source/.git/objects/abc", "none", ptrStages()},
	} {
		got, err := policy.Resolve(tc.path)
		if err != nil || got.Search != tc.search || !reflect.DeepEqual(got.Enrichment, tc.stages) {
			t.Fatalf("%s: %+v %v", tc.path, got, err)
		}
	}
}

func ptrStages(stages ...string) *[]string {
	value := append([]string{}, stages...)
	return &value
}
