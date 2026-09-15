package cloudstorage

import (
	"context"
	"time"
)

type Driver interface {
	Status(ctx context.Context) (RemoteStatus, error)
	List(ctx context.Context, prefix string) ([]RemoteEntry, error)
	CopyToRemote(ctx context.Context, localPath, remotePath string, opts CopyOptions) (CopyResult, error)
	CopyFromRemote(ctx context.Context, remotePath, localPath string, opts CopyOptions) (CopyResult, error)
	Check(ctx context.Context, localPath, remotePath string) (CheckResult, error)
}

type RemoteProber interface {
	Probe(ctx context.Context, prefix string) (RemoteProbeResult, error)
}

type RemoteMover interface {
	MoveRemote(ctx context.Context, fromRemotePath, toRemotePath string, opts CopyOptions) (CopyResult, error)
}

// OptionedChecker is required when a backend needs transport-specific
// fidelity guarantees (currently symlink and Unix metadata preservation for
// legacy-tree backup snapshots). Plain Check remains available for callers
// with no such contract.
type OptionedChecker interface {
	CheckWithOptions(ctx context.Context, localPath, remotePath string, opts CheckOptions) (CheckResult, error)
}

func ProbeRemote(ctx context.Context, driver Driver, prefix string) (RemoteProbeResult, error) {
	if prober, ok := driver.(RemoteProber); ok {
		return prober.Probe(ctx, prefix)
	}
	remote, err := driver.Status(ctx)
	if err != nil {
		return RemoteProbeResult{Status: remote, ErrorClass: ClassifyRemoteError(err)}, err
	}
	entries, err := driver.List(ctx, prefix)
	if err != nil {
		return RemoteProbeResult{Status: remote, ErrorClass: ClassifyRemoteError(err)}, err
	}
	if remote.Entries == 0 {
		remote.Entries = len(entries)
	}
	return RemoteProbeResult{Status: remote, Entries: entries}, nil
}

func driverWithoutRemoteLock(driver Driver) Driver {
	switch d := driver.(type) {
	case RcloneDriver:
		d.DisableRemoteLock = true
		return d
	case *RcloneDriver:
		clone := *d
		clone.DisableRemoteLock = true
		return &clone
	default:
		return driver
	}
}

type RemoteStatus struct {
	Reachable  bool      `json:"reachable"`
	RemoteURI  string    `json:"remote_uri"`
	CheckedAt  time.Time `json:"checked_at"`
	Entries    int       `json:"entries"`
	DurationMS int64     `json:"duration_ms,omitempty"`
}

type RemoteEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

type CopyOptions struct {
	DryRun           bool `json:"dry_run,omitempty"`
	Checksum         bool `json:"checksum,omitempty"`
	SingleFile       bool `json:"single_file,omitempty"`
	PreserveLinks    bool `json:"preserve_links,omitempty"`
	PreserveMetadata bool `json:"preserve_metadata,omitempty"`
}

type CheckOptions struct {
	PreserveLinks    bool `json:"preserve_links,omitempty"`
	PreserveMetadata bool `json:"preserve_metadata,omitempty"`
}

type CopyResult struct {
	Command    string    `json:"command"`
	Source     string    `json:"source"`
	Dest       string    `json:"dest"`
	DryRun     bool      `json:"dry_run"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
	Output     string    `json:"output,omitempty"`
}

type CheckResult struct {
	Matched    bool      `json:"matched"`
	LocalPath  string    `json:"local_path"`
	RemotePath string    `json:"remote_path"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
	Output     string    `json:"output,omitempty"`
}

type RootStatus struct {
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	RemoteURI string `json:"remote_uri"`
	Exists    bool   `json:"exists"`
	Status    string `json:"status"`
}
