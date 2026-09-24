package knowledge

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/storagecatalog"
)

func TestSourceRefreshClockCoalescesChanges(t *testing.T) {
	start := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	policy := projectcontracts.KnowledgeRefreshPolicy{QuietForSeconds: 600, MaxWaitSeconds: 1800}
	object := observeSourceRefresh(nil, KnowledgeObject{SourceHash: "first", SourceRevision: "1", Metadata: json.RawMessage(`{"source_root":{"knowledge_source":{"policy":{"refresh":{"quiet_for_seconds":600,"max_wait_seconds":1800}}}}}`)}, start)
	for i := 1; i <= 8; i++ {
		next := object
		next.SourceHash += "x"
		next.SourceRevision += "x"
		object = observeSourceRefresh(&object, next, start.Add(time.Duration(i)*4*time.Minute))
	}
	if got := sourceRefreshEligibleAt(object, policy, start.Add(time.Hour)); !got.Equal(start.Add(30 * time.Minute)) {
		t.Fatalf("max wait moved: %s", got)
	}
	before := objectRefreshClock(object)
	polled := object
	polled.SourceRevision = "same-bytes-new-revision"
	polled.UpdatedAt = start.Add(2 * time.Hour)
	polled = observeSourceRefresh(&object, polled, start.Add(2*time.Hour))
	if got := objectRefreshClock(polled); !got.LastContentChangeAt.Equal(before.LastContentChangeAt) || !got.PendingSince.Equal(*before.PendingSince) {
		t.Fatal("unchanged bytes reset refresh clock")
	}
}

func TestSourceRefreshWaitOnceAndLegacyTiming(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	object := KnowledgeObject{FileClass: storagecatalog.FileClassPDF, SourceHash: "first", Metadata: json.RawMessage(`{"source_root":{"knowledge_source":{"policy":{"refresh":{"quiet_for_seconds":60,"max_wait_seconds":180}}}}}`)}
	object = observeSourceRefresh(nil, object, now)
	plan, err := CompilePipelinePlan(object, DefaultPipelinePolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range plan.Stages {
		if stage.QuietWindow != (stage.StageKey == FilePipelineStageMetadata) {
			t.Fatalf("wrong quiet stage: %+v", stage)
		}
	}
	if when, ok := pipelineQuietWindowEligibleAt(object, plan, now.Add(time.Second)); !ok || !when.Equal(now.Add(time.Minute)) {
		t.Fatalf("eligibility: %s %t", when, ok)
	}
	object.Metadata = json.RawMessage(`{}`)
	if got := observeSourceRefresh(nil, object, now); string(got.Metadata) != "{}" {
		t.Fatal("legacy admission metadata changed")
	}
	legacy, err := CompilePipelinePlan(object, DefaultPipelinePolicy())
	if err != nil || legacy.Stages[0].QuietWindow {
		t.Fatal("legacy native timing changed")
	}
}

func TestSourceRefreshPreservesUnrelatedMetadata(t *testing.T) {
	object := KnowledgeObject{SourceHash: "first", Metadata: json.RawMessage(`{"inode":9007199254740993,"source_root":{"knowledge_source":{"policy":{"refresh":{"quiet_for_seconds":60,"max_wait_seconds":180}}}}}`)}
	object = observeSourceRefresh(nil, object, time.Now())
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object.Metadata, &fields); err != nil || string(fields["inode"]) != "9007199254740993" {
		t.Fatalf("unrelated metadata lost precision: %s %v", object.Metadata, err)
	}
}
