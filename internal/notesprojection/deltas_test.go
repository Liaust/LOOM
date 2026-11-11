package notesprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

type deltaSources struct {
	sources    map[string]SourceObject
	withdrawn  map[string]string
	reads      [][]string
	fullReads  int
	refuseFull bool
}

func (p *deltaSources) ListProjectionSources(context.Context, SourceListInput) ([]SourceObject, error) {
	p.fullReads++
	if p.refuseFull {
		return nil, errors.New("unexpected full source enumeration")
	}
	var sources []SourceObject
	for _, s := range p.sources {
		sources = append(sources, s)
	}
	sort.Slice(sources, func(i, j int) bool { return projectionSourceOrder(sources[i]) < projectionSourceOrder(sources[j]) })
	return sources, nil
}
func (p *deltaSources) ReadProjectionSources(_ context.Context, ids []string) (SourceDelta, error) {
	p.reads = append(p.reads, append([]string(nil), ids...))
	result := SourceDelta{Withdrawn: map[string]string{}}
	for _, id := range ids {
		if s, ok := p.sources[id]; ok {
			result.Sources = append(result.Sources, s)
		} else if reason := p.withdrawn[id]; reason != "" {
			result.Withdrawn[id] = reason
		}
	}
	return result, nil
}
func deltaFixture(t *testing.T) (Service, *deltaSources) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projection")
	t.Cleanup(func() { makeProjectionWritableForCleanup(t, root) })
	p := &deltaSources{sources: map[string]SourceObject{}, withdrawn: map[string]string{}}
	for _, id := range []string{"a", "b", "unrelated"} {
		relative := "collision.md"
		if id == "unrelated" {
			relative = "untouched/note.md"
		}
		payload := "old-" + id
		size := int64(len(payload))
		p.sources[id] = SourceObject{KnowledgeObjectID: id, NotesSourceRootID: "root", RootKind: RootKindBoxNotes, SourceNodeKey: "main", RelativePath: relative, SourcePath: writeProjectionSource(t, id+".md", payload), SourceHash: "hash-" + id, SourceRevision: "rev-1", SizeBytes: &size}
	}
	s := NewService(p, root)
	s.Now = fixedProjectionNow
	if _, err := s.Rebuild(t.Context(), RebuildInput{}); err != nil {
		t.Fatal(err)
	}
	p.refuseFull = true
	return s, p
}
func deltaManifest(t *testing.T, s Service) Manifest {
	t.Helper()
	m, found, err := ReadManifest(s.ProjectionRoot)
	if err != nil || !found {
		t.Fatalf("manifest: %v", err)
	}
	return m
}
func deltaEntries(m Manifest) map[string]ProjectionEntry {
	out := map[string]ProjectionEntry{}
	for _, e := range m.Entries {
		out[e.KnowledgeObjectID] = e
	}
	return out
}

func TestProjectionDeltaTouchesOnlyChangedIdentity(t *testing.T) {
	s, p := deltaFixture(t)
	before := deltaEntries(deltaManifest(t, s))
	// A full filesystem permission walk would change this unrelated file.
	if err := os.Chmod(before["unrelated"].FilesystemPath, 0o640); err != nil {
		t.Fatal(err)
	}
	source := p.sources["a"]
	payload := "new-a"
	if err := os.WriteFile(source.SourcePath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	source.SourceHash = "hash-new"
	source.SourceRevision = "rev-2"
	p.sources["a"] = source
	changed, err := s.ApplyDelta(t.Context(), []string{"a"})
	if err != nil || !changed {
		t.Fatalf("delta: %t %v", changed, err)
	}
	after := deltaEntries(deltaManifest(t, s))
	if !reflect.DeepEqual(p.reads, [][]string{{"a"}}) || p.fullReads != 1 {
		t.Fatalf("unbounded read: %+v", p.reads)
	}
	if !reflect.DeepEqual(before["b"], after["b"]) || !reflect.DeepEqual(before["unrelated"], after["unrelated"]) {
		t.Fatal("unrelated manifest entry changed")
	}
	payloadBytes, err := os.ReadFile(after["a"].FilesystemPath)
	if err != nil || string(payloadBytes) != payload {
		t.Fatalf("payload: %s %v", payloadBytes, err)
	}
	if info, err := os.Stat(after["unrelated"].FilesystemPath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatal("delta walked unrelated file")
	}
	if info, err := os.Stat(s.ProjectionRoot); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatal("root became writable")
	}
}

