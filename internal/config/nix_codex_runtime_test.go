package config_test

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestNixCodexRuntimeAndIndependentMinaTools(t *testing.T) {
	nix := "/nix/var/nix/profiles/default/bin/nix"
	if path, err := exec.LookPath("nix"); err == nil {
		nix = path
	}
	expression := `let
 source = (import ./tests/nix/source-flake.nix {}).nixosConfigurations.hardware-main;
 s = source.extendModules { modules = [({ lib, ... }: {
   # Native Codex tools do not need to realize the Linux Hermes backend.
   loom.hermesPlatform.enable = lib.mkForce false;
   loom.codexRuntime.recoveryEnable = true;
   loom.codexRuntime.autoUpdateEnable = true;
   loom.notesWorkspace.enable = true;
   loom.notesWorkspace.couchDB.enable = true;
   loom.notesWorkspace.recovery.enable = true;
 })]; };
 c = s.config;
 without = (s.extendModules { modules = [ ({ lib, ... }: { loom.codexRuntime.enable = lib.mkForce false; }) ]; }).config;
 off = (s.extendModules { modules = [ ({ lib, ... }: {
   loom.mina.enable = lib.mkForce false;
   loom.mina.scheduleObservationEnabled = lib.mkForce false;
   loom.mina.projectSchedulesEnabled = lib.mkForce false;
 }) ]; }).config;
 in {
  failed = map (x: x.message) (builtins.filter (x: !x.assertion) off.assertions);
  linger = c.users.users.agents.linger;
  unit = c.systemd.user.units."loom-codex-app-server.service".text;
  updater = c.systemd.user.units."loom-codex-update.service".text;
  updaterTimer = c.systemd.user.units."loom-codex-update.timer".text;
  retryTimer = c.systemd.user.units."loom-codex-update-retry.timer".text;
  capture = c.systemd.units."loom-codex-recovery.service".text;
  captureTimer = c.systemd.units."loom-codex-recovery.timer".text;
  access = c.system.activationScripts.loomAgentsAccess.text;
  tools = map (p: p.pname or p.name) off.users.users.agents.packages;
  systemTools = map (p: p.pname or p.name) off.environment.systemPackages;
  jobUnit = c.systemd.units."loom-codex-jobs@.service".text;
  binding = builtins.fromJSON off.environment.etc."loom-mina-accounts.json".text;
  orcaUnchanged = !(c.systemd.units ? "orca-serve.service") && !(without.systemd.units ? "orca-serve.service");
  hermesRetired = !c.loom.mina.enable && !c.loom.mina.recoveryEnabled && !c.loom.mina.scheduleObservationEnabled && !c.loom.mina.projectSchedulesEnabled;
  selected = c.loom.mina.selected;
  captureDefaultOff = source.options.loom.codexRuntime.recoveryEnable.default == false;
  updateDefaultOff = source.options.loom.codexRuntime.autoUpdateEnable.default == false;
  retainedKey = c.systemd.services.loomd.environment.LOOM_MINA_RECOVERY_PUBLIC_KEY;
  recovery = c.systemd.units."loom-notes-recovery.service".text;
 }`
	command := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--impure", "--json", "--expr", expression)
	command.Dir = minaRepo(t)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex runtime render: %v\n%s", err, output)
	}
	var got struct {
		Failed, Tools, SystemTools          []string
		JobUnit                             string
		Linger, OrcaUnchanged               bool
		Unit                                string
		Updater, UpdaterTimer, RetryTimer   string
		Capture, CaptureTimer               string
		Access                              string
		HermesRetired, Selected             bool
		CaptureDefaultOff, UpdateDefaultOff bool
		RetainedKey, Recovery               string
		Binding                             struct {
			Basecamp struct {
				Profile    string
				IdentityID int `json:"identity_id"`
			}
		}
	}
	// Nix's dirty-tree warning is on stderr, not part of its JSON result.
	start := strings.Index(string(output), "{\"")
	if start < 0 || json.Unmarshal(output[start:], &got) != nil {
		t.Fatalf("invalid render: %s", output)
	}
	if len(got.Failed) != 0 || !got.Linger || !got.OrcaUnchanged {
		t.Fatalf("lifecycle or unrelated ORCA drift: %+v", got)
	}
	for _, term := range []string{"ConditionUser=agents", "ConditionFileIsExecutable=/home/agents/.local/bin/codex", "Type=forking", "GuessMainPID=true", "X-RestartIfChanged=false", "WantedBy=default.target", "/home/agents/.local/bin/codex app-server daemon start", "daemon stop", "NoNewPrivileges=true"} {
		if !strings.Contains(got.Unit, term) {
			t.Errorf("Codex unit lost %s", term)
		}
	}
	for _, term := range []string{"ConditionUser=agents", "Type=oneshot", "NoNewPrivileges=true", "loom-codex-update", "WorkingDirectory=/home/agents"} {
		if !strings.Contains(got.Updater, term) {
			t.Errorf("idle updater lost %s", term)
		}
	}
	if !strings.Contains(got.UpdaterTimer, "04:30:00 Europe/Amsterdam") || !strings.Contains(got.UpdaterTimer, "Persistent=false") || !strings.Contains(got.RetryTimer, "OnActiveSec=30min") || strings.Contains(got.RetryTimer, "WantedBy=") {
		t.Fatal("Codex daily check/retry cadence drift")
	}
	for _, name := range []string{"loom-codex-message", "loom-mina-gh", "loom-mina-basecamp", "agent-browser", "chromium", "loom-mac-computer-use-bridge-0.1.0"} {
		if !slices.Contains(got.Tools, name) {
			t.Errorf("Hermes-disabled tools lost %s", name)
		}
	}
	if !slices.Contains(got.SystemTools, "python3") || !strings.Contains(got.JobUnit, "/run/current-system/sw/bin") {
		t.Fatal("scheduled Codex needs Python in the system profile, including login shells")
	}
	if !strings.Contains(got.Capture, "User=agents") || !strings.Contains(got.Capture, "Group=loom") || !strings.Contains(got.CaptureTimer, "02:40:00 Europe/Amsterdam") || !strings.Contains(got.CaptureTimer, "Persistent=false") {
		t.Fatal("Codex capture identity or cadence drift")
	}
	if strings.Count(got.Access, "-path /srv/loom/agents/mina/recovery-codex") != 2 {
		t.Fatal("generic workspace ACL traversal must prune sealed Codex recovery directories and files")
	}
	if got.Binding.Basecamp.Profile != "mina" || got.Binding.Basecamp.IdentityID != 200001 {
		t.Fatal("MINA binding changed when disabling Hermes")
	}
	if !got.HermesRetired || !got.Selected || !got.CaptureDefaultOff || !got.UpdateDefaultOff || len(got.RetainedKey) != 64 {
		t.Fatal("active Hermes ownership remains or historical MINA identity was lost")
	}
	if !strings.Contains(got.Recovery, "ExecStopPost=") || !strings.Contains(got.Recovery, "--cleanup") {
		t.Fatal("Notes capture termination must restore transport and clear its pause marker")
	}
}
