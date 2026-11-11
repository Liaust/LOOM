package knowledge

import (
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

func TestPendingCatalogAdmissionRevisionAndNoopPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	ctx := t.Context()
	size := int64(8)
	entry, err := storagecatalog.NewService(s.store.db).RegisterEntry(ctx, storagecatalog.RegisterEntryInput{
		StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaNotes,
		OriginNodeID: *roots[0].NodeID, OriginNodeKey: roots[0].NodeKey,
		WatchedRootKey: roots[0].BackendRootKey, LogicalPath: "catalog-only.md", OriginalSourcePath: "catalog-only.md",
		FileClass: "markdown", MimeType: "text/markdown", SizeBytes: &size,
		ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(hashArtifactValue("catalog"), "sha256:"),
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := s.store.db.QueryRowContext(ctx, `SELECT revision FROM knowledge.pending_catalog_admissions WHERE storage_entry_id=$1`, entry.StorageEntryID).Scan(&revision); err != nil {
		t.Fatal("catalog producer lost durable work", err)
	}
	before, err := s.NotesAdmissionRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.ExecContext(ctx, `UPDATE storage.storage_entries SET updated_at=now() WHERE storage_entry_id=$1`, entry.StorageEntryID); err != nil {
		t.Fatal(err)
	}
	var observed int64
	if err := s.store.db.QueryRowContext(ctx, `SELECT revision FROM knowledge.pending_catalog_admissions WHERE storage_entry_id=$1`, entry.StorageEntryID).Scan(&observed); err != nil || observed != revision {
		t.Fatal("observation-only write enqueued admission", observed, err)
	}
	after, err := s.NotesAdmissionRevision(ctx)
	if err != nil || before != after {
		t.Fatal("catalog observation changed global authority revision", err)
	}
	if _, err := s.store.db.ExecContext(ctx, `UPDATE storage.storage_entries SET mime_type='text/plain' WHERE storage_entry_id=$1`, entry.StorageEntryID); err != nil {
		t.Fatal(err)
	}
	if err := s.acknowledgePendingCatalog(ctx, []string{entry.StorageEntryID}, []int64{revision}, false); err != nil {
		t.Fatal(err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at == nil || at.After(time.Now()) {
		t.Fatal("old acknowledgement erased a concurrent change", at, err)
	}
	result := AdmissionResult{ElapsedMS: map[string]int64{}}
	if found, err := s.admitPendingCatalog(ctx, roots, 8, &result); err != nil || !found || result.CatalogObserved != 1 || result.Applied != 1 {
		t.Fatal("catalog-only admission failed", result, err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at != nil {
		t.Fatal("catalog work did not drain", at, err)
	}
	if _, err := s.store.db.ExecContext(ctx, `UPDATE storage.storage_entries SET updated_at=now() WHERE storage_entry_id=$1`, entry.StorageEntryID); err != nil {
		t.Fatal(err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at != nil {
		t.Fatal("idle observation recreated pending work", at, err)
	}
}
