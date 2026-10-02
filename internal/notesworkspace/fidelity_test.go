package notesworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

type retainedTestResolver struct {
	*testResolver
	previous string
}

func (r retainedTestResolver) WithRetainedSource(ctx context.Context, id, relative, generation string, fn func(Source) error) error {
	return r.WithSource(ctx, id, relative, func(src Source) error {
		if src.Generation != generation {
			if generation != r.previous {
				return ErrMembership
			}
			src.Generation = generation
		}
		return fn(src)
	})
}

func TestFinalizedJournalEnrollmentTransitionIsObservationOnly(t *testing.T) {
	s, m, r := fixture(t)
	write(t, filepath.Join(r.root, "note.md"), "A")
	receive(t, s, "transition", readBase(t, s, "note.md"), "B")
	op := apply(t, s, "transition", Accepted)
	r.generation = "generation-2"
	s.Sources = retainedTestResolver{testResolver: r, previous: "generation-1"}
	got, err := s.Reconcile(t.Context(), "transition")
	if err != nil || got.State != "clean" {
		t.Fatalf("finalized journal lost observation: %+v %v", got, err)
	}
	contents(t, filepath.Join(r.root, "note.md"), "B")
	contents(t, filepath.Join(r.root, op.Journal, "displaced"), "A")
	op.State = Pending
	m.ops["transition"] = op
	got, err = s.Reconcile(t.Context(), "transition")
	if err != nil || got.State != Held || !strings.Contains(got.Reason, ErrMembership.Error()) {
		t.Fatalf("unfinished operation gained historical admission: %+v %v", got, err)
	}
	op.State = Accepted
	m.ops["transition"] = op
	r.active = false
	got, err = s.Reconcile(t.Context(), "transition")
	if err != nil || got.State != Held {
		t.Fatalf("withdrawn source gained historical admission: %+v %v", got, err)
	}
}

