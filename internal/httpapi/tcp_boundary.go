package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/nodes"
)

type tcpTransportKey struct{}

// Only the listener can mark a request as TCP; caller-supplied headers cannot
// turn a network request into trusted local administration.
func markTCPTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tcpTransportKey{}, true)))
	})
}

type tcpBoundary struct {
	next         http.Handler
	authenticate func(context.Context, string) (nodes.Node, error)
	transfers    filetransfer.TransferStore
	reject       func(http.ResponseWriter, *http.Request, int, string)
}

func (s Server) withTCPBoundary(next http.Handler) http.Handler {
	return tcpBoundary{
		next: next,
		authenticate: func(ctx context.Context, token string) (nodes.Node, error) {
			if s.services.Nodes.DB == nil {
				return nodes.Node{}, errors.New("node authentication is unavailable")
			}
			_, node, err := s.services.Nodes.AuthenticateCredential(ctx, token)
			return node, err
		},
		transfers: s.services.FileTransfer.Store,
		reject: func(w http.ResponseWriter, r *http.Request, status int, code string) {
			correlationID, _ := requestMeta(r)
			w.Header().Set("Cache-Control", "no-store")
			s.writeError(w, correlationID, status, code, "transport", "private_tcp",
				"Private TCP permits node protocol only; use the local socket through SSH for administration.", nil)
		},
	}
}

func (b tcpBoundary) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if tcp, _ := r.Context().Value(tcpTransportKey{}).(bool); !tcp {
		b.next.ServeHTTP(w, r)
		return
	}
	// Reject noncanonical paths before ServeMux can redirect or clean them into
	// another endpoint. Keep this list explicit: a new API is not remotely trusted.
	if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "//") || strings.Contains(r.URL.Path, "/./") || strings.Contains(r.URL.Path, "/../") {
		b.reject(w, r, http.StatusForbidden, "transport.local_required")
		return
	}
	if r.Method == http.MethodPost && nodeProtocolPath(r.URL.Path) {
		// These existing handlers authenticate their typed node credential (or
		// create a pending enrollment, which does not grant access).
		b.next.ServeHTTP(w, r)
		return
	}
	if !tcpTransferRoute(r.Method, r.URL.Path) {
		b.reject(w, r, http.StatusForbidden, "transport.local_required")
		return
	}
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		b.reject(w, r, http.StatusUnauthorized, "transport.node_credential_required")
		return
	}
	node, err := b.authenticate(r.Context(), fields[1])
	if err != nil || node.NodeID == "" || node.NodeKey == "" || node.Status != "active" {
		b.reject(w, r, http.StatusUnauthorized, "transport.node_credential_invalid")
		return
	}
	if b.transfers == nil {
		b.reject(w, r, http.StatusServiceUnavailable, "transport.node_transfer_unavailable")
		return
	}
	if r.URL.Path == "/v1/file-transfers" {
		b.createTransfer(w, r, node)
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, "/v1/file-transfers/")
	id := strings.SplitN(ref, "/", 2)[0]
	manifest, err := b.transfers.GetManifest(r.Context(), id)
	if err != nil || !nodeOwnsTransfer(node, manifest) {
		// Do not disclose whether another node's transfer exists.
		b.reject(w, r, http.StatusForbidden, "transport.node_transfer_denied")
		return
	}
	b.next.ServeHTTP(w, r)
}

func nodeProtocolPath(path string) bool {
	switch path {
	case "/v1/node-agent/enroll", "/v1/node-agent/heartbeat",
		"/v1/node-agent/poll", "/v1/node-agent/ack",
		"/v1/node-agent/providers/advertise", "/v1/node-agent/capability-result",
		"/v1/node-agent/sync/batches", "/v1/node-agent/sync/object-upload",
		"/v1/node-agent/sync/private-backup", "/v1/node-agent/sync/deletion-request",
		"/v1/node-agent/watched-roots/report", "/v1/node-agent/watched-roots/backup-batches":
		return true
	}
	return false
}

func tcpTransferRoute(method, path string) bool {
	if path == "/v1/file-transfers" {
		return method == http.MethodPost
	}
	if !strings.HasPrefix(path, "/v1/file-transfers/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/file-transfers/"), "/")
	if parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return false
	}
	if len(parts) == 1 {
		return method == http.MethodGet
	}
	if len(parts) == 2 && (parts[1] == "complete" || parts[1] == "abort") {
		return method == http.MethodPost
	}
	if len(parts) == 3 && parts[1] == "chunks" && method == http.MethodPut {
		index, err := strconv.ParseInt(parts[2], 10, 64)
		return err == nil && index >= 0
	}
	return false
}

func nodeOwnsTransfer(node nodes.Node, manifest filetransfer.Manifest) bool {
	return manifest.SourceNodeID == node.NodeID && manifest.SourceNodeKey == node.NodeKey &&
		manifest.TransferKind == filetransfer.KindWatchedRootBackup &&
		manifest.CustodyMode == filetransfer.CustodyModeBackupCopy
}

func (b tcpBoundary) createTransfer(w http.ResponseWriter, r *http.Request, node nodes.Node) {
	const maxManifestBytes = 64 * 1024
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManifestBytes))
	if err != nil {
		b.reject(w, r, http.StatusRequestEntityTooLarge, "transport.node_manifest_too_large")
		return
	}
	var manifest filetransfer.Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(new(any)) != io.EOF {
		b.reject(w, r, http.StatusBadRequest, "request.invalid_json")
		return
	}
	if !nodeOwnsTransfer(node, manifest) {
		b.reject(w, r, http.StatusForbidden, "transport.node_transfer_denied")
		return
	}
	if manifest.TransferID != "" || (manifest.IdempotencyKey != "" && manifest.IdempotencyKey != filetransfer.IdempotencyKey(manifest)) {
		// Remote creation must use a server-generated ID and the deterministic
		// node-bound key, preventing cross-node insertion/replay races.
		b.reject(w, r, http.StatusForbidden, "transport.node_transfer_identity_invalid")
		return
	}
	// Creation can replay an existing ID or idempotency key. Authorize those
	// stored identities too, not just the new caller-supplied manifest.
	lookups := []func() (filetransfer.Manifest, error){
		func() (filetransfer.Manifest, error) {
			key := strings.TrimSpace(manifest.IdempotencyKey)
			if key == "" {
				key = filetransfer.IdempotencyKey(manifest)
			}
			return b.transfers.GetManifestByIdempotencyKey(r.Context(), key)
		},
	}
	for _, lookup := range lookups {
		existing, err := lookup()
		if errors.Is(err, filetransfer.ErrNotFound) {
			continue
		}
		if err != nil || !nodeOwnsTransfer(node, existing) {
			b.reject(w, r, http.StatusForbidden, "transport.node_transfer_denied")
			return
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	b.next.ServeHTTP(w, r)
}
