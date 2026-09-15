package knowledge

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
)

//go:embed testdata/archive_lifecycle/corpus.json
var notesArchiveCorpus []byte

//go:embed testdata/archive_lifecycle/queries.json
var notesArchiveQueries []byte

type notesArchiveFixture struct {
	ID, Root, Kind, Topic, Collection, Project string
	Lifecycle                                  SourceLifecycle
	Processing                                 string
	OriginalPath                               string `json:"original_path"`
	CanonicalPath                              string `json:"canonical_path"`
	ArchiveOperation                           string `json:"archive_operation"`
	RestoreOperation                           string `json:"restore_operation"`
	Readable                                   bool
	Version                                    string
	Chunks, Embeddings                         []string
}

func decodeNotesArchiveFixture(t *testing.T, raw []byte, target any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		t.Fatal(err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		t.Fatal("fixture has trailing data", err)
	}
}

func TestNotesArchiveLifecycleContract(t *testing.T) {
	var corpus struct {
		SchemaVersion          string `json:"schema_version"`
		IdentityBinding        string `json:"identity_binding"`
		Objects                []notesArchiveFixture
		IdentityInvariants     []string `json:"identity_invariants"`
		PathOnlyProcessingJobs int      `json:"path_only_processing_jobs"`
	}
	var queries struct {
		SchemaVersion string `json:"schema_version"`
		Order         string
		Queries       []struct {
			Name             string
			Filter           SourceLifecycleFilter
			Active, Archived []string
			Omitted          int `json:"archived_matches_omitted"`
		}
	}
	decodeNotesArchiveFixture(t, notesArchiveCorpus, &corpus)
	decodeNotesArchiveFixture(t, notesArchiveQueries, &queries)
	if corpus.SchemaVersion != "loom.notes_archive.corpus.v1" || corpus.IdentityBinding != "fixture_keys_are_not_runtime_ids" || len(corpus.Objects) != 9 ||
		queries.SchemaVersion != "loom.notes_archive.queries.v1" || queries.Order != "object_id_asc_within_lifecycle" || len(queries.Queries) != 4 || corpus.PathOnlyProcessingJobs != 0 {
		t.Fatal("frozen archive fixture envelope changed")
	}
	if !reflect.DeepEqual(corpus.IdentityInvariants, []string{"root", "id", "version", "chunks", "embeddings", "original_path"}) {
		t.Fatal("stable identity fields changed")
	}
	seen := map[string]bool{}
	byID := map[string]notesArchiveFixture{}
	for _, object := range corpus.Objects {
		if object.ID == "" || seen[object.ID] || object.Root == "" || object.Version == "" || object.OriginalPath == "" || object.CanonicalPath == "" || object.Chunks == nil || object.Embeddings == nil {
			t.Fatal("fixture has missing/duplicate identity or content evidence")
		}
		seen[object.ID], byID[object.ID] = true, object
		if _, err := SourceLifecycleIncluded(SourceLifecycleFilterAll, object.Lifecycle); err != nil {
			t.Fatal(err)
		}
		if object.Lifecycle == SourceLifecycleArchived && (object.ArchiveOperation == "" || object.CanonicalPath == object.OriginalPath) {
			t.Fatal("archive is not attributed to moved custody")
		}
		if object.Lifecycle == SourceLifecycleActive && object.CanonicalPath != object.OriginalPath {
			t.Fatal("active source has stale archive custody")
		}
		if object.RestoreOperation != "" && (object.ArchiveOperation == "" || object.Lifecycle != SourceLifecycleActive) {
			t.Fatal("restored source lost archive chain")
		}
	}
	if byID["active-neighbour"].Root != byID["archived-topic"].Root || byID["active-neighbour"].Lifecycle == byID["archived-topic"].Lifecycle {
		t.Fatal("shared-root mixed lifecycle case is absent")
	}
	if byID["archived-failed"].Processing != "failed" || byID["archived-metadata"].Processing != "metadata_only" || byID["restored-topic"].RestoreOperation == "" {
		t.Fatal("independent processing/restore cases absent")
	}
	for _, query := range queries.Queries {
		t.Run(query.Name, func(t *testing.T) {
			active, archived, omitted := []string{}, []string{}, 0
			filter, err := NormalizeSourceLifecycleFilter(query.Filter)
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range corpus.Objects {
				if !object.Readable {
					continue
				}
				included, err := SourceLifecycleIncluded(filter, object.Lifecycle)
				if err != nil {
					t.Fatal(err)
				}
				if !included {
					if filter == SourceLifecycleFilterActive && object.Lifecycle == SourceLifecycleArchived {
						omitted++
					}
					continue
				}
				if object.Lifecycle == SourceLifecycleActive {
					active = append(active, object.ID)
				} else {
					archived = append(archived, object.ID)
				}
			}
			sort.Strings(active)
			sort.Strings(archived)
			if !reflect.DeepEqual(active, query.Active) || !reflect.DeepEqual(archived, query.Archived) || omitted != query.Omitted {
				t.Fatalf("groups/omitted differ: %v %v %d", active, archived, omitted)
			}
		})
	}
}

