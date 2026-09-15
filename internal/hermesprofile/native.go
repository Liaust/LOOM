package hermesprofile

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// RunNativeBackup executes the reviewed supported command with no inherited
// provider, platform, database, or account environment. The production caller
// must already be agents; this adapter never uses sudo or changes identity.
func RunNativeBackup(ctx context.Context, binary, profile, output string) (string, error) {
	return RunNativeBackupForIdentity(ctx, "", binary, profile, output)
}

// RunNativeBackupForIdentity keeps the same agents/pinned-binary boundary while
// allowing an explicitly selected current producer. It never tries both roots.
func RunNativeBackupForIdentity(ctx context.Context, id RecoveryIdentity, binary, profile, output string) (string, error) {
	root, err := id.WorkspaceRoot()
	if err != nil {
		return "", err
	}
	u, err := user.Current()
	if err != nil || u.Username != "agents" {
		return "", fmt.Errorf("native recovery requires agents identity")
	}
	if captureWorkspace(profile, output) != root {
		return "", fmt.Errorf("native recovery requires canonical private capture")
	}
	return runNative(ctx, binary, profile, output)
}

// RunFixtureBackup is only for disposable acceptance. Both paths must share a
// canonical .loom-acceptance ancestor and must not resolve through symlinks.
func RunFixtureBackup(ctx context.Context, binary, profile, output string) (string, error) {
	root := captureWorkspace(profile, output)
	if !FixtureWorkspace(root) {
		return "", fmt.Errorf("fixture paths are not confined")
	}
	c, err := openCustody(profile)
	if err != nil {
		return "", err
	}
	defer c.close()
	owned := false
	for _, dir := range c.dirs {
		owned = owned || dir.name == ".loom-acceptance"
		if owned && dir.initial.Owner != uint32(os.Getuid()) {
			return "", fmt.Errorf("fixture capture is not owned by caller")
		}
	}
	digest, err := runNative(ctx, binary, profile, output)
	if err != nil {
		return "", err
	}
	return digest, c.revalidate()
}

func captureWorkspace(profile, output string) string {
	if !filepath.IsAbs(profile) || filepath.Clean(profile) != profile || filepath.Clean(output) != output || filepath.Base(profile) != ".capture" || filepath.Base(output) != PayloadFile || filepath.Dir(profile) != filepath.Dir(output) {
		return ""
	}
	attempt := filepath.Dir(profile)
	staging := filepath.Dir(attempt)
	recovery := filepath.Dir(staging)
	if filepath.Base(staging) != ".staging" || filepath.Base(recovery) != "recovery" {
		return ""
	}
	return filepath.Dir(recovery)
}

// This companion uses the pinned Hermes SQLite backup API, never raw database
// copying. Inherited descriptors bind both source and the newly owned output.
func snapshotNativeDatabase(ctx context.Context, binary string, parent, source, target *os.File, sourcePath, targetPath string) error {
	if !strings.HasPrefix(binary, "/nix/store/") || !strings.HasSuffix(binary, "-hermes-agent-"+Version+"/bin/hermes") {
		return fmt.Errorf("pinned database snapshot helper required")
	}
	helper := filepath.Join(filepath.Dir(binary), "loom-hermes-snapshot-db")
	chain, err := openCustody(filepath.Dir(helper))
	if err != nil {
		return err
	}
	defer chain.close()
	f, err := openAt(chain.leaf(), filepath.Base(helper), false)
	if err != nil {
		return err
	}
	defer f.Close()
	initial, err := statFD(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, "snapshot", sourcePath, targetPath)
	cmd.ExtraFiles = []*os.File{parent, source, target}
	cmd.Env = []string{"HOME=" + filepath.Dir(targetPath), "HERMES_HOME=" + filepath.Dir(targetPath), "PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1", "LANG=C.UTF-8"}
	cmd.Dir = filepath.Dir(targetPath)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("native database capture failed")
	}
	named, err := statAt(chain.leaf(), filepath.Base(helper))
	if err != nil || named != initial {
		return fmt.Errorf("native database helper changed")
	}
	return chain.revalidate()
}
func runNative(ctx context.Context, binary, profile, output string) (string, error) {
	if !strings.HasPrefix(binary, "/nix/store/") || !strings.HasSuffix(binary, "-hermes-agent-"+Version+"/bin/hermes") {
		return "", fmt.Errorf("pinned Nix Hermes executable required")
	}
	bc, err := openCustody(filepath.Dir(binary))
	if err != nil {
		return "", err
	}
	defer bc.close()
	executable, err := openAt(bc.leaf(), "hermes", false)
	if err != nil {
		return "", err
	}
	defer executable.Close()
	digest, _, err := hashFile(ctx, executable)
	if err != nil {
		return "", err
	}
	// No real HOME is exposed. External provider paths are also refused by the
	// source config and ZIP inventory boundary.
	home := filepath.Dir(output)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	env := []string{"HOME=" + home, "HERMES_HOME=" + profile, "HERMES_MANAGED=true", "HERMES_DISABLE_LAZY_INSTALLS=1", "HERMES_SKIP_NODE_BOOTSTRAP=1", "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TMPDIR=" + home}
	helper, err := openAt(bc.leaf(), "loom-hermes-snapshot-db", false)
	if err != nil {
		return "", err
	}
	defer helper.Close()
	helperID, err := statFD(helper)
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, filepath.Join(filepath.Dir(binary), "loom-hermes-snapshot-db"), "backup", profile, output)
	command.Env = env
	command.Dir = filepath.Dir(profile)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err = command.Run(); err != nil {
		return "", fmt.Errorf("Hermes backup did not complete")
	}
	helperNamed, err := statAt(bc.leaf(), "loom-hermes-snapshot-db")
	if err != nil || helperNamed != helperID {
		return "", fmt.Errorf("native backup helper changed")
	}
	after, _, err := hashFile(ctx, executable)
	if err != nil || after != digest {
		return "", fmt.Errorf("Hermes binary changed")
	}
	if err = bc.revalidate(); err != nil {
		return "", err
	}
	named, err := statAt(bc.leaf(), "hermes")
	held, e := statFD(executable)
	if err != nil || e != nil || named != held {
		return "", fmt.Errorf("Hermes executable substituted")
	}
	return digest, nil
}

// ReadSigningKey reads an externally provisioned owner-only key. No key is
// created, copied into recovery evidence, printed, or retained in configuration.
func ReadSigningKey(path string) ([]byte, error) {
	c, err := openCustody(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer c.close()
	f, err := openAt(c.leaf(), filepath.Base(path), false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := statFD(f)
	if err != nil || info.Owner != uint32(os.Getuid()) || info.Mode&07777 != 0600 {
		return nil, fmt.Errorf("recovery signing key must be owner-only")
	}
	b, err := readBounded(f, 64)
	if err != nil || len(b) != 64 {
		return nil, fmt.Errorf("invalid recovery signing key")
	}
	return b, c.revalidate()
}
