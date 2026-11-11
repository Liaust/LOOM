package config

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestGoodnotesPrivateSocketDoesNotDependOnPostBasicNetworking(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix unavailable")
	}
	module := filepath.Join(repoRoot(t), "nix/hosts/hardware-main/goodnotes-private.nix")
	expression := `let m = import ` + module + ` { pkgs.systemd = "/fixture/systemd"; }; in {
  socket = m.systemd.sockets.loom-goodnotes-private;
  proxy = m.systemd.services.loom-goodnotes-private;
  ports = m.networking.firewall.interfaces.wg0.allowedTCPPorts;
}`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, nix, "--extra-experimental-features", "nix-command", "eval", "--impure", "--json", "--expr", expression).CombinedOutput()
	if err != nil {
		t.Fatalf("evaluate private socket: %v\n%s", err, raw)
	}
	var got struct {
		Socket struct {
			After, Requires, WantedBy []string
			SocketConfig              struct {
				ListenStream string
				FreeBind     bool
			}
		}
		Proxy struct{ After, Requires []string }
		Ports []int
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Socket.After) != 0 || len(got.Socket.Requires) != 0 || !got.Socket.SocketConfig.FreeBind || got.Socket.SocketConfig.ListenStream != "10.44.0.2:18081" || !slices.Equal(got.Socket.WantedBy, []string{"sockets.target"}) {
		t.Fatalf("early socket still waits for networking or lost private bind: %+v", got.Socket)
	}
	if !slices.Contains(got.Proxy.After, "wireguard-wg0.service") || !slices.Contains(got.Proxy.Requires, "wireguard-wg0.service") || !slices.Equal(got.Ports, []int{18081}) {
		t.Fatalf("proxy/network boundary changed: %+v", got)
	}
}
