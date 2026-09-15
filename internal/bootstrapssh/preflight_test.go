package bootstrapssh

import "testing"

func TestParsePreflightOutput(t *testing.T) {
	facts := ParsePreflightOutput(`
hostname=loom-workspace
os=Linux
arch=x86_64
user=loomadmin
uid=1000
home_dir=/home/loomadmin
has_sudo=true
has_systemd=true
has_launchd=false
has_nix=true
has_git=true
has_go=false
has_rsync=true
has_tar=true
existing_loom=/usr/local/bin/loom
existing_loomd=
existing_node_agent=/usr/local/bin/loom-node-agent
existing_manifest_path=/home/loomadmin/.config/loom/install.yaml
`)
	if facts.Hostname != "loom-workspace" || facts.OS != "linux" || facts.Arch != "amd64" {
		t.Fatalf("facts identity mismatch: %#v", facts)
	}
	if !facts.HasSudo || !facts.HasSystemd || facts.HasLaunchd || !facts.HasNix || !facts.HasGit || facts.HasGo || !facts.HasRsync || !facts.HasTar {
		t.Fatalf("facts booleans mismatch: %#v", facts)
	}
	if facts.ExistingLoom == "" || facts.ExistingLoomd != "" || facts.ExistingNodeAgent == "" || facts.ExistingManifestPath == "" {
		t.Fatalf("facts binaries/manifest mismatch: %#v", facts)
	}
}
