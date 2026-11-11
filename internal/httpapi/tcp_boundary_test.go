package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/nodes"
)

func testTCPBoundary(store filetransfer.TransferStore, next http.Handler) http.Handler {
	return markTCPTransport(tcpBoundary{
		next: next, transfers: store,
		authenticate: func(_ context.Context, token string) (nodes.Node, error) {
			if token != "fixture-valid" {
				return nodes.Node{}, errors.New("invalid or revoked credential")
			}
			return nodes.Node{NodeID: "node_mac", NodeKey: "macbook", Status: "active"}, nil
		},
		reject: func(w http.ResponseWriter, _ *http.Request, status int, code string) {
			http.Error(w, code, status)
		},
	})
}

func TestTCPBoundaryDeniesAdministrationAndPathBypasses(t *testing.T) {
	for _, path := range []string{
		"/v1/status", "/v1/projects", "/v1/files/roots", "/v1/capabilities",
		"/v1/file-transfers", "/v1/node-agent/future-admin",
		"/v1/node-agent/../projects", "/v1//node-agent/heartbeat",
		"/v1/node-agent/heartbeat/", "/v1/node-agent/%68eartbeat",
		"/v1/file-transfers/id/complete/extra",
	} {
		t.Run(path, func(t *testing.T) {
			called := false
			handler := testTCPBoundary(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead, http.MethodOptions} {
				request := httptest.NewRequest(method, path, nil)
				request.Header.Set("Authorization", "Bearer fixture-valid")
				request.Header.Set("X-LOOM-Actor", "owner")
				request.Header.Set("X-Forwarded-For", "127.0.0.1")
				result := httptest.NewRecorder()
				handler.ServeHTTP(result, request)
				if result.Code < 400 || called {
					t.Fatalf("%s %s bypassed boundary: status=%d called=%t", method, path, result.Code, called)
				}
			}
		})
	}
}

func TestTCPBoundaryLeavesLocalAndTypedNodeProtocolAvailable(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	local := NewServer(Services{}, nil).withTCPBoundary(next)
	result := httptest.NewRecorder()
	local.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if result.Code != http.StatusNoContent {
		t.Fatalf("local administration was blocked: %d", result.Code)
	}
	for _, path := range []string{
		"enroll", "heartbeat", "poll", "ack", "providers/advertise", "capability-result",
		"sync/batches", "sync/object-upload", "sync/private-backup", "sync/deletion-request",
		"watched-roots/report", "watched-roots/backup-batches",
	} {
		result := httptest.NewRecorder()
		testTCPBoundary(nil, next).ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/v1/node-agent/"+path, nil))
		if result.Code != http.StatusNoContent {
			t.Errorf("typed node handler %s is unavailable: %d", path, result.Code)
		}
	}
}

func TestTCPBoundaryAuthenticatesTransferOwnershipAndReplay(t *testing.T) {
	store := newMemoryTransferStore()
	owned := filetransfer.Manifest{TransferID: "owned", SourceNodeID: "node_mac", SourceNodeKey: "macbook",
		TransferKind: filetransfer.KindWatchedRootBackup, CustodyMode: filetransfer.CustodyModeBackupCopy}
	foreign := owned
	foreign.TransferID, foreign.SourceNodeID, foreign.SourceNodeKey = "foreign", "node_other", "other"
	foreign.IdempotencyKey = "foreign-replay"
	manual := owned
	manual.TransferID, manual.TransferKind = "manual", filetransfer.KindManualUpload
	store.manifests[owned.TransferID], store.manifests[foreign.TransferID], store.manifests[manual.TransferID] = owned, foreign, manual
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := testTCPBoundary(store, next)
	for _, test := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/v1/file-transfers/owned", "", 401},
		{"GET", "/v1/file-transfers/owned", "invalid", 401},
		{"GET", "/v1/file-transfers/owned", "revoked", 401},
		{"GET", "/v1/file-transfers/owned", "fixture-valid", 204},
		{"PUT", "/v1/file-transfers/owned/chunks/0", "fixture-valid", 204},
		{"POST", "/v1/file-transfers/owned/complete", "fixture-valid", 204},
		{"POST", "/v1/file-transfers/owned/abort", "fixture-valid", 204},
		{"GET", "/v1/file-transfers/foreign", "fixture-valid", 403},
		{"PUT", "/v1/file-transfers/foreign/chunks/0", "fixture-valid", 403},
		{"GET", "/v1/file-transfers/manual", "fixture-valid", 403},
		{"GET", "/v1/file-transfers/unknown", "fixture-valid", 403},
		{"PUT", "/v1/file-transfers/owned/chunks/-1", "fixture-valid", 403},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != test.want {
			t.Errorf("%s %s (%s) = %d, want %d", test.method, test.path, test.token, result.Code, test.want)
		}
	}
	creation := owned
	creation.TransferID = ""
	for _, test := range []struct {
		name   string
		change func(*filetransfer.Manifest)
		want   int
	}{
		{"own creation", func(*filetransfer.Manifest) {}, 204},
		{"wrong node", func(m *filetransfer.Manifest) { m.SourceNodeID = "node_other" }, 403},
		{"wrong key", func(m *filetransfer.Manifest) { m.SourceNodeKey = "other" }, 403},
		{"wrong kind", func(m *filetransfer.Manifest) { m.TransferKind = filetransfer.KindManualUpload }, 403},
		{"foreign id", func(m *filetransfer.Manifest) { m.TransferID = "foreign" }, 403},
		{"foreign idempotency", func(m *filetransfer.Manifest) { m.IdempotencyKey = "foreign-replay" }, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := creation
			test.change(&manifest)
			raw, _ := json.Marshal(manifest)
			request := httptest.NewRequest(http.MethodPost, "/v1/file-transfers", strings.NewReader(string(raw)))
			request.Header.Set("Authorization", "Bearer fixture-valid")
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != test.want {
				t.Fatalf("status=%d, want %d: %s", result.Code, test.want, result.Body.String())
			}
		})
	}
}

func TestTCPBoundaryBoundsManifestAndRejectsTrailingJSON(t *testing.T) {
	handler := testTCPBoundary(newMemoryTransferStore(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid body reached handler") }))
	for _, body := range []string{`{} {}`, strings.Repeat(" ", 65537), `{"unknown":true}`} {
		request := httptest.NewRequest(http.MethodPost, "/v1/file-transfers", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer fixture-valid")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code < 400 {
			t.Fatalf("invalid body accepted: %d", result.Code)
		}
	}
}
