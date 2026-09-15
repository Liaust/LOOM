package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

var accountFixtureTools struct {
	sync.Once
	Bash, Coreutils, Error string
	MissingNix             bool
}

func accountTools(t *testing.T) (string, string) {
	t.Helper()
	accountFixtureTools.Do(func() {
		system := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH] + "-" + runtime.GOOS
		nix, err := exec.LookPath("nix")
		if err != nil {
			nix = "/nix/var/nix/profiles/default/bin/nix"
			if _, statErr := os.Stat(nix); statErr != nil {
				accountFixtureTools.MissingNix = true
				return
			}
		}
		cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--offline", "--impure", "--json", "--expr", fmt.Sprintf(`let p = ((import ./tests/nix/source-flake.nix {})).inputs.nixpkgs.legacyPackages.%s; in { bash = toString p.bash; coreutils = toString p.coreutils; }`, system))
		cmd.Dir = repoRoot(t)
		out, err := cmd.Output()
		if err != nil {
			accountFixtureTools.Error = err.Error()
			return
		}
		var paths struct{ Bash, Coreutils string }
		if err = json.Unmarshal(out, &paths); err != nil {
			accountFixtureTools.Error = err.Error()
			return
		}
		for _, p := range []string{paths.Bash + "/bin/bash", paths.Coreutils + "/bin/stat"} {
			if _, err := os.Stat(p); err != nil {
				accountFixtureTools.Error = "native Nix bash/coreutils must be realized for fixture gate"
				return
			}
		}
		accountFixtureTools.Bash = paths.Bash
		accountFixtureTools.Coreutils = paths.Coreutils
	})
	if accountFixtureTools.MissingNix {
		t.Skip("Nix unavailable; wrapper release gate must run with realized native tools")
	}
	if accountFixtureTools.Error != "" {
		t.Fatal(accountFixtureTools.Error)
	}
	return accountFixtureTools.Bash, accountFixtureTools.Coreutils
}

type accountFixture struct{ Root, Wrapper, Native, Coreutils string }

func newAccountFixture(t *testing.T, tool string) accountFixture {
	return newNamedAccountFixture(t, tool, "morathustra", false)
}

