package projects

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type membershipTestConnector struct {
	store    *runtimeGuardFakeStore
	writes   *int
	readOnly *bool
}

func (c membershipTestConnector) Connect(context.Context) (driver.Conn, error) {
	return membershipTestConn{runtimeGuardFakeConn{c.store}, c}, nil
}
func (c membershipTestConnector) Driver() driver.Driver { return runtimeGuardFakeDriver{} }

type membershipTestConn struct {
	runtimeGuardFakeConn
	connector membershipTestConnector
}

func (c membershipTestConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	*c.connector.readOnly = options.ReadOnly
	return membershipTestTx{}, nil
}
func (c membershipTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if *c.connector.readOnly {
		return nil, fmt.Errorf("dry run attempted mutation")
	}
	if strings.Contains(query, "pg_advisory_xact_lock") {
		return driver.RowsAffected(1), nil
	}
	if !strings.HasPrefix(query, "UPDATE projects.projects SET metadata=") || strings.Contains(query, "config") {
		return nil, fmt.Errorf("unexpected mutation: %s", query)
	}
	*c.connector.writes++
	return driver.RowsAffected(1), nil
}

type membershipTestTx struct{}

func (membershipTestTx) Commit() error   { return nil }
func (membershipTestTx) Rollback() error { return nil }

func TestBoxMembershipDryRunApplyReplayAndStaleEvidence(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	writes, readOnly := 0, false
	db := sql.OpenDB(membershipTestConnector{store, &writes, &readOnly})
	t.Cleanup(func() { db.Close() })
	service := NewService(db).WithBoxCatalog(membershipTestCatalog())
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	expect := func(metadata, path string) {
		store.expect("FROM projects.projects p", nil, []driver.Value{"project_fixture", "scope_fixture", "scope:fixture", "fixture", "Fixture", "", "actor", "actor", nil, nil, nil, "active", "normal", "normal", []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(metadata), now, now})
		store.expect("FROM projects.project_contract_registrations", nil, []driver.Value{"registration", "project_fixture", path, path + "/project.yaml", "sha256:" + strings.Repeat("a", 64), "project.contract.v0.4", []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`[]`), []byte(`[]`), "registered", "inactive", int64(1), "actor", now, nil, nil, []byte(`{}`), now, now})
	}
	input := BoxMembershipReconcileInput{ProjectIDs: []string{"project_fixture"}}
	expect(`{}`, "/fixture/company/Projects/app")
	preview, err := service.ReconcileBoxMembership(t.Context(), input)
	if err != nil || preview.Changes[0].Status != "would_assign" || writes != 0 || !readOnly {
		t.Fatalf("dry run: %+v %v writes=%d", preview, err, writes)
	}
	input.Apply, input.CatalogDigest, input.Evidence = true, preview.CatalogDigest, map[string]string{"project_fixture": preview.Changes[0].EvidenceDigest}
	expect(`{}`, "/fixture/company/Projects/app")
	applied, err := service.ReconcileBoxMembership(t.Context(), input)
	if err != nil || applied.Changes[0].Status != "assigned" || writes != 1 {
		t.Fatalf("apply: %+v %v writes=%d", applied, err, writes)
	}
	metadata, _ := json.Marshal(map[string]string{"home_box_id": "box_company", "box_membership_revision": preview.Changes[0].EvidenceDigest})
	expect(string(metadata), "/fixture/company/Projects/app")
	replay, err := service.ReconcileBoxMembership(t.Context(), input)
	if err != nil || replay.Changes[0].Status != "unchanged" || writes != 1 {
		t.Fatalf("replay: %+v %v writes=%d", replay, err, writes)
	}
	expect(`{}`, "/fixture/company/Projects/moved")
	if _, err := service.ReconcileBoxMembership(t.Context(), input); err == nil || writes != 1 {
		t.Fatal("stale placement was applied")
	}
	store.requireDone()
}
