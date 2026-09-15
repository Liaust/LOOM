package storagedoctor

import (
	"fmt"
	"os"
	"strings"
	"time"

	loomconfig "loom.local/loom/internal/config"
	"loom.local/loom/internal/storagecatalog"
)

const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

type Options struct {
	MaxEntries      int  `json:"max_entries,omitempty"`
	MaxInspections  int  `json:"max_inspections,omitempty"`
	IncludeMount    bool `json:"include_mount,omitempty"`
	IncludeFidelity bool `json:"include_fidelity,omitempty"`
}

type Report struct {
	Status      string    `json:"status"`
	Summary     Summary   `json:"summary"`
	Checks      []Check   `json:"checks"`
	Findings    []Finding `json:"findings,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

type Summary struct {
	Checks   int `json:"checks"`
	OK       int `json:"ok"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
	Skipped  int `json:"skipped"`
}

type Check struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Summary    string    `json:"summary"`
	RepairHint string    `json:"repair_hint,omitempty"`
	Findings   []Finding `json:"findings,omitempty"`
}

type Finding struct {
	Severity   string `json:"severity"`
	CheckID    string `json:"check_id,omitempty"`
	Kind       string `json:"kind"`
	Target     string `json:"target,omitempty"`
	Message    string `json:"message"`
	RepairHint string `json:"repair_hint,omitempty"`
}

// FilesystemStatus is bounded operational truth for canonical physical roots
// and a sampled catalog. It deliberately does not walk payload trees.
type FilesystemStatus struct {
	SchemaVersion string                  `json:"schema_version"`
	Status        string                  `json:"status"`
	LayoutMode    string                  `json:"layout_mode"`
	Roots         []PhysicalRootStatus    `json:"roots"`
	Catalog       CatalogStatus           `json:"catalog"`
	Export        ExportCompatibilityInfo `json:"export"`
	GeneratedAt   time.Time               `json:"generated_at"`
}

