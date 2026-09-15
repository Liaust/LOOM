package cloudstorage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var ErrRemoteLockBusy = errors.New("cloud remote lock busy")

type RemoteLockOptions struct {
	Operation string
	Wait      time.Duration
}

type RemoteLockBusyError struct {
	Path      string
	Operation string
}

func (e RemoteLockBusyError) Error() string {
	if e.Operation == "" {
		return fmt.Sprintf("%v: %s", ErrRemoteLockBusy, e.Path)
	}
	return fmt.Sprintf("%v: %s held during %s", ErrRemoteLockBusy, e.Path, e.Operation)
}

func (e RemoteLockBusyError) Unwrap() error {
	return ErrRemoteLockBusy
}

func WithRemoteLock(ctx context.Context, cfg Config, opts RemoteLockOptions, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath := RemoteLockPath(cfg)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o750); err != nil {
		return fmt.Errorf("create cloud remote lock dir: %w", err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return fmt.Errorf("open cloud remote lock: %w", err)
	}
	defer file.Close()

	if err := acquireRemoteLock(ctx, file, lockPath, opts); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func RemoteLockPath(cfg Config) string {
	if cfg.RemoteLockPath != "" {
		return cfg.RemoteLockPath
	}
	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return filepath.Join(stateDir, "locks", "storagebox.lock")
}

func RemoteLockWait(cfg Config) time.Duration {
	if cfg.RemoteLockWaitSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.RemoteLockWaitSeconds) * time.Second
}

func RemoteLockWorkerWait(cfg Config) time.Duration {
	if cfg.RemoteLockWorkerWaitSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.RemoteLockWorkerWaitSeconds) * time.Second
}

func RemoteLockEffectfulWait(cfg Config) time.Duration {
	if cfg.RemoteLockEffectfulWaitSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.RemoteLockEffectfulWaitSeconds) * time.Second
}

func acquireRemoteLock(ctx context.Context, file *os.File, path string, opts RemoteLockOptions) error {
	wait := opts.Wait
	started := time.Now()
	for {
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return nil
		} else if !isRemoteLockBusy(err) {
			return fmt.Errorf("acquire cloud remote lock: %w", err)
		}
		if wait <= 0 || time.Since(started) >= wait {
			return RemoteLockBusyError{Path: path, Operation: opts.Operation}
		}
		remaining := wait - time.Since(started)
		sleep := 100 * time.Millisecond
		if remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isRemoteLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
