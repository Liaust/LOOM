package provenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DatabaseName       = "loom_provenance"
	DatabaseRole       = "loom_provenance"
	runtimePingTimeout = 5 * time.Second
)

var ErrWrongRole = errors.New("provenance runtime connected with the wrong database role")

type ReadinessState string

const (
	ReadinessReady    ReadinessState = "ready"
	ReadinessNotReady ReadinessState = "not_ready"
)

type ReadinessCode string

const (
	ReadinessCodeReady                ReadinessCode = "ready"
	ReadinessCodeInvalidConfiguration ReadinessCode = "invalid_configuration"
	ReadinessCodeWrongDatabase        ReadinessCode = "wrong_database"
	ReadinessCodeWrongRole            ReadinessCode = "wrong_role"
	ReadinessCodeDatabaseUnavailable  ReadinessCode = "database_unavailable"
	ReadinessCodeSchemaBehind         ReadinessCode = "schema_behind"
	ReadinessCodeSchemaAhead          ReadinessCode = "schema_ahead"
	ReadinessCodeHistoryMismatch      ReadinessCode = "history_mismatch"
	ReadinessCodeSchemaTampered       ReadinessCode = "schema_tampered"
	ReadinessCodeMigrationFailed      ReadinessCode = "migration_failed"
)

// RuntimeReadiness is deliberately metadata-only. It never includes a
// connection string, credential, semantic claim, or source excerpt.
type RuntimeReadiness struct {
	State           ReadinessState `json:"state"`
	Code            ReadinessCode  `json:"code"`
	Database        string         `json:"database,omitempty"`
	Role            string         `json:"role,omitempty"`
	AppliedHead     int            `json:"applied_head"`
	PackagedHead    int            `json:"packaged_head"`
	PendingVersions []int          `json:"pending_versions,omitempty"`
}

type RuntimeError struct {
	Readiness RuntimeReadiness
	cause     error
}

func (e *RuntimeError) Error() string {
	return fmt.Sprintf("provenance runtime is not ready: %s", e.Readiness.Code)
}

func (e *RuntimeError) Unwrap() error {
	return e.cause
}

type Runtime struct {
	pool      *pgxpool.Pool
	readiness RuntimeReadiness
	closeOnce sync.Once
}

func OpenRuntime(ctx context.Context, databaseURL string, autoMigrate bool) (*Runtime, RuntimeReadiness, error) {
	return openRuntime(ctx, databaseURL, DatabaseName, DatabaseRole, autoMigrate)
}

func openRuntime(ctx context.Context, databaseURL, expectedDatabase, expectedRole string, autoMigrate bool) (*Runtime, RuntimeReadiness, error) {
	notReady := RuntimeReadiness{
		State:        ReadinessNotReady,
		Code:         ReadinessCodeInvalidConfiguration,
		Database:     strings.TrimSpace(expectedDatabase),
		Role:         strings.TrimSpace(expectedRole),
		PackagedHead: SchemaHead,
	}
	if strings.TrimSpace(databaseURL) == "" || strings.TrimSpace(expectedDatabase) == "" || strings.TrimSpace(expectedRole) == "" {
		return nil, notReady, newRuntimeError(notReady, errors.New("provenance database configuration is required"))
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, notReady, newRuntimeError(notReady, err)
	}
	return openRuntimeWithConfig(ctx, poolConfig, expectedDatabase, expectedRole, autoMigrate)
}

