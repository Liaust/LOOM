package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

func TestHermesScheduleCLIWireAndUnavailableJSON(t *testing.T) {
	for _, availability := range []hermesschedules.Availability{hermesschedules.Available, hermesschedules.Unavailable, hermesschedules.Malformed, hermesschedules.Unsupported} {
		t.Run(string(availability), func(t *testing.T) {
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/schedules/hermes" || r.URL.RawQuery != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				response.WriteJSON(w, 200, response.Success("corr_hermes", hermesschedules.Observation{Owner: "hermes", Availability: availability, Jobs: []hermesschedules.Job{}}))
			})
			defer stop()
			var stdout, stderr bytes.Buffer
			err := Execute([]string{"--socket", socket, "--json", "schedules", "hermes"}, &stdout, &stderr)
			out := stdout.String()
			if (err == nil) != (availability == hermesschedules.Available) {
				t.Fatalf("availability=%s err=%v", availability, err)
			}
			decoder := json.NewDecoder(strings.NewReader(out))
			var result response.Envelope[hermesschedules.Observation]
			if err := decoder.Decode(&result); err != nil {
				t.Fatalf("%v output=%s", err, out)
			}
			if result.Data.Availability != availability {
				t.Fatal(out)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("extra output: %s", out)
			}
		})
	}
}

func TestHermesScheduleCLICompleteDisplay(t *testing.T) {
	when, _ := time.Parse(time.RFC3339Nano, "2026-09-28T17:00:00.123456+02:00")
	expr, minutes, delivery := "0 9 * * MON-FRI", int64(30), true
	obs := hermesschedules.Observation{Owner: "hermes", Availability: hermesschedules.Available,
		Source: hermesschedules.Source{Host: "main", Profile: "mina", Revision: hermesschedules.NativeRevision}, AttemptedAt: when, ObservedAt: &when, StoreUpdatedAt: &when,
		Jobs: []hermesschedules.Job{
			{ID: "cron", Schedule: hermesschedules.Schedule{Kind: hermesschedules.Cron, Expression: &expr}, NextRunAt: &when, PausedAt: &when, LastRunAt: &when, LastResult: hermesschedules.Error, DeliveryErrorReported: &delivery},
			{ID: "interval", Project: &hermesschedules.ProjectOwnership{ProjectID: "project_test", Resource: "review", Profile: "mina", Retired: true}, Schedule: hermesschedules.Schedule{Kind: hermesschedules.Interval, Minutes: &minutes}},
			{ID: "once", Schedule: hermesschedules.Schedule{Kind: hermesschedules.Once, RunAt: &when}},
		},
	}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, 200, response.Success("corr", obs))
	})
	defer stop()
	var out, stderr bytes.Buffer
	if err := Execute([]string{"--socket", socket, "schedules", "hermes"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`expression="0 9 * * MON-FRI"`, "minutes=30", "run_at=2026-09-28T17:00:00.123456+02:00", "paused_at=2026-09-28T17:00:00.123456+02:00", "stored_next=2026-09-28T17:00:00.123456+02:00", "result_at=2026-09-28T17:00:00.123456+02:00", "Observed: 2026-09-28T17:00:00.123456+02:00", "Store updated:", "enabled=unknown", "stored_state=unknown", "last_result=unknown", "delivery=unknown", "delivery=error_reported", "timezone=unknown", "current_completion=unknown", "project=project_test resource=review profile=mina retired=true"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	obs.Availability = hermesschedules.Malformed
	out.Reset()
	stderr.Reset()
	if err := Execute([]string{"--socket", socket, "schedules", "hermes"}, &out, &stderr); err == nil {
		t.Fatal("unavailable exited successfully")
	}
	if strings.Contains(out.String(), "expression=") || strings.Contains(out.String(), "No native jobs") || stderr.Len() != 0 {
		t.Fatalf("failed inventory rendered jobs or duplicate failure: %s / %s", out.String(), stderr.String())
	}
}

func TestHermesScheduleCLICancellation(t *testing.T) {
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("canceled command reached server") })
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := NewRootCommand()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--socket", socket, "--json", "schedules", "hermes"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("canceled command succeeded")
	}
	var result response.ErrorEnvelope
	dec := json.NewDecoder(&out)
	if err := dec.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Error.Code != "transport.unavailable" {
		t.Fatalf("canceled command rendered success: %+v", result)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		t.Fatal("extra document")
	}
}
