package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

func TestHermesScheduleInventoryAvailabilityAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		readErr       error
		availability  hermesschedules.Availability
	}{
		{"empty", `{"jobs":[]}`, nil, hermesschedules.Available},
		{"private", `{"jobs":[{"id":"012345abcdef","prompt":"PRIVATE_SENTINEL","name":"PRIVATE_SENTINEL","last_status":"ok","last_delivery_error":"PRIVATE_SENTINEL"}]}`, nil, hermesschedules.Available},
		{"malformed", `{"jobs":[{"prompt":"PRIVATE_SENTINEL"}]}`, nil, hermesschedules.Malformed},
		{"unavailable", "", errors.New("PRIVATE_SENTINEL"), hermesschedules.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &hermesschedules.Observer{Source: hermesschedules.Source{Host: "main", Profile: "mina", Revision: hermesschedules.NativeRevision}, Read: func(context.Context, int64) ([]byte, error) { return []byte(tc.payload), tc.readErr }}
			out := httptest.NewRecorder()
			NewServer(Services{HermesSchedules: observer}, slog.Default()).Handler().ServeHTTP(out, httptest.NewRequest("GET", "/v1/schedules/hermes", nil))
			if out.Code != 200 || strings.Contains(out.Body.String(), "PRIVATE_SENTINEL") {
				t.Fatalf("response %d %s", out.Code, out.Body.String())
			}
			var result response.Envelope[hermesschedules.Observation]
			if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.Availability != tc.availability || result.Data.Owner != "hermes" || result.Meta.Source != "hermes-native-store" || result.Meta.Freshness != string(tc.availability) {
				t.Fatalf("observation %+v", result)
			}
			if tc.availability != hermesschedules.Available && (result.Data.Jobs != nil || result.Data.ObservedAt != nil) {
				t.Fatal("unavailable became observed inventory")
			}
		})
	}
}

func TestHermesScheduleInventoryUnconfiguredAndReadOnly(t *testing.T) {
	srv := NewServer(Services{}, slog.Default()).Handler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/v1/schedules/hermes", 200},
		{"POST", "/v1/schedules/hermes", 405},
		{"DELETE", "/v1/schedules/hermes", 405},
		{"GET", "/v1/schedules/hermes?home=/tmp/other", 400},
	} {
		out := httptest.NewRecorder()
		srv.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
		if out.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, out.Code, out.Body.String())
		}
		if tc.status == 200 && (!strings.Contains(out.Body.String(), `"availability":"unavailable"`) || !strings.Contains(out.Body.String(), `"jobs":null`)) {
			t.Fatal(out.Body.String())
		}
	}
}

func TestHermesScheduleRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := &hermesschedules.Observer{
		Source: hermesschedules.Source{Host: "main", Profile: "mina", Revision: hermesschedules.NativeRevision},
		Read: func(readCtx context.Context, _ int64) ([]byte, error) {
			cancel()
			if readCtx.Err() != context.Canceled {
				t.Fatal("request cancellation did not reach reader")
			}
			return []byte(`{"jobs":[]}`), nil
		},
	}
	out := httptest.NewRecorder()
	NewServer(Services{HermesSchedules: observer}, slog.Default()).Handler().ServeHTTP(out, httptest.NewRequest("GET", "/v1/schedules/hermes", nil).WithContext(ctx))
	var result response.Envelope[hermesschedules.Observation]
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Availability != hermesschedules.Unavailable || result.Data.ObservedAt != nil || result.Data.Jobs != nil || result.Data.Source.Profile != "mina" {
		t.Fatalf("cancellation became healthy: %+v", result)
	}
}
