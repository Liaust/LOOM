package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
)

func TestNotesSearchCoverageDoesNotClaimPreservedCorpusWasSearched(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	renderNotesSearchSummary(cmd, knowledge.NotesSearchResultSet{Coverage: &knowledge.NotesSearchCoverage{}, CandidateRetrievalTruncated: true})
	for _, required := range []string{"Coverage of returned results", "enrolled readable sources", "outside enrollment", "missing bodies/vectors are not counted", "not exhaustive"} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("missing %q: %s", required, output.String())
		}
	}
}

func TestNotesSearchCommandError(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, fmt.Errorf("transport: %w", context.DeadlineExceeded)} {
		got := notesSearchCommandError(err)
		if got.Code != "notes.search_timeout" || !strings.Contains(got.Summary, "10-second") || !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("lost deadline classification: %+v", got)
		}
	}
	if got := notesSearchCommandError(fmt.Errorf("transport: %w", context.Canceled)); got.Code != "notes.search_canceled" || !errors.Is(got, context.Canceled) {
		t.Fatalf("lost cancellation classification: %+v", got)
	}
	for _, err := range []error{errors.New("transport unavailable")} {
		if got := notesSearchCommandError(err); got.Code != "notes.search_failed" {
			t.Fatalf("non-deadline misclassified: %+v", got)
		}
	}
}

func TestNotesSearchAndReadPreserveBackendFailure(t *testing.T) {
	for _, command := range [][]string{{"notes", "search", "natural question"}, {"notes", "search", "natural question", "--details"}, {"notes", "read", "object_known"}} {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			failure := response.ErrorEnvelope{Error: response.ErrorBody{Code: "knowledge.file_source_root_blocked", Summary: "The managed source root is blocked.", Domain: "knowledge", Target: "object_known", Hint: "Inspect the registered source root.", CorrelationID: "server-original"}, Meta: response.Meta{CorrelationID: "server-original", Source: "main-authoritative", Freshness: "live"}}
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				response.WriteJSON(w, http.StatusConflict, failure)
			})
			defer stop()
			args := append([]string{"--json", "--socket", socket}, command...)
			out, _, err := executeRootCommand(args...)
			var got response.ErrorEnvelope
			decoder := json.NewDecoder(strings.NewReader(out))
			if err == nil || decoder.Decode(&got) != nil || got.Error != failure.Error || got.Meta.CorrelationID != "server-original" {
				t.Fatalf("lost original backend error: %s %v", out, err)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("output is not exactly one JSON document: %s", out)
			}
		})
	}
}
