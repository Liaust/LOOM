package config_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Only evaluation: hardware-main plus disposable overrides, never activation.
const macRender = `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in let
 f = (import ./tests/nix/source-flake.nix {});
 old = system.config;
 oldOn = (system.extendModules { modules = [{ loom.morathustra.macComputerUseEnabled = true; }]; }).config;
 minaBase = { lib, ... }: {
   loom.morathustra.enable = lib.mkForce false;
   loom.morathustra.recoveryEnabled = lib.mkForce false;
   loom.morathustra.recoveryPublicKey = lib.mkForce "";
   loom.mina = { selected = true; enable = true; browserEnabled = true; externalAccountsEnabled = true;
     externalAccountBinding = old.loom.morathustra.externalAccountBinding;
     model = old.loom.morathustra.model; modelProvider = old.loom.morathustra.modelProvider;
   };
 };
 mina = (system.extendModules { modules = [ minaBase ]; }).config;
 minaOn = (system.extendModules { modules = [ minaBase { loom.mina.macComputerUseEnabled = true; } ]; }).config;
 minaPinned = (system.extendModules { modules = [ minaBase { loom.mina = {
   macComputerUseEnabled = true;
   macComputerUseBindingSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
 }; } ]; }).config;
 failures = c: map (a: a.message) (builtins.filter (a: !a.assertion) c.assertions);
 conflicts = [
   (system.extendModules { modules = [{ loom.mina.macComputerUseEnabled = true; }]; }).config
   (system.extendModules { modules = [ minaBase { loom.morathustra.macComputerUseEnabled = true; } ]; }).config
   (system.extendModules { modules = [ minaBase ({lib,...}: { loom.mina.enable = lib.mkForce false; loom.mina.macComputerUseEnabled = true; }) ]; }).config
   (system.extendModules { modules = [({lib,...}: { loom.morathustra.enable = lib.mkForce false; loom.morathustra.macComputerUseEnabled = true; })]; }).config
   (system.extendModules { modules = [{ loom.mina.enable = true; loom.morathustra.macComputerUseEnabled = true; loom.mina.macComputerUseEnabled = true; }]; }).config
 ];
 capture = c: id: let r = c.loom.${id}; svc = c.systemd.services."loom-${id}"; in {
   failed = failures c;
   home = r.hermesHome;
   macEnabled = r.macComputerUseEnabled;
   baseline = r.baselineSettings;
   baselineText = r.baselineConfig.text;
   env = svc.environment;
   gateway = c.systemd.units."loom-${id}.service".text;
   serviceConfig = svc.serviceConfig;
   unitConfig = svc.unitConfig;
   packages = map toString c.users.users.agents.packages;
   native = toString r.package;
   interactive = toString r.interactivePackage;
   selector = r.interactivePackage.macSelector or "";
   wrapperBuild = r.interactivePackage.buildCommand or "";
   orcaDrv = c.loom.orca.package.drvPath;
   shellDrvs = builtins.attrNames (builtins.getContext c.system.activationScripts.loomOrcaShell.text);
   stable = {
     accounts = builtins.mapAttrs (_: v: v.text) (system.lib.filterAttrs (n: _: builtins.match "loom-.*-accounts.json" n != null) c.environment.etc);
     activation = builtins.mapAttrs (n: v: if builtins.isAttrs v then v.text else v) (system.lib.filterAttrs (n: _: builtins.match "loom.*" n != null && n != "loomOrcaShell") c.system.activationScripts);
     timers = builtins.mapAttrs (n: _: c.systemd.units."${n}.timer".text) c.systemd.timers;
     recovery = builtins.mapAttrs (n: _: c.systemd.units."${n}.service".text) (system.lib.filterAttrs (n: _: builtins.match "loom-.*-recovery-publish" n != null) c.systemd.services);
     loomdEnv = c.systemd.services.loomd.environment;
     orcaEnv = builtins.mapAttrs (_: v: builtins.replaceStrings [ (toString c.system.path) (toString c.loom.orca.package) ] [ "SYSTEM_PATH" "SELECTED_ORCA" ] v) c.systemd.services.orca-serve.environment;
     orcaService = c.systemd.services.orca-serve.serviceConfig // { ExecStart = "package-dependent"; };
     globals = builtins.removeAttrs (c.environment.variables // c.environment.sessionVariables) [ "NAUTILUS_4_EXTENSION_DIR" ];
     users = c.users.users.agents // { packages = []; };
     serviceNames = builtins.attrNames c.systemd.services;
     etcNames = builtins.attrNames c.environment.etc;
     tmpfiles = c.systemd.tmpfiles.rules;
   };
 };
 in {
   old = capture old "morathustra"; oldOn = capture oldOn "morathustra";
   mina = capture mina "mina"; minaOn = capture minaOn "mina";
   minaPinned = capture minaPinned "mina";
   pinnedBridge = (f.packages.x86_64-linux.mac-computer-use-bridge.overrideAttrs (_: {
     pinnedBindingSHA256 = minaPinned.loom.mina.macComputerUseBindingSHA256;
   })).drvPath;
   conflicts = map failures conflicts;
   conflictActivationRefused = !(builtins.tryEval (builtins.head conflicts).system.build.toplevel.drvPath).success;
   connector = builtins.listToAttrs (map (system: { name = system; value = toString f.packages.${system}.mac-computer-use-bridge; }) [ "aarch64-darwin" "x86_64-linux" "aarch64-linux" ]);
 }`

