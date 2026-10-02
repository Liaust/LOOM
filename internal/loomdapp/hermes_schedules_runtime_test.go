package loomdapp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/httpapi"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/response"
)

func TestHermesScheduleProjectRuntimeOptIn(t *testing.T) {
	current := func() (config.Config, error) { return config.Config{}, nil }
	t.Setenv("LOOM_HERMES_PROJECT_SCHEDULES", "")
	r := &projectapply.LocalResolver{}
	configureProjectHermes(r, current)
	if r.Hermes != nil || projectHermesArchivePause(current) != nil {
		t.Fatal("default-off project scheduling installed native mutation hooks")
	}
	t.Setenv("LOOM_HERMES_PROJECT_SCHEDULES", "1")
	configureProjectHermes(r, current)
	if r.Hermes == nil || projectHermesArchivePause(current) == nil {
		t.Fatal("enabled scheduling silently omitted its owner or archive hook")
	}
	if err := r.PauseHermesProject(context.Background(), "project_01M3MWE8ZV20KP0N9NVTK8WNZ6"); err == nil {
		t.Fatal("missing selected-profile binding accepted a native mutation")
	}
}

func TestHermesScheduleRuntimeBinding(t *testing.T) {
	t.Setenv("HERMES_HOME", "/private/ambient/profile")
	observer, err := runtimeHermesSchedules(config.Config{NodeID: "main", MinaSelected: true})
	if err != nil || observer != nil {
		t.Fatal("default binding adopted ambient profile", err)
	}
	home := filepath.Join(t.TempDir(), "selected")
	cfg := config.Config{NodeID: "main", MinaSelected: true, HermesSchedulesProfile: "mina", HermesSchedulesHome: home, HermesSchedulesRevision: hermesschedules.NativeRevision}
	observer, err = runtimeHermesSchedules(cfg)
	if err != nil || observer == nil {
		t.Fatal(err)
	}
	obs, err := observer.Observe(context.Background())
	if err == nil || obs.Availability != hermesschedules.Unavailable || obs.Jobs != nil {
		t.Fatal("absent native file became healthy", obs, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("binding provisioned profile", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "cron"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "cron", "jobs.json")
	input := `{"jobs":[{"id":"aabbccddeeff","name":"PRIVATE","prompt":"PRIVATE","enabled":false,"state":"paused","last_status":null}]}`
	if err := os.WriteFile(path, []byte(input), 0400); err != nil {
		t.Fatal(err)
	}
	// Existing S1c file bindings cannot make loomd read native bytes. A helper
	// must be installed; the test substitutes only its sanitized IPC boundary.
	obs, err = observer.Observe(context.Background())
	if err == nil || obs.Availability != hermesschedules.Unavailable {
		t.Fatal("daemon fell back to native file", obs, err)
	}
	bindingPath := filepath.Join(t.TempDir(), "binding.json")
	binding, _ := json.Marshal(hermesschedules.HelperBinding{Source: observer.Source, Home: home})
	if err := os.WriteFile(bindingPath, binding, 0600); err != nil {
		t.Fatal(err)
	}
	observer.ReadSanitized = func(ctx context.Context, _ int64) ([]byte, error) {
		var out bytes.Buffer
		err := hermesschedules.RunFixedHelper(ctx, nil, bindingPath, &out)
		return out.Bytes(), err
	}
	out := httptest.NewRecorder()
	httpapi.NewServer(httpapi.Services{HermesSchedules: observer}, slog.Default()).Handler().ServeHTTP(out, httptest.NewRequest("GET", "/v1/schedules/hermes", nil))
	var envelope response.Envelope[hermesschedules.Observation]
	if err := json.Unmarshal(out.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	got := envelope.Data
	if out.Code != 200 || got.Availability != hermesschedules.Available || got.Source.Host != "main" || got.Source.Profile != "mina" || got.Source.Revision != hermesschedules.NativeRevision || len(got.Jobs) != 1 || *got.Jobs[0].Enabled || got.Jobs[0].LastResult != hermesschedules.ResultUnknown || strings.Contains(out.Body.String(), "PRIVATE") {
		t.Fatalf("binding/API evidence lost: %s", out.Body.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != input {
		t.Fatal("binding modified native file", err)
	}
	cfg.HermesSchedulesRevision = strings.Repeat("b", 40)
	observer, err = runtimeHermesSchedules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	obs, err = observer.Observe(context.Background())
	if err != hermesschedules.ErrRevision || obs.Availability != hermesschedules.Unsupported || obs.Jobs != nil || obs.Source.Revision != cfg.HermesSchedulesRevision {
		t.Fatal("revision mismatch not surfaced", obs, err)
	}
	cfg.HermesSchedulesProfile = "morathustra"
	if _, err := runtimeHermesSchedules(cfg); err == nil {
		t.Fatal("mismatched profile bound")
	}
}
