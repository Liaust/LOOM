package notesworkspace

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPDFDedupPlanIntegrityAndLosslessMetadata(t *testing.T) {
	first := []byte(`{"base_id":"b","content":"eA==","extra":9007199254740992}`)
	second := []byte(`{"content":null,"extra":9007199254740993,"base_id":"b"}`)
	a, err := metadataHash(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := metadataHash(second)
	if err != nil || a == b {
		t.Fatalf("distinct large metadata integers aliased: %s %s %v", a, b, err)
	}
	c, _ := metadataHash([]byte(`{"extra":9007199254740992,"base_id":"b"}`))
	if a != c {
		t.Fatal("content representation changed metadata identity")
	}
	p := PDFDedupPlan{Schema: "loom.notes_pdf_dedup.v1", CreatedAt: time.Now().UTC(), Entries: []PDFDedupEntry{{BaseID: "b", FileID: "f", PayloadHash: digest([]byte("x")), PayloadBytes: 1, OriginalHash: "old", LogicalHash: "logical", MetadataHash: a}}}
	p.PlanHash = p.Hash()
	if err := p.Validate(p.PlanHash); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var replay PDFDedupPlan
	if err := json.Unmarshal(raw, &replay); err != nil || replay.Validate(p.PlanHash) != nil {
		t.Fatal("plan round-trip failed", err)
	}
	replay.Entries[0].BaseID = "other"
	if replay.Validate(p.PlanHash) == nil {
		t.Fatal("substituted plan accepted")
	}
}