func TestProjectionDeltaRenameAndWithdrawalCollisions(t *testing.T) {
	s, p := deltaFixture(t)
	before := deltaEntries(deltaManifest(t, s))
	source := p.sources["a"]
	source.RelativePath = "renamed/new.md"
	p.sources["a"] = source
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	after := deltaEntries(deltaManifest(t, s))
	if after["b"].ProjectedPath != before["a"].ProjectedPath || !reflect.DeepEqual(p.reads, [][]string{{"a"}, {"b"}}) {
		t.Fatalf("collision not recalculated: %+v %+v", after, p.reads)
	}
	delete(p.sources, "a")
	p.withdrawn["a"] = "source_withdrawn"
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := deltaEntries(deltaManifest(t, s))["a"]; exists {
		t.Fatal("withdrawn identity survived")
	}
	if _, err := os.Stat(after["a"].FilesystemPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("withdrawn payload survived: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(after["a"].FilesystemPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("affected empty parent survived")
	}
	if payload, err := os.ReadFile(after["b"].FilesystemPath); err != nil || string(payload) != "old-b" {
		t.Fatal("collision peer payload lost")
	}
}

func TestProjectionDeltaIncompleteEvidencePreservesPayloadAndManifest(t *testing.T) {
	s, p := deltaFixture(t)
	before := deltaManifest(t, s)
	entry := deltaEntries(before)["a"]
	delete(p.sources, "a") // No authoritative withdrawal evidence.
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); !errors.Is(err, ErrDeltaUncertain) {
		t.Fatalf("uncertainty ignored: %v", err)
	}
	if !reflect.DeepEqual(before, deltaManifest(t, s)) {
		t.Fatal("incomplete inventory changed manifest")
	}
	if _, err := os.Stat(entry.FilesystemPath); err != nil {
		t.Fatal("incomplete inventory pruned payload")
	}
	p.refuseFull = false
	if _, err := s.Rebuild(t.Context(), RebuildInput{}); !errors.Is(err, ErrDeltaUncertain) {
		t.Fatalf("full repair pruned uncertain omitted identity: %v", err)
	}
	if !reflect.DeepEqual(before, deltaManifest(t, s)) {
		t.Fatal("full repair changed uncertain manifest")
	}
}

func TestProjectionDeltaUnavailableEvidencePrecedesManifestRead(t *testing.T) {
	s, p := deltaFixture(t)
	source := p.sources["a"]
	delete(p.sources, "a")
	// An unreadable generated manifest proves this refusal never decoded it or
	// requested a global repair on the strength of unrelated unavailable work.
	file := manifestPath(s.ProjectionRoot)
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("{invalid retained manifest")
	if err := os.WriteFile(file, invalid, 0o444); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.ApplyDelta(t.Context(), []string{"a"}); !errors.Is(err, ErrDeltaUncertain) || errors.Is(err, ErrProjectionReconcileRequired) {
			t.Fatalf("unavailable evidence loaded the manifest: %v", err)
		}
	}
	if actual, err := os.ReadFile(file); err != nil || string(actual) != string(invalid) || p.fullReads != 1 {
		t.Fatal("metadata refusal changed or enumerated the generated view", err)
	}
	p.sources["a"] = source
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); !errors.Is(err, ErrProjectionReconcileRequired) {
		t.Fatalf("eligible source failed to reach manifest validation: %v", err)
	}
}