func newNamedAccountFixture(t *testing.T, tool, identity string, retired bool) accountFixture {
	t.Helper()
	bash, core := accountTools(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "auth")
	for _, p := range []string{"", "home", "config", "cache", "gh", "config/basecamp"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"gh/config.yml", "gh/hosts.yml", "config/basecamp/config.json", "config/basecamp/credentials.json", "config/basecamp/.last-run-version"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("metadata fixture only\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	native := filepath.Join(filepath.Dir(root), "native-"+tool)
	body := "#!" + bash + "/bin/bash\nprintf 'dispatch=%s\\n' \"$0\"\nprintf 'arg=%s\\n' \"$@\"\n'" + core + "/bin/env'\n"
	if err := os.WriteFile(native, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	actor, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(filepath.Dir(root), "loom-"+identity+"-"+tool)
	script := strings.NewReplacer("@basecampAccountID@", "300001", "@identity@", identity, "@retired@", fmt.Sprint(retired), "@bash@", bash, "@coreutils@", core, "@cacert@", "/nix/store/fixture-ca", "@authRoot@", root, "@owner@", actor.Username, "@tool@", tool, "@native@", native, "@childPath@", core+"/bin").Replace(readRepoFile(t, "nix/files/morathustra-account-tool.sh"))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return accountFixture{root, wrapper, native, core}
}
func (f accountFixture) run(env []string, args ...string) (string, error) {
	cmd := exec.Command(f.Wrapper, args...)
	cmd.Env = append([]string{"PATH=/untrusted-path", "HOME=/untrusted-home", "XDG_CONFIG_HOME=/untrusted-config", "XDG_CACHE_HOME=/untrusted-cache", "UNRELATED_SECRET=sentinel-private"}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
func morathustraAccountWrapperDispatch(t *testing.T, identity string) {
	for _, tool := range []string{"gh", "basecamp"} {
		t.Run(tool, func(t *testing.T) {
			f := newNamedAccountFixture(t, tool, identity, false)
			args := []string{"api", "user", "--jq", ".login"}
			if tool == "basecamp" {
				args = []string{"--profile", identity, "me", "--agent"}
			}
			out, err := f.run([]string{"SSH_CONNECTION=metadata", "SSH_CLIENT=metadata", "SSH_TTY=/dev/pts/1", "SSH_ASKPASS=", "SSL_CERT_FILE=/launcher/ca", "NIX_SSL_CERT_FILE=/launcher/ca"}, args...)
			if err != nil {
				t.Fatalf("dispatch: %v %s", err, out)
			}
			for _, want := range []string{"dispatch=" + f.Native, "HOME=" + f.Root + "/home", "XDG_CONFIG_HOME=" + f.Root + "/config", "XDG_CACHE_HOME=" + f.Root + "/cache", "SSL_CERT_FILE=/nix/store/fixture-ca/etc/ssl/certs/ca-bundle.crt", "DBUS_SESSION_BUS_ADDRESS=unix:path=/dev/null"} {
				if !strings.Contains(out, want+"\n") {
					t.Errorf("missing child value %q", want)
				}
			}
			for _, bad := range []string{"sentinel-private", "/untrusted", "SSH_CONNECTION=", "SSH_TTY=", "NIX_SSL_CERT_FILE=", "BASECAMP_PROFILE="} {
				if strings.Contains(out, bad) {
					t.Errorf("inherited child context %q", bad)
				}
			}
			if tool == "gh" {
				for _, want := range []string{"GH_CONFIG_DIR=" + f.Root + "/gh", "GH_HOST=github.com"} {
					if !strings.Contains(out, want+"\n") {
						t.Error("wrong GitHub binding")
					}
				}
			} else {
				for _, want := range []string{"arg=--profile\narg=" + identity + "\narg=me", "BASECAMP_NO_KEYRING=1", "BASECAMP_ACCOUNT_ID=300001", "BASECAMP_BASE_URL=https://3.basecampapi.com"} {
					if !strings.Contains(out, want) {
						t.Error("wrong Basecamp binding")
					}
				}
			}
			// The ordinary client still receives the caller's own environment unchanged.
			ordinary := exec.Command(f.Native, "--version")
			ordinary.Env = []string{"GH_CONFIG_DIR=/ordinary", "BASECAMP_PROFILE=codex", "HOME=/ordinary"}
			raw, err := ordinary.Output()
			if err != nil || !strings.Contains(string(raw), "BASECAMP_PROFILE=codex") {
				t.Fatal("ordinary CLI changed")
			}
		})
	}
}
func morathustraAccountWrapperHermesPager(t *testing.T, identity string) {
	for _, tool := range []string{"gh", "basecamp"} {
		t.Run(tool, func(t *testing.T) {
			f := newNamedAccountFixture(t, tool, identity, false)
			args := []string{"api", "user"}
			if tool == "basecamp" {
				args = []string{"--profile", identity, "me", "--agent"}
			}
			// Pinned Hermes _wrap_command adds this even without a login snapshot.
			out, err := f.run([]string{"GIT_PAGER=cat", "PAGER=cat"}, args...)
			if err != nil || !strings.Contains(out, "dispatch="+f.Native+"\n") {
				t.Fatalf("native Hermes pager refused: %v %s", err, out)
			}
			if strings.Contains(out, "PAGER=") {
				t.Fatal("inherited pager leaked into native child")
			}
			for _, value := range []string{"", "less", "cat; sentinel-private", "/tmp/cat", "cat\nsentinel-private"} {
				out, err := f.run([]string{"GIT_PAGER=" + value}, args...)
				if err != nil || !strings.Contains(out, "dispatch="+f.Native) || strings.Contains(out, "PAGER=") || strings.Contains(out, "sentinel-private") {
					t.Fatal("ambient pager was not safely discarded")
				}
			}
			for _, key := range []string{"GH_TOKEN", "BASECAMP_PROFILE", "GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "LD_PRELOAD"} {
				out, err := f.run([]string{"GIT_PAGER=cat", key + "=sentinel-private"}, args...)
				if err != nil || !strings.Contains(out, "dispatch="+f.Native) || strings.Contains(out, "sentinel-private") {
					t.Fatalf("ambient setting was not safely discarded: %s", key)
				}
			}
		})
	}
}

func morathustraAccountWrapperRefusals(t *testing.T, identity string) {
	for _, tool := range []string{"gh", "basecamp"} {
		t.Run(tool, func(t *testing.T) {
			f := newNamedAccountFixture(t, tool, identity, false)
			prefix := []string{}
			good := []string{"api", "user"}
			if tool == "basecamp" {
				prefix = []string{"--profile", identity}
				good = []string{"me", "--agent"}
			}
			for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_CONFIG_DIR", "GH_HOST", "GH_REPO", "BASECAMP_TOKEN", "BASECAMP_PROFILE", "BASECAMP_BASE_URL", "BASECAMP_ACCOUNT_ID", "BASECAMP_LAUNCHPAD_URL", "SSH_AUTH_SOCK", "SSH_ASKPASS", "GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "HTTPS_PROXY"} {
				t.Run(key, func(t *testing.T) {
					out, err := f.run([]string{key + "=sentinel-private"}, append(slices.Clone(prefix), good...)...)
					if err != nil || !strings.Contains(out, "dispatch="+f.Native) || strings.Contains(out, "sentinel-private") {
						t.Fatalf("ambient override not discarded: %s: %v %s", key, err, out)
					}
				})
			}
			cases := [][]string{{"auth", "login"}, {"login"}, {"logout"}, {"tui"}, {"config", "set"}, {"--jq", ".x", "auth", "login"}, {"me", "--account=9"}, {"me", "--profile=codex"}, {"api", "user", "--hostname", "evil.example"}}
			if tool == "basecamp" {
				cases = append(cases, []string{"me", "-ja9"}, []string{"me", "-Pcodex"})
			} else {
				cases = append(cases, []string{"api", "https://evil.example/user"}, []string{"api", "--method", "GET", "https://evil.example/user"}, []string{"api", "--preview", "example", "https://evil.example/user"}, []string{"api", "-ip", "example", "https://evil.example/user"}, []string{"repo", "view", "-R", "evil.example/owner/repo"})
				for _, header := range []string{"Authorization: sentinel-private", "authorization: sentinel-private", "Host: evil.example", "Cookie: sentinel-private", "Proxy-Authorization: sentinel-private"} {
					for _, flag := range [][]string{{"-H", header}, {"--header", header}, {"--header=" + header}, {"-H" + header}, {"-H=" + header}, {"-iH" + header}} {
						cases = append(cases, append([]string{"api", "user"}, flag...))
					}
				}
			}
			for i, args := range cases {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					out, err := f.run(nil, append(slices.Clone(prefix), args...)...)
					if err == nil || strings.Contains(out, "dispatch=") || strings.Contains(out, "sentinel-private") || !strings.Contains(out, "explicit_override_refused") {
						t.Fatalf("unsafe arguments dispatched: %v", args)
					}
				})
			}
			if tool == "basecamp" {
				for _, args := range [][]string{{"me"}, {"-P" + identity, "me"}, {"--profile=" + identity, "me"}, {"--profile", "codex", "me"}} {
					if _, err := f.run(nil, args...); err == nil {
						t.Fatal("literal profile not required")
					}
				}
			}
		})
	}
}
func morathustraAccountWrapperPayloads(t *testing.T, identity string) {
	f := newNamedAccountFixture(t, "gh", identity, false)
	for _, args := range [][]string{{"api", "user", "--raw-field", "body=https://example.org/link"}, {"api", "--jq", ".url // \"https://example.org\"", "user"}, {"api", "--preview", "example", "user", "-H", "Accept: application/vnd.github+json"}, {"api", "user", "--header=X-GitHub-Api-Version: 2022-11-28", "-XGET"}, {"api", "user", "-HAccept: application/json"}, {"repo", "view", "-R", "ZeroToOrbit/repo"}, {"repo", "view", "--repo=github.com/beboldcat/repo"}} {
		if out, err := f.run(nil, args...); err != nil {
			t.Fatalf("valid native arguments rejected: %v %s", args, out)
		}
	}
}
func morathustraAccountWrapperCustody(t *testing.T, identity string) {
	for _, tool := range []string{"gh", "basecamp"} {
		t.Run(tool, func(t *testing.T) {
			required := []string{"", "home", "config", "cache"}
			files := []string{"gh/config.yml", "gh/hosts.yml"}
			if tool == "gh" {
				required = append(required, "gh")
			} else {
				required = append(required, "config/basecamp")
				files = []string{"config/basecamp/config.json", "config/basecamp/credentials.json"}
			}
			for _, path := range append(required, files...) {
				for _, mode := range []string{"missing", "mode", "symlink", "hardlink"} {
					if mode == "hardlink" && !slices.Contains(files, path) {
						continue
					}
					t.Run(path+"/"+mode, func(t *testing.T) {
						f := newNamedAccountFixture(t, tool, identity, false)
						target := filepath.Join(f.Root, path)
						switch mode {
						case "missing":
							if err := os.RemoveAll(target); err != nil {
								t.Fatal(err)
							}
						case "mode":
							if err := os.Chmod(target, 0755); err != nil {
								t.Fatal(err)
							}
						case "symlink":
							moved := target + "-real"
							if err := os.Rename(target, moved); err != nil {
								t.Fatal(err)
							}
							if err := os.Symlink(moved, target); err != nil {
								t.Fatal(err)
							}
						case "hardlink":
							if err := os.Link(target, target+"-link"); err != nil {
								t.Fatal(err)
							}
						}
						args := []string{"api", "user"}
						if tool == "basecamp" {
							args = []string{"--profile", identity, "me", "--agent"}
						}
						out, err := f.run(nil, args...)
						if err == nil || strings.Contains(out, "dispatch=") {
							t.Fatalf("unsafe custody accepted: %s %s", path, mode)
						}
						want := "custody_invalid"
						if mode == "missing" {
							want = "auth_missing"
						}
						if !strings.Contains(out, want) {
							t.Fatalf("missing bounded reason %s: %s", want, out)
						}
					})
				}
			}
		})
	}
}

func TestAccountWrapperNativeAuthFailure(t *testing.T) {
	for _, tool := range []string{"gh", "basecamp"} {
		f := newNamedAccountFixture(t, tool, "mina", false)
		bash, _ := accountTools(t)
		if err := os.WriteFile(f.Native, []byte("#!"+bash+"/bin/bash\nprintf 'native_auth_expired\\n' >&2\nexit 3\n"), 0700); err != nil {
			t.Fatal(err)
		}
		args := []string{"api", "user"}
		if tool == "basecamp" {
			args = []string{"--profile", "mina", "me", "--agent"}
		}
		out, err := f.run([]string{"GH_TOKEN=sentinel-private"}, args...)
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 3 || strings.TrimSpace(out) != "native_auth_expired" {
			t.Fatalf("native auth result lost: %v %s", err, out)
		}
	}
}

func morathustraAccountWrapperNativeSkill(t *testing.T, identity string) {
	f := newNamedAccountFixture(t, "basecamp", identity, false)
	if out, err := f.run(nil, "--profile", identity, "skill", "--agent"); err != nil || !strings.Contains(out, "arg=skill\narg=--agent\n") {
		t.Fatalf("native embedded skill read refused: %v %s", err, out)
	}
	for _, args := range [][]string{{"skill"}, {"skill", "install"}, {"skill", "install", "--agent"}, {"skill", "--json"}, {"skill", "--agent", "--force"}, {"skill", "--agent", "install"}} {
		out, err := f.run(nil, append([]string{"--profile", identity}, args...)...)
		if err == nil || strings.Contains(out, "dispatch=") {
			t.Fatalf("skill mutation/wizard dispatched: %v", args)
		}
	}
}

func TestMorathustraAccountWrapperDispatch(t *testing.T) {
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) { morathustraAccountWrapperDispatch(t, identity) })
	}
}

