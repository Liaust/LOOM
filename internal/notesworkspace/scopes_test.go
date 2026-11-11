package notesworkspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type scopeValidationQuery struct {
	calls int
	err   error
}

func (q *scopeValidationQuery) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	q.calls++
	return nil, q.err
}

func (*scopeValidationQuery) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("validation must finish before querying a scope")
}

func TestEnsureBoxScopeValidation(t *testing.T) {
	valid := SourceScope{ID: "filesystem_box_test", Path: "/srv/loom/box", NodeKey: "main", RootIdentity: "device:inode"}
	for _, tc := range []struct {
		name   string
		change func(*SourceScope)
		want   string
	}{
		{"missing ID", func(s *SourceScope) { s.ID = "" }, "scope ID"},
		{"notes ID", func(s *SourceScope) { s.ID = "notes_source_root_test" }, "scope ID"},
		{"missing suffix", func(s *SourceScope) { s.ID = "filesystem_box_" }, "scope ID"},
		{"empty path", func(s *SourceScope) { s.Path = "" }, "absolute and clean"},
		{"relative path", func(s *SourceScope) { s.Path = "srv/loom/box" }, "absolute and clean"},
		{"parent segment", func(s *SourceScope) { s.Path = "/srv/loom/../box" }, "absolute and clean"},
		{"dot segment", func(s *SourceScope) { s.Path = "/srv/./box" }, "absolute and clean"},
		{"double separator", func(s *SourceScope) { s.Path = "/srv//box" }, "absolute and clean"},
		{"trailing separator", func(s *SourceScope) { s.Path += "/" }, "absolute and clean"},
		{"NUL path", func(s *SourceScope) { s.Path += "\x00" }, "absolute and clean"},
		{"empty identity", func(s *SourceScope) { s.RootIdentity = "" }, "root identity"},
		{"blank identity", func(s *SourceScope) { s.RootIdentity = " \t\n" }, "root identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := valid
			tc.change(&scope)
			q := &scopeValidationQuery{}
			err := (SQLStore{query: q}).EnsureBoxScope(t.Context(), scope)
			if err == nil || !strings.Contains(err.Error(), tc.want) || q.calls != 0 {
				t.Fatalf("validation: err=%v database calls=%d", err, q.calls)
			}
		})
	}
	for _, path := range []string{valid.Path, "/", "/srv/box with spaces"} {
		scope := valid
		scope.Path = path
		stop := errors.New("database reached")
		q := &scopeValidationQuery{err: stop}
		if err := (SQLStore{query: q}).EnsureBoxScope(t.Context(), scope); !errors.Is(err, stop) || q.calls != 1 {
			t.Fatalf("valid path %q rejected: %v calls=%d", path, err, q.calls)
		}
	}
	if err := (SQLStore{}).EnsureBoxScope(t.Context(), valid); err == nil || !strings.Contains(err.Error(), "database required") {
		t.Fatalf("unconfigured store: %v", err)
	}
	if _, err := (SQLStore{}).ScopePath(t.Context(), valid.ID); err == nil || !strings.Contains(err.Error(), "database required") {
		t.Fatalf("unconfigured path lookup: %v", err)
	}
}
