package config

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func orcaGeneratedWrapper(t *testing.T) (string, string) {
	t.Helper()
	expr := `let
  flake = import ./tests/nix/source-flake.nix {};
  pkgs = flake.inputs.nixpkgs.legacyPackages.x86_64-linux;
  spec = pkgs.callPackage ./nix/packages/orca-headless.nix {
    buildFHSEnv = args: args;
    writeShellScript = name: text: { inherit text; };
  };
in { wrapper = spec.runScript.text; appDir = toString spec.passthru.appDir; }`
	output := sourceCommand(t, repoRoot(t), sourceNix(t),
		"--extra-experimental-features", "nix-command flakes", "eval", "--offline",
		"--no-write-lock-file", "--impure", "--json", "--expr", expr)
	var result struct{ Wrapper, AppDir string }
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Wrapper == "" || !strings.HasPrefix(result.AppDir, "/nix/store/") {
		t.Fatal("missing actual generated package wrapper")
	}
	return result.Wrapper, result.AppDir
}

func TestOrcaFolderBootstrapGeneratedDispatch(t *testing.T) {
	wrapper, appDir := orcaGeneratedWrapper(t)
	root := t.TempDir()
	// Substitute only the immutable appDir prefix and its two executable
	// boundaries. The routing/environment logic is the actual Nix output.
	for _, executable := range []string{"AppRun", "orca-ide"} {
		body := "#!/usr/bin/env bash\nprintf '%s\\0' '" + executable + "' \"${ELECTRON_RUN_AS_NODE-unset}\" \"${NODE_OPTIONS-unset}\" \"${ORCA_NODE_OPTIONS-unset}\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(root, executable), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	filename := filepath.Join(root, "wrapper.sh")
	if err := os.WriteFile(filename, []byte(strings.ReplaceAll(wrapper, appDir, root)), 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		cli  bool
	}{
		{"skills", []string{"skills", "get", "orca-cli"}, true},
		{"project", []string{"project", "list", "--json"}, true},
		{"quoted-path", []string{"project", "setup-existing-folder", "--path", "/fixture/ordinary notes"}, true},
		{"json-prefix", []string{"--json", "skills", "get", "orca-cli"}, true},
		{"environment-prefix", []string{"--environment", "fixture", "project", "list"}, true},
		{"pairing-prefix", []string{"--pairing-code", "fixture", "skills", "list"}, true},
		{"equals-prefix", []string{"--environment=skills", "--pairing-code=project", "--json", "skills", "list"}, true},
		{"family-valued-prefix", []string{"--environment", "project", "--pairing-code", "skills", "project", "list"}, true},
		{"help-family", []string{"project", "--help"}, true},
		{"help-prefix", []string{"--help", "skills"}, true},
		{"missing-topic", []string{"skills", "get"}, true},
		{"bare-value-after-family", []string{"project", "list", "--environment"}, true},
		{"missing-value-before-boolean", []string{"--environment", "--json", "skills", "get", "orca-cli"}, true},
		{"no-args", nil, false},
		{"repo", []string{"repo", "add", "--path", "/fixture/notes", "--kind", "folder"}, false},
		{"serve", []string{"serve", "--port", "0", "--no-pairing"}, false},
		{"root-help", []string{"--help"}, false},
		{"help-command", []string{"help", "skills"}, false},
		{"short-help", []string{"-h"}, false},
		{"desktop-flag", []string{"--no-sandbox"}, false},
		{"desktop-path", []string{"/fixture/project"}, false},
		{"environment-value", []string{"--environment", "skills"}, false},
		{"pairing-value", []string{"--pairing-code", "project"}, false},
		{"environment-only", []string{"--environment=skills"}, false},
		{"missing-global-value", []string{"--environment"}, false},
		{"unknown-value", []string{"--user-data-dir", "project"}, false},
		{"unknown-value-and-tail", []string{"--user-data-dir", "skills", "project", "list"}, false},
		{"known-then-desktop-value", []string{"--json", "--user-data-dir", "project", "skills", "list"}, false},
		{"missing-value-before-desktop-value", []string{"--environment", "--user-data-dir", "project"}, false},
		{"missing-value-before-desktop-tail", []string{"--pairing-code", "--user-data-dir", "skills", "project", "list"}, false},
		{"valued-repo", []string{"--environment", "skills", "repo", "list"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", append([]string{filename}, tc.args...)...)
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root,
				"NODE_OPTIONS=fixture-node-options", "ORCA_NODE_OPTIONS=older-node-options"}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("generated wrapper: %v %s", err, output)
			}
			var got []string
			for _, value := range bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0}) {
				got = append(got, string(value))
			}
			want := []string{"AppRun", "unset", "fixture-node-options", "older-node-options"}
			if tc.cli {
				want = []string{"orca-ide", "1", "unset", "fixture-node-options",
					filepath.Join(root, "resources/app.asar.unpacked/out/cli/index.js")}
			}
			want = append(want, tc.args...)
			if !slices.Equal(got, want) {
				t.Fatalf("dispatch/argument/environment mismatch\ngot  %q\nwant %q", got, want)
			}
		})
	}
}

