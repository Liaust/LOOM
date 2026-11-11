package runtimes

import (
	"errors"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/boxregistry"
)

func TestMainBackupBoxContextProducerPreservesUpdateState(t *testing.T) {
	runtime := MainBackupRuntime{DataDir: t.TempDir()}
	cfg := runtime.defaultMainBackupConfig()
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	raw, err := runtime.operationalUpdateState(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if context, err := backupstrategy.ReadBoxRecoveryContext(raw); err != nil || context != nil {
		t.Fatalf("legacy producer changed: %+v %v", context, err)
	}
	calls := 0
	runtime = runtime.WithBoxCatalog(func() (boxregistry.Catalog, error) {
		calls++
		return boxregistry.Catalog{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal", Boxes: []boxregistry.Box{
			{ID: "box_personal", Key: "personal", RootPath: "/srv/loom/box", Node: "main", Primary: true, Available: true, RootRef: "box"},
			{Key: "company", RootPath: "/srv/loom/boxes/company", Node: "main", Reason: "descriptor_missing"},
		}}, nil
	})
	raw, err = runtime.operationalUpdateState(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	context, err := backupstrategy.ReadBoxRecoveryContext(raw)
	if err != nil || context == nil || len(context.Boxes) != 2 || calls != 1 || context.Boxes[1].Available || context.Boxes[1].Reason != "descriptor_missing" {
		t.Fatalf("catalog content missing or unactivated Box claimed present: %+v %v calls=%d", context, err, calls)
	}
	if !strings.Contains(string(raw), cfg.UpdateStateDir) {
		t.Fatal("existing update-state fields were replaced")
	}
	runtime.BoxCatalog = func() (boxregistry.Catalog, error) { return boxregistry.Catalog{}, errors.New("invalid catalog") }
	if _, err := runtime.operationalUpdateState(cfg, now); err == nil {
		t.Fatal("invalid catalog silently omitted")
	}
}
