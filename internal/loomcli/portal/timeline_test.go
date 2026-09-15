package portal

import (
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/loomcli/actions"
)

func TestTimelineRenderGroupsEventsByDate(t *testing.T) {
	now := time.Date(2026, 6, 22, 15, 30, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)
	data := BuildTimelineData(TimelineBuildInput{Snapshot: Snapshot{CapturedAt: now}})
	data.Events = []TimelineEvent{
		{ID: "today", Domain: "Jobs", Kind: "job", Title: "Today Job", Status: "succeeded", StartedAt: now},
		{ID: "yesterday", Domain: "Jobs", Kind: "job", Title: "Yesterday Job", Status: "failed", StartedAt: yesterday},
	}
	data.Recent = data.Events
	data.Running = nil
	data.Failed = nil
	state := NewScreenState(ScreenTimeline)
	state.Status = ScreenLoadLoaded
	state.Data.Timeline = data

	output := RenderScreenWithState(RenderInput{Mode: testMode(), HomeSnapshot: state.Data.Timeline.Snapshot, Registry: actions.DefaultRegistry(), Screen: ScreenTimeline, State: state, Width: 100, Height: 40})
	for _, want := range []string{"Timeline", "Full History", "Today, Jun 22, 2026", "Yesterday, Jun 21, 2026", "Today Job", "Yesterday Job", "failed", "[jobs]"} {
		if !strings.Contains(output, want) {
			t.Fatalf("timeline output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\nFailed\n") {
		t.Fatalf("timeline should keep failures in chronological flow, not in a separate section:\n%s", output)
	}
}

func TestTimelineDoesNotTreatStaleLaneTransferAsRunning(t *testing.T) {
	now := time.Date(2026, 6, 22, 15, 30, 0, 0, time.UTC)
	started := now.Add(-2 * time.Hour)
	progressUpdated := now.Add(-90 * time.Minute)
	data := BuildTimelineData(TimelineBuildInput{
		Snapshot: Snapshot{CapturedAt: now},
		Storage: StorageData{
			LaneStatus: &lane.Status{
				State: lane.BatchStatusTransferring,
				LastTransfer: &lane.TransferSummary{
					BatchID:    "lane_stale",
					Status:     lane.BatchStatusTransferring,
					FileCount:  1,
					TotalBytes: 1024,
					StartedAt:  &started,
					Progress: lane.Progress{
						Observed:         true,
						BytesTransferred: 512,
						TotalBytes:       1024,
						Percent:          50,
						UpdatedAt:        &progressUpdated,
					},
				},
			},
		},
	})

	if len(data.Running) != 0 {
		t.Fatalf("stale lane transfer should not be running: %#v", data.Running)
	}
	if len(data.Recent) == 0 || data.Recent[0].ID != "lane.transfer.lane_stale" {
		t.Fatalf("stale lane transfer should remain visible in timeline: %#v", data.Recent)
	}
}

func TestTimelineSurfacesStalePendingScheduleInvocation(t *testing.T) {
	now := time.Date(2026, 6, 22, 15, 30, 0, 0, time.UTC)
	oldest := now.Add(-75 * time.Second)
	data := BuildTimelineData(TimelineBuildInput{
		Snapshot: Snapshot{
			CapturedAt: now,
			ScheduleStatus: automation.ScheduleStatus{
				PendingInvocationCount:    1,
				OldestPendingInvocationAt: &oldest,
			},
		},
	})

	if !timelineContainsEvent(data.Events, "schedules.summary.pending_stale") {
		t.Fatalf("timeline should include stale pending schedule warning: %#v", data.Events)
	}
	output := RenderScreenWithState(RenderInput{
		Mode:         testMode(),
		HomeSnapshot: data.Snapshot,
		Registry:     actions.DefaultRegistry(),
		Screen:       ScreenTimeline,
		State: ScreenState{
			Screen: ScreenTimeline,
			Status: ScreenLoadLoaded,
			Data:   ScreenData{Timeline: data},
		},
		Width:  100,
		Height: 40,
	})
	if !strings.Contains(output, "Schedule dispatch delayed") || !strings.Contains(output, "oldest waiting 1m15s") {
		t.Fatalf("timeline output missing stale pending schedule warning:\n%s", output)
	}
}
