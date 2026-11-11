package workspacenotes

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/workspacefiles"
)

func TestCatalogInputBoundsAndExactFilters(t *testing.T) {
	in, err := ParseCatalogInput(url.Values{"action": {"catalog"}, "root": {"box"}, "path": {"Notes"}, "query": {"Exact Alias"}, "tag": {"#Physics"}, "type": {"Concept"}, "limit": {"200"}, "after": {"workspace_note_abc123"}})
	if err != nil || in.Limit != 200 || in.After != "workspace_note_abc123" || in.Query != "Exact Alias" || in.Tag != "#Physics" || in.Type != "Concept" {
		t.Fatal(in, err)
	}
	defaults, err := ParseCatalogInput(url.Values{"root": {"box"}, "path": {"."}})
	if err != nil || defaults.Limit != 100 || defaults.Path != "" {
		t.Fatal(defaults, err)
	}
	for _, fragment := range []string{
		"limit=0", "limit=-1", "limit=201", "limit=no", "limit=", "limit=1&limit=2", "root=storage", "path=../private", "path=/etc", "path=a/../b", "path=a//b", "path=a%5Cb", "path=a%00b", "path=%FF", "after=../private", "after=workspace_note_", "after=workspace_note_a%2Fb", "after=workspace_note_a%00b", "query=%00", "query=%FF", "sort=path", "tag=a&tag=b", "type=" + strings.Repeat("x", 201),
	} {
		q, err := url.ParseQuery("root=box&" + fragment)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseCatalogInput(q); err == nil {
			t.Errorf("accepted %q", fragment)
		}
	}
	if _, err := ParseCatalogInput(url.Values{}); err == nil {
		t.Fatal("accepted missing root")
	}
}

func TestCatalogQueryConfinesScopeEscapesPrefixesAndBindsFilters(t *testing.T) {
	in := CatalogInput{Root: "box", Path: "Notes_100%", Query: "Name' OR true --", Tag: "#Physics", Type: "Concept", Limit: 200}
	query, args := catalogQuery(in, "Notes_100%/last.md")
	want := []any{"box", "Notes_100%", `Notes\_100\%/%`, "Notes_100%/last.md", "Name' OR true --", "name' or true --", "physics", metadataVersion, "Concept", 201}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	for _, fragment := range []string{
		"d.root_ref=$1 AND NOT d.retired", "(d.path=$2 OR d.path LIKE $3)", "d.path>$4", "n.kind='link' AND n.normalized=$6", "n.kind='tag' AND n.normalized=$7", "d.metadata_version>=$8", "jsonb_typeof(d.property_values->'type')='string'", "d.property_values->>'type'=$9", "ORDER BY d.path LIMIT $10", "source_workspace_document_id=d.document_id", "d.observed_at IS NULL AND source_knowledge_object_id=d.knowledge_object_id", "LIMIT 501",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("missing query guard %q", fragment)
		}
	}
	if strings.Contains(query, in.Query) || strings.Contains(query, in.Path) {
		t.Fatal("input interpolated into SQL")
	}
	for _, tc := range []struct {
		prefix, path string
		want         bool
	}{
		{"", "Notes/a.md", true}, {"Notes", "Notes/a.md", true}, {"Notes", "Notes", true}, {"Notes", "Notes-old/a.md", false}, {"Notes", "Elsewhere/a.md", false},
	} {
		if catalogWithin(tc.prefix, tc.path) != tc.want {
			t.Fatal(tc)
		}
	}
}

func TestCompactCatalogOmitsReferenceJoinButKeepsBoundedScope(t *testing.T) {
	in, err := ParseCatalogInput(url.Values{"root": {"box"}, "compact": {"true"}, "path": {"Notes"}, "limit": {"200"}})
	if err != nil || !in.Compact {
		t.Fatal(in, err)
	}
	query, args := catalogQuery(in, "Notes/A.md")
	if strings.Contains(query, "object_links") || !strings.Contains(query, "NOT d.retired") || !strings.Contains(query, "ORDER BY d.path LIMIT") || len(args) != 5 {
		t.Fatal(query, args)
	}
	if _, err = ParseCatalogInput(url.Values{"root": {"box"}, "compact": {"maybe"}}); err == nil {
		t.Fatal("accepted invalid compact flag")
	}
}

func TestCatalogUsesFilesScopeBeforeAnyDatabaseRead(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: root}}}
	t.Cleanup(func() { _ = files.Close() })
	s := Service{Files: files} // No database: each request must fail at the Files fence.
	for _, tc := range []struct {
		root, path string
		want       error
	}{
		{"unknown", "", workspacefiles.ErrUnknownRoot}, {"box", "../outside", workspacefiles.ErrInvalidPath}, {"box", ".loom/state", fs.ErrPermission}, {"box", "escape", fs.ErrPermission}, {"box", "missing", fs.ErrNotExist},
	} {
		_, err := s.Catalog(t.Context(), CatalogInput{Root: tc.root, Path: tc.path})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Catalog(ctx, CatalogInput{Root: "box"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestMetadataVersionRequiresLegacyRowsToBeObservedAgain(t *testing.T) {
	now := time.Now()
	d := Document{Revision: "same", ObservedAt: &now}
	if d.current("same") {
		t.Fatal("old observed row bypassed value parsing at unchanged revision")
	}
	d.MetadataVersion = metadataVersion
	if !d.current("same") || d.current("different") {
		t.Fatal("revision fence failed")
	}
	d.ObservedAt = nil
	if d.current("same") {
		t.Fatal("indexed hint treated as exact observation")
	}
}

func TestIndexedSeedPropertiesPreserveSafeValuesAndNumberPrecision(t *testing.T) {
	var data map[string]any
	if err := decodePropertyValues([]byte(`{"type":"concept","count":9007199254740993,"tags":["#physics"],"aliases":["Name"],"related":["[[Other]]"],"nothing":null,"nested":{"ok":true}}`), &data); err != nil {
		t.Fatal(err)
	}
	m := propertyMetadata(data)
	if m.Values["count"] != json.Number("9007199254740993") || m.Values["type"] != "concept" || !reflect.DeepEqual(m.Tags, []string{"physics"}) || !reflect.DeepEqual(m.Links, []Reference{{Kind: "wikilink", Target: "Other"}}) {
		t.Fatal(m)
	}
	if raw, err := json.Marshal(m.Values); err != nil || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal(string(raw), err)
	}
}
