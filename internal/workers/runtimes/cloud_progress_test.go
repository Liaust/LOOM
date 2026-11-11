package runtimes

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCloudArchiveBudgetReservesShutdownBeforeWorkerDeadline(t *testing.T) {
	outer := time.Now().Add(10 * time.Minute)
	ctx, cancel, err := cloudArchiveWorkContext(context.Background(), outer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(outer.Add(-cloudArchiveCompletionReserve)) {
		t.Fatal("cleanup reservation lost", deadline)
	}
	short, shortCancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer shortCancel()
	if _, _, err := cloudArchiveWorkContext(short, time.Time{}, time.Hour); err == nil {
		t.Fatal("started without shutdown budget")
	}
	bounded, stop, err := cloudArchiveWorkContext(context.Background(), time.Time{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	limit, _ := bounded.Deadline()
	if remaining := time.Until(limit); remaining <= 0 || remaining > time.Minute {
		t.Fatal("custom work ceiling ignored", remaining)
	}
	var cfg struct {
		MaxRuntimeMS int `json:"max_runtime_ms"`
	}
	r := CloudSnapshotUploadRuntime{}
	if err := json.Unmarshal(r.DefaultConfig(), &cfg); err != nil {
		t.Fatal(err)
	}
	if time.Duration(cfg.MaxRuntimeMS)*time.Millisecond+cloudArchiveCompletionReserve != time.Duration(cloudSnapshotUploadRunTimeoutSeconds)*time.Second {
		t.Fatal("seeded clocks disagree")
	}
}
