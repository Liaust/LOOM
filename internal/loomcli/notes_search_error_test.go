package loomcli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNotesSearchCommandError(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, fmt.Errorf("transport: %w", context.DeadlineExceeded)} {
		got := notesSearchCommandError(err)
		if got.Code != "notes.search_timeout" || !strings.Contains(got.Summary, "10-second") || !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("lost deadline classification: %+v", got)
		}
	}
	for _, err := range []error{context.Canceled, errors.New("transport unavailable")} {
		if got := notesSearchCommandError(err); got.Code != "notes.search_failed" {
			t.Fatalf("non-deadline misclassified: %+v", got)
		}
	}
}
