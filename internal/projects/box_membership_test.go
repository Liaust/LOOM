package projects

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
)

func membershipTestCatalog() boxregistry.Catalog {
	return boxregistry.Catalog{Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootPath: "/fixture/personal", Available: true},
		{ID: "box_company", Key: "company", RootPath: "/fixture/company", Available: true},
	}}
}

func TestBoxMembershipRegistrationEvidenceAndPreservation(t *testing.T) {
	input := RegisterProjectContractInput{ProjectRoot: "/fixture/company/Projects/tool", Metadata: json.RawMessage(`{"source":"test"}`)}
	bound, err := BindBoxMembership(input, membershipTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := projectRegistrationMetadata(json.RawMessage(`{"other":"keep"}`), bound)
	if err != nil || HomeBoxID(metadata) != "box_company" || !strings.Contains(string(metadata), `"other":"keep"`) {
		t.Fatalf("membership/preservation: %s %v", metadata, err)
	}
	if _, err = projectRegistrationMetadata(json.RawMessage(`{"home_box_id":"box_personal"}`), bound); err == nil {
		t.Fatal("registration silently reassociated project")
	}
	input.Metadata = json.RawMessage(`{"home_box_id":"box_personal"}`)
	if _, err = BindBoxMembership(input, membershipTestCatalog()); err == nil {
		t.Fatal("client claim contradicted placement")
	}
	input.ProjectRoot = "/external/project"
	if _, err = BindBoxMembership(input, membershipTestCatalog()); err == nil {
		t.Fatal("external path was labelled Personal")
	}
	metadata, err = projectRegistrationMetadata(json.RawMessage(`{"home_box_id":"box_company"}`), RegisterProjectContractInput{})
	if err != nil || HomeBoxID(metadata) != "box_company" {
		t.Fatal("legacy replay lost membership")
	}
}

func TestBoxMembershipSelectionIsFailClosedAndInternalGlobal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection *boxregistry.ResolvedSelection
		predicate string
		args      int
	}{
		{"internal", nil, "true", 0},
		{"all", &boxregistry.ResolvedSelection{AllBoxes: true}, "true", 0},
		{"empty", &boxregistry.ResolvedSelection{}, "false", 0},
		{"company", &boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}}, "home_box_id", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.selection != nil {
				ctx = boxregistry.WithSelection(ctx, *tc.selection)
			}
			args := []any{}
			predicate := projectBoxSelectionSQL(ctx, "p", &args)
			if !strings.Contains(predicate, tc.predicate) || len(args) != tc.args {
				t.Fatalf("%s %v", predicate, args)
			}
		})
	}
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	for _, metadata := range []string{`{}`, `{"home_box_id":"box_personal"}`} {
		if ensureProjectBoxSelection(ctx, Project{Slug: "same", Metadata: json.RawMessage(metadata)}) == nil {
			t.Fatal("foreign/unknown collision accepted")
		}
	}
}

func TestBoxMembershipOverviewCountsAndCursorStaySelected(t *testing.T) {
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}, Digest: "company"})
	service := overviewTestService(t, func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if len(args) != 7 || args[6].Value != `["box_company"]` || strings.Index(query, "home_box_id") > strings.Index(query, "), filtered AS") {
			t.Fatalf("scope after counts/pagination: %s %v", query, args)
		}
		return overviewTestResult(time.Now(), 2, ProjectOverviewCounts{}, 2, []ProjectOverviewItem{{ProjectID: "project_a", UpdatedAt: time.Now()}, {ProjectID: "project_b", UpdatedAt: time.Now()}}), nil
	})
	result, err := service.Overview(ctx, OverviewFilter{Limit: 1})
	if err != nil || result.NextCursor == "" {
		t.Fatalf("overview: %+v %v", result, err)
	}
	other := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_personal"}, Digest: "personal"})
	if _, err = (Service{}).Overview(other, OverviewFilter{Cursor: result.NextCursor}); !errors.Is(err, ErrInvalidOverviewFilter) {
		t.Fatalf("cross-Box cursor accepted: %v", err)
	}
}

func TestBoxMembershipReconcileBoundsAndCatalogRevision(t *testing.T) {
	catalog := membershipTestCatalog()
	digest := BoxCatalogDigest(catalog)
	catalog.Boxes[0], catalog.Boxes[1] = catalog.Boxes[1], catalog.Boxes[0]
	if digest != BoxCatalogDigest(catalog) {
		t.Fatal("catalog order changed identity")
	}
	service := (Service{}).WithBoxCatalog(catalog)
	for _, input := range []BoxMembershipReconcileInput{{}, {ProjectIDs: make([]string, 101)}, {ProjectIDs: []string{"project_x"}, Apply: true, CatalogDigest: "stale"}} {
		if _, err := service.ReconcileBoxMembership(t.Context(), input); err == nil {
			t.Fatal("invalid batch reached database")
		}
	}
	catalog.Boxes[0].RootPath = "/changed"
	if digest == BoxCatalogDigest(catalog) {
		t.Fatal("changed catalog did not invalidate preview")
	}
}
