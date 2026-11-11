package projects

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

const dirtyProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAX"

type contextChangeSourcesFixture struct {
	sources []ProjectContextChangeSource
}

func (f *contextChangeSourcesFixture) ListProjectContextChangeSources(_ context.Context, _, after string, limit int) ([]ProjectContextChangeSource, error) {
	result := []ProjectContextChangeSource{}
	for _, source := range f.sources {
		if source.ProjectID > after && len(result) < limit {
			result = append(result, source)
		}
	}
	return result, nil
}

func TestProjectContextChangesIdleSafetyGenerationAndWithdrawal(t *testing.T) {
	now := time.Now().UTC()
	f := &contextChangeSourcesFixture{sources: []ProjectContextChangeSource{{ProjectID: dirtyProjectID, Generation: "registered"}}}
	c := NewProjectContextChanges(f, "main", nil)
	seen := map[string]ProjectContextObservation{}
	page, _, err := c.Pending(t.Context(), seen, now, false)
	if err != nil || len(page) != 1 {
		t.Fatal(page, err)
	}
	seen[dirtyProjectID] = ProjectContextObservation{Generation: page[0].Generation, AttemptedAt: now, Status: "refreshed"}
	page, next, err := c.Pending(t.Context(), seen, now.Add(time.Minute), false)
	if err != nil || len(page) != 0 || next == nil || !next.Equal(now.Add(ProjectContextSafetyInterval)) {
		t.Fatal("idle probe invented work", page, next, err)
	}
	page, _, err = c.Pending(t.Context(), seen, now.Add(ProjectContextSafetyInterval), false)
	if err != nil || len(page) != 1 || page[0].Reason != "safety_reconcile" {
		t.Fatal("missed-event repair lost", page, err)
	}
	f.sources[0].Generation = "new-registration"
	page, _, err = c.Pending(t.Context(), seen, now.Add(ProjectContextSafetyInterval+time.Minute), false)
	if err != nil || len(page) != 1 || page[0].Generation == seen[dirtyProjectID].Generation {
		t.Fatal("registry change lost", page, err)
	}
	f.sources = nil
	page, _, err = c.Pending(t.Context(), seen, now.Add(ProjectContextSafetyInterval+2*time.Minute), false)
	if err != nil || len(page) != 1 || page[0].Reason != "source_removed" {
		t.Fatal("source withdrawal lost", page, err)
	}
	seen[dirtyProjectID] = ProjectContextObservation{Generation: page[0].Generation, AttemptedAt: now, Status: "refreshed"}
	page, _, err = c.Pending(t.Context(), seen, now.Add(time.Hour), false)
	if err != nil || len(page) != 0 {
		t.Fatal("withdrawal repeated forever", page, err)
	}
}

func TestProjectContextChangesNativeAtomicSaveAndNoFollow(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".project"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".project", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".project", "z-decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &contextChangeSourcesFixture{sources: []ProjectContextChangeSource{{ProjectID: dirtyProjectID, Root: root, Generation: "registered"}}}
	wakes := make(chan struct{}, 1)
	c := NewProjectContextChanges(f, "main", func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c.Start(ctx)
	seen := map[string]ProjectContextObservation{}
	page, _, err := c.Pending(ctx, seen, time.Now(), false)
	if err != nil || len(page) != 1 || c.WatchStatus() != "native" {
		t.Fatal(page, c.WatchStatus(), err)
	}
	seen[dirtyProjectID] = ProjectContextObservation{Generation: page[0].Generation, AttemptedAt: time.Now(), Status: "refreshed"}
	c.mu.Lock()
	_, followed := c.watched[filepath.Join(root, ".project", "linked")]
	_, siblingWatched := c.watched[filepath.Join(root, ".project", "z-decisions")]
	c.mu.Unlock()
	if followed || !siblingWatched {
		t.Fatal("followed a symlink or skipped its later siblings")
	}
	name := filepath.Join(root, ".project", "STATE.md")
	if err := os.WriteFile(name+".tmp", []byte("# Updated state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name+".tmp", name); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakes:
	case <-time.After(3 * time.Second):
		t.Fatal("native atomic save did not wake context owner")
	}
	page, _, err = c.Pending(ctx, seen, time.Now(), false)
	if err != nil || len(page) != 1 || page[0].Generation == seen[dirtyProjectID].Generation {
		t.Fatal("native edit was acknowledged without refresh", page, err)
	}
}

func TestProjectContextChangesArchivedUnavailableRestartAndConcurrentHints(t *testing.T) {
	now := time.Now().UTC()
	f := &contextChangeSourcesFixture{sources: []ProjectContextChangeSource{{ProjectID: dirtyProjectID, Root: "/missing", Generation: "archive", Archived: true}}}
	c := NewProjectContextChanges(f, "main", nil)
	page, _, _ := c.Pending(t.Context(), nil, now, false)
	seen := map[string]ProjectContextObservation{dirtyProjectID: {Generation: page[0].Generation, AttemptedAt: now, Status: "refreshed"}}
	page, next, err := c.Pending(t.Context(), seen, now.Add(time.Hour), false)
	if err != nil || len(page) != 0 || next != nil {
		t.Fatal("unchanged archive was refreshed", page, next, err)
	}
	observed := seen[dirtyProjectID]
	observed.Status = "unavailable"
	seen[dirtyProjectID] = observed
	page, next, err = c.Pending(t.Context(), seen, now.Add(time.Minute), false)
	if err != nil || len(page) != 0 || next == nil {
		t.Fatal("unavailable source did not back off", page, next, err)
	}
	restarted := NewProjectContextChanges(f, "main", nil)
	page, _, err = restarted.Pending(t.Context(), seen, now.Add(time.Minute), false)
	if err != nil || len(page) != 1 {
		t.Fatal("restart did not repair missed hints", page, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.invalidateAll() }()
	}
	wg.Wait()
	page, _, err = c.Pending(t.Context(), seen, now.Add(2*time.Minute), false)
	if err != nil || len(page) != 1 {
		t.Fatal("overflow/reconnect repair lost", page, err)
	}
	if contextEventRelevant("/source", "/source/notes/unrelated.md") || !contextEventRelevant("/source", "/source/.project/STATE.md") ||
		!contextEventRelevant("/source", "/source/AGENTS.md") {
		t.Fatal("unrelated project payload invalidated context")
	}
	c.observeEvent(fsnotify.Event{Name: "/missing", Op: fsnotify.Rename})
}
