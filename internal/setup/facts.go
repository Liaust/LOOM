package setup

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

func CollectLocalFacts() TargetFacts {
	hostname, _ := os.Hostname()
	currentUser, _ := user.Current()
	userName := ""
	homeDir := ""
	if currentUser != nil {
		userName = currentUser.Username
		homeDir = currentUser.HomeDir
	}
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	facts := TargetFacts{
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		Hostname:          hostname,
		UserName:          userName,
		HomeDir:           filepath.Clean(homeDir),
		HasLaunchd:        runtime.GOOS == "darwin",
		HasSudo:           commandExists("sudo"),
		HasNix:            commandExists("nix"),
		HasGit:            commandExists("git"),
		HasGo:             commandExists("go"),
		ExistingLoom:      binaryFact("loom", homeDir),
		ExistingLoomd:     binaryFact("loomd", homeDir),
		ExistingNodeAgent: binaryFact("loom-node-agent", homeDir),
	}
	facts.HasSystemd = runtime.GOOS == "linux" && (pathExists("/run/systemd/system") || commandExists("systemctl"))
	facts.ExistingManifestPath = firstExistingPath(
		"/etc/loom/install.yaml",
		filepath.Join(facts.HomeDir, ".config", "loom", "install.yaml"),
	)
	return facts
}

func binaryFact(name string, homeDir string) BinaryFact {
	path, err := exec.LookPath(name)
	if err == nil {
		return BinaryFact{Name: name, Path: path, Found: true}
	}
	if strings.TrimSpace(homeDir) != "" {
		localPath := filepath.Join(filepath.Clean(homeDir), ".local", "bin", name)
		if pathExists(localPath) {
			return BinaryFact{Name: name, Path: localPath, Found: true}
		}
	}
	return BinaryFact{Name: name}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		if pathExists(path) {
			return path
		}
	}
	return ""
}
