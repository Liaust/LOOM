package hermesschedules

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoxProfileInventoryAndSavedContext(t *testing.T) {
	realTemp := func() string {
		t.Helper()
		path, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	home, root := realTemp(), realTemp()
	profile := filepath.Join(home, "profiles", "tyler")
	for _, dir := range []string{filepath.Join(home, "cron"), filepath.Join(profile, "cron"), filepath.Join(root, ".loom")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	owner := BoxOwnership{BoxID: "box_company", Profile: "tyler", Workdir: root, ContextRevision: "sha256:" + strings.Repeat("a", 64)}
	marker, _ := json.Marshal(struct {
		BoxOwnership
		SchemaVersion string `json:"schema_version"`
		BoxKey        string `json:"box_key"`
	}{owner, "loom.box_agent.v1", "company"})
	write(filepath.Join(profile, "loom-box-owner.json"), string(marker))
	write(filepath.Join(home, "cron/jobs.json"), `{"jobs":[]}`)
	contextJSON, _ := json.Marshal(owner)
	write(filepath.Join(profile, "cron/jobs.json"), `{"jobs":[{"id":"owned","prompt":"PRIVATE","state":"paused"},{"id":"workspace","name":"PRIVATE","prompt":"PRIVATE","state":"paused","origin":{"loom_box_context":`+string(contextJSON)+`,"loom_workspace_schedule":{"token":"PRIVATE","creation_signature":"PRIVATE"}}}]}`)
	write(filepath.Join(root, ".loom/box.yaml"), "schema_version: loom.box.v0.5\nbox_id: box_company\nowner_node: main\nroot_path: "+root+"\n")
	catalog := filepath.Join(realTemp(), "boxes.json")
	raw, _ := json.Marshal(map[string]any{"schema_version": "loom.box_catalog.v1", "default_box": "personal", "boxes": []map[string]string{{"key": "company", "root_path": root, "node": "main", "label": "Company"}}})
	write(catalog, string(raw))
	binding := HelperBinding{Home: home, Source: Source{Host: "main", Profile: "mina", Revision: PlatformRevision}, BoxesFile: catalog}
	var out bytes.Buffer
	if err := runFixedBinding(context.Background(), binding, &out); err != nil {
		t.Fatal(err)
	}
	observed, err := decodeObservation(out.Bytes(), binding.Source)
	if err != nil || observed.Availability != Available || len(observed.Jobs) != 2 || strings.Contains(out.String(), "PRIVATE") {
		t.Fatalf("Box context lost: %#v %v", observed, err)
	}
	for _, job := range observed.Jobs {
		if job.Profile != "tyler" || job.Box == nil || *job.Box != owner || job.Project != nil {
			t.Fatalf("native/workspace Box context lost: %#v", job)
		}
	}
	wrongOwner := owner
	wrongOwner.ContextRevision = "sha256:" + strings.Repeat("b", 64)
	wrongJSON, _ := json.Marshal(wrongOwner)
	write(filepath.Join(profile, "cron/jobs.json"), `{"jobs":[{"id":"workspace","origin":{"loom_box_context":`+string(wrongJSON)+`,"loom_workspace_schedule":{"token":"PRIVATE"}}}]}`)
	out.Reset()
	if err := runFixedBinding(context.Background(), binding, &out); err != nil {
		t.Fatal(err)
	}
	observed, err = decodeObservation(out.Bytes(), binding.Source)
	if err != nil || observed.Availability != Unavailable || observed.Jobs != nil {
		t.Fatal("workspace saved context drift admitted", observed, err)
	}
	write(filepath.Join(profile, "cron/jobs.json"), `{"jobs":[{"id":"owned"}]}`)
	write(filepath.Join(root, ".loom/box.yaml"), "schema_version: loom.box.v0.5\nbox_id: box_changed\nowner_node: main\nroot_path: "+root+"\n")
	out.Reset()
	if err := runFixedBinding(context.Background(), binding, &out); err != nil {
		t.Fatal(err)
	}
	observed, err = decodeObservation(out.Bytes(), binding.Source)
	if err != nil || observed.Availability != Unavailable || observed.Jobs != nil {
		t.Fatal("stale Box marker admitted", observed, err)
	}
}

func TestBoxNativeScheduleAndEventsProjection(t *testing.T) {
	for _, kind := range []string{"loom_box_schedule", "loom_box_context", "loom_internal_events"} {
		t.Run(kind, func(t *testing.T) {
			origin := map[string]any{kind: map[string]any{"box_id": "box_company", "profile": "tyler", "workdir": "/srv/loom/boxes/company", "context_revision": "sha256:" + strings.Repeat("a", 64), "project_id": ""}}
			raw, _ := json.Marshal(map[string]any{"jobs": []map[string]any{{"id": "owned", "origin": origin}}})
			jobs, _, err := parse(context.Background(), raw)
			if err != nil || len(jobs) != 1 || jobs[0].Box == nil || jobs[0].Box.Profile != "tyler" || jobs[0].Project != nil {
				t.Fatal("Box native metadata lost", jobs, err)
			}
		})
	}
}