func TestMorathustraAccountWrapperHermesPager(t *testing.T) {
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) { morathustraAccountWrapperHermesPager(t, identity) })
	}
}

func TestMorathustraAccountWrapperRefusals(t *testing.T) {
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) { morathustraAccountWrapperRefusals(t, identity) })
	}
}

func TestMorathustraAccountWrapperPayloads(t *testing.T) {
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) { morathustraAccountWrapperPayloads(t, identity) })
	}
}

func TestMorathustraAccountWrapperCustody(t *testing.T) {
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) { morathustraAccountWrapperCustody(t, identity) })
	}
}

func TestMorathustraAccountWrapperNativeSkill(t *testing.T) {
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) { morathustraAccountWrapperNativeSkill(t, identity) })
	}
}

func TestMinaRetiredAccountEntryPoints(t *testing.T) {
	for _, tool := range []string{"gh", "basecamp"} {
		f := newNamedAccountFixture(t, tool, "morathustra", true)
		// Refusal precedes all custody and CLI access even when old state is absent.
		if err := os.RemoveAll(f.Root); err != nil {
			t.Fatal(err)
		}
		out, err := f.run(nil, "--profile", "mina", "me", "--agent")
		if err == nil || !strings.Contains(out, "retired") || strings.Contains(out, "dispatch=") {
			t.Fatalf("retired dispatch: %v %s", err, out)
		}
	}
	f := newNamedAccountFixture(t, "basecamp", "mina", false)
	if out, err := f.run(nil, "--profile", "morathustra", "me", "--agent"); err == nil || strings.Contains(out, "dispatch=") {
		t.Fatal("old profile admitted by MINA wrapper")
	}
}
