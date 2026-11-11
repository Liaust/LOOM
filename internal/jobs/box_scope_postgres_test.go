package jobs

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
)

// Opt-in existing test connection, using transaction-local fixtures only. No
// runtime jobs, scheduler, schema migrations or historical relabeling.
func TestBoxPredicatesPostgres(t *testing.T) {
	url := os.Getenv("LOOM_TEST_DB_URL")
	if url == "" {
		t.Skip("LOOM_TEST_DB_URL is unset")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `CREATE TEMP TABLE box_scope_projects(project_id text, project_scope_id text, metadata jsonb) ON COMMIT DROP;
		CREATE TEMP TABLE box_scope_scripts(script_id text, name text) ON COMMIT DROP;
		CREATE TEMP TABLE box_scope_jobs(job_id text, job_type text DEFAULT 'script_run', status text,
		failure_attention_status text DEFAULT 'active', manual_action_required boolean DEFAULT false,
		scope_id text, metadata jsonb DEFAULT '{}', target_kind text, target_id text,
		created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now(),
		queued_at timestamptz, started_at timestamptz, completed_at timestamptz, failed_at timestamptz, cancelled_at timestamptz, failure_code text) ON COMMIT DROP;
		INSERT INTO pg_temp.box_scope_projects VALUES
		('personal','scope_p','{"home_box_id":"box_personal"}'),
		('company','scope_c','{"home_box_id":"box_company"}'),('legacy','scope_legacy','{}');
		INSERT INTO pg_temp.box_scope_jobs(job_id,status,scope_id,metadata,updated_at) VALUES
		('p','queued','scope_p','{}',now()-interval '5 minutes'),
		('c','queued','scope_c','{}',now()-interval '4 minutes'),
		('workspace_c','queued',NULL,'{"home_box_id":"box_company"}',now()-interval '3 minutes'),
		('spoof_p','queued','scope_p','{"home_box_id":"box_company"}',now()-interval '2 minutes'),
		('unassigned','queued',NULL,'{}',now()),
		('legacy_spoof','queued','scope_legacy','{"home_box_id":"box_company"}',now());`); err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO pg_temp.box_scope_jobs(job_id,status,scope_id,completed_at) VALUES
		('done_p','completed','scope_p',$1),('done_c','completed','scope_c',$1)`, end.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	rewrite := strings.NewReplacer("jobs.jobs", "pg_temp.box_scope_jobs", "projects.projects", "pg_temp.box_scope_projects", "packages.scripts", "pg_temp.box_scope_scripts")
	for _, tc := range []struct {
		ctx      context.Context
		queued   int64
		first    string
		outcomes int64
	}{
		{selectedBox(t, "box_personal"), 2, "spoof_p", 1},
		{selectedBox(t, "box_company"), 2, "workspace_c", 1},
		{t.Context(), 6, "unassigned", 2},
		{boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true}), 6, "unassigned", 2},
	} {
		countArgs := []any{}
		outcomeArgs := []any{end.Add(-time.Hour), end, float64(60)}
		counts, outcomes := overviewQueries(false, jobBoxPredicate(tc.ctx, &countArgs), jobBoxPredicate(tc.ctx, &outcomeArgs))
		var got OverviewCounts
		if err := tx.QueryRowContext(tc.ctx, rewrite.Replace(counts), countArgs...).Scan(&got.Queued, &got.Running, &got.Attention); err != nil {
			t.Fatal(err)
		}
		if got.Queued != tc.queued || got.Running != 0 || got.Attention != 0 {
			t.Fatalf("counts=%+v want queued=%d", got, tc.queued)
		}
		rows, err := tx.QueryContext(tc.ctx, rewrite.Replace(outcomes), outcomeArgs...)
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for rows.Next() {
			var bucket int
			var done, failed, cancelled int64
			if err := rows.Scan(&bucket, &done, &failed, &cancelled); err != nil {
				t.Fatal(err)
			}
			total += done
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if total != tc.outcomes {
			t.Fatalf("outcomes=%d want=%d", total, tc.outcomes)
		}
		query, args := overviewItemsQuery("queued", nil, "", 1, tc.ctx)
		rows, err = tx.QueryContext(tc.ctx, rewrite.Replace(query), args...)
		if err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			t.Fatal("missing scoped item")
		}
		var item OverviewItem
		if err := rows.Scan(&item.JobID, &item.JobType, &item.Status, &item.CreatedAt, &item.UpdatedAt, &item.QueuedAt, &item.StartedAt, &item.CompletedAt, &item.FailedAt, &item.CancelledAt, &item.FailureAttentionStatus, &item.ManualActionRequired, &item.ScopeID, &item.DisplayName, &item.FailureCode); err != nil {
			t.Fatal(err)
		}
		if item.JobID != tc.first {
			t.Fatalf("limit preceded Box filter: got=%s want=%s", item.JobID, tc.first)
		}
		rows.Close()
	}
}
