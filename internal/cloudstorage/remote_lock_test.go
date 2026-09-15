package cloudstorage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteLockImmediateBusy(t *testing.T) {
	t.Parallel()

	cfg := testRemoteLockConfig(t)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	err := WithRemoteLock(context.Background(), cfg, RemoteLockOptions{Operation: "second"}, func(context.Context) error {
		t.Fatal("second lock should not run while first lock is held")
		return nil
	})
	if !errors.Is(err, ErrRemoteLockBusy) {
		t.Fatalf("err = %v, want ErrRemoteLockBusy", err)
	}
}

func TestRemoteLockWaitSucceedsAfterRelease(t *testing.T) {
	t.Parallel()

	cfg := testRemoteLockConfig(t)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	waitHeld()

	done := make(chan error, 1)
	ran := make(chan struct{}, 1)
	go func() {
		done <- WithRemoteLock(context.Background(), cfg, RemoteLockOptions{Operation: "second", Wait: time.Second}, func(context.Context) error {
			ran <- struct{}{}
			return nil
		})
	}()

	time.Sleep(50 * time.Millisecond)
	release()
	if err := <-done; err != nil {
		t.Fatalf("second lock returned error: %v", err)
	}
	select {
	case <-ran:
	default:
		t.Fatal("second lock callback did not run")
	}
}

func testRemoteLockConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		StateDir:      filepath.Join(t.TempDir(), "cloud"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func holdRemoteLockForTest(t *testing.T, cfg Config) (release func(), waitHeld func()) {
	t.Helper()
	held := make(chan struct{})
	releaseCh := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithRemoteLock(context.Background(), cfg, RemoteLockOptions{Operation: "holder"}, func(context.Context) error {
			close(held)
			<-releaseCh
			return nil
		})
	}()
	return func() {
			select {
			case <-releaseCh:
			default:
				close(releaseCh)
			}
			if err := <-done; err != nil {
				t.Errorf("holder lock returned error: %v", err)
			}
		}, func() {
			select {
			case <-held:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for holder lock")
			}
		}
}
