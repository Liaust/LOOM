// loom-notes-dedup is an explicit local operator migration, not an agent API.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/sys/unix"
	"loom.local/loom/internal/notesworkspace"
)

func main() {
	runtime.GOMAXPROCS(2)
	debug.SetMemoryLimit(512 << 20)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "notes PDF migration:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: loom-notes-dedup plan|apply|verify|reclaim [flags]; run locally as loom")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	planPath := flags.String("plan", "", "reviewed plan JSON")
	hash := flags.String("plan-hash", "", "exact reviewed plan hash")
	out := flags.String("out", "", "new private plan file (plan command)")
	confirm := flags.Bool("confirm", false, "confirm the explicit mutation")
	cloud := flags.String("cloud-operation-id", "", "succeeded exact-selected post-conversion cloud operation (reclaim only)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if args[0] != "plan" && args[0] != "apply" && args[0] != "verify" && args[0] != "reclaim" {
		return fmt.Errorf("unknown command")
	}
	if (args[0] == "apply" || args[0] == "reclaim") && !*confirm {
		return fmt.Errorf("--confirm required")
	}
	if args[0] == "reclaim" && *cloud == "" {
		return fmt.Errorf("--cloud-operation-id required after independent cloud metadata verification")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", "postgres:///loom_main?host=/run/postgresql&user=loom")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	var actor, database string
	if err := db.QueryRowContext(ctx, `SELECT current_user,current_database()`).Scan(&actor, &database); err != nil {
		return err
	}
	if actor != "loom" || database != "loom_main" {
		return fmt.Errorf("local operator database identity mismatch")
	}
	store := notesworkspace.SQLStore{DB: db}
	return store.WithLock(ctx, "pdf-dedup", func(notesworkspace.Store) error {
		if args[0] == "plan" {
			if *out == "" {
				return fmt.Errorf("--out required; existing files are not overwritten")
			}
			p, err := store.PlanPDFDedup(ctx)
			if err != nil {
				return err
			}
			if err := savePlan(*out, p); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "planned", "plan_hash": p.PlanHash, "bases": len(p.Entries), "unique_payloads": p.UniquePayloads, "inline_payload_bytes": p.InlinePayloadBytes, "unique_payload_bytes": p.UniquePayloadBytes, "out": *out})
		}
		p, err := readPlan(*planPath)
		if err != nil {
			return err
		}
		if err := p.Validate(*hash); err != nil {
			return err
		}
		if args[0] == "apply" {
			result, err := store.ApplyPDFDedup(ctx, p, *hash, func(n int) {
				if n%100 == 0 || n == len(p.Entries) {
					fmt.Fprintf(os.Stderr, "converted/verified %d/%d bases\n", n, len(p.Entries))
				}
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		result, err := store.VerifyPDFDedup(ctx, p, *hash)
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		if err := store.CheckPDFDedupProtection(ctx, p, *cloud); err != nil {
			return err
		}
		return reclaim(ctx, db, store, p, *cloud)
	})
}

func savePlan(path string, p notesworkspace.PDFDedupPlan) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(p); err != nil {
		return err
	}
	return f.Sync()
}

func readPlan(path string) (p notesworkspace.PDFDedupPlan, err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return p, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Size > 64<<20 || st.Mode&0077 != 0 || int(st.Uid) != os.Geteuid() {
		return p, fmt.Errorf("plan must be a bounded private single-link operator-owned regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, (64<<20)+1))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, fmt.Errorf("trailing plan data")
	}
	return
}

func reclaim(ctx context.Context, db *sql.DB, store notesworkspace.SQLStore, p notesworkspace.PDFDedupPlan, cloud string) error {
	var before int64
	if err := db.QueryRowContext(ctx, `SELECT pg_total_relation_size('notes_workspace.bases')`).Scan(&before); err != nil {
		return err
	}
	var disk unix.Statfs_t
	if err := unix.Statfs("/var/lib/postgresql", &disk); err != nil {
		return err
	}
	available := uint64(disk.Bavail) * uint64(disk.Bsize)
	total := uint64(disk.Blocks) * uint64(disk.Bsize)
	if available < uint64(before)*2+(10<<30) || available < total/5 {
		return fmt.Errorf("insufficient table rewrite/WAL headroom")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	// Settings belong only to this dedicated connection, not other LOOM sessions.
	_, err = conn.ExecContext(ctx, `SET lock_timeout='5s'`)
	if err == nil {
		_, err = conn.ExecContext(ctx, `SET statement_timeout='20min'`)
	}
	if err == nil {
		_, err = conn.ExecContext(ctx, `VACUUM (FULL, ANALYZE) notes_workspace.bases`)
	}
	// The process exits after this command; do not reuse its configured connection.
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	conn.Close()
	if err != nil {
		return err
	}
	verified, err := store.VerifyPDFDedup(ctx, p, p.PlanHash)
	if err != nil {
		return err
	}
	var after int64
	if err := db.QueryRowContext(ctx, `SELECT pg_total_relation_size('notes_workspace.bases')`).Scan(&after); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "reclaimed", "plan_hash": p.PlanHash, "cloud_operation_id": cloud, "verified": verified.Verified, "bases_bytes_before": before, "bases_bytes_after": after, "bases_bytes_reclaimed": before - after, "finished_at": time.Now().UTC()})
}
