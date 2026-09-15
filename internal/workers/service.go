package workers

import (
	"database/sql"
	"log/slog"
)

type Service struct {
	DB       *sql.DB
	Registry *Registry
	Logger   *slog.Logger
}

func NewService(db *sql.DB, registry *Registry, logger *slog.Logger) Service {
	return Service{DB: db, Registry: registry, Logger: logger}
}