func openRuntimeWithConfig(ctx context.Context, poolConfig *pgxpool.Config, expectedDatabase, expectedRole string, autoMigrate bool) (*Runtime, RuntimeReadiness, error) {
	notReady := RuntimeReadiness{
		State:        ReadinessNotReady,
		Code:         ReadinessCodeInvalidConfiguration,
		Database:     strings.TrimSpace(expectedDatabase),
		Role:         strings.TrimSpace(expectedRole),
		PackagedHead: SchemaHead,
	}
	if poolConfig == nil || strings.TrimSpace(expectedDatabase) == "" || strings.TrimSpace(expectedRole) == "" {
		return nil, notReady, newRuntimeError(notReady, errors.New("provenance database configuration is required"))
	}
	poolConfig = poolConfig.Copy()
	configuredDatabase := strings.TrimSpace(poolConfig.ConnConfig.Database)
	if configuredDatabase != expectedDatabase || configuredDatabase == "loom_main" {
		notReady.Code = ReadinessCodeWrongDatabase
		notReady.Database = configuredDatabase
		return nil, notReady, newRuntimeError(notReady, fmt.Errorf(
			"%w: connected=%q expected=%q", ErrWrongDatabase, configuredDatabase, expectedDatabase,
		))
	}
	configuredRole := strings.TrimSpace(poolConfig.ConnConfig.User)
	if configuredRole != expectedRole {
		notReady.Code = ReadinessCodeWrongRole
		notReady.Role = configuredRole
		return nil, notReady, newRuntimeError(notReady, fmt.Errorf(
			"%w: connected=%q expected=%q", ErrWrongRole, configuredRole, expectedRole,
		))
	}
	poolConfig.MaxConns = 4
	poolConfig.MinConns = 0
	poolConfig.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		notReady.Code = ReadinessCodeDatabaseUnavailable
		return nil, notReady, newRuntimeError(notReady, err)
	}
	verificationCtx, verificationCancel := context.WithTimeout(ctx, runtimePingTimeout)
	var connectedDatabase, currentRole, sessionRole string
	err = pool.QueryRow(verificationCtx, `SELECT current_database(), current_user, session_user`).Scan(&connectedDatabase, &currentRole, &sessionRole)
	verificationCancel()
	if err != nil {
		pool.Close()
		notReady.Code = ReadinessCodeDatabaseUnavailable
		return nil, notReady, newRuntimeError(notReady, err)
	}
	if connectedDatabase != expectedDatabase || connectedDatabase == "loom_main" {
		pool.Close()
		notReady.Code = ReadinessCodeWrongDatabase
		notReady.Database = connectedDatabase
		return nil, notReady, newRuntimeError(notReady, fmt.Errorf(
			"%w: connected=%q expected=%q", ErrWrongDatabase, connectedDatabase, expectedDatabase,
		))
	}
	if currentRole != expectedRole || sessionRole != expectedRole {
		pool.Close()
		notReady.Code = ReadinessCodeWrongRole
		notReady.Role = currentRole
		return nil, notReady, newRuntimeError(notReady, fmt.Errorf(
			"%w: current=%q session=%q expected=%q", ErrWrongRole, currentRole, sessionRole, expectedRole,
		))
	}
	pingCtx, cancel := context.WithTimeout(ctx, runtimePingTimeout)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		pool.Close()
		notReady.Code = ReadinessCodeDatabaseUnavailable
		return nil, notReady, newRuntimeError(notReady, err)
	}

	var status MigrationStatus
	if autoMigrate {
		status, err = ApplyMigrations(ctx, pool, expectedDatabase)
	} else {
		status, err = InspectSchema(ctx, pool, expectedDatabase)
	}
	readiness := runtimeReadiness(status, err)
	readiness.Role = currentRole
	if err != nil {
		pool.Close()
		return nil, readiness, newRuntimeError(readiness, err)
	}

	runtime := &Runtime{pool: pool, readiness: readiness}
	return runtime, runtime.Readiness(), nil
}

func runtimeReadiness(status MigrationStatus, err error) RuntimeReadiness {
	readiness := RuntimeReadiness{
		State:           ReadinessNotReady,
		Code:            readinessCode(err),
		Database:        status.Database,
		AppliedHead:     status.AppliedHead,
		PackagedHead:    status.PackagedHead,
		PendingVersions: append([]int(nil), status.PendingVersions...),
	}
	if err == nil && status.Ready && status.AppliedHead == SchemaHead {
		readiness.State = ReadinessReady
		readiness.Code = ReadinessCodeReady
	}
	return readiness
}

func readinessCode(err error) ReadinessCode {
	switch {
	case err == nil:
		return ReadinessCodeReady
	case errors.Is(err, ErrWrongDatabase):
		return ReadinessCodeWrongDatabase
	case errors.Is(err, ErrWrongRole):
		return ReadinessCodeWrongRole
	case errors.Is(err, ErrSchemaBehind):
		return ReadinessCodeSchemaBehind
	case errors.Is(err, ErrSchemaAhead):
		return ReadinessCodeSchemaAhead
	case errors.Is(err, ErrHistoryMismatch):
		return ReadinessCodeHistoryMismatch
	case errors.Is(err, ErrSchemaTampered):
		return ReadinessCodeSchemaTampered
	default:
		return ReadinessCodeMigrationFailed
	}
}

func newRuntimeError(readiness RuntimeReadiness, cause error) *RuntimeError {
	return &RuntimeError{Readiness: readiness, cause: cause}
}

func (r *Runtime) Readiness() RuntimeReadiness {
	if r == nil {
		return RuntimeReadiness{State: ReadinessNotReady, Code: ReadinessCodeInvalidConfiguration, PackagedHead: SchemaHead}
	}
	readiness := r.readiness
	readiness.PendingVersions = append([]int(nil), readiness.PendingVersions...)
	return readiness
}

func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.pool != nil {
			r.pool.Close()
		}
	})
}
