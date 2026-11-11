package notesworkspace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/objectstore"
)

func TestMultiBoxUploadIntentSyntaxAndOptionalIdentity(t *testing.T) {
	v := UploadIntent{ID: "desktop_upload_company", Root: "box:box_company", Path: "Documents/file.bin", Hash: digest(nil), Collision: "skip"}
	if err := ValidateUploadIntent(v); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if bytes.Contains(raw, []byte(`"box_id"`)) {
		t.Fatal("omitted optional BoxID was added to intent")
	}
	v.BoxID = "box_company"
	if err := ValidateUploadIntent(v); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*UploadIntent){
		func(v *UploadIntent) { v.BoxID = "box_foreign" },
		func(v *UploadIntent) { v.Root = "company" },
		func(v *UploadIntent) { v.Root = "box:../escape" },
		func(v *UploadIntent) { v.Root = "storage" },
	} {
		bad := v
		change(&bad)
		if ValidateUploadIntent(bad) == nil {
			t.Fatal("unsafe root identity accepted", bad)
		}
	}
}

func TestMultiBoxReceiptFieldsPreserveHistoricalEncoding(t *testing.T) {
	old := Operation{Request: Request{OperationID: "saved", Generation: "old"}}
	raw, _ := json.Marshal(old)
	if bytes.Contains(raw, []byte(`"box_id"`)) {
		t.Fatal("old Personal operation encoding changed")
	}
	current := old
	current.Request.BoxID = "box_company"
	if sameIntent(old, current) {
		t.Fatal("different Box request matched")
	}
	raw, _ = json.Marshal(current)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if !bytes.Contains(fields["request"], []byte(`"box_id":"box_company"`)) {
		t.Fatal("Timeline request.BoxID missing")
	}
	raw, _ = json.Marshal(TreeMove{})
	if bytes.Contains(raw, []byte(`"box_id"`)) {
		t.Fatal("old Personal tree encoding changed")
	}
	raw, _ = json.Marshal(TreeMove{BoxID: "box_company"})
	if !bytes.Contains(raw, []byte(`"box_id":"box_company"`)) {
		t.Fatal("Timeline tree BoxID missing")
	}
}

func TestMultiBoxImportProducerRecordsBoundIdentity(t *testing.T) {
	s, _, _ := fixture(t)
	s.Blobs = objectstore.New(t.TempDir())
	source := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(source, []byte("company bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	blob, err := s.Blobs.PutFile(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	generation, parent, err := s.ImportDestination(ctx, "collection", "new.bin")
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.CreateImport(ctx, "collection", "new.bin", "desktop_upload_company", generation, parent, blob)
	if err != nil || op.State != Accepted || op.Request.BoxID != "box_company" {
		t.Fatal(op, err)
	}
}
