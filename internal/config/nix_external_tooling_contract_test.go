package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestNixBasecampReleasePin(t *testing.T) {
	var lock struct {
		Root  string
		Nodes map[string]struct {
			Inputs map[string]string
			Locked struct{ Rev, NarHash, Owner, Repo string }
		}
	}
	// Hermes has indirect input arrays; decode only the owners under review.
	var raw struct {
		Root  string
		Nodes map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "flake.lock")), &raw); err != nil {
		t.Fatal(err)
	}
	lock.Root = raw.Root
	lock.Nodes = make(map[string]struct {
		Inputs map[string]string
		Locked struct{ Rev, NarHash, Owner, Repo string }
	})
	for _, name := range []string{raw.Root, "basecamp-upstream"} {
		node := lock.Nodes[name]
		if err := json.Unmarshal(raw.Nodes[name], &node); err != nil {
			t.Fatal(err)
		}
		lock.Nodes[name] = node
	}
	upstream := lock.Nodes["basecamp-upstream"]
	if upstream.Locked.Owner != "basecamp" || upstream.Locked.Repo != "basecamp-cli" ||
		upstream.Locked.Rev != "e4bfd014bf137771d454515b4f7314ce651ee096" ||
		upstream.Locked.NarHash != "sha256-lmuOpPosN3RiTv6DtejEaaY59SOUmimpZI+H/rl/soo=" {
		t.Fatalf("Basecamp release/source identity drift: %+v", upstream.Locked)
	}
	for name, want := range map[string]string{
		lock.Nodes[raw.Root].Inputs["nixpkgs"]: "26ef669cffa904b6f6832ab57b77892a37c1a671",
		upstream.Inputs["nixpkgs"]:             "c27cdad491a991b11ed731760aa2ef8db0cb0410",
	} {
		var node struct{ Locked struct{ Rev string } }
		if err := json.Unmarshal(raw.Nodes[name], &node); err != nil {
			t.Fatal(err)
		}
		if node.Locked.Rev != want {
			t.Fatalf("toolchain pin drift for %s: %s", name, node.Locked.Rev)
		}
	}
	packageFile := readRepoFile(t, "nix/packages/basecamp-cli.nix")
	for _, want := range []string{
		`basecamp-upstream.packages.${system}.basecamp`,
		`assert basecamp-upstream.rev == revision;`,
		`assert upstream.version == "0.10.0";`,
		`assert upstream.vendorHash == "sha256-zNTp8pw3ZViwSmpsxQ78MU5lLgn0P2nb9aAxqp/96Eg=";`,
		`internal/version.Commit=${revision}`,
	} {
		requireContains(t, packageFile, want)
	}
	for _, forbidden := range []string{
		"BASECAMP_PROFILE", "GH_CONFIG_DIR", "credentials", "fetchTarball", "curl ",
		"postPatch", "substituteInPlace", "./../", "src =", "auth login", "profile create",
	} {
		if strings.Contains(packageFile, forbidden) {
			t.Fatalf("unexpected package profile/source mutation: %s", forbidden)
		}
	}
}

func TestNixOrcaPublicAccountBindingProjection(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable; release gate must run with Nix")
		}
	}
	// Evaluate the actual package's FHS arguments without building Electron.
	expr := `let
  flake = (import ./tests/nix/source-flake.nix {});
  pkgs = flake.inputs.nixpkgs.legacyPackages.x86_64-linux;
  spec = pkgs.callPackage ./nix/packages/orca-headless.nix {
    buildFHSEnv = args: args;
  };
in spec.extraBwrapArgs or []`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--impure", "--json", "--expr", expr)
	cmd.Dir = repoRoot(t)
	var diagnostic strings.Builder
	cmd.Stderr = &diagnostic
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("evaluate ORCA FHS projection: %v %s", err, diagnostic.String())
	}
	var args []string
	if err := json.Unmarshal(out, &args); err != nil {
		t.Fatal(err)
	}
	want := []string{"--symlink /.host-etc/loom-morathustra-accounts.json /etc/loom-morathustra-accounts.json"}
	if !slices.Equal(args, want) {
		t.Fatalf("expected only the public binding through existing host-etc custody: %q", args)
	}
}

