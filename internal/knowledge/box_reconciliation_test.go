package knowledge

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
)

type sourceMembershipTestDB struct {
	t                 *testing.T
	metadata          string
	home              string
	writes            int
	readOnly          bool
	unregisteredNotes bool
}

func (s *sourceMembershipTestDB) Connect(context.Context) (driver.Conn, error) {
	return sourceMembershipTestConn{s}, nil
}
func (s *sourceMembershipTestDB) Driver() driver.Driver { return sourceMembershipTestDriver{} }

type sourceMembershipTestDriver struct{}

func (sourceMembershipTestDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use connector")
}

type sourceMembershipTestConn struct{ state *sourceMembershipTestDB }

func (sourceMembershipTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("not supported")
}
func (sourceMembershipTestConn) Close() error              { return nil }
func (sourceMembershipTestConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("use BeginTx") }
func (c sourceMembershipTestConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.state.readOnly = options.ReadOnly
	return sourceMembershipTestTx{}, nil
}

type sourceMembershipTestTx struct{}

func (sourceMembershipTestTx) Commit() error   { return nil }
func (sourceMembershipTestTx) Rollback() error { return nil }
func (c sourceMembershipTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	var values []driver.Value
	switch {
	case strings.Contains(query, "FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1"):
		if len(args) != 1 || args[0].Value != "source" {
			s.t.Fatalf("unbounded root query: %s %v", query, args)
		}
		now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
		values = []driver.Value{"source", "project_material", "node", "main", "project", nil, "registration", "project__app", "Application", "/var/lib/application/data", "notes", "active", []byte(`{}`), []byte(s.metadata), now, now, []byte(`{}`)}
		if s.unregisteredNotes {
			values = []driver.Value{"source", "box_notes", "node", "main", nil, nil, nil, "legacy__notes", "Legacy Notes", "/fixture/personal/Notes", "Notes", "active", []byte(`{}`), []byte(s.metadata), now, now, []byte(`{}`)}
		}
	case strings.Contains(query, "JOIN projects.project_watched_root_registrations wr"):
		values = []driver.Value{s.home, "revision"}
	case s.unregisteredNotes && strings.Contains(query, "FROM box.watch_root_registrations"):
		if len(args) != 2 || args[0].Value != nil {
			s.t.Fatalf("unregistered source acquired synthetic registration: %v", args)
		}
	default:
		s.t.Fatalf("unexpected/corpus query: %s", query)
	}
	return &sourceMembershipTestRows{values}, nil
}
func (c sourceMembershipTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.state
	if s.readOnly || !strings.HasPrefix(query, "UPDATE knowledge.notes_source_roots SET metadata=") || strings.Contains(query, "config") || len(args) != 3 {
		s.t.Fatalf("unexpected mutation: %s", query)
	}
	s.writes++
	raw, _ := json.Marshal(map[string]any{"box_id": args[1].Value, "box_membership_revision": args[2].Value})
	s.metadata = string(raw)
	return driver.RowsAffected(1), nil
}

type sourceMembershipTestRows struct{ values []driver.Value }

func (r *sourceMembershipTestRows) Columns() []string {
	out := make([]string, len(r.values))
	for i := range out {
		out[i] = fmt.Sprint(i)
	}
	return out
}
func (*sourceMembershipTestRows) Close() error { return nil }
func (r *sourceMembershipTestRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

func TestBoxSelectionSourceDryRunApplyReplayAndStaleEvidence(t *testing.T) {
	state := &sourceMembershipTestDB{t: t, metadata: `{}`, home: "box_company"}
	db := sql.OpenDB(state)
	t.Cleanup(func() { db.Close() })
	service := NewService(db)
	catalog := boxregistry.Catalog{Boxes: []boxregistry.Box{{ID: "box_company", Key: "company", RootPath: "/fixture/company", Available: true}, {ID: "box_other", Key: "other", RootPath: "/fixture/other", Available: true}}}
	input := SourceBoxMembershipInput{SourceRootIDs: []string{"source"}}
	preview, err := service.ReconcileSourceBoxMembership(t.Context(), catalog, input)
	if err != nil || preview.Changes[0].Status != "would_assign" || state.writes != 0 || !state.readOnly {
		t.Fatalf("dry run: %+v %v", preview, err)
	}
	input.Apply, input.CatalogDigest, input.Evidence = true, preview.CatalogDigest, map[string]string{"source": preview.Changes[0].EvidenceDigest}
	applied, err := service.ReconcileSourceBoxMembership(t.Context(), catalog, input)
	if err != nil || applied.Changes[0].Status != "assigned" || state.writes != 1 {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	replay, err := service.ReconcileSourceBoxMembership(t.Context(), catalog, input)
	if err != nil || replay.Changes[0].Status != "unchanged" || state.writes != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	state.metadata, state.home = `{}`, "box_other"
	if _, err := service.ReconcileSourceBoxMembership(t.Context(), catalog, input); err == nil || state.writes != 1 {
		t.Fatal("stale project evidence was applied")
	}
}

func TestBoxSelectionUnregisteredLegacyNotesRemainQualifiedWithoutCurrentOwnerEvidence(t *testing.T) {
	state := &sourceMembershipTestDB{t: t, metadata: `{}`, unregisteredNotes: true}
	db := sql.OpenDB(state)
	t.Cleanup(func() { db.Close() })
	service := NewService(db)
	catalog := boxregistry.Catalog{Boxes: []boxregistry.Box{{ID: "box_personal", Key: "personal", Node: "main", RootPath: "/fixture/personal", Available: true}}}
	input := SourceBoxMembershipInput{SourceRootIDs: []string{"source"}}
	preview, err := service.ReconcileSourceBoxMembership(t.Context(), catalog, input)
	if err != nil || len(preview.Changes) != 1 || preview.Changes[0].Status != "unassigned" || preview.Changes[0].BoxID != "" || state.writes != 0 {
		t.Fatalf("path containment fabricated current ownership: %+v %v", preview, err)
	}
	input.Apply, input.CatalogDigest, input.Evidence = true, preview.CatalogDigest, map[string]string{"source": preview.Changes[0].EvidenceDigest}
	applied, err := service.ReconcileSourceBoxMembership(t.Context(), catalog, input)
	if err != nil || applied.Changes[0].Status != "unassigned" || state.writes != 0 || state.metadata != `{}` {
		t.Fatalf("apply changed qualified historical ownership: %+v %v", applied, err)
	}
}
