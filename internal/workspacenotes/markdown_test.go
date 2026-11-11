package workspacenotes

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMarkdownPropertyValuesAreTypedJSONAndKeepPropertyNames(t *testing.T) {
	m := Parse([]byte(`---
Display Name: Example
type: concept
published: true
count: 7
ratio: 2.5
date: 2026-11-03
nothing: null
list: [one, 2, false, null]
nested: {rating: 4, person: Alice}
unsafe: .nan
infinity: .inf
nonstring: {1: invalid key}
binary: !!binary AA==
---
Body
`))
	want := map[string]any{
		"Display Name": "Example", "type": "concept", "published": true, "count": 7, "ratio": 2.5, "date": "2026-11-03T00:00:00Z", "nothing": nil,
		"list": []any{"one", 2, false, nil}, "nested": map[string]any{"rating": 4, "person": "Alice"},
	}
	if !reflect.DeepEqual(m.Values, want) {
		t.Fatalf("values = %#v, want %#v", m.Values, want)
	}
	for _, key := range []string{"Display Name", "unsafe", "infinity", "nonstring", "binary"} {
		if !slices.Contains(m.Properties, key) {
			t.Fatalf("property name %q lost: %#v", key, m.Properties)
		}
	}
	if _, err := json.Marshal(m); err != nil {
		t.Fatal(err)
	}
}

func TestMarkdownPropertyValuesBoundDepthSizeAndUnsafeValues(t *testing.T) {
	var deep any = "leaf"
	for range 20 {
		deep = []any{deep}
	}
	m := propertyMetadata(map[string]any{"a": strings.Repeat("x", 40000), "b": strings.Repeat("y", 40000), "deep": deep, "oversized": strings.Repeat("z", 70000), "nul": "bad\x00value"})
	if len(m.Properties) != 5 || len(m.Values) != 1 || m.Values["a"] != strings.Repeat("x", 40000) {
		t.Fatal("unsafe or unbounded values retained", m.Properties, len(m.Values))
	}
	for _, source := range []string{"Body only", "---\nself: &value [*value]\n---\nBody", "---\nbad: [\n---\nBody"} {
		m := Parse([]byte(source))
		if m.Values == nil || len(m.Values) != 0 {
			t.Fatalf("invalid/absent frontmatter produced values: %#v", m)
		}
	}
}

func TestMarkdownMetadataIncludesPropertiesAliasesTagsAndOnlyRealReferences(t *testing.T) {
	content := []byte("---\naliases: [Alias]\ntags: [frontmatter]\nrelated: ['[[Previous]]']\n---\n# Heading\n\n[[Target|label]] and [paper](paper.pdf) #physics/qm\n\n`[[literal]] #literal`\n\n```python\n[[also literal]] #not-tag\n```\n\\[[escaped]] \\#escaped\n")
	metadata := Parse(content)
	if !reflect.DeepEqual(metadata.Aliases, []string{"Alias"}) {
		t.Fatal(metadata)
	}
	if !reflect.DeepEqual(metadata.Tags, []string{"frontmatter", "physics/qm"}) {
		t.Fatal(metadata.Tags)
	}
	expected := map[string]bool{"Previous": true, "Target": true, "paper.pdf": true}
	for _, link := range metadata.Links {
		if !expected[link.Target] {
			t.Fatalf("unexpected link %#v", link)
		}
		delete(expected, link.Target)
	}
	if len(expected) > 0 {
		t.Fatal("missing", expected, metadata)
	}
}
func TestReferencesKeepDottedTitlesAndRejectSchemes(t *testing.T) {
	if newDocumentID() == newDocumentID() {
		t.Fatal("new document identities collided")
	}
	target, heading, err := referenceTarget("[[Idea v1.2#Section|label]]")
	if err != nil || target != "Idea v1.2" || heading != "Section" {
		t.Fatal(target, heading, err)
	}
	if _, _, err = referenceTarget("javascript:alert(1)"); err == nil {
		t.Fatal("scheme accepted")
	}
	if documentID("box", "one.md") == documentID("storage", "one.md") {
		t.Fatal("root collision")
	}
}
