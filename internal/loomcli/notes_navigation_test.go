package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
)

func TestNotesNavigationCommandRoundtrip(t *testing.T) {
	input := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: knowledge.SourceLifecycleFilterActive}
	result := knowledge.NotesSearchResult{SourceKind: knowledge.KnowledgeSearchSourceKind, KnowledgeObjectID: input.KnowledgeObjectID, KnowledgeObjectVersionID: input.KnowledgeObjectVersionID, KnowledgeChunkID: input.KnowledgeChunkID, PassageFollowup: &input, NotesCustodyContext: knowledge.NotesCustodyContext{SourceLifecycle: knowledge.SourceLifecycleActive}}
	line := notesNavigationFollowupLine(result, 1)
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/knowledge/notes/passages/"+input.KnowledgeChunkID+"/workspace" || len(q) != 4 || q.Get("object_id") != input.KnowledgeObjectID || q.Get("version_id") != input.KnowledgeObjectVersionID || q.Get("source_hash") != input.SourceHash || q.Get("source_lifecycle") != "active" {
			t.Errorf("citation lost: %s", r.URL)
		}
		response.WriteJSON(w, 200, response.Success("navigation", notesworkspacesync.Navigation{Citation: input, Status: "unbound", Reason: "source_not_enrolled"}))
	})
	defer stop()
	args := append([]string{"--json", "--socket", socket}, strings.Fields(strings.TrimPrefix(line, "Workspace [1]: loom "))...)
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("command failed: %s %v", out.String(), err)
	}
	var got notesworkspacesync.Navigation
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Citation != input || got.Status != "unbound" || calls != 1 {
		t.Fatalf("response: %+v %v calls=%d", got, err, calls)
	}
}
