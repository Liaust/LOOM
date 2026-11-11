package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/filepolicy"
)

func boundInitFixture(t *testing.T, id string) Resolved {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(ResolveInput{BoxID: id, RootRef: "box:" + id, ConfiguredPath: filepath.Join(base, "company"), RuntimeStateRoot: filepath.Join(base, "state", id), NodeID: "main", NodeRole: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeBoundInitFixtureFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0660); err != nil {
		t.Fatal(err)
	}
}

func TestInitBoundBoxUsesSelectedIDAndPreservesFiles(t *testing.T) {
	resolved := boundInitFixture(t, "box_selected")
	note := filepath.Join(resolved.RootPath, "Notes", "user.md")
	journal := filepath.Join(resolved.RuntimeStateRoot, "journal.json")
	writeBoundInitFixtureFile(t, note, "existing company note")
	writeBoundInitFixtureFile(t, journal, "existing runtime journal")
	result, err := Init(InitInput{Resolved: resolved})
	if err != nil || result.StatusAfter.Contract == nil || result.StatusAfter.Contract.BoxID != resolved.BoxID {
		t.Fatalf("selected ID was allocated again: %+v %v", result, err)
	}
	lockPath := filepath.Join(resolved.RootPath, metadataDir, "tmp", "box-init.lock")
	info, err := os.Stat(lockPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0660 {
		t.Fatalf("lock is not group-compatible: %v %v", info, err)
	}
	if filepolicy.EvaluateBuiltIn(filepolicy.ProfileExact, ".loom/tmp/box-init.lock").Included {
		t.Fatal("internal initialization lock is not ignored")
	}
	before, err := os.ReadFile(ContractPath(resolved.RootPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(ContractPath(resolved.RootPath))
	if err != nil || string(after) != string(before) {
		t.Fatal("repeat initialization changed the descriptor")
	}
	for path, expected := range map[string]string{note: "existing company note", journal: "existing runtime journal"} {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != expected {
			t.Fatalf("initialization changed retained file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(resolved.RootPath, metadataDir, "state")); !os.IsNotExist(err) {
		t.Fatalf("initialization created legacy runtime state: %v", err)
	}
}

func TestInitBoundBoxRejectsFactoryMismatchBeforeWrites(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		resolved := boundInitFixture(t, "box_selected")
		calls := 0
		_, err := Init(InitInput{Resolved: resolved, DryRun: dryRun, NewBoxID: func() string {
			calls++
			return "box_other"
		}})
		if err == nil || !strings.Contains(err.Error(), "factory disagrees") || calls != 1 {
			t.Fatalf("mismatching factory accepted: %v calls=%d", err, calls)
		}
		if _, err := os.Lstat(resolved.RootPath); !os.IsNotExist(err) {
			t.Fatalf("factory mismatch created source/lock directories: %v", err)
		}
		if _, err := os.Lstat(resolved.RuntimeStateRoot); !os.IsNotExist(err) {
			t.Fatalf("factory mismatch created runtime state: %v", err)
		}
	}
}

func TestInitBoundBoxValidatesMatchingFactoryOnce(t *testing.T) {
	resolved := boundInitFixture(t, "box_selected")
	calls := 0
	result, err := Init(InitInput{Resolved: resolved, NewBoxID: func() string {
		calls++
		return resolved.BoxID
	}})
	if err != nil || calls != 1 || result.StatusAfter.Contract == nil || result.StatusAfter.Contract.BoxID != resolved.BoxID {
		t.Fatalf("factory was reused or selected identity changed: %+v %v calls=%d", result, err, calls)
	}
}

func TestInitBoundBoxRejectsExistingDescriptorIdentity(t *testing.T) {
	resolved := boundInitFixture(t, "box_original")
	if _, err := Init(InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ContractPath(resolved.RootPath))
	if err != nil {
		t.Fatal(err)
	}
	resolved.BoxID, resolved.RootRef = "box_other", "box:box_other"
	for _, dryRun := range []bool{true, false} {
		if _, err := Init(InitInput{Resolved: resolved, DryRun: dryRun, RetireLane: true}); err == nil || !strings.Contains(err.Error(), "descriptor identity disagrees") {
			t.Fatalf("repair accepted foreign descriptor identity: %v", err)
		}
	}
	after, err := os.ReadFile(ContractPath(resolved.RootPath))
	if err != nil || string(after) != string(before) {
		t.Fatal("refused repair changed the existing descriptor")
	}
	if _, err := os.Stat(filepath.Join(resolved.RootPath, metadataDir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("refused repair created descriptor before-images: %v", err)
	}
}

func TestInitBoundBoxDryRunCreatesNoLock(t *testing.T) {
	resolved := boundInitFixture(t, "box_selected")
	result, err := Init(InitInput{Resolved: resolved, DryRun: true})
	if err != nil || !result.DryRun || len(result.PlannedDirs) == 0 {
		t.Fatalf("bound dry run failed: %+v %v", result, err)
	}
	if _, err := os.Lstat(resolved.RootPath); !os.IsNotExist(err) {
		t.Fatalf("dry run created root, metadata or lock: %v", err)
	}
}

func TestInitBoundBoxSerializesCompetingIdentities(t *testing.T) {
	first := boundInitFixture(t, "box_first")
	second := first
	second.BoxID, second.RootRef = "box_second", "box:box_second"
	second.RuntimeStateRoot = filepath.Join(filepath.Dir(first.RuntimeStateRoot), second.BoxID)
	for _, path := range []string{first.RuntimeStateRoot, second.RuntimeStateRoot} {
		writeBoundInitFixtureFile(t, filepath.Join(path, "journal.json"), "retained "+filepath.Base(path))
	}
	locked, release, secondStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	type outcome struct {
		result InitResult
		err    error
	}
	firstDone, secondDone := make(chan outcome, 1), make(chan outcome, 1)
	go func() {
		result, err := Init(InitInput{Resolved: first, Now: func() time.Time {
			close(locked)
			<-release
			return time.Now().UTC()
		}})
		firstDone <- outcome{result, err}
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("first init did not acquire the lock")
	}
	lockPath := filepath.Join(first.RootPath, metadataDir, "tmp", "box-init.lock")
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	unix.Close(fd)
	if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("first init did not hold a real filesystem lock: %v", err)
	}
	// A dry run must neither wait for nor mutate the real initializer's lock.
	if _, err := Init(InitInput{Resolved: second, DryRun: true}); err != nil {
		t.Fatalf("dry run could not inspect a locked root: %v", err)
	}
	go func() {
		result, err := Init(InitInput{Resolved: second, NewBoxID: func() string {
			close(secondStarted)
			return second.BoxID
		}})
		secondDone <- outcome{result, err}
	}()
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second init did not start")
	}
	unlock()
	for i, done := range []chan outcome{firstDone, secondDone} {
		select {
		case got := <-done:
			if i == 0 && (got.err != nil || got.result.StatusAfter.Contract == nil || got.result.StatusAfter.Contract.BoxID != first.BoxID) {
				t.Fatalf("first initializer failed: %+v %v", got.result, got.err)
			}
			if i == 1 && (got.err == nil || !strings.Contains(got.err.Error(), "descriptor identity disagrees")) {
				t.Fatalf("competing initializer rebound the Box: %+v %v", got.result, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("initializer did not release its file lock")
		}
	}
	contract, err := LoadContract(ContractPath(first.RootPath))
	if err != nil || contract.BoxID != first.BoxID {
		t.Fatalf("winning descriptor was overwritten: %+v %v", contract, err)
	}
	for _, root := range []string{first.RuntimeStateRoot, second.RuntimeStateRoot} {
		if raw, err := os.ReadFile(filepath.Join(root, "journal.json")); err != nil || string(raw) != "retained "+filepath.Base(root) {
			t.Fatalf("competing initializer changed retained state: %s %v", root, err)
		}
	}
}

func TestInitBoundBoxRefusesAliasedLockPaths(t *testing.T) {
	for _, level := range []string{"root", "metadata", "tmp", "lock", "hardlink", "descriptor"} {
		t.Run(level, func(t *testing.T) {
			resolved := boundInitFixture(t, "box_selected")
			target := filepath.Join(filepath.Dir(resolved.RootPath), "outside")
			writeBoundInitFixtureFile(t, filepath.Join(target, "guard"), "unchanged")
			var path, destination string
			switch level {
			case "root":
				path, destination = resolved.RootPath, target
			case "metadata":
				path, destination = filepath.Join(resolved.RootPath, metadataDir), target
			case "tmp":
				path, destination = filepath.Join(resolved.RootPath, metadataDir, "tmp"), target
			case "lock", "hardlink":
				path, destination = filepath.Join(resolved.RootPath, metadataDir, "tmp", "box-init.lock"), filepath.Join(target, "guard")
			case "descriptor":
				path, destination = ContractPath(resolved.RootPath), filepath.Join(target, "guard")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0770); err != nil {
				t.Fatal(err)
			}
			var err error
			if level == "hardlink" {
				err = os.Link(destination, path)
			} else {
				err = os.Symlink(destination, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Init(InitInput{Resolved: resolved}); err == nil {
				t.Fatal("bound initialization accepted an aliased path")
			}
			if raw, err := os.ReadFile(filepath.Join(target, "guard")); err != nil || string(raw) != "unchanged" {
				t.Fatal("refused initialization changed the aliased target")
			}
		})
	}
}
