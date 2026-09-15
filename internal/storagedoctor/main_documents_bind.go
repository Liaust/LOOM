package storagedoctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"loom.local/loom/internal/fsaccess"
)

const defaultCanonicalMainDocumentsDir = "/srv/loom/box/Documents"

// RunMainDocumentsBindPreflight retains its historical Go/API name for
// compatibility, but now checks the canonical Box Documents directory. The
// legacy bind mount and generated export target are no longer runtime inputs.
func RunMainDocumentsBindPreflight(opts MainDocumentsBindOptions) MainDocumentsBindStatus {
	canonicalRoot := filepath.Clean(firstNonEmpty(
		opts.MainDocumentsDir,
		canonicalDocumentsFromBox(os.Getenv("LOOM_BOX_PATH")),
		defaultCanonicalMainDocumentsDir,
	))
	status := MainDocumentsBindStatus{
		Status:           StatusOK,
		MainDocumentsDir: canonicalRoot,
		TargetPath:       canonicalRoot,
		GeneratedAt:      time.Now().UTC(),
		Checks: []MountCheck{
			checkCanonicalDirectory("canonical_dir", canonicalRoot),
		},
	}
	if runtime.GOOS == "linux" {
		status.Checks = append(status.Checks, checkNotSeparateMount(canonicalRoot))
	} else {
		status.Checks = append(status.Checks, MountCheck{
			ID:      "not_separate_mount",
			Status:  StatusSkipped,
			Summary: "separate-mount check only applies on Linux",
			Detail:  runtime.GOOS,
		})
	}
	status.Status = statusFromMountChecks(status.Checks)
	return status
}

func canonicalDocumentsFromBox(boxRoot string) string {
	boxRoot = strings.TrimSpace(boxRoot)
	if boxRoot == "" {
		return ""
	}
	return filepath.Join(boxRoot, "Documents")
}

func checkCanonicalDirectory(id, path string) MountCheck {
	return checkCanonicalDirectoryWithAccess(id, path, fsaccess.Check)
}

func checkCanonicalDirectoryWithAccess(id, path string, check func(string, ...fsaccess.Requirement) fsaccess.Result) MountCheck {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MountCheck{ID: id, Status: StatusError, Summary: "canonical directory does not exist", Detail: path}
		}
		return MountCheck{ID: id, Status: StatusError, Summary: "could not inspect canonical directory: " + err.Error(), Detail: path}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return MountCheck{ID: id, Status: StatusError, Summary: "canonical directory must not be a symlink", Detail: path}
	}
	if !info.IsDir() {
		return MountCheck{ID: id, Status: StatusError, Summary: "canonical path is not a directory", Detail: path}
	}
	access := check(path, fsaccess.Write, fsaccess.Execute)
	if !access.OK {
		detail := path
		if strings.TrimSpace(access.Error) != "" {
			detail += ": " + access.Error
		}
		return MountCheck{
			ID:      id,
			Status:  StatusError,
			Summary: "canonical directory is not writable and searchable by the service account",
			Detail:  detail,
		}
	}
	return MountCheck{ID: id, Status: StatusOK, Summary: "canonical Box Documents is a writable real directory", Detail: path}
}

func checkNotSeparateMount(path string) MountCheck {
	return checkNotSeparateMountWithRunner(path, func(path string) ([]byte, error) {
		return exec.Command("findmnt", "-rn", "--mountpoint", path).CombinedOutput()
	})
}

func checkNotSeparateMountWithRunner(path string, run func(string) ([]byte, error)) MountCheck {
	output, err := run(path)
	if err == nil {
		return MountCheck{
			ID:      "not_separate_mount",
			Status:  StatusError,
			Summary: "canonical Box Documents must not be a separate or bind mount",
			Detail:  path,
		}
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || strings.TrimSpace(string(output)) != "" {
		detail := err.Error()
		if rendered := strings.TrimSpace(string(output)); rendered != "" {
			detail += ": " + rendered
		}
		return MountCheck{
			ID:      "not_separate_mount",
			Status:  StatusError,
			Summary: "could not verify whether canonical Box Documents is a separate mount: " + detail,
			Detail:  path,
		}
	}
	return MountCheck{
		ID:      "not_separate_mount",
		Status:  StatusOK,
		Summary: "canonical Box Documents is not a separate mountpoint",
		Detail:  path,
	}
}
