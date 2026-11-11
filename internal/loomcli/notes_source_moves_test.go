package loomcli

import (
	"bytes"
	"encoding/json"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
	"net/http"
	"strings"
	"testing"
)

func TestNotesSourceMoveCommands(t *testing.T) {
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/knowledge/notes/moves" || r.Method != "POST" {
			t.Fatal(r.URL)
		}
		var input notesworkspacesync.SourceMoveInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Validate() != nil || input.From != "Notes/a.md" || input.To != "Projects/Book/Notes/b.md" {
			t.Fatal(input)
		}
		response.WriteJSON(w, 200, response.Success("test", notesworkspacesync.SourceMoveView{Status: "queued"}))
	})
	defer stop()
	for _, name := range []string{"review", "apply"} {
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		args := []string{"--json", "--socket", socket, "notes", "moves", name, "Notes/a.md", "Projects/Book/Notes/b.md"}
		if name == "apply" {
			args = append(args, "--review", "sha256:"+strings.Repeat("a", 64), "--yes")
		}
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(out.String(), err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
