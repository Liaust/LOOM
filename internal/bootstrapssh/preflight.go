package bootstrapssh

import (
	"context"
	"fmt"
	"strings"
)

func CollectRemoteFacts(ctx context.Context, runner Runner) (RemoteFacts, error) {
	if runner == nil {
		return RemoteFacts{}, fmt.Errorf("ssh runner is required")
	}
	result, err := runner.Run(ctx, RemoteCommand{Command: PreflightScript()})
	if err != nil {
		return RemoteFacts{}, err
	}
	return ParsePreflightOutput(result.Stdout), nil
}

func PreflightScript() string {
	return strings.TrimSpace(`
kv() {
  key="$1"
  shift
  value="$*"
  printf '%s=%s\n' "$key" "$value"
}
has_cmd() {
  command -v "$1" >/dev/null 2>&1 && printf true || printf false
}
cmd_path() {
  command -v "$1" 2>/dev/null || true
}
manifest_path() {
  if [ -n "${HOME:-}" ] && [ -f "$HOME/.config/loom/install.yaml" ]; then
    printf '%s' "$HOME/.config/loom/install.yaml"
  elif [ -f /etc/loom/install.yaml ]; then
    printf '%s' /etc/loom/install.yaml
  fi
}
home_dir="${HOME:-}"
if [ -z "$home_dir" ] && command -v getent >/dev/null 2>&1; then
  home_dir="$(getent passwd "$(whoami 2>/dev/null || id -un 2>/dev/null || printf unknown)" | cut -d: -f6)"
fi
kv hostname "$(hostname 2>/dev/null || printf unknown)"
kv os "$(uname -s 2>/dev/null || printf unknown)"
kv arch "$(uname -m 2>/dev/null || printf unknown)"
kv user "$(whoami 2>/dev/null || id -un 2>/dev/null || printf unknown)"
kv uid "$(id -u 2>/dev/null || printf unknown)"
kv home_dir "$home_dir"
kv has_sudo "$(has_cmd sudo)"
kv has_systemd "$(if [ -d /run/systemd/system ] || command -v systemctl >/dev/null 2>&1; then printf true; else printf false; fi)"
kv has_launchd "$(has_cmd launchctl)"
kv has_nix "$(has_cmd nix)"
kv has_git "$(has_cmd git)"
kv has_go "$(has_cmd go)"
kv has_rsync "$(has_cmd rsync)"
kv has_tar "$(has_cmd tar)"
kv existing_loom "$(cmd_path loom)"
kv existing_loomd "$(cmd_path loomd)"
kv existing_node_agent "$(cmd_path loom-node-agent)"
kv existing_manifest_path "$(manifest_path)"
`)
}

func ParsePreflightOutput(output string) RemoteFacts {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return RemoteFacts{
		Hostname:             values["hostname"],
		OS:                   normalizeOS(values["os"]),
		Arch:                 normalizeArch(values["arch"]),
		User:                 values["user"],
		UID:                  values["uid"],
		HomeDir:              values["home_dir"],
		HasSudo:              parseBool(values["has_sudo"]),
		HasSystemd:           parseBool(values["has_systemd"]),
		HasLaunchd:           parseBool(values["has_launchd"]),
		HasNix:               parseBool(values["has_nix"]),
		HasGit:               parseBool(values["has_git"]),
		HasGo:                parseBool(values["has_go"]),
		HasRsync:             parseBool(values["has_rsync"]),
		HasTar:               parseBool(values["has_tar"]),
		ExistingLoom:         values["existing_loom"],
		ExistingLoomd:        values["existing_loomd"],
		ExistingNodeAgent:    values["existing_node_agent"],
		ExistingManifestPath: values["existing_manifest_path"],
	}
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}

func normalizeOS(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "darwin", "macos":
		return "darwin"
	case "linux", "gnu/linux":
		return "linux"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeArch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l":
		return "arm"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
