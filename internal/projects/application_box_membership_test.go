package projects

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
)

const applicationMembershipProject = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
const applicationOtherMembershipProject = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"

type applicationMembershipRows struct {
	values   [][]driver.Value
	index    int
	finalErr error
}

func (*applicationMembershipRows) Columns() []string { return []string{"project_id", "home_box_id"} }
func (*applicationMembershipRows) Close() error      { return nil }
func (r *applicationMembershipRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		if r.finalErr != nil {
			return r.finalErr
		}
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func TestApplicationProjectBoxesSelectedMetadataOnly(t *testing.T) {
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	service := overviewTestService(t, func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.HasPrefix(query, "SELECT p.project_id,COALESCE(p.metadata->>'home_box_id','')") || strings.Contains(query, " JOIN ") || strings.Index(query, "home_box_id' IN") > strings.Index(query, " LIMIT ") || len(args) != 3 {
			t.Fatalf("membership was not selected before bound: %s %v", query, args)
		}
		if args[0].Value != `["box_company"]` || args[2].Value != int64(MaxApplicationProjectBoxes+1) {
			t.Fatal(args)
		}
		return &applicationMembershipRows{values: [][]driver.Value{{applicationMembershipProject, "box_company"}}}, nil
	}).WithBoxCatalog(membershipTestCatalog())
	result, err := service.ApplicationProjectBoxes(ctx, nil)
	if err != nil || len(result) != 1 || result[applicationMembershipProject] != "box_company" {
		t.Fatal(result, err)
	}
}

func TestApplicationProjectBoxesExactIDsAndBounds(t *testing.T) {
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	service := overviewTestService(t, func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "p.project_id IN (SELECT jsonb_array_elements_text($2::jsonb))") || args[1].Value != `["`+applicationMembershipProject+`"]` {
			t.Fatal(query, args)
		}
		return &applicationMembershipRows{}, nil
	})
	result, err := service.ApplicationProjectBoxes(ctx, []string{applicationMembershipProject})
	if err != nil || len(result) != 0 {
		t.Fatal("missing/foreign exact project fell back", result, err)
	}
	for _, ids := range [][]string{make([]string, MaxApplicationProjectBoxes+1), {"invalid"}, {applicationMembershipProject, applicationMembershipProject}} {
		if _, err := (Service{}).ApplicationProjectBoxes(ctx, ids); !errors.Is(err, ErrApplicationProjectBoxesUnavailable) {
			t.Fatal("invalid IDs reached database", err)
		}
	}
	if empty, err := (Service{}).ApplicationProjectBoxes(ctx, []string{}); err != nil || len(empty) != 0 {
		t.Fatal("empty exact IDs were global", empty, err)
	}
	if empty, err := (Service{}).ApplicationProjectBoxes(boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{}), nil); err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
}

func TestApplicationProjectBoxesUnknownAndUnavailableStayQualified(t *testing.T) {
	catalog := membershipTestCatalog()
	catalog.Boxes[0].Available = false
	service := overviewTestService(t, func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "NULLIF(p.metadata->>'home_box_id','') IS NOT NULL") {
			t.Fatal("unknown ownership not excluded in SQL")
		}
		return &applicationMembershipRows{values: [][]driver.Value{{applicationMembershipProject, "box_personal"}, {applicationOtherMembershipProject, ""}}}, nil
	}).WithBoxCatalog(catalog)
	all := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true})
	result, err := service.ApplicationProjectBoxes(all, nil)
	if err != nil || len(result) != 0 {
		t.Fatal("retired/unknown ownership was labelled", result, err)
	}
	legacy := overviewTestService(t, func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "WHERE true ORDER BY") {
			t.Fatal("absent context was scoped", query)
		}
		return &applicationMembershipRows{values: [][]driver.Value{{applicationMembershipProject, ""}}}, nil
	})
	result, err = legacy.ApplicationProjectBoxes(context.Background(), nil)
	if err != nil || len(result) != 1 || result[applicationMembershipProject] != "" {
		t.Fatal("unknown legacy membership invented or dropped", result, err)
	}
}

func TestApplicationProjectBoxesNeverTruncatesAndPropagatesReadFailure(t *testing.T) {
	values := make([][]driver.Value, 0, MaxApplicationProjectBoxes+1)
	for i := 0; i <= MaxApplicationProjectBoxes; i++ {
		values = append(values, []driver.Value{ids.NewProjectID(), "box_company"})
	}
	service := overviewTestService(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return &applicationMembershipRows{values: values}, nil
	})
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	result, err := service.ApplicationProjectBoxes(ctx, nil)
	if !errors.Is(err, ErrApplicationProjectBoxesUnavailable) || result != nil {
		t.Fatal("overflow returned partial inventory", len(result), err)
	}
	failure := errors.New("fixture metadata read failed")
	service = overviewTestService(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return &applicationMembershipRows{values: values[:1], finalErr: failure}, nil
	})
	result, err = service.ApplicationProjectBoxes(ctx, nil)
	if !errors.Is(err, failure) || !errors.Is(err, ErrApplicationProjectBoxesUnavailable) || result != nil {
		t.Fatal("failed read returned partial inventory", result, err)
	}
}
