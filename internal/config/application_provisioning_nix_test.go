package config

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNixApplicationProvisioningNodePolicy(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable")
		}
	}
	apply := `system: let
  base = system.config;
  configured = cloud: valid: system.extendModules { modules = [ ({ lib, ... }: {
    loom.projectApplications.enable = lib.mkForce true;
    loom.projectApplications.grants = lib.mkForce [];
    loom.projectApplications.provisioning = {
      enable = lib.mkForce true;
      cloudBackup = lib.mkForce cloud;
      dataRoot = lib.mkForce (if valid then "/srv/loom/application-data" else "");
      maxMemoryBytes = lib.mkForce 536870912;
      maxPlannedBytes = lib.mkForce 10737418240;
      protonShareIDs = lib.mkForce [ "fixture-share==" ];
      protonAllowGeneration = lib.mkForce true;
    };
  }) ]; };
  c = (configured false true).config;
  backup = (configured true true).config;
  invalid = (configured false false).config;
in {
  defaultEnabled = system.options.loom.projectApplications.provisioning.enable.default;
  defaultCloudBackup = system.options.loom.projectApplications.provisioning.cloudBackup.default;
  defaultGeneration = system.options.loom.projectApplications.provisioning.protonAllowGeneration.default;
  edgeDefault = system.options.loom.projectApplications.edge.enable.default;
  edge = { enabled = base.services.caddy.enable; dataDir = base.services.caddy.dataDir; global = base.services.caddy.globalConfig; import = base.services.caddy.extraConfig; firewall = base.networking.firewall.extraCommands; allowed = base.networking.firewall.allowedTCPPorts; };
  configuredCapabilities = c.systemd.services.loomd.serviceConfig.AmbientCapabilities or [];
  policy = builtins.fromJSON c.environment.etc."loom/project-applications.json".text;
  helperPaths = c.systemd.services."loom-project-applications@".serviceConfig.ReadWritePaths;
  bootPaths = c.systemd.services.loom-project-applications-restore.serviceConfig.ReadWritePaths;
  sessionPaths = c.systemd.services."loom-project-applications@".serviceConfig.BindPaths;
  helperHome = c.systemd.services."loom-project-applications@".serviceConfig.ProtectHome;
  bootHome = c.systemd.services.loom-project-applications-restore.serviceConfig.ProtectHome;
  helperCapabilities = c.systemd.services."loom-project-applications@".serviceConfig.AmbientCapabilities;
  appHome = c.systemd.services."loom-application@".serviceConfig.ProtectHome;
  helper = builtins.fromJSON (builtins.unsafeDiscardStringContext c.environment.etc."loom/project-applications-helper.json".text);
  tmpfiles = c.systemd.tmpfiles.rules;
  directorySetup = c.systemd.services.loom-project-application-directories.script;
  directoryMounts = c.systemd.services.loom-project-application-directories.unitConfig.RequiresMountsFor;
  caddyRequires = c.systemd.services.caddy.requires;
  backupPolicy = builtins.fromJSON backup.environment.etc."loom/project-applications.json".text;
  backupRoot = backup.systemd.services.loomd.environment.LOOM_APPLICATION_DATA_BACKUP_ROOT;
  backupCapabilities = backup.systemd.services.loomd.serviceConfig.AmbientCapabilities;
  backupMounts = backup.systemd.services.loomd.unitConfig.RequiresMountsFor;
  appUmask = backup.systemd.services."loom-application@".serviceConfig.UMask;
  backupFailures = map (a: a.message) (builtins.filter (a: !a.assertion) backup.assertions);
  failures = map (a: a.message) (builtins.filter (a: !a.assertion) c.assertions);
  invalidFailures = map (a: a.message) (builtins.filter (a: !a.assertion) invalid.assertions);
}`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, nix, "--extra-experimental-features", "nix-command flakes", "eval", "--no-write-lock-file", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = repoRoot(t)
	raw, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("render application node policy: %v\n%s", err, exit.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		EdgeDefault bool
		Edge        struct {
			Enabled                           bool
			DataDir, Global, Import, Firewall string
			Allowed                           []int
		}
		BackupRoot, AppUmask                             string
		BackupCapabilities, BackupMounts, BackupFailures []string
		BackupPolicy                                     struct {
			Provisioning struct {
				CloudBackup bool `json:"cloud_backup"`
			} `json:"provisioning"`
		}
		DefaultEnabled, DefaultCloudBackup, DefaultGeneration bool
		ConfiguredCapabilities                                []string
		Policy                                                struct {
			Grants       []json.RawMessage `json:"grants"`
			Provisioning struct {
				DataRoot        string `json:"data_root"`
				MaxMemoryBytes  uint64 `json:"max_memory_bytes"`
				MaxPlannedBytes uint64 `json:"max_planned_bytes"`
				Proton          struct {
					AllowGeneration bool     `json:"allow_generation"`
					ShareIDs        []string `json:"share_ids"`
					CredentialRoot  string   `json:"credential_root"`
				} `json:"proton"`
			} `json:"provisioning"`
		}
		HelperPaths, BootPaths, Tmpfiles, Failures, InvalidFailures []string
		SessionPaths                                                []string
		HelperHome, BootHome                                        string
		HelperCapabilities                                          []string
		AppHome                                                     bool
		DirectorySetup                                              string
		DirectoryMounts, CaddyRequires                              []string
		Helper                                                      struct {
			TrustedParentOwners map[string]uint32 `json:"trusted_parent_owners"`
			PassCLI             string            `json:"pass_cli"`
			PassSessionEnsure   string            `json:"pass_session_ensure"`
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.EdgeDefault || !got.Edge.Enabled || got.Edge.DataDir != "/srv/loom/application-data/.caddy" || !strings.Contains(got.Edge.Global, "default_bind 10.44.0.2") || !strings.Contains(got.Edge.Import, "import /var/lib/loom-project-applications-edge/*.caddy") || !strings.Contains(got.Edge.Firewall, "-i wg0 -s 10.44.0.1/32 -d 10.44.0.2/32 -p tcp -m multiport --dports 80,443") || slices.Contains(got.Edge.Allowed, 80) || slices.Contains(got.Edge.Allowed, 443) {
		t.Fatal("public application ingress boundary", got.Edge)
	}
	if got.DefaultEnabled || got.DefaultCloudBackup || got.DefaultGeneration || len(got.ConfiguredCapabilities) != 0 || len(got.Failures) != 0 || len(got.InvalidFailures) == 0 {
		t.Fatalf("activation/default policy mismatch: %+v", got)
	}
	if got.BackupRoot != "/srv/loom/application-data" || !got.BackupPolicy.Provisioning.CloudBackup || !slices.Equal(got.BackupCapabilities, []string{"CAP_DAC_READ_SEARCH"}) || !slices.Contains(got.BackupMounts, got.BackupRoot) || got.AppUmask != "0077" || len(got.BackupFailures) != 0 {
		t.Fatalf("backup root/authority/app privacy mismatch: %+v", got)
	}
	p := got.Policy.Provisioning
	if got.HelperHome != "tmpfs" || got.BootHome != "tmpfs" || !got.AppHome || !slices.Equal(got.HelperCapabilities, []string{"CAP_SETUID", "CAP_SETGID"}) || !slices.Equal(got.SessionPaths, []string{"/home/agents/.config/proton-pass-cli", "/home/agents/.local/state/proton-pass-cli", "/home/agents/.local/share/proton-pass-cli"}) {
		t.Fatal("helper cannot traverse only its Proton binds or switch child identity", got)
	}
	if len(got.Policy.Grants) != 0 || p.DataRoot != "/srv/loom/application-data" || p.MaxMemoryBytes != 536870912 || p.MaxPlannedBytes != 10737418240 {
		t.Fatalf("rendered policy mismatch: %+v", got.Policy)
	}
	if !slices.Contains(got.HelperPaths, p.DataRoot) || !slices.Contains(got.BootPaths, p.DataRoot) || !strings.Contains(got.DirectorySetup, "install -d -o root -g root -m 0711") || !strings.Contains(got.DirectorySetup, "install -d -o caddy -g caddy -m 0700") || !slices.Contains(got.DirectoryMounts, p.DataRoot) || !slices.Contains(got.CaddyRequires, "loom-project-application-directories.service") || got.Helper.TrustedParentOwners["/srv/loom"] != 1000 {
		t.Fatalf("allocation root not available without per-app grants: %+v", got)
	}
	if !p.Proton.AllowGeneration || p.Proton.CredentialRoot != "/var/lib/loom-project-application-credentials" || !slices.Equal(p.Proton.ShareIDs, []string{"fixture-share=="}) || !slices.Contains(got.HelperPaths, p.Proton.CredentialRoot) || !slices.Contains(got.Tmpfiles, "d /var/lib/loom-project-application-credentials 0700 root root -") || !slices.Contains(got.SessionPaths, "/home/agents/.local/share/proton-pass-cli") || got.Helper.PassCLI == "" || got.Helper.PassSessionEnsure != "/run/current-system/sw/bin/pass-session-ensure" {
		t.Fatal("Proton materialization policy/custody not wired", got)
	}
}
