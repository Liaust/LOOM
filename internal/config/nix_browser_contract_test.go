package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestNixAgentBrowserEnvironment(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("Nix unavailable; run rendered browser gate before deployment")
		}
	}
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--impure", "--json",
		".#nixosConfigurations.hardware-main", "--apply", `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in
let
  on = system.config;
  off = (system.extendModules { modules = [ ({lib,...}: {
    loom.morathustra.browserEnabled = lib.mkForce false;
  }) ]; }).config;
  names = c: map (p: p.pname or p.name) c.users.users.agents.packages;
in {
  enabled = on.loom.morathustra.browserEnabled;
  packages = names on;
  disabled = names off;
  path = on.systemd.services.loom-morathustra.environment.PATH;
  service = on.systemd.services.loom-morathustra.serviceConfig;
  runtimeEnvironment = on.systemd.services.loom-morathustra.environment.LOOM_BROWSER_RUNTIME_DIR;
  offRuntime = off.systemd.services.loom-morathustra.serviceConfig ? RuntimeDirectory;
  sameService = builtins.removeAttrs on.systemd.services.loom-morathustra.serviceConfig [ "RuntimeDirectory" "RuntimeDirectoryMode" ] == off.systemd.services.loom-morathustra.serviceConfig;
  sameGlobalEnvironment = on.environment.variables == off.environment.variables && on.environment.sessionVariables == off.environment.sessionVariables;
  sameTmpfiles = on.systemd.tmpfiles.rules == off.systemd.tmpfiles.rules && on.systemd.tmpfiles.settings == off.systemd.tmpfiles.settings;
}`)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("browser render: %v\n%s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		Enabled, SameService, SameGlobalEnvironment, SameTmpfiles bool
		OffRuntime                                                bool
		RuntimeEnvironment                                        string
		Packages, Disabled                                        []string
		Path                                                      string
		Service                                                   struct {
			User, Group, ProtectSystem               string
			RuntimeDirectory, RuntimeDirectoryMode   string
			NoNewPrivileges, ProtectHome, PrivateTmp bool
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-browser", "chromium"} {
		if !slices.Contains(got.Packages, name) || slices.Contains(got.Disabled, name) || !strings.Contains(got.Path, "-"+name+"-") {
			t.Fatalf("browser package/PATH activation mismatch for %s: %+v", name, got)
		}
	}
	if !got.Enabled || !got.SameService || !got.SameGlobalEnvironment || !got.SameTmpfiles ||
		got.OffRuntime || got.RuntimeEnvironment != "/run/loom-morathustra-browser" ||
		got.Service.RuntimeDirectory != "loom-morathustra-browser" || got.Service.RuntimeDirectoryMode != "0700" ||
		got.Service.User != "agents" || got.Service.Group != "agents" || got.Service.ProtectSystem != "strict" ||
		!got.Service.NoNewPrivileges || !got.Service.ProtectHome || !got.Service.PrivateTmp {
		t.Fatalf("browser changed unrelated custody/isolation: %+v", got)
	}
}

func TestNixAgentBrowserPackageAndSkill(t *testing.T) {
	pkg := readRepoFile(t, "nix/packages/agent-browser.nix")
	for _, want := range []string{
		`version = "0.26.0";`,
		`sha256 = "8784dc259abf72ee04e751b45677d956387af50c99aec5dcd7a41a9bc498e3c3";`,
		`--set AGENT_BROWSER_EXECUTABLE_PATH ${chromium}/bin/chromium`,
		`../files/agent-browser/SKILL.md`,
		`export XDG_CONFIG_HOME="$LOOM_BROWSER_RUNTIME_DIR/config"`,
		`export XDG_CACHE_HOME="$LOOM_BROWSER_RUNTIME_DIR/cache"`,
		`AGENT_BROWSER_SOCKET_DIR:-$LOOM_BROWSER_RUNTIME_DIR/sockets`,
	} {
		requireContains(t, pkg, want)
	}
	for _, forbidden := range []string{"--no-sandbox", "npm ", "pip ", "install --", "postinstall", "export HOME=", "--set HOME", "--remote-debugging-address"} {
		if strings.Contains(pkg, forbidden) {
			t.Fatalf("browser package widens runtime behavior: %s", forbidden)
		}
	}
	skill := readRepoFile(t, "nix/files/agent-browser/SKILL.md")
	for _, want := range []string{"name: agent-browser", "native browser tools", "snapshot -i", "fill @e1", "screenshot", "close", "do not inherit Safari/Chrome logins", "Nix"} {
		requireContains(t, skill, want)
	}
	for _, forbidden := range []string{"hidden: true", "close --all", "npm i -g", "--no-sandbox"} {
		if strings.Contains(skill, forbidden) {
			t.Fatalf("browser skill exposes hidden/global/unsafe behavior: %s", forbidden)
		}
	}
}

func TestNixMinaBrowserRuntimeCustody(t *testing.T) {
	bash, core := accountTools(t)
	nix := sourceNix(t)
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--raw", ".#packages.x86_64-linux.agent-browser.installPhase")
	cmd.Dir = repoRoot(t)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// Execute the exact evaluated makeWrapper --run body with a native fixture
	// shell. Chromium and its Linux executable are deliberately not launched.
	_, body, ok := strings.Cut(string(raw), "--run '")
	if !ok {
		t.Fatal("missing native wrapper body")
	}
	body, _, ok = strings.Cut(body, "'\n")
	if !ok {
		t.Fatal("ambiguous native wrapper body")
	}
	for _, path := range []string{"/run/loom-morathustra-browser", "/run/loom-mina-browser", "", "/tmp/browser", "/run/loom-mina-browser/../other"} {
		t.Run(path, func(t *testing.T) {
			run := exec.Command(bash+"/bin/bash", "-c", body+"\nexec '"+core+"/bin/env'")
			run.Env = []string{"HOME=/fixture/home", "LOOM_BROWSER_RUNTIME_DIR=" + path, "PATH=" + core + "/bin"}
			out, err := run.Output()
			if err != nil {
				t.Fatal(err)
			}
			enabled := path == "/run/loom-morathustra-browser" || path == "/run/loom-mina-browser"
			for _, key := range []string{"XDG_CONFIG_HOME=", "XDG_CACHE_HOME=", "AGENT_BROWSER_SOCKET_DIR="} {
				if strings.Contains(string(out), key) != enabled {
					t.Fatalf("browser custody at %q: %s", path, out)
				}
			}
			if enabled {
				for _, want := range []string{"XDG_CONFIG_HOME=" + path + "/config\n", "XDG_CACHE_HOME=" + path + "/cache\n", "AGENT_BROWSER_SOCKET_DIR=" + path + "/sockets\n"} {
					requireContains(t, string(out), want)
				}
			}
			requireContains(t, string(out), "HOME=/fixture/home\n")
		})
	}
}
