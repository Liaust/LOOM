package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loom.local/loom/internal/minidashboard"
)

type miniDashboardStub struct {
	snapshot minidashboard.DomainSnapshot
	err      error
}

func (s miniDashboardStub) Snapshot(context.Context) (minidashboard.DomainSnapshot, error) {
	return s.snapshot, s.err
}

func TestMiniDashboardStatusReturnsVersionedPartialSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	snapshot := minidashboard.DomainSnapshot{SchemaVersion: minidashboard.SchemaVersion, GeneratedAt: now, Freshness: minidashboard.Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: minidashboard.SourceLive}, Node: minidashboard.NodeIdentity{Key: "main", Label: "MAIN"}}
	server := NewServer(Services{MiniDashboard: miniDashboardStub{snapshot: snapshot}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/v1/mini-dashboard/status", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var body struct {
		OK   bool                         `json:"ok"`
		Data minidashboard.DomainSnapshot `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Data.SchemaVersion != minidashboard.SchemaVersion {
		t.Fatalf("body = %+v", body)
	}
}

func TestMiniDashboardStatusRejectsMethodAndUnavailableService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		method string
		want   int
	}{{http.MethodPost, http.StatusMethodNotAllowed}, {http.MethodGet, http.StatusServiceUnavailable}} {
		server := NewServer(Services{}, logger)
		request := httptest.NewRequest(test.method, "/v1/mini-dashboard/status", nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s status = %d", test.method, response.Code)
		}
	}
}