type PhysicalRootStatus struct {
	Key       string `json:"key"`
	Path      string `json:"path"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Exists    bool   `json:"exists"`
	Directory bool   `json:"directory"`
	Symlink   bool   `json:"symlink"`
	Message   string `json:"message,omitempty"`
}

type CatalogStatus struct {
	QueryLimit int  `json:"query_limit"`
	Returned   int  `json:"returned"`
	Truncated  bool `json:"truncated"`
	Available  int  `json:"available"`
	Pending    int  `json:"pending"`
	Failed     int  `json:"failed"`
	Deleted    int  `json:"deleted"`
}

type ExportCompatibilityInfo struct {
	Deprecated          bool     `json:"deprecated"`
	Active              bool     `json:"active"`
	LegacyPath          string   `json:"legacy_path,omitempty"`
	Message             string   `json:"message"`
	ReplacementCommands []string `json:"replacement_commands"`
}

func BuildFilesystemStatus(cfg loomconfig.Config, entries []storagecatalog.Entry, queryLimit int, catalogErr error) FilesystemStatus {
	if queryLimit <= 0 {
		queryLimit = 500
	}
	status := FilesystemStatus{
		SchemaVersion: "v0.7",
		Status:        StatusOK,
		LayoutMode:    "canonical",
		Catalog:       CatalogStatus{QueryLimit: queryLimit},
		Export: ExportCompatibilityInfo{
			Deprecated: true,
			Active:     false,
			LegacyPath: strings.TrimSpace(cfg.StorageExport),
			Message:    "The generated filesystem export is retired and is not active operational state.",
			ReplacementCommands: []string{
				"loom storage filesystem status",
				"loom storage filesystem verify",
				"loom storage list",
			},
		},
		GeneratedAt: time.Now().UTC(),
	}
	storageRole := "canonical physical Storage root"
	if cfg.LegacySplitRoots {
		status.LayoutMode = "legacy_split_transition"
		storageRole = "planned canonical physical Storage target; active custody remains on explicit legacy roots"
		status.Export.Active = true
		status.Export.Message = "The generated filesystem export is a deprecated read-only pre-cutover SMB inspection path; it is not runtime custody."
	}
	for _, root := range []struct{ key, path, role string }{
		{"box", cfg.BoxPath, "human-owned canonical files"},
		{"storage", cfg.StorageRoot, storageRole},
		{"imports", cfg.ImportsRoot, "accepted Lane custody"},
		{"user_backups", cfg.UserBackupsRoot, "private backup custody"},
		{"archive", cfg.ArchiveRoot, "archive custody"},
		{"generated", cfg.GeneratedRoot, "regenerable projections"},
	} {
		rootStatus := inspectPhysicalRoot(root.key, root.path, root.role)
		if rootStatus.Status == StatusError {
			status.Status = StatusError
		} else if rootStatus.Status == StatusWarning && status.Status == StatusOK {
			status.Status = StatusWarning
		}
		status.Roots = append(status.Roots, rootStatus)
	}
	if catalogErr != nil {
		status.Catalog.Failed = 1
		status.Status = StatusError
		return status
	}
	status.Catalog.Returned = len(entries)
	status.Catalog.Truncated = len(entries) >= queryLimit
	for _, entry := range entries {
		if entry.DeletedAt != nil || entry.AvailabilityState == storagecatalog.AvailabilityStateDeleted || entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned {
			status.Catalog.Deleted++
			continue
		}
		switch entry.AvailabilityState {
		case storagecatalog.AvailabilityStateAvailable, storagecatalog.AvailabilityStateArchived, storagecatalog.AvailabilityStateSuperseded:
			status.Catalog.Available++
		case storagecatalog.AvailabilityStateFailed:
			status.Catalog.Failed++
		default:
			status.Catalog.Pending++
		}
	}
	if status.Catalog.Failed > 0 {
		status.Status = StatusError
	} else if (status.Catalog.Pending > 0 || status.Catalog.Truncated) && status.Status == StatusOK {
		status.Status = StatusWarning
	}
	return status
}

func inspectPhysicalRoot(key, pathValue, role string) PhysicalRootStatus {
	root := PhysicalRootStatus{Key: key, Path: strings.TrimSpace(pathValue), Role: role, Status: StatusOK}
	if root.Path == "" {
		root.Status = StatusError
		root.Message = "root is not configured"
		return root
	}
	info, err := os.Lstat(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			root.Status = StatusWarning
			root.Message = "root does not exist"
		} else {
			root.Status = StatusError
			root.Message = fmt.Sprintf("inspect root: %v", err)
		}
		return root
	}
	root.Exists = true
	root.Symlink = info.Mode()&os.ModeSymlink != 0
	root.Directory = info.IsDir()
	if root.Symlink || !root.Directory {
		root.Status = StatusError
		root.Message = "root must be a real directory"
	}
	return root
}

type RepairPlan struct {
	Status      string    `json:"status"`
	Source      string    `json:"source,omitempty"`
	DryRun      bool      `json:"dry_run"`
	Actions     []string  `json:"actions,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

type MountOptions struct {
	Protocol     string `json:"protocol,omitempty"`
	MountPath    string `json:"mount_path,omitempty"`
	RcloneConfig string `json:"rclone_config,omitempty"`
	RemoteName   string `json:"remote_name,omitempty"`
	SMBHost      string `json:"smb_host,omitempty"`
	SMBShare     string `json:"smb_share,omitempty"`
	SMBUser      string `json:"smb_user,omitempty"`
	Strict       bool   `json:"strict,omitempty"`
}

type MountStatus struct {
	Status       string       `json:"status"`
	Protocol     string       `json:"protocol"`
	MountPath    string       `json:"mount_path"`
	RcloneConfig string       `json:"rclone_config"`
	RemoteName   string       `json:"remote_name"`
	SMBHost      string       `json:"smb_host,omitempty"`
	SMBShare     string       `json:"smb_share,omitempty"`
	SMBUser      string       `json:"smb_user,omitempty"`
	Checks       []MountCheck `json:"checks"`
	GeneratedAt  time.Time    `json:"generated_at"`
}

type MainDocumentsBindOptions struct {
	MainDocumentsDir  string `json:"main_documents_dir,omitempty"`
	StorageExportRoot string `json:"storage_export_root,omitempty"`
}

type MainDocumentsBindStatus struct {
	Status           string       `json:"status"`
	MainDocumentsDir string       `json:"main_documents_dir"`
	TargetPath       string       `json:"target_path"`
	FindmntSource    string       `json:"findmnt_source,omitempty"`
	FindmntFSRoot    string       `json:"findmnt_fsroot,omitempty"`
	Checks           []MountCheck `json:"checks"`
	GeneratedAt      time.Time    `json:"generated_at"`
}

type MountCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}