func TestOrcaFolderBootstrapPatchLogRefusal(t *testing.T) {
	// Execute the actual package guards, followed by a publication sentinel.
	// A negated grep under errexit wrongly reaches that sentinel on a match.
	packageFile := readRepoFile(t, "nix/packages/orca-headless.nix")
	guards := regexp.MustCompile(`(?s)    if grep -Ei 'offset\|fuzz\|FAILED' "\$TMPDIR/(patch-(?:dry-run|apply)\.log)"; then\n.*?\n    fi`).FindAllStringSubmatch(packageFile, -1)
	if len(guards) != 2 {
		t.Fatalf("expected two explicit fail-closed package guards, got %d", len(guards))
	}
	for _, guard := range guards {
		for name, log := range map[string]string{
			"clean":  "checking file resources/app.asar.unpacked/out/cli/help.js\n",
			"offset": "Hunk #1 succeeded at 12 (offset 1 line).\n",
			"fuzz":   "Hunk #1 succeeded at 12 with fuzz 1.\n",
			"failed": "Hunk #1 FAILED at 12.\n",
		} {
			t.Run(guard[1]+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, guard[1]), []byte(log), 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+guard[0]+"\nprintf published > \"$TMPDIR/published\"\n")
				cmd.Dir = root
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + root}
				output, err := cmd.CombinedOutput()
				_, published := os.Stat(filepath.Join(root, "published"))
				if name == "clean" {
					if err != nil || published != nil {
						t.Fatalf("clean patch refused: %v %s", err, output)
					}
				} else if err == nil || !os.IsNotExist(published) {
					t.Fatalf("%s log reached publication: error=%v output=%s", name, err, output)
				}
			})
		}
	}
}

func TestOrcaFolderBootstrapPatchBoundary(t *testing.T) {
	patch := readRepoFile(t, "nix/patches/orca-folder-bootstrap.patch")
	prefix := "resources/app.asar.unpacked/out/cli/"
	want := map[string]bool{
		"handlers/repo.js": false, "repo-kind-flag.js": false,
		"handlers/project.js": false, "specs/core.js": false,
		"help.js": false, "bundled-skill-guides.js": false,
	}
	for _, match := range regexp.MustCompile(`(?m)^\+\+\+ b/(.+)$`).FindAllStringSubmatch(patch, -1) {
		if !strings.HasPrefix(match[1], prefix) {
			t.Fatalf("patch escapes public CLI: %s", match[1])
		}
		name := strings.TrimPrefix(match[1], prefix)
		seen, allowed := want[name]
		if !allowed || seen {
			t.Fatalf("unexpected or duplicate patch target: %s", name)
		}
		want[name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing patch target: %s", name)
		}
	}
	// Avoid accidentally vendoring a whole generated guide or unrelated bundle.
	if len(patch) > 12000 {
		t.Fatalf("narrow compiled patch exceeded 12KB: %d", len(patch))
	}
	packageFile := readRepoFile(t, "nix/packages/orca-headless.nix")
	for _, text := range []string{
		`version = "1.4.191"`,
		`sha256-GWoQ+dr2eH+iWr63caGoKc39MpWSoadlXKX5dgyZ5kk=`,
		`--fuzz=0`, `--dry-run`, `sha256sum --check`,
		`cp -a ${unpatchedAppDir}/. "$out/"`,
		`inherit appDir unpatchedAppDir src`,
	} {
		requireContains(t, packageFile, text)
	}
}

func TestOrcaFolderBootstrapNixEvaluation(t *testing.T) {
	nix := sourceNix(t)
	expr := `let
  flake = import ./tests/nix/source-flake.nix {};
  pkgs = flake.inputs.nixpkgs.legacyPackages.x86_64-linux;
  orca = pkgs.callPackage ./nix/packages/orca-headless.nix {};
  check = import ./nix/checks/orca-folder-bootstrap.nix { inherit pkgs orca; };
in {
  package = orca.drvPath;
  appDir = orca.appDir.drvPath;
  original = orca.unpatchedAppDir.drvPath;
  wrapper = orca.appRun.drvPath;
  bootstrap = orca.bootstrapContract.drvPath;
  check = check.drvPath;
  system = check.system;
}`
	output := sourceCommand(t, repoRoot(t), nix,
		"--extra-experimental-features", "nix-command flakes", "eval", "--offline",
		"--no-write-lock-file", "--impure", "--json", "--expr", expr)
	var actual map[string]string
	if err := json.Unmarshal(output, &actual); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"package", "appDir", "original", "wrapper", "bootstrap", "check"} {
		if !strings.HasPrefix(actual[key], "/nix/store/") || !strings.HasSuffix(actual[key], ".drv") {
			t.Errorf("%s is not a evaluated derivation: %q", key, actual[key])
		}
	}
	if actual["appDir"] == actual["original"] {
		t.Fatal("patching must create a distinct immutable appDir")
	}
	if actual["system"] != "x86_64-linux" {
		t.Fatalf("unexpected contract platform: %q", actual["system"])
	}
	// Existing FHS account and Mac connector projection tests remain required;
	// this evaluates the real package/check graph, without building remotely.
}
