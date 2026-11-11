package jobs

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/requestctx"
)

func TestLocalJobReadinessRetryAndClaimFencesPostgres(t *testing.T) {
	u := os.Getenv("LOOM_TEST_DB_URL")
	if u == "" {
		t.Skip("LOOM_TEST_DB_URL is unset")
	}
	if _, err := migrations.Up(t.Context(), u, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "corr_job_readiness")
	if err != nil {
		t.Fatal(err)
	}
	id := ids.NewJobID()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM jobs.jobs WHERE job_id=$1`, id) })
	if _, err := db.Exec(`INSERT INTO jobs.jobs(job_id,job_type,status,origin_actor_id,origin_node_id,execution_node_id)
		VALUES ($1,'script_run','queued',$2,$3,$3)`, id, req.ActorID, req.OriginNodeID); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: db}
	now := time.Now().UTC().Truncate(time.Microsecond)
	check := func(want *time.Time) {
		t.Helper()
		got, err := s.NextLocalJobAt(t.Context(), req.OriginNodeID, now)
		if err != nil || (want == nil && got != nil) || (want != nil && (got == nil || !got.Equal(*want))) {
			t.Fatalf("readiness got=%v want=%v err=%v", got, want, err)
		}
	}
	check(&now)
	retry := now.Add(time.Minute)
	if _, err := db.Exec(`UPDATE jobs.jobs SET next_attempt_at=$2 WHERE job_id=$1`, id, retry); err != nil {
		t.Fatal(err)
	}
	check(&retry)
	for _, fence := range []string{"manual_action_required=true", "cancel_requested_at=now()", "cancelled_at=now()", "status='completed'"} {
		if _, err := db.Exec(`UPDATE jobs.jobs SET `+fence+` WHERE job_id=$1`, id); err != nil {
			t.Fatal(err)
		}
		check(nil)
		if _, err := db.Exec(`UPDATE jobs.jobs SET manual_action_required=false,cancel_requested_at=NULL,cancelled_at=NULL,status='queued' WHERE job_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NextLocalJobAt(t.Context(), "node_other", now)
	if err != nil || got != nil {
		t.Fatalf("wrong-node jobs admitted: %v %v", got, err)
	}
}