type macSnapshot struct {
	Failed                                                                            []string
	Home, BaselineText, Gateway, Native, Interactive, Selector, WrapperBuild, OrcaDrv string
	MacEnabled                                                                        bool
	Baseline, ServiceConfig, UnitConfig, Stable                                       map[string]any
	Env                                                                               map[string]string
	Packages, ShellDrvs                                                               []string
	Shell, Bwrap                                                                      string
}

// Accept both Nix 2.x and 3.x derivation-show envelopes. No realization.
func macDerivation(t *testing.T, nix, drv string) map[string]any {
	t.Helper()
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "derivation", "show", drv)
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc["derivations"]; ok {
		doc = v.(map[string]any)
	}
	if len(doc) != 1 {
		t.Fatalf("ambiguous derivation %s", drv)
	}
	for _, v := range doc {
		return v.(map[string]any)
	}
	panic("empty derivation")
}

func macBwrap(t *testing.T, nix, drv string) string {
	t.Helper()
	d := macDerivation(t, nix, drv)
	deps, _ := d["inputDrvs"].(map[string]any)
	if inputs, ok := d["inputs"].(map[string]any); ok {
		deps = inputs["drvs"].(map[string]any)
	}
	for name := range deps {
		if strings.HasSuffix(name, "-orca-1.4.191-bwrap.drv") {
			if !strings.HasPrefix(name, "/") {
				name = "/nix/store/" + name
			}
			return macDerivation(t, nix, name)["env"].(map[string]any)["text"].(string)
		}
	}
	t.Fatal("actual selected ORCA derivation lacks bwrap dependency")
	return ""
}