func TestReplacementPreservesAccessMetadata(t *testing.T) {
	s, _, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	fd, err := unix.Open(name, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Fchmod(fd, 0640); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fsetxattr(fd, "user.notes_workspace_test", []byte("owned metadata"), 0); err != nil {
		t.Fatal(err)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	// Use an actually permitted group, without privileges or arbitrary ownership.
	if len(groups) > 0 {
		if err := unix.Fchown(fd, os.Geteuid(), groups[len(groups)-1]); err != nil {
			t.Fatal(err)
		}
	}
	wanted, err := metadataAt(fd)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, s, "metadata", readBase(t, s, "note.md"), "B")
	op := apply(t, s, "metadata", Accepted)
	contents(t, name, "B")
	contents(t, filepath.Join(r.root, op.Journal, "displaced"), "A")
	replacement, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	actual, err := metadataAt(int(replacement.Fd()))
	if err != nil || !reflect.DeepEqual(wanted, actual) {
		t.Fatalf("metadata mismatch: %+v %+v %v", wanted, actual, err)
	}
}

func TestUnsupportedReplacementMetadataHoldsOriginal(t *testing.T) {
	s, _, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	if err := unix.Chmod(name, 04700); err != nil {
		t.Fatal(err)
	}
	receive(t, s, "metadata", readBase(t, s, "note.md"), "B")
	op := apply(t, s, "metadata", Held)
	if !strings.Contains(op.Reason, "unsupported special permission bits") {
		t.Fatalf("missing metadata hold: %s", op.Reason)
	}
	contents(t, name, "A")
	contents(t, filepath.Join(r.root, op.Journal, "proposed"), "B")
	var stat unix.Stat_t
	if err := unix.Stat(name, &stat); err != nil || uint32(stat.Mode)&07777 != 04700 {
		t.Fatalf("source metadata lost: %+v %v", stat, err)
	}
}

func TestLateRetainedWriteHasDurableSeparateRecovery(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	writer, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	receive(t, s, "late", readBase(t, s, "note.md"), "B")
	accepted := apply(t, s, "late", Accepted)
	clean, err := s.Reconcile(t.Context(), "late")
	if err != nil || clean.State != "clean" || len(m.recoveries) != 0 {
		t.Fatalf("clean reconcile: %+v %v", clean, err)
	}
	if _, err := writer.WriteAt([]byte("C"), 0); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	recovered, err := (Service{Store: m, Sources: r}).Reconcile(t.Context(), "late")
	if err != nil || recovered.State != Conflict || string(recovered.Retained) != "C" || string(recovered.Current) != "B" {
		t.Fatalf("late recovery: %+v %v", recovered, err)
	}
	if len(m.recoveries) != 1 || m.recoveries[recovered.ID].State != Conflict {
		t.Fatal("missing durable recovery")
	}
	// Repeated observation deduplicates, but a later variant remains new evidence.
	again, err := s.Reconcile(t.Context(), "late")
	if err != nil || again.ID != recovered.ID || len(m.recoveries) != 1 {
		t.Fatalf("reconcile replay: %+v %v", again, err)
	}
	if _, err := writer.WriteAt([]byte("D"), 0); err != nil {
		t.Fatal(err)
	}
	next, err := s.Reconcile(t.Context(), "late")
	if err != nil || next.ID == recovered.ID || len(m.recoveries) != 2 {
		t.Fatalf("later variant: %+v %v", next, err)
	}
	if replay := apply(t, s, "late", Accepted); !reflect.DeepEqual(accepted, replay) {
		t.Fatal("recovery changed accepted receipt")
	}
	contents(t, name, "B")
	contents(t, filepath.Join(r.root, accepted.Journal, "displaced"), "D")
	r.active = false
	held, err := s.Reconcile(t.Context(), "late")
	if err != nil || held.State != Held || len(m.recoveries) != 3 {
		t.Fatalf("withdrawn recovery: %+v %v", held, err)
	}
}

func TestUnreadableRetainedVariantIsVisibleAndPreserved(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	receive(t, s, "late", readBase(t, s, "note.md"), "B")
	accepted := apply(t, s, "late", Accepted)
	retained := filepath.Join(r.root, accepted.Journal, "displaced")
	write(t, retained, "\x00binary")
	held, err := s.Reconcile(t.Context(), "late")
	if err != nil || held.State != Held || !strings.Contains(held.Reason, "retained inode unreadable") || len(m.recoveries) != 1 {
		t.Fatalf("unsupported variant silently ignored: %+v %v", held, err)
	}
	contents(t, name, "B")
	contents(t, retained, "\x00binary")
}

func TestRecoveryPersistenceFailureDoesNotAcknowledgeObservation(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	receive(t, s, "late", readBase(t, s, "note.md"), "B")
	accepted := apply(t, s, "late", Accepted)
	retained := filepath.Join(r.root, accepted.Journal, "displaced")
	write(t, retained, "C")
	failure := errors.New("recovery persistence unavailable")
	m.beforeRecovery = func(Recovery) error { return failure }
	if _, err := s.Reconcile(t.Context(), "late"); !errors.Is(err, failure) {
		t.Fatalf("missing persistence failure: %v", err)
	}
	if len(m.recoveries) != 0 {
		t.Fatal("failed persistence reported as durable")
	}
	contents(t, name, "B")
	contents(t, retained, "C")
	m.beforeRecovery = nil
	recovery, err := s.Reconcile(t.Context(), "late")
	if err != nil || recovery.State != Conflict || len(m.recoveries) != 1 {
		t.Fatalf("recovery retry: %+v %v", recovery, err)
	}
	if replay := apply(t, s, "late", Accepted); !reflect.DeepEqual(accepted, replay) {
		t.Fatal("failed observation changed receipt")
	}
}

func TestReconcileRestoredEditAlreadyEqualsProposal(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	receive(t, s, "save", readBase(t, s, "note.md"), "B")
	failed := false
	m.beforeSave = func(op Operation) error {
		if !failed && op.ObservedExists {
			failed = true
			return errors.New("interrupted save")
		}
		return nil
	}
	apply(t, s, "save", Held)
	contents(t, name, "A")
	m.beforeSave = nil
	write(t, name, "B")
	accepted := apply(t, s, "save", Accepted)
	if accepted.Reason != "already equals proposal" {
		t.Fatalf("unexpected receipt: %+v", accepted)
	}
	observation, err := s.Reconcile(t.Context(), "save")
	if err != nil || observation.State != "clean" {
		t.Fatalf("restored inode incorrectly considered lost: %+v %v", observation, err)
	}
}
