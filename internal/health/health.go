package health

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/version"
)

type NodeInfo struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type CheckResult struct {
	Status         string `json:"status"`
	Path           string `json:"path,omitempty"`
	Detail         string `json:"detail,omitempty"`
	CurrentVersion int64  `json:"current_version,omitempty"`
	LatestVersion  int64  `json:"latest_version,omitempty"`
	Pending        int64  `json:"pending,omitempty"`
	Error          string `json:"error,omitempty"`
}

type StorageCheck struct {
	Status      string `json:"status"`
	DataDir     string `json:"data_dir"`
	ObjectStore string `json:"object_store"`
	Error       string `json:"error,omitempty"`
}

type Checks struct {
	Config     CheckResult  `json:"config"`
	Database   db.Result    `json:"database"`
	Storage    StorageCheck `json:"storage"`
	Migrations CheckResult  `json:"migrations"`
	Bootstrap  CheckResult  `json:"bootstrap"`
}

type Report struct {
	Service     string   `json:"service"`
	Status      string   `json:"status"`
	Version     string   `json:"version"`
	Environment string   `json:"environment"`
	Node        NodeInfo `json:"node"`
	Checks      Checks   `json:"checks"`
}

type Service struct {
	Config  config.Config
	Version version.Info
}

func NewService(cfg config.Config, info version.Info) Service {
	return Service{
		Config:  cfg,
		Version: info,
	}
}

func (s Service) Check(ctx context.Context) Report {
	configCheck := CheckResult{Status: "ok"}
	databaseCheck := db.Check(ctx, s.Config.DBURL)
	storageCheck := checkStorage(s.Config.DataDir, s.Config.ObjectStore)
	migrationCheck := checkMigrations(ctx, s.Config)
	bootstrapCheck := checkBootstrap(ctx, s.Config)

	status := "ok"
	for _, checkStatus := range []string{
		configCheck.Status,
		databaseCheck.Status,
		storageCheck.Status,
		migrationCheck.Status,
		bootstrapCheck.Status,
	} {
		if checkStatus == "disabled" {
			continue
		}
		if checkStatus == "unhealthy" {
			status = "unhealthy"
			break
		}
		if checkStatus == "degraded" && status == "ok" {
			status = "degraded"
		}
	}

	return Report{
		Service:     "loomd",
		Status:      status,
		Version:     s.Version.Version,
		Environment: s.Config.Env,
		Node: NodeInfo{
			ID:   s.Config.NodeID,
			Role: s.Config.NodeRole,
		},
		Checks: Checks{
			Config:     configCheck,
			Database:   databaseCheck,
			Storage:    storageCheck,
			Migrations: migrationCheck,
			Bootstrap:  bootstrapCheck,
		},
	}
}

func checkMigrations(ctx context.Context, cfg config.Config) CheckResult {
	result := migrations.Status(ctx, cfg.DBURL, cfg.MigrationsDir)
	return CheckResult{
		Status:         result.Status,
		Path:           result.Directory,
		Detail:         result.Detail,
		CurrentVersion: result.CurrentVersion,
		LatestVersion:  result.LatestVersion,
		Pending:        result.Pending,
		Error:          result.Error,
	}
}

func checkBootstrap(ctx context.Context, cfg config.Config) CheckResult {
	if cfg.BootstrapMode == "production" {
		input := productionBootstrapStatusInputFromConfig(cfg)
		summary := bootstrap.CheckProduction(ctx, cfg.DBURL, input)
		if !summary.Ready {
			return CheckResult{
				Status: "unhealthy",
				Detail: "production bootstrap records are incomplete",
				Error:  strings.Join(summary.Missing, ", "),
			}
		}
		return CheckResult{
			Status: "ok",
			Detail: "production bootstrap records present",
		}
	}
	if !cfg.BootstrapDev {
		return CheckResult{
			Status: "disabled",
			Detail: "dev bootstrap is disabled",
		}
	}

	summary := bootstrap.Check(ctx, cfg.DBURL)
	if !summary.Ready {
		return CheckResult{
			Status: "unhealthy",
			Detail: "bootstrap records are incomplete",
			Error:  strings.Join(summary.Missing, ", "),
		}
	}
	return CheckResult{
		Status: "ok",
		Detail: "bootstrap records present",
	}
}

func productionBootstrapStatusInputFromConfig(cfg config.Config) bootstrap.ProductionStatusInput {
	input := bootstrap.DefaultProductionInput()
	if strings.TrimSpace(cfg.NodeID) != "" && cfg.NodeID != config.DefaultNodeID {
		input.NodeKey = strings.TrimSpace(cfg.NodeID)
		input.NodeScopeKey = ""
		input.NodeScopeSlug = ""
		input.NodeScopeName = ""
	}
	return bootstrap.ProductionStatusInputFromProductionInput(bootstrap.NormalizeProductionInput(input))
}

func checkStorage(dataDir, objectStore string) StorageCheck {
	check := StorageCheck{
		Status:      "ok",
		DataDir:     dataDir,
		ObjectStore: objectStore,
	}

	if err := requireDir(dataDir); err != nil {
		check.Status = "unhealthy"
		check.Error = err.Error()
		return check
	}
	if err := requireDir(objectStore); err != nil {
		check.Status = "unhealthy"
		check.Error = err.Error()
		return check
	}

	probePath := filepath.Join(objectStore, ".loom-health-probe")
	if err := os.WriteFile(probePath, []byte("ok\n"), 0o600); err != nil {
		check.Status = "unhealthy"
		check.Error = "object store is not writable"
		return check
	}
	_ = os.Remove(probePath)

	return check
}

func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &os.PathError{Op: "stat", Path: path, Err: os.ErrInvalid}
	}
	return nil
}