func TestSourceRootArchiveLifecycleFilterClosed(t *testing.T) {
	for _, value := range []SourceLifecycleFilter{"Active", " active", "deleted", "indexed", "*"} {
		if _, err := NormalizeSourceLifecycleFilter(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []SourceLifecycle{"", "failed", "metadata_only", "disabled", "deleted"} {
		if included, err := SourceLifecycleIncluded(SourceLifecycleFilterAll, value); err == nil || included {
			t.Fatalf("accepted %q", value)
		}
	}
}

func validNotesCustodyTransition() NotesCustodyTransition {
	transition := NotesCustodyTransition{
		KnowledgeObjectID:         ids.NewKnowledgeObjectID(),
		WorkspaceLifecycleEventID: "workspace_lifecycle_event_01K41SEARCH000000000000001",
		ArchiveOperationID:        "workspace_archive_operation_01K41SEARCH000000000000002",
		OriginalPath:              "/fixture/box/Topics/example/note.md", CanonicalPath: "/fixture/storage/archive/topics/example/content/note.md",
		WorkspaceRelativePath: "note.md", ManifestDigest: "sha256:" + strings.Repeat("a", 64),
	}
	transition.TransitionDigest = NotesCustodyTransitionDigest(transition)
	return transition
}

func TestNotesArchiveLifecycleTransitionContract(t *testing.T) {
	base := validNotesCustodyTransition()
	if err := ValidateNotesCustodyTransition(base); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*NotesCustodyTransition){
		"object": func(x *NotesCustodyTransition) { x.KnowledgeObjectID = ids.NewKnowledgeObjectID() },
		"event": func(x *NotesCustodyTransition) {
			x.WorkspaceLifecycleEventID = "workspace_lifecycle_event_01K41SEARCH000000000000003"
		},
		"archive": func(x *NotesCustodyTransition) {
			x.ArchiveOperationID = "workspace_archive_operation_01K41SEARCH000000000000004"
		},
		"project": func(x *NotesCustodyTransition) { x.ProjectEventID = ids.NewEventID() },
		"previous": func(x *NotesCustodyTransition) {
			x.PreviousEventID = "workspace_lifecycle_event_01K41SEARCH000000000000005"
		},
		"original":  func(x *NotesCustodyTransition) { x.OriginalPath += "x" },
		"canonical": func(x *NotesCustodyTransition) { x.CanonicalPath += "x" },
		"relative":  func(x *NotesCustodyTransition) { x.WorkspaceRelativePath = "other.md" },
		"manifest":  func(x *NotesCustodyTransition) { x.ManifestDigest = "sha256:" + strings.Repeat("b", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			x := base
			mutate(&x)
			if NotesCustodyTransitionDigest(x) == base.TransitionDigest || ValidateNotesCustodyTransition(x) == nil {
				t.Fatal("identity substitution accepted")
			}
			x.TransitionDigest = NotesCustodyTransitionDigest(x)
			if err := ValidateNotesCustodyTransition(x); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, relative := range []string{"", ".", "..", "../other", "/absolute", "a/../b", "a//b", "a/", "a\\b", "a\nb"} {
		x := base
		x.WorkspaceRelativePath = relative
		x.TransitionDigest = NotesCustodyTransitionDigest(x)
		if ValidateNotesCustodyTransition(x) == nil {
			t.Fatalf("unsafe member path accepted: %q", relative)
		}
	}
	for _, mutate := range []func(*NotesCustodyTransition){
		func(x *NotesCustodyTransition) { x.PreviousEventID = x.WorkspaceLifecycleEventID },
		func(x *NotesCustodyTransition) { x.ProjectEventID = "candidate_unaccepted" },
		func(x *NotesCustodyTransition) { x.OriginalPath = strings.Repeat("x", 8193) },
		func(x *NotesCustodyTransition) { x.CanonicalPath = "contains\x00control" },
		func(x *NotesCustodyTransition) { x.ManifestDigest = "not_authenticated" },
	} {
		x := base
		mutate(&x)
		x.TransitionDigest = NotesCustodyTransitionDigest(x)
		if ValidateNotesCustodyTransition(x) == nil {
			t.Fatal("malformed custody accepted")
		}
	}
	before, _ := json.Marshal(base)
	for i := 0; i < 20; i++ {
		var replay NotesCustodyTransition
		if err := json.Unmarshal(before, &replay); err != nil {
			t.Fatal(err)
		}
		if NotesCustodyTransitionDigest(replay) != base.TransitionDigest {
			t.Fatal("serialized replay changed identity")
		}
	}
	after, _ := json.Marshal(base)
	if !bytes.Equal(before, after) {
		t.Fatal("hashing mutated the receipt")
	}
}
