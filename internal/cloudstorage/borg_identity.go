package cloudstorage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

var borgHostIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)

func validBorgHostID(value string) bool { return borgHostIDPattern.MatchString(value) }

func borgHostID(cfg Config) (string, error) {
	if id := cfg.Snapshots.Borg.HostID; id != "" {
		if !validBorgHostID(id) {
			return "", fmt.Errorf("invalid Borg host identity")
		}
		return id, nil
	}
	// Borg's uuid.getnode can become random when Nix's service PATH has no ip
	// utility. Locks must remain attributable after cancellation and reboot.
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/etc/machine-id")
		if err != nil {
			return "", fmt.Errorf("read stable Borg machine identity; configure snapshots.borg.host_id: %w", err)
		}
		id := strings.TrimSpace(string(raw))
		if len(id) != 32 {
			return "", fmt.Errorf("invalid machine-id; configure snapshots.borg.host_id")
		}
		if _, err := hex.DecodeString(id); err != nil {
			return "", fmt.Errorf("invalid machine-id; configure snapshots.borg.host_id")
		}
		return hashedBorgHostID("machine-id:" + strings.ToLower(id)), nil
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("stable hostname unavailable; configure snapshots.borg.host_id")
	}
	return hashedBorgHostID("hostname:" + host), nil
}

func hashedBorgHostID(identity string) string {
	hash := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("loom-%x", hash[:16])
}
