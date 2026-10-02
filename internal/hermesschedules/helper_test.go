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

func helperFixture(t *testing.T) (string, string, Source) {
	t.Helper()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "cron"), 0700); err != nil {
		t.Fatal(err)
	}
	source := Source{Host: "main", Profile: "mina", Revision: NativeRevision}
	configPath := filepath.Join(t.TempDir(), "binding.json")
	raw, _ := json.Marshal(HelperBinding{Source: source, Home: home})
	if err := os.WriteFile(configPath, raw, 0400); err != nil {
		t.Fatal(err)
	}
	return configPath, filepath.Join(home, "cron", "jobs.json"), source
}

func TestHelperFixedSanitizedFreshRead(t *testing.T) {
	binding, path, source := helperFixture(t)
	t.Setenv("HERMES_HOME", "/private/other")
	t.Setenv("LOOM_HERMES_SCHEDULES_PROFILE", "other")
	for _, state := range []string{"paused", "scheduled"} {
		input := `{"jobs":[{"id":"aabbccddeeff","prompt":"PRIVATE_PROMPT","name":"PRIVATE_PROMPT","last_error":"PRIVATE_ERROR","base_url":"PRIVATE_SECRET","state":"` + state + `"}]}`
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunFixedHelper(context.Background(), nil, binding, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), filepath.Dir(path)) {
			t.Fatal("private data escaped helper")
		}
		observed, err := decodeObservation(out.Bytes(), source)
		if err != nil || observed.Availability != Available || len(observed.Jobs) != 1 || observed.Jobs[0].NativeState != State(state) {
			t.Fatal("replacement or fixed binding lost", observed, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != input {
			t.Fatal("helper mutated native state", err)
		}
	}
	for _, args := range [][]string{{"--help"}, {"--home", "/private/other"}, {""}} {
		var out bytes.Buffer
		if err := RunFixedHelper(context.Background(), args, binding, &out); err == nil || out.Len() != 0 {
			t.Fatal("helper accepted caller arguments")
		}
	}
}

func TestHelperFailureTruth(t *testing.T) {
	binding, path, source := helperFixture(t)
	for _, malformed := range []bool{false, true} {
		if malformed {
			if err := os.WriteFile(path, []byte(`{"prompt":"PRIVATE",`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		if err := RunFixedHelper(context.Background(), nil, binding, &out); err != nil {
			t.Fatal(err)
		}
		observation, err := decodeObservation(out.Bytes(), source)
		want := Unavailable
		if malformed {
			want = Malformed
		}
		if err != nil || observation.Availability != want || observation.Jobs != nil || observation.ObservedAt != nil {
			t.Fatal("native failure became healthy", observation, err)
		}
	}
}

func TestHelperHealthyEmptyInventory(t *testing.T) {
	binding, path, source := helperFixture(t)
	if err := os.WriteFile(path, []byte(`{"jobs":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunFixedHelper(context.Background(), nil, binding, &out); err != nil {
		t.Fatal(err)
	}
	observation, err := decodeObservation(out.Bytes(), source)
	if err != nil || observation.Availability != Available || observation.Jobs == nil || len(observation.Jobs) != 0 || observation.ObservedAt == nil {
		t.Fatal("healthy empty inventory lost", observation, err)
	}
}

func TestHelperIPCContract(t *testing.T) {
	binding, path, source := helperFixture(t)
	if err := os.WriteFile(path, []byte(`{"jobs":[{"id":"abc","last_status":"ok"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunFixedHelper(context.Background(), nil, binding, &out); err != nil {
		t.Fatal(err)
	}
	valid := out.String()
	for name, raw := range map[string]string{
		"wrong source":      strings.Replace(valid, `"profile":"mina"`, `"profile":"other"`, 1),
		"private field":     strings.Replace(valid, `"id":"abc"`, `"id":"abc","prompt":"PRIVATE"`, 1),
		"unknown result":    strings.Replace(valid, `"last_result":"ok"`, `"last_result":"PRIVATE"`, 1),
		"failure with jobs": strings.Replace(valid, `"availability":"available"`, `"availability":"unavailable"`, 1),
		"empty masquerade":  strings.Replace(valid, `"jobs":[`, `"jobs":null,"other":[`, 1),
		"two results":       valid + valid,
		"raw store":         `{"jobs":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			observer := Observer{Source: source, ReadSanitized: func(context.Context, int64) ([]byte, error) { return []byte(raw), nil }}
			got, err := observer.Observe(context.Background())
			if err != ErrMalformed || got.Availability != Malformed || got.Jobs != nil || got.ObservedAt != nil {
				t.Fatal("invalid IPC accepted", got, err)
			}
		})
	}
}

func TestServeFixedHelperRejectsUnauthenticatedStream(t *testing.T) {
	// fd0 must be an authenticated connected Unix socket before any response.
	fixture := t.TempDir()
	binding := filepath.Join(fixture, "binding.json")
	if err := os.WriteFile(binding, []byte(`{"source":{"host":"main","profile":"mina","revision":"`+NativeRevision+`"},"home":"/not-read","caller":"root"}`), 0600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	for _, args := range [][]string{nil, {"--help"}, {"--home=/other"}} {
		var out bytes.Buffer
		if ServeFixedHelper(context.Background(), args, binding, in, &out) == nil || out.Len() != 0 {
			t.Fatal("unauthenticated stream exposed response")
		}
	}
}