func TestNixMorathustraExternalToolingEnvironment(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable; release gate must run with Nix")
		}
	}
	apply := `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in
let
  on = (system.extendModules { modules = [ ({ lib, ... }: {
    loom.morathustra.enable = lib.mkForce true;
    loom.morathustra.externalAccountsEnabled = lib.mkForce false;
    loom.morathustra.recoveryEnabled = lib.mkForce false;
  }) ]; }).config;
  off = (system.extendModules { modules = [ ({ lib, ... }: {
    loom.morathustra.enable = lib.mkForce false;
    loom.morathustra.externalAccountsEnabled = lib.mkForce false;
    loom.morathustra.recoveryEnabled = lib.mkForce false;
  }) ]; }).config;
  package = system.pkgs;
in {
  github = toString package.gh;
  githubVersion = package.gh.version;
  users = map toString on.users.users.agents.packages;
  disabledUsers = map toString off.users.users.agents.packages;
  global = map toString on.environment.systemPackages;
  environment = on.systemd.services.loom-morathustra.environment;
  globalEnvironment = on.environment.variables // on.environment.sessionVariables;
  unit = on.systemd.units."loom-morathustra.service".text;
  service = on.systemd.services.loom-morathustra.serviceConfig;
  sameTmpfiles = on.systemd.tmpfiles.rules == off.systemd.tmpfiles.rules;
}`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes",
		"eval", "--impure", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = repoRoot(t)
	payload, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("render external tooling: %v\n%s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		Basecamp, Github, GithubVersion, Unit string
		Users, DisabledUsers, Global          []string
		Environment, GlobalEnvironment        map[string]string
		SameTmpfiles                          bool
		Service                               struct {
			User, Group, ProtectSystem, UMask        string
			NoNewPrivileges, ProtectHome, PrivateTmp bool
			ReadWritePaths, ReadOnlyPaths            []string
		}
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	packageCmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes",
		"eval", "--raw", ".#packages.x86_64-linux.basecamp-cli.outPath")
	packageCmd.Dir = repoRoot(t)
	expected, err := packageCmd.Output()
	if err != nil {
		t.Fatalf("evaluate exact Basecamp output: %v", err)
	}
	got.Basecamp = string(expected)
	if got.GithubVersion != "2.83.2" {
		t.Fatalf("GitHub must use LOOM's pinned nixpkgs gh: %s", got.GithubVersion)
	}
	for _, pkg := range []string{got.Basecamp, got.Github} {
		if !strings.HasPrefix(pkg, "/nix/store/") || !slices.Contains(got.Users, pkg) ||
			slices.Contains(got.DisabledUsers, pkg) || slices.Contains(got.Global, pkg) ||
			!slices.Contains(strings.Split(got.Environment["PATH"], ":"), pkg+"/bin") {
			t.Fatalf("external executable not confined to enabled agents and gateway PATH: %s", pkg)
		}
	}
	for _, environment := range []map[string]string{got.Environment, got.GlobalEnvironment} {
		for key := range environment {
			if strings.HasPrefix(key, "BASECAMP_") || strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") ||
				key == "SSH_AUTH_SOCK" || strings.HasPrefix(key, "GIT_CONFIG") {
				t.Fatalf("tooling must not select or inherit auth/profile/transport state: %s", key)
			}
		}
	}
	for _, forbidden := range []string{"EnvironmentFile=", "LoadCredential=", "BASECAMP_PROFILE", "GH_CONFIG_DIR"} {
		if strings.Contains(got.Unit, forbidden) {
			t.Fatalf("tooling introduces auth/profile binding: %s", forbidden)
		}
	}
	s := got.Service
	if s.User != "agents" || s.Group != "agents" || !s.NoNewPrivileges || !s.ProtectHome ||
		!s.PrivateTmp || s.ProtectSystem != "strict" || s.UMask != "0077" || !got.SameTmpfiles ||
		!slices.Equal(s.ReadWritePaths, []string{"/srv/loom/agents/morathustra"}) ||
		!slices.Equal(s.ReadOnlyPaths, []string{"-/srv/loom/agents/morathustra/skills/installed"}) {
		t.Fatal("tooling widened gateway filesystem, privilege or provisioning boundaries")
	}
	basecampProtocol := readRepoFile(t, "ai-loom-pack/templates/morathustra/protocols/BASECAMP.md")
	requireContains(t, basecampProtocol, "basecamp --profile morathustra me --agent")
	requireContains(t, basecampProtocol, "Ordinary coding agents retain literal `--profile codex`")
	githubProtocol := readRepoFile(t, "ai-loom-pack/templates/morathustra/protocols/GITHUB.md")
	requireContains(t, githubProtocol, "Bind `GH_CONFIG_DIR` to that")
	requireContains(t, githubProtocol, "outside the live workspace and its projection")
}

