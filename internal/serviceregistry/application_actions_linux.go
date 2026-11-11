//go:build linux

package serviceregistry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (h *ApplicationHostSystem) actionConfigurationPath(e applicationActionExecution) string {
	return filepath.Join(h.Config.ConfigRoot, e.Request.Owner.Instance(), "actions", applicationSHA(e.Request.Token)[7:], "config.json")
}

func (h *ApplicationHostSystem) publishActionConfiguration(ctx context.Context, e applicationActionExecution) error {
	path := h.actionConfigurationPath(e)
	store := ApplicationStateStore{Root: h.Config.StateRoot, OwnerUID: 0}
	for _, dir := range []string{h.Config.ConfigRoot, filepath.Join(h.Config.ConfigRoot, e.Request.Owner.Instance()), filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
		gid, mode, purpose := e.Generation.UID, os.FileMode(0710), "action-config:"+dir
		infrastructure := dir == h.Config.ConfigRoot
		if infrastructure {
			gid, mode = 0, 0711
		}
		if infrastructure || dir == filepath.Join(h.Config.ConfigRoot, e.Request.Owner.Instance()) {
			purpose = "generation:" + dir
		}
		if err := store.ensureClaimedDirectory(ctx, dir, purpose, 0, gid, mode, infrastructure, h.FailureHook); err != nil {
			return err
		}
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0400)
	if err == nil {
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		if err = f.Chown(int(e.Generation.UID), int(e.Generation.UID)); err == nil {
			err = f.Chmod(0400)
		}
		if err == nil {
			_, err = f.Write(e.Config)
		}
		if err == nil {
			err = f.Sync()
		}
		if err == nil {
			err = applicationSyncDir(filepath.Dir(path))
		}
	} else if !errors.Is(err, unix.EEXIST) {
		return applicationError("action.configuration_custody")
	}
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return applicationError("action.configuration_publish")
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0400 || st.Nlink != 1 || st.Uid != e.Generation.UID || st.Gid != e.Generation.UID || st.Size > 65536 {
		return applicationError("action.configuration_custody")
	}
	fd, err = unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return applicationError("action.configuration_custody")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var opened unix.Stat_t
	raw, readErr := io.ReadAll(io.LimitReader(f, 65537))
	if readErr != nil || unix.Fstat(fd, &opened) != nil || opened.Dev != st.Dev || opened.Ino != st.Ino || !bytes.Equal(raw, e.Config) {
		return applicationError("action.configuration_custody")
	}
	return nil
}

func (h *ApplicationHostSystem) startApplicationAction(ctx context.Context, e applicationActionExecution) error {
	if os.Geteuid() != 0 || !applicationAbsolutePath(h.Config.SystemdRun) {
		return applicationError("action.root_backend_required")
	}
	tool, err := filepath.EvalSymlinks(h.Config.SystemdRun)
	if err != nil || applicationImmutableParents(tool, 0) != nil || applicationCustody(tool, 0, false) != nil {
		return applicationError("action.tool_custody")
	}
	args, err := applicationActionCommandArgs(e, h.actionConfigurationPath(e))
	if err != nil {
		return err
	}
	process, err := h.Observe(ctx, e.Generation)
	if err != nil || process.PID != 0 || process.State != "inactive" && process.State != "failed" {
		return applicationError("action.service_not_quiescent")
	}
	proc := applicationNetworkProc{fs: os.DirFS("/proc"), readlink: func(path string) (string, error) { return os.Readlink(filepath.Join("/proc", path)) }}
	if err = proc.quiescent(ctx, e.Generation.UID); err != nil {
		return err
	}
	if err = h.checkApplicationNetwork(ctx, e.Generation, e.Grant); err != nil {
		return err
	}
	if err = h.publishActionConfiguration(ctx, e); err != nil {
		return err
	}
	_, err = h.command(ctx, tool, args...)
	return err
}

func (h *ApplicationHostSystem) observeApplicationAction(ctx context.Context, e applicationActionExecution) (applicationActionObservation, error) {
	raw, err := h.command(ctx, h.Config.Systemctl, "show", e.Unit, "--all", "--property="+applicationActionUnitProperties)
	if err != nil {
		return applicationActionObservation{Phase: ApplicationActionUncertain}, applicationError("action.unit_unavailable")
	}
	// A manager reload/reset-failed can collect a failed transient unit. Recover
	// only an authenticated current-boot failure for this exact request; absence
	// itself never proves completion and this path never submits another unit.
	if bytes.Contains(append([]byte{'\n'}, raw...), []byte("\nLoadState=not-found\n")) {
		systemctl, err := filepath.EvalSymlinks(h.Config.Systemctl)
		if err != nil {
			return applicationActionObservation{Phase: ApplicationActionUncertain}, applicationError("action.unit_unavailable")
		}
		tool := filepath.Join(filepath.Dir(systemctl), "journalctl")
		if applicationImmutableParents(tool, 0) != nil || applicationCustody(tool, 0, false) != nil {
			return applicationActionObservation{Phase: ApplicationActionUncertain}, applicationError("action.tool_custody")
		}
		journal, err := h.command(ctx, tool, "--boot", "--unit="+e.Unit, "--lines=32", "--output=json", "--no-pager")
		if err != nil {
			return applicationActionObservation{Phase: ApplicationActionUncertain}, applicationError("action.unit_unavailable")
		}
		return applicationActionJournalFailure(journal, e), nil
	}
	return applicationActionUnitObservation(raw, e, h.actionConfigurationPath(e)), nil
}

var _ applicationActionSystem = (*ApplicationHostSystem)(nil)
