package cloudstorage

import (
	"context"
	"fmt"
)

const (
	SnapshotBackendLegacyTree = "legacy_tree"
	SnapshotBackendBorg       = "borg"
)

type SnapshotBackend interface {
	BackendKind() string
	Push(ctx context.Context, input SnapshotPushInput) (SnapshotPushResult, error)
	List(ctx context.Context, input SnapshotListInput) (SnapshotListResult, error)
	Verify(ctx context.Context, input SnapshotVerifyInput) (SnapshotVerifyResult, error)
	Fetch(ctx context.Context, input SnapshotFetchInput) (SnapshotFetchResult, error)
	PlanRetention(ctx context.Context, input SnapshotRetentionInput) (SnapshotRetentionPlan, error)
	ApplyRetention(ctx context.Context, input SnapshotRetentionApplyInput) (SnapshotRetentionApplyResult, error)
}

func resolveSnapshotBackend(cfg Config, driver Driver) (SnapshotBackend, error) {
	switch cfg.Snapshots.Backend {
	case "", SnapshotBackendLegacyTree:
		return LegacyTreeSnapshotBackend{Driver: driver}, nil
	case SnapshotBackendBorg:
		return NewBorgSnapshotBackend(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported cloud snapshot backend %q", cfg.Snapshots.Backend)
	}
}