func TestNixMacComputerUseRuntimeRender(t *testing.T) {
	nix := "/nix/var/nix/profiles/default/bin/nix"
	if p, err := exec.LookPath("nix"); err == nil {
		nix = p
	} else if _, err := os.Stat(nix); err != nil {
		t.Skip("Mac runtime render requires Nix")
	}
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--impure", "--json", ".#nixosConfigurations.hardware-main", "--apply", macRender)
	cmd.Dir = minaRepo(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("required actual Mac render: %v\n%s", err, stderr.String())
	}
	var result struct {
		Old, OldOn, Mina, MinaOn, MinaPinned macSnapshot
		PinnedBridge                         string
		Conflicts                            [][]string
		ConflictActivationRefused            bool
		Connector                            map[string]string
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	pinEnv := macDerivation(t, nix, result.PinnedBridge)["env"].(map[string]any)
	if pinEnv["pinnedBindingSHA256"] != strings.Repeat("a", 64) ||
		result.MinaPinned.Env["HERMES_CUA_DRIVER_CMD"] != pinEnv["out"].(string)+"/bin/loom-mac-computer-use-bridge" ||
		result.MinaPinned.Env["HERMES_MAC_BINDING"] != result.MinaOn.Env["HERMES_MAC_BINDING"] ||
		result.MinaPinned.Native != result.MinaOn.Native ||
		!reflect.DeepEqual(result.MinaPinned.ServiceConfig, result.MinaOn.ServiceConfig) ||
		!reflect.DeepEqual(result.MinaPinned.Baseline, result.MinaOn.Baseline) ||
		len(result.MinaPinned.Failed) != 0 {
		t.Fatal("public pin did not select only the immutable transport launcher")
	}
	require := func(body string, wants ...string) {
		t.Helper()
		for _, s := range wants {
			if !strings.Contains(body, s) {
				t.Errorf("render missing %q", s)
			}
		}
	}
	if !result.ConflictActivationRefused || len(result.Conflicts) != 5 {
		t.Fatal("missing selector refusal")
	}
	for _, v := range result.Conflicts {
		if len(v) == 0 {
			t.Fatal("orphan/retired/conflicting opt-in admitted")
		}
	}
	for _, v := range []*macSnapshot{&result.Old, &result.OldOn, &result.Mina, &result.MinaOn} {
		if len(v.Failed) > 0 {
			t.Fatalf("valid render refused: %v", v.Failed)
		}
		v.Bwrap = macBwrap(t, nix, v.OrcaDrv)
		for _, drv := range v.ShellDrvs {
			if strings.HasSuffix(drv, "-agents.bashrc.drv") {
				v.Shell = macDerivation(t, nix, drv)["env"].(map[string]any)["text"].(string)
			}
		}
		if v.Shell == "" {
			t.Fatal("actual shell missing")
		}
		require(v.Gateway, "User=agents", "Group=agents", "NoNewPrivileges=true", "ProtectHome=true", "ProtectSystem=strict", "PrivateTmp=true")
		require(v.Bwrap, "--ro-bind /etc /.host-etc", `auto_mounts+=(--bind "$dir" "$dir")`)
		if strings.Contains(v.Bwrap, "ignored=(/nix /dev /proc /etc /var") {
			t.Fatal("key namespace hidden")
		}
		if v.MacEnabled {
			require(v.Shell, `export HERMES_HOME="${HERMES_HOME:-`+v.Home+`}"`)
		} else {
			require(v.Shell, "export HERMES_HOME="+v.Home)
		}
		for k := range v.Env {
			if strings.HasPrefix(k, "HERMES_MAC") && k != "HERMES_MAC_BINDING" {
				t.Fatal("invented Mac variable")
			}
		}
	}
	for _, pair := range [][2]*macSnapshot{{&result.Old, &result.OldOn}, {&result.Mina, &result.MinaOn}} {
		off, on := pair[0], pair[1]
		if off.MacEnabled || !on.MacEnabled {
			t.Fatal("default/opt-in wrong")
		}
		if !reflect.DeepEqual(off.Stable, on.Stable) {
			for k, v := range off.Stable {
				if !reflect.DeepEqual(v, on.Stable[k]) {
					t.Errorf("stable %s changed: before=%v after=%v", k, v, on.Stable[k])
				}
			}
		}
		if !reflect.DeepEqual(off.UnitConfig, on.UnitConfig) {
			t.Fatal("Mac binding must not gate startup")
		}
		if off.Native != off.Interactive || on.Native == on.Interactive {
			t.Fatal("launcher selection wrong")
		}
		if off.Selector != "" || off.Env["HERMES_MAC_BINDING"] != "" || off.Env["HERMES_CUA_DRIVER_CMD"] != "" || strings.Contains(off.Bwrap, "loom-mac-computer-use") {
			t.Fatal("disabled route escaped")
		}
		bridge := result.Connector["x86_64-linux"]
		if on.Env["HERMES_CUA_DRIVER_CMD"] != bridge+"/bin/loom-mac-computer-use-bridge" || on.Env["HERMES_MAC_BINDING"] != "/etc/loom-mac-computer-use/binding.json" {
			t.Fatal("canonical binding/installed bridge mismatch")
		}
		if !slices.Contains(on.Packages, bridge) || slices.Contains(off.Packages, bridge) || !slices.Contains(on.Packages, on.Interactive) || slices.Contains(on.Packages, on.Native) {
			t.Fatal("profile package collision or missing bridge")
		}
		require(on.WrapperBuild, "hermes hermes-agent hermes-acp", "wrapProgram", on.Selector[:15])
		require(on.Bwrap, "--symlink /.host-etc/loom-mac-computer-use /etc/loom-mac-computer-use")
		id := "morathustra"
		if strings.Contains(on.Home, "/mina/") {
			id = "mina"
		}
		require(on.Bwrap, "--symlink /.host-etc/loom-"+id+"-accounts.json /etc/loom-"+id+"-accounts.json")
		// All service settings except the two optional readonly paths remain exact.
		ro := on.ServiceConfig["ReadOnlyPaths"].([]any)
		if !reflect.DeepEqual(ro, append(append([]any{}, off.ServiceConfig["ReadOnlyPaths"].([]any)...), "-/etc/loom-mac-computer-use", "-/var/lib/loom-mac-computer-use/id_ed25519")) {
			t.Fatal("key/public namespace scope")
		}
		onSC := map[string]any{}
		for k, v := range on.ServiceConfig {
			onSC[k] = v
		}
		onSC["ReadOnlyPaths"] = off.ServiceConfig["ReadOnlyPaths"]
		if !reflect.DeepEqual(off.ServiceConfig, onSC) {
			t.Fatal("service privilege drift")
		}
		for k, v := range off.Env {
			if k != "PATH" && on.Env[k] != v {
				t.Fatalf("unrelated env %s changed", k)
			}
		}
		require(on.Env["PATH"], bridge+"/bin")
		// Baselines are data: compare every other setting and disabled choice.
		offBase := map[string]any{}
		for k, v := range off.Baseline {
			offBase[k] = v
		}
		onBase := map[string]any{}
		for k, v := range on.Baseline {
			onBase[k] = v
		}
		offSkills := offBase["skills"].(map[string]any)
		onSkills := onBase["skills"].(map[string]any)
		want := []any{}
		for _, s := range offSkills["disabled"].([]any) {
			if s != "computer-use" {
				want = append(want, s)
			}
		}
		if !reflect.DeepEqual(want, onSkills["disabled"]) {
			t.Fatal("unrelated skill disabled choice changed")
		}
		// Marshal copies so the receipt retains the real enabled and disabled lists.
		clone := map[string]any{}
		for k, v := range onSkills {
			clone[k] = v
		}
		clone["disabled"] = offSkills["disabled"]
		onBase["skills"] = clone
		delete(onBase, "platform_toolsets")
		delete(onBase, "computer_use")
		if !reflect.DeepEqual(offBase, onBase) {
			t.Fatal("unrelated model/context/skill baseline changed")
		}
	}
	if len(result.Connector) != 3 {
		t.Fatal("exported connector platform set changed")
	}
	// Source evaluation must not require a maintainer's previously realized store
	// output. The output hash changes when the pinned package inputs change.
	for _, platform := range []string{"aarch64-darwin", "x86_64-linux", "aarch64-linux"} {
		out := result.Connector[platform]
		if !strings.HasPrefix(out, "/nix/store/") || !strings.HasSuffix(out, "-loom-mac-computer-use-bridge-0.1.0") || filepath.Clean(out) != out {
			t.Errorf("invalid connector output for %s: %q", platform, out)
		}
	}
	receipt := filepath.Join(minaRepo(t), ".loom-acceptance/main-mac-wiring")
	if err := os.MkdirAll(receipt, 0700); err != nil {
		t.Fatal(err)
	}
	b, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipt, "render.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipt, "render-apply.nix"), []byte(macRender), 0600); err != nil {
		t.Fatal(err)
	}
}
