package config_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/hermesprofile"
)

func minaRepo(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A real module render, including writeShellScript/writeText derivation bodies.
// It never realizes the system, invokes activation or reads a live profile.
const minaRender = `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in let
 legacy = system.config;
 base = { lib, ... }: {
   loom.morathustra.enable = lib.mkForce false;
   loom.morathustra.recoveryEnabled = lib.mkForce false;
   loom.morathustra.recoveryPublicKey = lib.mkForce "";
   loom.mina.selected = true;
   loom.mina.model = legacy.loom.morathustra.model;
   loom.mina.modelProvider = legacy.loom.morathustra.modelProvider;
 };
 make = extra: (system.extendModules { modules = [ base extra ]; }).config;
 on = make {
   loom.mina.enable = true;
   loom.mina.recoveryEnabled = true;
   loom.mina.recoveryPublicKey = builtins.concatStringsSep "" (builtins.genList (_: "a") 64);
   loom.mina.retainedMorathustra = [ "retained-v1" "retained-v2" ];
   loom.mina.browserEnabled = true;
   loom.mina.externalAccountsEnabled = true;
   loom.mina.externalAccountBinding = legacy.loom.morathustra.externalAccountBinding;
 };
 off = make {};
 runtimeOnly = make { loom.mina.enable = true; };
 alternateBox = make ({ lib, ... }: { loom.mina.enable = true; loom.boxPath = lib.mkForce "/srv/fixture/alternate-box"; });
 recoveryOnly = make { loom.mina.recoveryEnabled = true; loom.mina.recoveryPublicKey = builtins.concatStringsSep "" (builtins.genList (_: "a") 64); };
 failures = c: map (a: a.message) (builtins.filter (a: !a.assertion) c.assertions);
 conflicts = [
   (system.extendModules { modules = [ { loom.mina.enable = true; } ]; }).config
   (make { loom.mina.recoveryEnabled = true; })
   (make { loom.mina.retainedMorathustra = [ "same" "same" ]; })
   (make { loom.mina.retainedMorathustra = [ "*" ]; })
   (make { loom.mina.recoveryPublicKey = "bad"; })
   (system.extendModules { modules = [ { loom.mina.retainedMorathustra = [ "old" ]; } ]; }).config
 ];
 names = c: builtins.filter (n: builtins.match "loom-(mina|morathustra)(-recovery-publish)?" n != null) (builtins.attrNames c.systemd.services);
 summary = c: { services = names c; env = c.systemd.services.loomd.environment; failed = failures c; acl = c.system.activationScripts.loomOrcaAccess.text; };
 spec = identity: system.pkgs.callPackage ./nix/packages/orca-headless.nix { agentIdentity = identity; buildFHSEnv = args: args; };
 tools = system.pkgs.callPackage ./nix/packages/morathustra-external-tools.nix { agentIdentity = "mina"; basecampAccountID = 300001; basecamp-cli = ((import ./tests/nix/source-flake.nix {})).packages.x86_64-linux.basecamp-cli; };
 in {
   legacy = summary legacy;
   on = summary on;
   off = summary off;
   runtimeOnly = summary runtimeOnly;
   recoveryOnly = summary recoveryOnly;
   conflicts = map failures conflicts;
   conflictActivationRefused = !(builtins.tryEval (builtins.head conflicts).system.build.toplevel.drvPath).success;
   gateway = on.systemd.units."loom-mina.service".text;
   alternateGateway = alternateBox.systemd.units."loom-mina.service".text;
   legacyGateway = legacy.systemd.units."loom-morathustra.service".text;
   gatewayEnv = on.systemd.services.loom-mina.environment;
   publisher = on.systemd.units."loom-mina-recovery-publish.service".text;
   publishScript = on.systemd.services.loom-mina-recovery-publish.serviceConfig.ExecStart.text;
   custodyScript = on.systemd.services.loom-mina.serviceConfig.ExecStartPre.text;
   timer = on.systemd.units."loom-mina-recovery-publish.timer".text;
   shellActivation = on.system.activationScripts.loomOrcaShell.text;
   shellDerivations = builtins.attrNames (builtins.getContext on.system.activationScripts.loomOrcaShell.text);
   # The shell file is an input of the activation script, exposed as a
   # writeText derivation in its string context; exact content is tested below.
   baseline = on.loom.mina.baselineSettings;
   baselineText = on.loom.mina.baselineConfig.text;
   legacyBaseline = legacy.loom.morathustra.baselineSettings;
   skin = on.loom.mina.skin.text;
   binding = builtins.fromJSON on.environment.etc."loom-mina-accounts.json".text;
   bindingNames = builtins.filter (n: builtins.match "loom-(mina|morathustra)-accounts.json" n != null) (builtins.attrNames on.environment.etc);
   fhs = { mina = (spec "mina").extraBwrapArgs; morathustra = (spec "morathustra").extraBwrapArgs; };
   selectedOrca = toString on.loom.orca.package;
   expectedOrca = toString (((import ./tests/nix/source-flake.nix {})).packages.x86_64-linux.orca-headless.override { agentIdentity = "mina"; });
   accountBuilds = map (p: p.buildCommand) tools;
   packages = map (p: p.pname or p.name) on.users.users.agents.packages;
   globalEnvironments = { legacy = legacy.environment.variables // legacy.environment.sessionVariables; mina = on.environment.variables // on.environment.sessionVariables; };
   # NixOS derives this GNOME path from system.path, whose package closure
   # necessarily changes with the selected ORCA FHS package.
   globalEnvSame = let
     stable = attrs: builtins.removeAttrs attrs [ "NAUTILUS_4_EXTENSION_DIR" ];
     derived = c: (c.environment.variables // c.environment.sessionVariables).NAUTILUS_4_EXTENSION_DIR == "${c.system.path}/lib/nautilus/extensions-4";
   in stable legacy.environment.variables == stable on.environment.variables
     && stable legacy.environment.sessionVariables == stable on.environment.sessionVariables
     && derived legacy && derived on;
   home = on.users.users.agents.home;
   orcaEnvironment = on.systemd.services.orca-serve.environment;
   otherTimersSame = let timers = c: builtins.mapAttrs (n: _: c.systemd.units."${n}.timer".text) (builtins.removeAttrs c.systemd.timers [ "loom-morathustra-recovery-publish" "loom-mina-recovery-publish" ]); in timers legacy == timers on;
 }`

func TestNixMinaSelectedRuntimeRender(t *testing.T) {
	nix := "/nix/var/nix/profiles/default/bin/nix"
	if p, err := exec.LookPath("nix"); err == nil {
		nix = p
	} else if _, err := os.Stat(nix); err != nil {
		t.Skip("MINA runtime render requires Nix")
	}
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--impure", "--json", ".#nixosConfigurations.hardware-main", "--apply", minaRender)
	cmd.Dir = minaRepo(t)
	var diagnostic strings.Builder
	cmd.Stderr = &diagnostic
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("required MINA actual render: %v\n%s", err, diagnostic.String())
	}
	// Preserve reproducible acceptance receipts, without making them source files.
	receipt := filepath.Join(minaRepo(t), ".loom-acceptance/mina-w3")
	if err := os.MkdirAll(receipt, 0700); err != nil {
		t.Fatal(err)
	}
	for n, b := range map[string][]byte{"render.json": out, "render-apply.nix": []byte(minaRender)} {
		if err := os.WriteFile(filepath.Join(receipt, n), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	type summary struct {
		Services, Failed []string
		Env              map[string]string
		ACL              string
	}
	var got struct {
		AlternateGateway, LegacyGateway                                                                                                string
		Legacy, On, Off, RuntimeOnly, RecoveryOnly                                                                                     summary
		Conflicts                                                                                                                      [][]string
		Gateway, Publisher, PublishScript, CustodyScript, Timer, ShellActivation, BaselineText, Skin, Home, SelectedOrca, ExpectedOrca string
		GatewayEnv, OrcaEnvironment                                                                                                    map[string]string
		Baseline, LegacyBaseline                                                                                                       map[string]any
		Binding                                                                                                                        struct {
			SchemaVersion int `json:"schema_version"`
			Github        struct {
				Host, Login string
				ID          int
			}
			Basecamp struct {
				Profile, Email string
				IdentityID     int `json:"identity_id"`
				AccountID      int `json:"account_id"`
				PersonID       int `json:"person_id"`
			}
		}
		BindingNames, Packages, AccountBuilds, ShellDerivations   []string
		FHS                                                       map[string][]string
		GlobalEnvSame, OtherTimersSame, ConflictActivationRefused bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, c := range []summary{got.Legacy, got.On, got.Off, got.RuntimeOnly, got.RecoveryOnly} {
		if len(c.Failed) != 0 {
			t.Fatalf("valid runtime assertions: %v", c.Failed)
		}
	}
	if !got.ConflictActivationRefused {
		t.Fatal("conflicting selection reached activation derivation")
	}
	for _, fails := range got.Conflicts {
		if len(fails) == 0 {
			t.Fatal("conflicting/invalid render lacks refusal")
		}
	}
	for _, c := range []struct{ got, want []string }{{got.Legacy.Services, []string{"loom-morathustra", "loom-morathustra-recovery-publish"}}, {got.On.Services, []string{"loom-mina", "loom-mina-recovery-publish"}}, {got.Off.Services, []string{}}, {got.RuntimeOnly.Services, []string{"loom-mina"}}, {got.RecoveryOnly.Services, []string{"loom-mina-recovery-publish"}}} {
		if !slices.Equal(c.got, c.want) {
			t.Fatalf("writer selection %v want %v", c.got, c.want)
		}
	}
	for _, s := range []summary{got.On, got.Off, got.RuntimeOnly, got.RecoveryOnly} {
		if s.Env["LOOM_MINA_SELECTED"] != "true" {
			t.Fatal("disabled selection lost")
		}
		for k := range s.Env {
			if strings.HasPrefix(k, "LOOM_MORATHUSTRA_") {
				t.Fatal("old config escaped selected env")
			}
		}
	}
	if got.Legacy.Env["LOOM_MORATHUSTRA_ENABLED"] != "true" || got.Legacy.Env["LOOM_MORATHUSTRA_RECOVERY_ENABLED"] != "true" {
		t.Fatal("current host legacy behavior lost")
	}
	if got.On.Env["LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS"] != `["retained-v1","retained-v2"]` {
		t.Fatal("retained IDs changed")
	}
	require := func(body string, wants ...string) {
		t.Helper()
		for _, s := range wants {
			if !strings.Contains(body, s) {
				t.Errorf("render missing %q", s)
			}
		}
	}
	require(got.Gateway, "User=agents", "Group=agents", "WorkingDirectory=/srv/loom/agents/mina", "ReadWritePaths=/srv/loom/agents/mina", "ReadWritePaths=/var/lib/loom-mina-auth", "ProtectHome=true", "ProtectSystem=strict", "RuntimeDirectory=loom-mina-browser", "RuntimeDirectoryMode=0700", "NoNewPrivileges=true")
	require(got.Gateway, "ReadWritePaths=/srv/loom/box", "RequiresMountsFor=/srv/loom/box", "AssertPathIsDirectory=/srv/loom/box", "ReadOnlyPaths=-/srv/loom/agents/mina/skills/installed")
	require(got.AlternateGateway, "ReadWritePaths=/srv/fixture/alternate-box", "RequiresMountsFor=/srv/fixture/alternate-box", "AssertPathIsDirectory=/srv/fixture/alternate-box")
	if strings.Contains(got.AlternateGateway, "ReadWritePaths=/srv/loom/box") || strings.Contains(got.LegacyGateway, "ReadWritePaths=/srv/loom/box") || strings.Contains(got.Publisher, "ReadWritePaths=/srv/loom/box") {
		t.Fatal("Box grant escaped selected gateway scope")
	}
	require(got.Publisher, "Requires=loom-mina.service", "After=loom-mina.service", "PrivateNetwork=true", "ReadOnlyPaths=/var/lib/loom/mina-recovery/signing.key", "ReadWritePaths=/srv/loom/agents/mina", "ProtectHome=true", "ProtectSystem=strict", "User=agents")
	require(got.PublishScript, "--identity mina", "--workspace /srv/loom/agents/mina", "+mina-%Y%m%d-%H%M%S", "--signing-key-file /var/lib/loom/mina-recovery/signing.key")
	require(got.CustodyScript, "profile=/srv/loom/agents/mina/.hermes", "agents:agents", "-links +1", "-perm /0077", "user:loom:")
	require(got.Timer, "OnCalendar=*-*-* 02:45:00 Europe/Amsterdam", "Persistent=true")
	for _, s := range []summary{got.Legacy, got.On, got.Off} {
		// Each real find expression prunes both private namespaces before broad ACLs.
		for _, line := range strings.Split(s.ACL, "\n") {
			if strings.Contains(line, "-path /var/lib/loom/morathustra-recovery") {
				require(line, "-path /var/lib/loom/mina-recovery")
			}
			if strings.Contains(line, "-path /srv/loom/agents/morathustra/.hermes") {
				require(line, "-path /srv/loom/agents/morathustra/recovery", "-path /srv/loom/agents/mina/.hermes", "-path /srv/loom/agents/mina/recovery")
			}
		}
		require(s.ACL, "-path /var/lib/loom/mina-recovery", "-path /srv/loom/agents/mina/.hermes")
	}
	require(got.On.ACL, "protect_selected_recovery_key /var/lib/loom/mina-recovery /var/lib/loom/mina-recovery/signing.key", "grant_loom_service_selected_recovery_read_only /srv/loom/agents/mina/recovery")
	if strings.Contains(got.Off.ACL, "grant_loom_service_selected_recovery_read_only /srv/") || strings.Contains(got.On.ACL, "grant_loom_service_selected_recovery_read_only /srv/loom/agents/morathustra") {
		t.Fatal("unselected sealed recovery access")
	}
	if got.Home != "/home/agents" || got.GatewayEnv["HOME"] != got.Home || got.OrcaEnvironment["HOME"] != got.Home || got.GatewayEnv["HERMES_HOME"] != "/srv/loom/agents/mina/.hermes" || got.GatewayEnv["LOOM_BROWSER_RUNTIME_DIR"] != "/run/loom-mina-browser" || !got.GlobalEnvSame || !got.OtherTimersSame {
		t.Fatal("runtime scope/home/timers changed")
	}
	if got.GatewayEnv["HERMES_INFERENCE_MODEL"] != "gpt-6-astra" || got.GatewayEnv["HERMES_TUI_PROVIDER"] != "openai-codex" {
		t.Fatal("configured model lost")
	}
	if got.SelectedOrca != got.ExpectedOrca {
		t.Fatal("selected FHS package not wired")
	}
	for _, id := range []string{"mina", "morathustra"} {
		if !slices.Equal(got.FHS[id], []string{"--symlink /.host-etc/loom-" + id + "-accounts.json /etc/loom-" + id + "-accounts.json"}) {
			t.Fatal("FHS binding mismatch")
		}
	}
	if !slices.Equal(got.BindingNames, []string{"loom-mina-accounts.json"}) || got.Binding.SchemaVersion != 1 || got.Binding.Github.Host != "github.com" || got.Binding.Github.Login != "example-operator" || got.Binding.Github.ID != 100001 || got.Binding.Basecamp.Profile != "mina" || got.Binding.Basecamp.IdentityID != 200001 || got.Binding.Basecamp.AccountID != 300001 || got.Binding.Basecamp.PersonID != 400001 || got.Binding.Basecamp.Email != "agent@example.test" {
		t.Fatal("numeric external actor/binding drift")
	}
	for _, pkg := range []string{"loom-mina-gh", "loom-mina-basecamp", "agent-browser", "chromium"} {
		if !slices.Contains(got.Packages, pkg) {
			t.Fatalf("missing selected package %s", pkg)
		}
	}
	for _, build := range got.AccountBuilds {
		require(build, "/var/lib/loom-mina-auth", "'@identity@' 'mina'", "'@retired@' 'true'", "$out/bin/loom-morathustra-")
	}
	for _, key := range []string{"model", "curator", "sessions", "security", "platforms"} {
		if !reflect.DeepEqual(got.Baseline[key], got.LegacyBaseline[key]) {
			t.Fatalf("baseline %s drift", key)
		}
	}
	for _, section := range []string{"memory", "skills"} {
		if got.Baseline[section].(map[string]any)["write_approval"] != false || got.LegacyBaseline[section].(map[string]any)["write_approval"] != true {
			t.Fatalf("native %s approval must be MINA-only", section)
		}
	}
	if got.Baseline["skills"].(map[string]any)["guard_agent_created"] != false || got.LegacyBaseline["skills"].(map[string]any)["guard_agent_created"] != true {
		t.Fatal("native created-skill guard must be MINA-only")
	}
	require(got.CustodyScript, `"$profile/skills"`, "chmod u+rwx,go=", "chmod u+rw,go=")
	if got.Baseline["display"].(map[string]any)["skin"] != "mina-matrix-teal" || got.LegacyBaseline["display"] != nil {
		t.Fatal("skin selection not MINA-only")
	}
	require(got.BaselineText, `"external_dirs":["/srv/loom/agents/mina/skills/installed"]`, `"cwd":"/srv/loom/agents/mina"`)
	shellFound := false
	for _, drv := range got.ShellDerivations {
		if !strings.HasSuffix(drv, "-agents.bashrc.drv") {
			continue
		}
		shellFound = true
		inspect := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "derivation", "show", drv)
		raw, err := inspect.Output()
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if nested, ok := doc["derivations"]; ok {
			if err := json.Unmarshal(nested, &doc); err != nil {
				t.Fatal(err)
			}
		}
		matched := false
		for _, value := range doc {
			var d struct {
				Name string
				Env  map[string]string
			}
			if json.Unmarshal(value, &d) != nil || d.Name != "agents.bashrc" {
				continue
			}
			matched = true
			shell := d.Env["text"]
			require(shell, "export HERMES_HOME=/srv/loom/agents/mina/.hermes", "export HERMES_INFERENCE_MODEL=gpt-6-astra", "export HERMES_TUI_PROVIDER=openai-codex")
			original, err := os.ReadFile(filepath.Join(minaRepo(t), "nix/files/agents.bashrc"))
			if err != nil || !strings.HasPrefix(shell, string(original)) {
				t.Fatal("interactive shell baseline changed")
			}
			if strings.Contains(shell, "export HOME=") || strings.Contains(shell, "BASECAMP_PROFILE=") || strings.Contains(shell, "HERMES_MAC_BINDING=") || strings.Contains(shell, "HERMES_CUA_DRIVER_CMD=") {
				t.Fatal("interactive authority widened")
			}
			if err := os.WriteFile(filepath.Join(receipt, "agents.bashrc"), []byte(shell), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if !matched {
			t.Fatal("shell derivation has no actual text")
		}
	}
	if !shellFound {
		t.Fatal("selected shell derivation not rendered")
	}

	asset, err := os.ReadFile(filepath.Join(minaRepo(t), "nix/files/mina/skins/mina-matrix-teal.yaml"))
	if err != nil || string(asset) != got.Skin {
		t.Fatal("immutable skin package changed bytes")
	}
}

// Reuse the committed W2 frozen bytes; no fixture is re-signed or rewritten.
func frozenMinaInputs(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(minaRepo(t), "internal/hermesprofile/recovery_test.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]byte{}
	ast.Inspect(f, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range v.Names {
			if !strings.HasPrefix(name.Name, "frozenLegacy") {
				continue
			}
			lit, ok := v.Values[i].(*ast.BasicLit)
			if !ok {
				t.Fatal("fixture not literal")
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				t.Fatal(err)
			}
			result[name.Name] = b
		}
		return true
	})
	if len(result) != 4 {
		t.Fatal("frozen W2 fixture inventory drift")
	}
	return result
}

func TestMinaEnvironmentPolicyCoverageRetainedEvidence(t *testing.T) {
	python := os.Getenv("LOOM_TEST_HERMES_PYTHON")
	binary := os.Getenv("LOOM_TEST_HERMES_BINARY")
	if python == "" || binary == "" {
		t.Skip("set LOOM_TEST_HERMES_PYTHON and LOOM_TEST_HERMES_BINARY to the pinned patched Hermes runtime for native recovery acceptance")
	}
	for _, k := range []string{"LOOM_CONFIG_FILE", "LOOM_MORATHUSTRA_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY", "LOOM_MINA_SELECTED", "LOOM_MINA_ENABLED", "LOOM_MINA_RECOVERY_ENABLED", "LOOM_MINA_RECOVERY_PUBLIC_KEY", "LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS"} {
		t.Setenv(k, "")
	}
	// Public test seed from the accepted frozen fixtures, never a deployed key.
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	t.Setenv("LOOM_MINA_SELECTED", "true")
	t.Setenv("LOOM_MINA_RECOVERY_ENABLED", "true")
	t.Setenv("LOOM_MINA_RECOVERY_PUBLIC_KEY", hex.EncodeToString(public))
	t.Setenv("LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS", `["retained-v1","retained-v2"]`)
	c, err := config.Load(config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	opts := backupcoverage.OptionsFromConfig(c)
	if opts.HermesPolicyError != nil || opts.HermesRecovery.Workspace != hermesprofile.MinaWorkspaceRoot {
		t.Fatal("coverage did not receive exact selected policy")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, ".loom-acceptance", "mina")
	for _, dir := range []string{".hermes/cron", ".hermes/sessions", ".hermes/logs", ".hermes/memories", "recovery"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"SOUL.md": "Synthetic runtime fixture\n", "config.yaml": "model: {}\n"} {
		if err := os.WriteFile(filepath.Join(root, ".hermes", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	createDB := exec.Command(python, "-c", "import sqlite3,sys,os; p=sys.argv[1]; c=sqlite3.connect(p); c.execute('create table fixture(value text)'); c.execute(\"insert into fixture values ('retained')\"); c.commit(); c.close(); os.chmod(p,0o600)", filepath.Join(root, ".hermes/state.db"))
	createDB.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "HERMES_HOME=" + filepath.Join(root, ".hermes"), "PYTHONDONTWRITEBYTECODE=1"}
	if out, err := createDB.CombinedOutput(); err != nil {
		t.Fatalf("tiny sqlite fixture: %v %s", err, out)
	}
	frozen := frozenMinaInputs(t)
	var latest time.Time
	for _, v := range []string{"V1", "V2"} {
		id := "retained-" + strings.ToLower(v)
		dir := filepath.Join(root, "recovery", id)
		if err := os.Mkdir(dir, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0750); err != nil {
			t.Fatal(err)
		}
		for name, suffix := range map[string]string{hermesprofile.ManifestFile: "Manifest", hermesprofile.PayloadFile: "Payload"} {
			if err := os.WriteFile(filepath.Join(dir, name), frozen["frozenLegacy"+v+suffix], 0440); err != nil {
				t.Fatal(err)
			}
		}
		e, err := hermesprofile.Verify(context.Background(), dir, hermesprofile.WorkspaceRoot, public)
		if err != nil {
			t.Fatal(err)
		}
		if e.CreatedAt.After(latest) {
			latest = e.CreatedAt
		}
	}
	p := opts.HermesRecovery
	p.Workspace = root // Only physical fixture custody changes, never signed origin.
	if _, err := hermesprofile.Check(context.Background(), p, latest.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "lacks fresh") {
		t.Fatalf("old evidence met MINA freshness: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	e, err := hermesprofile.Publish(context.Background(), hermesprofile.PublishInput{Identity: hermesprofile.MinaIdentity, Workspace: root, ID: "mina-runtime", CreatedAt: now, PrivateKey: private, Binary: binary, Runner: hermesprofile.RunFixtureBackup})
	if err != nil {
		t.Fatal(err)
	}
	checkAt := now.Add(time.Minute)
	if latest.After(now) {
		checkAt = latest.Add(time.Minute)
	}
	roots := []backupstrategy.DirectArchiveRoot{{Name: "agents", Path: filepath.Dir(root)}}
	ex, ev, err := backupstrategy.HermesArchiveBoundary(context.Background(), roots, nil, p, checkAt)
	if err != nil || len(ev) != 3 {
		t.Fatalf("configured evidence boundary %v %v", ev, err)
	}
	if !slices.ContainsFunc(ex, func(x backupstrategy.DirectArchiveExclusion) bool { return x.RelativePath == "mina/.hermes" }) {
		t.Fatal("current profile not excluded")
	}
	if _, err := hermesprofile.Verify(context.Background(), e.Path, hermesprofile.WorkspaceRoot, public); err == nil {
		t.Fatal("MINA origin relabeled old")
	}
	for _, ids := range [][]string{nil, {"retained-v1"}, {"retained-v1", "retained-v2", "unknown"}, {"retained-v1", "retained-v1"}} {
		bad := p
		bad.RetainedMorathustra = ids
		if _, err := hermesprofile.Check(context.Background(), bad, checkAt); err == nil {
			t.Fatal("absent/unknown/duplicate binding admitted")
		}
	}
	// Disabled selection protects both fixed private trees without reading them.
	disabled := opts.HermesRecovery
	disabled.Enabled = false
	ex, _, err = backupstrategy.HermesArchiveBoundary(context.Background(), []backupstrategy.DirectArchiveRoot{{Name: "agents", Path: "/srv/loom/agents"}}, nil, disabled, checkAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"mina/.hermes", "mina/recovery", "morathustra/.hermes", "morathustra/recovery"} {
		if !slices.ContainsFunc(ex, func(x backupstrategy.DirectArchiveExclusion) bool { return x.RelativePath == path }) {
			t.Fatal("disabled transition omitted " + path)
		}
	}
	for _, v := range []string{"V1", "V2"} {
		for name, suffix := range map[string]string{hermesprofile.ManifestFile: "Manifest", hermesprofile.PayloadFile: "Payload"} {
			b, err := os.ReadFile(filepath.Join(root, "recovery", "retained-"+strings.ToLower(v), name))
			if err != nil || !bytes.Equal(b, frozen["frozenLegacy"+v+suffix]) {
				t.Fatal("old signed bytes changed")
			}
		}
	}
}
