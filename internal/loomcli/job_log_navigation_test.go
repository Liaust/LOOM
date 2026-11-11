package loomcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/response"
)

func TestJobLogNavigation(t *testing.T) {
	logs := []jobs.JobLog{{JobLogID: "stdout", Stream: "stdout"}, {JobLogID: "runner", Stream: "runner", TailText: "first\nsecond\nthird\n", ByteCount: 900}, {JobLogID: "stderr", Stream: "stderr", TailText: "problem\n", ByteCount: 100}}
	for i := range logs {
		logs[i].Metadata = json.RawMessage(`null`)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/jobs/job_fixture/logs" {
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
		response.WriteJSON(w, 200, response.Success("captured-correlation", logs))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	run := func(flags ...string) (string, error) {
		args := []string{"--config", cfg, "job", "logs", "job_fixture"}
		out, _, err := executeRootCommand(append(args, flags...)...)
		return out, err
	}
	out, err := run()
	if err != nil || !strings.Contains(out, "--stream runner --tail 40") || !strings.Contains(out, "--stream stderr --tail 40") || strings.Contains(out, "first") {
		t.Fatalf("default navigation: %s %v", out, err)
	}
	out, err = run("--stream", "stdout")
	if err != nil || strings.Contains(out, "available:") {
		t.Fatalf("selected stream switched: %s %v", out, err)
	}
	for _, tt := range []struct {
		flags []string
		want  []jobs.JobLog
	}{
		{[]string{"--json"}, logs},
		{[]string{"--json", "--stream", "runner", "--tail", "1"}, selectJobLogText(logs, "runner", 1)},
		{[]string{"--json", "--tail", "1"}, selectJobLogText(logs, "all", 1)},
		{[]string{"--json", "--stream", "all", "--tail", "0"}, logs},
	} {
		out, err := run(tt.flags...)
		var envelope struct {
			Data []jobs.JobLog
			Meta response.Meta
		}
		if err != nil || json.Unmarshal([]byte(out), &envelope) != nil || !reflect.DeepEqual(envelope.Data, tt.want) || envelope.Meta.CorrelationID != "captured-correlation" {
			t.Fatalf("JSON selector/envelope: %v %s %v", tt.flags, out, err)
		}
	}
	out, err = run("--stream", "runner", "--tail", "1")
	if err != nil || !strings.Contains(out, "third") || strings.Contains(out, "second") || !strings.Contains(out, "900 bytes") {
		t.Fatalf("human parity: %s %v", out, err)
	}
	before := requests
	for _, flags := range [][]string{{"--stream", "bogus"}, {"--stream", ""}, {"--tail", "-1"}} {
		if _, err := run(append(flags, "--json")...); err == nil {
			t.Fatal("invalid selector succeeded")
		}
	}
	if requests != before || logs[1].TailText != "first\nsecond\nthird\n" {
		t.Fatal("invalid selector reached backend or captured text mutated")
	}
}
