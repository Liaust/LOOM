package db

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	Status   string `json:"status"`
	Database string `json:"database,omitempty"`
	Error    string `json:"error,omitempty"`
}

func Check(ctx context.Context, dbURL string) Result {
	if strings.TrimSpace(dbURL) == "" {
		return Result{
			Status: "unhealthy",
			Error:  "LOOM_DB_URL is not configured",
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return Result{
			Status: "unhealthy",
			Error:  "database pool could not be created",
		}
	}
	defer pool.Close()

	var database string
	if err := pool.QueryRow(ctx, "select current_database()").Scan(&database); err != nil {
		return Result{
			Status: "unhealthy",
			Error:  "database ping failed",
		}
	}

	return Result{
		Status:   "ok",
		Database: database,
	}
}
