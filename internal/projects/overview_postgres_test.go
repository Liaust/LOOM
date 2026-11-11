package projects_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

func TestOverviewClassificationSearchAndLivePagesPostgres(t *testing.T) {
	db, req := projectRepositoryTransactionDatabase(t)
	ctx := context.Background()
	service := projects.NewService(db)
	baseline, err := service.Overview(ctx, projects.OverviewFilter{})
	if err != nil {
		t.Fatal(err)
	}
	token := repositoryTransactionSlug("overview")
	at := time.Date(2026, 11, 2, 12, 0, 0, 123456000, time.UTC)
	fixtures := []struct {
		status, metadata, group string
	}{
		{"active", `{"work_state":"working","private_config":"do-not-return"}`, "working"},
		{"paused", `{"work_state":"working"}`, "working"},
		{"active", `{"work_state":"passive"}`, "passive"},
		{"completed", `{"work_state":"passive"}`, "passive"},
		{"archived", `{"work_state":"working"}`, "archived"},
		{"archived", `{"work_state":"passive"}`, "archived"},
		{"active", `{}`, "unclassified"},
		{"active", `{"work_state":"active"}`, "unclassified"},
		{"blocked", `{"project":{"work_state":"working"}}`, "unclassified"},
		{"abandoned", `{"work_state":null}`, "unclassified"},
	}
	ids := make([]string, 0, len(fixtures))
	groups := map[string]string{}
	statuses := map[string]string{}
	timestamps := map[string]time.Time{}
	var workingID string
	for i, fixture := range fixtures {
		created, err := service.CreateProject(ctx, req, projects.CreateInput{
			Name: token + " " + fixture.group,
			Slug: repositoryTransactionSlug("overview-row"),
		})
		if err != nil {
			t.Fatal(err)
		}
		id := created.Project.Project.ProjectID
		ids = append(ids, id)
		groups[id], statuses[id] = fixture.group, fixture.status
		updatedAt := at
		description := "compact description"
		if i == 0 {
			workingID = id
			updatedAt = at.Add(time.Minute)
			description = strings.Repeat("x", 600) + " Literal%'_ marker"
		}
		timestamps[id] = updatedAt
		if _, err := db.ExecContext(ctx, `UPDATE projects.projects SET status=$2, metadata=$3::jsonb, updated_at=$4, description=$5 WHERE project_id=$1`, id, fixture.status, fixture.metadata, updatedAt, description); err != nil {
			t.Fatal(err)
		}
	}
	wantCounts := baseline.Counts
	wantCounts.Working += 2
	wantCounts.Passive += 2
	wantCounts.Archived += 2
	wantCounts.Unclassified += 4
	wantTotal := baseline.Total + int64(len(fixtures))
	// Every overview below runs on a read-only PostgreSQL connection, including
	// subsequent implicit transactions; no domain mutation can hide in a read.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `SET default_transaction_read_only = on`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `SET default_transaction_read_only = off`) }()
	for group, count := range map[string]int64{"all": 10, "working": 2, "passive": 2, "archived": 2, "unclassified": 4} {
		t.Run(group, func(t *testing.T) {
			before := time.Now()
			result, err := service.Overview(ctx, projects.OverviewFilter{Query: strings.ToUpper(token), WorkState: group})
			if err != nil {
				t.Fatal(err)
			}
			if result.Counts != wantCounts || result.Total != wantTotal || result.FilteredTotal != count || len(result.Items) != int(count) || !result.Complete || result.NextCursor != "" {
				t.Fatalf("counts/page disagree: %#v", result)
			}
			if result.ObservedAt.Before(before) || result.ObservedAt.After(time.Now()) {
				t.Fatalf("observation time does not describe this read: %s", result.ObservedAt)
			}
			for _, item := range result.Items {
				if item.WorkState != groups[item.ProjectID] || item.Status != statuses[item.ProjectID] || (group != "all" && item.WorkState != group) {
					t.Errorf("classification/lifecycle mismatch: %#v", item)
				}
				if item.ProjectID == workingID && len(item.Description) != 512 {
					t.Errorf("description was not bounded: %d", len(item.Description))
				}
			}
		})
	}
	for _, filter := range []projects.OverviewFilter{
		{Query: "Literal%'_"}, // Search the full source, not the compact preview.
		{Query: token, Status: "archived"},
		{Query: token, WorkState: "working", Status: "archived"},
		{Query: "' OR true --"},
	} {
		result, err := service.Overview(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if filter.Query == "Literal%'_" {
			want = 1
		} else if filter.Status == "archived" && filter.WorkState == "" {
			want = 2
		}
		if result.Counts != wantCounts || result.Total != wantTotal || result.FilteredTotal != int64(want) || len(result.Items) != want || result.Items == nil || !result.Complete {
			t.Fatalf("literal/lifecycle filter %#v: %#v", filter, result)
		}
	}

	sort.Slice(ids, func(i, j int) bool {
		if !timestamps[ids[i]].Equal(timestamps[ids[j]]) {
			return timestamps[ids[i]].After(timestamps[ids[j]])
		}
		return ids[i] > ids[j]
	})
	var seen []string
	filter := projects.OverviewFilter{Limit: 3, Query: token}
	for page := 0; page < 4; page++ {
		result, err := service.Overview(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if result.FilteredTotal != 10 || result.Total != wantTotal || result.Counts != wantCounts {
			t.Fatalf("cursor changed count scope: %#v", result)
		}
		for _, item := range result.Items {
			seen = append(seen, item.ProjectID)
			if !item.UpdatedAt.Equal(timestamps[item.ProjectID]) {
				t.Fatalf("timestamp precision changed: %s", item.UpdatedAt)
			}
		}
		if result.Complete {
			if page != 3 || result.NextCursor != "" {
				t.Fatalf("premature/invalid terminal page %d: %#v", page, result)
			}
			break
		}
		if result.NextCursor == "" {
			t.Fatal("incomplete page has no continuation")
		}
		filter.Cursor = result.NextCursor
	}
	if strings.Join(seen, ",") != strings.Join(ids, ",") {
		t.Fatalf("tied timestamps skipped/duplicated/reordered projects: got %v want %v", seen, ids)
	}

	first, err := service.Overview(ctx, projects.OverviewFilter{Limit: 1, Query: token, WorkState: "working"})
	if err != nil || first.Complete || first.FilteredTotal != 2 {
		t.Fatalf("live first page: %#v err=%v", first, err)
	}
	// Move the remaining matching row to a different group between page reads.
	// The continuation must recompute current totals and return a true empty end,
	// rather than claiming that the initial two-row result is a frozen snapshot.
	if _, err := db.ExecContext(ctx, `SET default_transaction_read_only = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE projects.projects SET metadata='{"work_state":"passive"}', updated_at=$2 WHERE project_id <> $1 AND project_id = ANY($3::text[]) AND metadata->>'work_state'='working' AND status <> 'archived'`, first.Items[0].ProjectID, at.Add(time.Hour), ids); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `SET default_transaction_read_only = on`); err != nil {
		t.Fatal(err)
	}
	last, err := service.Overview(ctx, projects.OverviewFilter{Limit: 1, Query: token, WorkState: "working", Cursor: first.NextCursor})
	if err != nil || !last.Complete || last.NextCursor != "" || len(last.Items) != 0 || last.FilteredTotal != 1 || last.Counts.Working != wantCounts.Working-1 || last.Counts.Passive != wantCounts.Passive+1 {
		t.Fatalf("live end/counts: %#v err=%v", last, err)
	}
}