func TestNixMorathustraAccountBindings(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("Nix unavailable; account release render gate requires Nix")
		}
	}
	apply := `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in let
 on = system.config;
 off = (system.extendModules { modules = [ ({ lib, ... }: { loom.morathustra.externalAccountsEnabled = lib.mkForce false; }) ]; }).config;
 disabled = (system.extendModules { modules = [ ({ lib, ... }: { loom.morathustra.enable = lib.mkForce false; }) ]; }).config;
 missing = (system.extendModules { modules = [ ({ lib, ... }: { loom.morathustra.externalAccountBinding = lib.mkForce null; }) ]; }).config;
 in {
 missingBindingRefused = builtins.any (a: !a.assertion && a.message == "Named external account tools require an explicit externalAccountBinding verified by the operator.") missing.assertions;
 missingBindingPublished = missing.environment.etc ? "loom-morathustra-accounts.json";
 defaultOff = system.options.loom.morathustra.externalAccountsEnabled.default == false;
 enabled = on.loom.morathustra.externalAccountsEnabled;
 binding = builtins.fromJSON on.environment.etc."loom-morathustra-accounts.json".text;
 bindingMode = on.environment.etc."loom-morathustra-accounts.json".mode;
 bindingTarget = on.environment.etc."loom-morathustra-accounts.json".target;
 bindingSource = toString on.environment.etc."loom-morathustra-accounts.json".source;
 offBinding = off.environment.etc ? "loom-morathustra-accounts.json";
 disabledBinding = disabled.environment.etc ? "loom-morathustra-accounts.json";
 legacyBindingPresent = builtins.any (c: c.environment.etc ? "loom/morathustra-accounts.json") [ on off disabled ];
 protectedAccessScript = on.system.activationScripts.loomOrcaAccess.text;
 addedUserPackages = builtins.filter (x: !(builtins.elem x (map toString off.users.users.agents.packages))) (map toString on.users.users.agents.packages);
 global = map toString on.environment.systemPackages;
 environment = on.systemd.services.loom-morathustra.environment;
 globalEnvironment = on.environment.variables // on.environment.sessionVariables;
 writePaths = on.systemd.services.loom-morathustra.serviceConfig.ReadWritePaths;
 offWritePaths = off.systemd.services.loom-morathustra.serviceConfig.ReadWritePaths;
 custodySame = builtins.removeAttrs on.systemd.services.loom-morathustra.serviceConfig [ "ReadWritePaths" "Environment" ] == builtins.removeAttrs off.systemd.services.loom-morathustra.serviceConfig [ "ReadWritePaths" "Environment" ];
 tmpfilesSame = on.systemd.tmpfiles.rules == off.systemd.tmpfiles.rules;
 # Only the declarative /etc binding and its aggregate activation script change.
 activationSame = builtins.removeAttrs on.system.activationScripts [ "etc" "script" ] == builtins.removeAttrs off.system.activationScripts [ "etc" "script" ];
 }`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--impure", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("account render: %v %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		DefaultOff, Enabled, OffBinding, DisabledBinding, CustodySame, TmpfilesSame, ActivationSame bool
		MissingBindingRefused, MissingBindingPublished                                              bool
		BindingMode, BindingSource                                                                  string
		BindingTarget, ProtectedAccessScript                                                        string
		LegacyBindingPresent                                                                        bool
		Binding                                                                                     json.RawMessage
		AddedUserPackages, Global, WritePaths, OffWritePaths                                        []string
		Environment, GlobalEnvironment                                                              map[string]string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !got.MissingBindingRefused || got.MissingBindingPublished {
		t.Fatal("missing operator binding must refuse without publishing a synthetic identity")
	}
	if !got.DefaultOff || !got.Enabled || got.OffBinding || got.DisabledBinding || !got.CustodySame || !got.TmpfilesSame || !got.ActivationSame || got.BindingMode != "symlink" || !strings.HasPrefix(got.BindingSource, "/nix/store/") {
		t.Fatalf("account option/custody/provisioning: defaultOff=%v enabled=%v offBinding=%v disabledBinding=%v custody=%v tmpfiles=%v activation=%v mode=%s", got.DefaultOff, got.Enabled, got.OffBinding, got.DisabledBinding, got.CustodySame, got.TmpfilesSame, got.ActivationSame, got.BindingMode)
	}
	if got.BindingTarget != "loom-morathustra-accounts.json" || got.LegacyBindingPresent {
		t.Fatal("public binding must use only /etc/loom-morathustra-accounts.json")
	}
	if !strings.Contains(got.ProtectedAccessScript, "setfacl -m u:agents:--- /etc/loom") {
		t.Fatal("protected /etc/loom must retain its agents deny ACL")
	}
	var binding map[string]any
	if err := json.Unmarshal(got.Binding, &binding); err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"github":{"host":"github.com","login":"example-operator","id":100001},"basecamp":{"profile":"morathustra","identity_id":200001,"account_id":300001,"person_id":400001,"email":"agent@example.test"}}`
	var expected map[string]any
	_ = json.Unmarshal([]byte(want), &expected)
	a, _ := json.Marshal(binding)
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		t.Fatal("operator binding differs from accepted receipt")
	}
	if !slices.Equal(got.WritePaths, []string{"/srv/loom/agents/morathustra", "/var/lib/loom-morathustra-auth"}) || !slices.Equal(got.OffWritePaths, []string{"/srv/loom/agents/morathustra"}) {
		t.Fatal("extra gateway write scope")
	}
	if len(got.AddedUserPackages) != 2 {
		t.Fatal("expected exactly two managed entry points")
	}
	for _, p := range got.AddedUserPackages {
		if !strings.Contains(p, "-loom-morathustra-") || slices.Contains(got.Global, p) || !slices.Contains(strings.Split(got.Environment["PATH"], ":"), p+"/bin") {
			t.Fatal("managed entry point missing from scoped paths")
		}
	}
	for _, env := range []map[string]string{got.Environment, got.GlobalEnvironment} {
		for key := range env {
			if strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") || strings.HasPrefix(key, "BASECAMP_") || (key == "XDG_CONFIG_HOME" || key == "XDG_CACHE_HOME") {
				t.Fatalf("account state escaped child: %s", key)
			}
		}
	}
}
