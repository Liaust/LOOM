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

func TestNotesConflictResolutionCommand(t *testing.T) {
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/knowledge/notes/conflicts" {
			t.Error(r.URL)
		}
		var input notesworkspacesync.ResolveConflictInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Validate() != nil || input.Text != "merged text" {
			t.Fatalf("%+v", input)
		}
		response.WriteJSON(w, 200, response.Success("test", notesworkspacesync.ResolutionResult{ID: "choice", Status: "pending"}))
	})
	defer stop()
	for _, yes := range []bool{false, true} {
		args := []string{"--json", "--socket", socket, "notes", "conflicts", "resolve", "old", "--review", "sha256:" + strings.Repeat("a", 64), "--choice", "merge", "--file", "-"}
		if yes {
			args = append(args, "--yes")
		}
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader("merged text"))
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(t.Context())
		if yes && err != nil {
			t.Fatal(err)
		}
		if !yes && err == nil {
			t.Fatal("missing confirmation accepted")
		}
		if yes {
			var result notesworkspacesync.ResolutionResult
			if json.Unmarshal(out.Bytes(), &result) != nil || result.Status != "pending" {
				t.Fatal(out.String())
			}
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
