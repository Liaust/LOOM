package activitysummary

import (
	"testing"
	"time"
)

func TestBuildOrdersAndNormalizesBoundedActivity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	older := now.Add(-time.Hour)
	got := Build([]Candidate{
		{Kind: "main_backup", State: "succeeded", CompletedAt: &older},
		{Kind: "cloud_snapshot_upload", State: "running", StartedAt: &now},
		{Kind: "/private/path", State: "payload failure text", CompletedAt: &now},
	})
	if got.Running == nil || got.Running.Kind != "cloud snapshot" || got.Running.State != "running" {
		t.Fatalf("running = %+v", got.Running)
	}
	if got.Recent == nil || got.Recent.Kind != "activity" || got.Recent.State != "unknown" {
		t.Fatalf("recent = %+v", got.Recent)
	}
}

func TestLabelAndStateAllowlist(t *testing.T) {
	if Label("../../secret") != "activity" {
		t.Fatal("untrusted kind was displayed")
	}
	if NormalizeState("arbitrary payload") != "unknown" {
		t.Fatal("untrusted state was displayed")
	}
}
