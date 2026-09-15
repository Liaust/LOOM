package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestLocalSyncConcurrentTransactions(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	const count = 24
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := store.QueueWatchedRootDeletion(config, state, watchedroots.OutputAction{RootKey: fmt.Sprintf("root%d", i), RelativePath: "note.md", LocalObjectID: fmt.Sprintf("local%d", i), MainObjectID: fmt.Sprintf("main%d", i)})
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	deletions, err := store.LoadSyncDeletions()
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := store.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(deletions) != count || len(outbox) != count {
		t.Fatalf("lost transactions: %d records / %d outbox", len(deletions), len(outbox))
	}
	sequences := map[int64]bool{}
	for _, d := range deletions {
		if sequences[d.LocalSequence] {
			t.Fatal("duplicate sequence")
		}
		sequences[d.LocalSequence] = true
		found := false
		for _, o := range outbox {
			if o.LocalRef == d.LocalDeletionID && o.PayloadHash == rawJSONHash(localDeletionPayload(d)) {
				found = true
			}
		}
		if !found {
			t.Fatal("missing companion")
		}
	}
}

func TestLocalSyncLockCrossProcess(t *testing.T) {
	if path := os.Getenv("LOOM_SYNC_LOCK_FIXTURE"); path != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, unlock, err := (Store{DataDir: path}).lockLocalSync(ctx)
		if err == nil {
			unlock()
			t.Fatal("child bypassed held queue lock")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("child failed for a reason other than lock contention: %v", err)
		}
		return
	}
	store, _, _, _ := localSyncObjectTestStore(t)
	locked, unlock, err := store.lockLocalSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := locked.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLocalSyncLockCrossProcess$")
	cmd.Env = append(os.Environ(), "LOOM_SYNC_LOCK_FIXTURE="+store.DataDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v: %s", err, output)
	}
}
