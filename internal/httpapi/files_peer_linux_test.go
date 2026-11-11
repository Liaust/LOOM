//go:build linux

package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixRootTransportUsesKernelPeer(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ConnContext: unixFileTransportContext, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		root, _ := r.Context().Value(localRootTransportKey{}).(bool)
		_ = json.NewEncoder(w).Encode(root)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	request, _ := http.NewRequest("GET", "http://local/peer?root=true", nil)
	request.Header.Set("X-Loom-Root", "true")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var root bool
	if err := json.NewDecoder(response.Body).Decode(&root); err != nil {
		t.Fatal(err)
	}
	if root != (os.Geteuid() == 0) {
		t.Fatalf("kernel root identity: %v", root)
	}
}
