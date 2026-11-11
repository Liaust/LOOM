package loomcli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
)

func TestNotesObjectsRoutineAndDetails(t *testing.T) {
	metadata := json.RawMessage(`{"extraction":{"status":"indexed"},"large_snapshot":"` + strings.Repeat("x", 12000) + `"}`)
	objects := []knowledge.KnowledgeObject{{KnowledgeObjectID: "knowledge_object_example", CanonicalFileID: "object_example",
		NotesSourceRootID: "root", SourcePath: "/source/brief.md", RelativePath: "brief.md", FileClass: "markdown",
		SourceHash: "sha256:source", SourceRevision: "revision", ProcessingState: "indexed", Metadata: metadata,
		LexicalVersionID: "text_version", SemanticVersionID: "vector_version"}}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/knowledge/notes/objects" || r.URL.Query().Get("project_id") != "project" {
			t.Errorf("unexpected selection: %s", r.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("objects", objects))
	})
	defer stop()
	out, _, err := executeRootCommand("--socket", socket, "--json", "notes", "objects", "list", "--project-id", "project", "--limit", "6")
	var summaries []notesObjectSummary
	if err != nil || json.Unmarshal([]byte(out), &summaries) != nil || len(summaries) != 1 {
		t.Fatalf("routine result: %s %v", out, err)
	}
	if len(out) > 1400 || strings.Contains(out, "large_snapshot") || strings.Contains(out, `"metadata"`) || summaries[0].FileID != "object_example" || summaries[0].LexicalVersionID != "text_version" || !strings.Contains(summaries[0].InspectCommand, "knowledge_object_example") || !strings.Contains(summaries[0].InspectCommand, "--summary") {
		t.Fatalf("heavy metadata or missing identity: %s", out)
	}
	out, _, err = executeRootCommand("--socket", socket, "--json", "notes", "objects", "list", "--project-id", "project", "--details")
	var full []knowledge.KnowledgeObject
	if err != nil || json.Unmarshal([]byte(out), &full) != nil || len(full) != 1 || string(full[0].Metadata) != string(metadata) {
		t.Fatalf("details lost raw metadata: %v", err)
	}
}