func TestProjectionDeltaPayloadManifestReplay(t *testing.T) {
	s, p := deltaFixture(t)
	before := deltaEntries(deltaManifest(t, s))
	source := p.sources["a"]
	source.SourceRevision = "rev-2"
	source.SourceHash = "new-hash"
	p.sources["a"] = source
	if err := os.WriteFile(source.SourcePath, []byte("new-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after one atomic payload replacement, before manifest.
	marker, _ := json.Marshal([]string{"a"})
	if err := writeFileAtomic(s.ProjectionRoot, filepath.Join(s.ProjectionRoot, ".loom", "rebuild-pending"), marker, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(s.ProjectionRoot, before["a"].FilesystemPath, []byte("new-a"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyDelta(t.Context(), []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if deltaEntries(deltaManifest(t, s))["a"].SourceRevision != "rev-2" || !reflect.DeepEqual(p.reads, [][]string{{"a", "b"}}) {
		t.Fatal("interrupted identity was not replayed")
	}
	if _, err := os.Stat(filepath.Join(s.ProjectionRoot, ".loom", "rebuild-pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replay marker remained")
	}
}

func TestProjectionDeltaFailedCopyKeepsDirtyReplayAndReadOnly(t *testing.T) {
	s, p := deltaFixture(t)
	before := deltaManifest(t, s)
	entry := deltaEntries(before)["a"]
	source := p.sources["a"]
	source.SourceRevision = "rev-2"
	size := int64(1)
	source.SizeBytes = &size
	p.sources["a"] = source
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); err == nil {
		t.Fatal("changed source size accepted")
	}
	if !reflect.DeepEqual(before, deltaManifest(t, s)) {
		t.Fatal("failed copy claimed new manifest")
	}
	if info, err := os.Stat(entry.FilesystemPath); err != nil || info.Mode().Perm() != 0o444 {
		t.Fatal("failed copy made old payload writable")
	}
	if _, err := os.Stat(filepath.Join(s.ProjectionRoot, ".loom", "rebuild-pending")); err != nil {
		t.Fatal("failed copy lost replay marker")
	}
	p.sources["a"] = sourceFromProjectionEntry(entry)
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionDeltaFailureDoesNotFollowParentLink(t *testing.T) {
	s, p := deltaFixture(t)
	out := t.TempDir()
	external := filepath.Join(out, "new.md")
	if err := os.WriteFile(external, []byte("safe"), 0o640); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(s.ProjectionRoot, "nodes", "main", "Notes")
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(parent, "linked")); err != nil {
		t.Fatal(err)
	}
	// Unlink before the existing whole-projection fixture cleanup, whose chmod
	// helper predates this test and would otherwise follow this owned link.
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755); _ = os.Remove(filepath.Join(parent, "linked")) })
	source := p.sources["a"]
	source.RelativePath = "linked/new.md"
	p.sources["a"] = source
	if _, err := s.ApplyDelta(t.Context(), []string{"a"}); err == nil {
		t.Fatal("linked parent accepted")
	}
	if info, err := os.Stat(external); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatal("failure cleanup changed external permissions")
	}
	if bytes, err := os.ReadFile(external); err != nil || string(bytes) != "safe" {
		t.Fatal("linked payload overwritten")
	}
}

func TestProjectionDeltaBoundRequiresExplicitRepair(t *testing.T) {
	s, p := deltaFixture(t)
	before := deltaManifest(t, s)
	ids := make([]string, MaxDeltaIdentities+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("object_%d", i)
	}
	if _, err := s.ApplyDelta(t.Context(), ids); !errors.Is(err, ErrProjectionReconcileRequired) {
		t.Fatalf("unbounded delta accepted: %v", err)
	}
	if len(p.reads) != 0 || !reflect.DeepEqual(before, deltaManifest(t, s)) {
		t.Fatal("overflow read/pruned an incomplete set")
	}
}
