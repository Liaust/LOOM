package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Result struct {
	Status         string `json:"status"`
	Detail         string `json:"detail,omitempty"`
	Directory      string `json:"directory,omitempty"`
	CurrentVersion int64  `json:"current_version,omitempty"`
	LatestVersion  int64  `json:"latest_version,omitempty"`
	Pending        int64  `json:"pending,omitempty"`
	Error          string `json:"error,omitempty"`
}

var (
	dialectOnce sync.Once
	dialectErr  error
)

func Up(ctx context.Context, dbURL, dir string) (Result, error) {
	if err := ensureDialect(); err != nil {
		return Result{Status: "unhealthy", Error: "migration dialect could not be configured"}, err
	}

	latest, err := latestVersion(dir)
	if err != nil {
		return Result{Status: "unhealthy", Directory: dir, Error: "migration directory could not be read"}, err
	}
	if latest == 0 {
		return Result{
			Status:    "not_configured",
			Directory: dir,
			Detail:    "no SQL migrations found",
		}, nil
	}

	db, err := open(ctx, dbURL)
	if err != nil {
		return Result{Status: "unhealthy", Directory: dir, LatestVersion: latest, Error: "database could not be opened"}, err
	}
	defer db.Close()

	if err := goose.UpContext(ctx, db, dir); err != nil {
		return Result{Status: "unhealthy", Directory: dir, LatestVersion: latest, Error: "migrations could not be applied"}, err
	}

	return Status(ctx, dbURL, dir), nil
}

func Status(ctx context.Context, dbURL, dir string) Result {
	if strings.TrimSpace(dbURL) == "" {
		return Result{Status: "unhealthy", Directory: dir, Error: "LOOM_DB_URL is not configured"}
	}
	if strings.TrimSpace(dir) == "" {
		return Result{Status: "not_configured", Error: "LOOM_MIGRATIONS_DIR is not configured"}
	}
	if err := ensureDialect(); err != nil {
		return Result{Status: "unhealthy", Directory: dir, Error: "migration dialect could not be configured"}
	}

	latest, err := latestVersion(dir)
	if err != nil {
		return Result{Status: "unhealthy", Directory: dir, Error: "migration directory could not be read"}
	}
	if latest == 0 {
		return Result{
			Status:    "not_configured",
			Directory: dir,
			Detail:    "no SQL migrations found",
		}
	}

	db, err := open(ctx, dbURL)
	if err != nil {
		return Result{Status: "unhealthy", Directory: dir, LatestVersion: latest, Error: "database could not be opened"}
	}
	defer db.Close()

	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return Result{Status: "unhealthy", Directory: dir, LatestVersion: latest, Error: "migration version could not be read"}
	}

	result := Result{
		Status:         "ok",
		Directory:      dir,
		CurrentVersion: version,
		LatestVersion:  latest,
	}

	switch {
	case version < latest:
		result.Status = "degraded"
		result.Pending = latest - version
		result.Detail = "pending migrations"
	case version > latest:
		result.Status = "degraded"
		result.Detail = "database migration version is ahead of migration files"
	default:
		result.Detail = "migrations current"
	}

	return result
}

func LatestVersion(dir string) (int64, error) {
	return latestVersion(dir)
}

func latestVersion(dir string) (int64, error) {
	migrations, err := goose.CollectMigrations(dir, 0, math.MaxInt64)
	if err != nil {
		if errors.Is(err, goose.ErrNoMigrationFiles) {
			return 0, nil
		}
		return 0, err
	}
	if migrations.Len() == 0 {
		return 0, nil
	}
	last, err := migrations.Last()
	if err != nil {
		return 0, err
	}
	return last.Version, nil
}

func open(ctx context.Context, dbURL string) (*sql.DB, error) {
	if strings.TrimSpace(dbURL) == "" {
		return nil, fmt.Errorf("LOOM_DB_URL is not configured")
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func ensureDialect() error {
	dialectOnce.Do(func() {
		dialectErr = goose.SetDialect("postgres")
	})
	return dialectErr
}
