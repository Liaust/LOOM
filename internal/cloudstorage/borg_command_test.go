package cloudstorage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultBorgExecReturnsStdoutOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	script := filepath.Join(t.TempDir(), "fake-borg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho stdout\necho stderr >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := defaultBorgExec(context.Background(), script, BorgCommand{}, nil)
	if err != nil {
		t.Fatalf("defaultBorgExec returned error: %v", err)
	}
	if got := string(out); got != "stdout\n" {
		t.Fatalf("output = %q, want stdout only", got)
	}
}

func TestDefaultBorgExecIncludesStderrOnFailure(t *testing.T) {
	t.Parallel()

	script := filepath.Join(t.TempDir(), "fake-borg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho stdout\necho stderr >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := defaultBorgExec(context.Background(), script, BorgCommand{}, nil)
	if err == nil {
		t.Fatal("defaultBorgExec returned nil error")
	}
	got := string(out)
	if !strings.Contains(got, "stdout") || !strings.Contains(got, "stderr") {
		t.Fatalf("output = %q, want stdout and stderr", got)
	}
}

func TestBorgCommandRunnerPreservesSuccessAndBoundsFailureDiagnostic(t *testing.T) {
	t.Run("successful stdout is unchanged", func(t *testing.T) {
		cfg := testBorgStatusConfig(t)
		runner := BorgCommandRunner{
			Config: cfg, DisableRemoteLock: true,
			Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
				return []byte("exact successful stdout\n"), nil
			},
		}
		out, err := runner.Run(context.Background(), BorgCommand{Args: []string{"list", "--json"}})
		if err != nil || string(out) != "exact successful stdout\n" {
			t.Fatalf("Run output=%q err=%v", out, err)
		}
	})

	t.Run("failure output is bounded normalized and credential redacted", func(t *testing.T) {
		cfg := testBorgStatusConfig(t)
		cfg.Snapshots.Borg.Repository = "ssh://environment-user:environment-password@example.test/repository"
		raw := []byte("\x1b[31mremote disconnected\x1b[0m\x00\nBORG_PASSPHRASE=do-not-leak\nssh://user:password@example.test/repo and https://api:token@second.example/repo\n-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-material\n-----END OPENSSH PRIVATE KEY-----\n" + strings.Repeat("leading output\n", borgFailureInspectLimit) + "\x1b[31mFINAL BORG ERROR: repository connection timed out\x1b[0m\x00\n")
		runner := BorgCommandRunner{
			Config: cfg, DisableRemoteLock: true,
			Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
				return raw, errors.New("exit status 2")
			},
		}
		out, err := runner.Run(context.Background(), BorgCommand{Args: []string{"create", "::pending"}})
		if err == nil || string(out) != string(raw) {
			t.Fatalf("Run output preserved=%t err=%v", string(out) == string(raw), err)
		}
		var executionErr *BorgExecutionError
		if !errors.As(err, &executionErr) {
			t.Fatalf("error type = %T, want *BorgExecutionError", err)
		}
		if !executionErr.DiagnosticTruncated || len(executionErr.Diagnostic) > borgFailureDiagnosticLimit || !strings.Contains(executionErr.Diagnostic, borgFailureOmissionMarker) {
			t.Fatalf("diagnostic length=%d truncated=%t value=%q", len(executionErr.Diagnostic), executionErr.DiagnosticTruncated, executionErr.Diagnostic)
		}
		for _, forbidden := range []string{"\x1b", "\x00", "do-not-leak", "user:password", "api:token", "private-key-material", "environment-user", cfg.Snapshots.Borg.PassphraseFile} {
			if strings.Contains(executionErr.Diagnostic, forbidden) || strings.Contains(err.Error(), forbidden) {
				t.Fatalf("credential/control value %q leaked: diagnostic=%q err=%q", forbidden, executionErr.Diagnostic, err)
			}
		}
		for _, required := range []string{"remote disconnected", borgFailureRedactedMarker, "ssh://[redacted]@example.test/repo", "https://[redacted]@second.example/repo", borgFailureOmissionMarker, "FINAL BORG ERROR: repository connection timed out"} {
			if !strings.Contains(executionErr.Diagnostic, required) {
				t.Fatalf("diagnostic missing %q: %q", required, executionErr.Diagnostic)
			}
		}
	})

	for _, test := range []struct {
		name       string
		secretLine string
		secret     string
	}{
		{
			name:       "oversized environment credential line",
			secretLine: "BORG_PASSPHRASE=" + strings.Repeat("env-secret-fragment", borgFailureInspectLimit),
			secret:     "env-secret-fragment",
		},
		{
			name:       "oversized URI userinfo line",
			secretLine: "ssh://archive-user:" + strings.Repeat("uri-secret-fragment", borgFailureInspectLimit) + "@example.test/repository",
			secret:     "uri-secret-fragment",
		},
	} {
		t.Run(test.name+" cannot cross the raw inspection boundary", func(t *testing.T) {
			cfg := testBorgStatusConfig(t)
			raw := []byte("safe diagnostic head\n" + test.secretLine + "\nFINAL BORG ERROR: safe terminal exception\n")
			runner := BorgCommandRunner{
				Config: cfg, DisableRemoteLock: true,
				Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
					return raw, errors.New("exit status 2")
				},
			}
			_, err := runner.Run(context.Background(), BorgCommand{Args: []string{"create", "::pending"}})
			var executionErr *BorgExecutionError
			if !errors.As(err, &executionErr) {
				t.Fatalf("error type = %T, want *BorgExecutionError", err)
			}
			if !executionErr.DiagnosticTruncated || len(executionErr.Diagnostic) > borgFailureDiagnosticLimit {
				t.Fatalf("diagnostic length=%d truncated=%t value=%q", len(executionErr.Diagnostic), executionErr.DiagnosticTruncated, executionErr.Diagnostic)
			}
			if strings.Contains(executionErr.Diagnostic, test.secret) || strings.Contains(err.Error(), test.secret) {
				t.Fatalf("partial-line credential survived raw bounding: diagnostic=%q err=%q", executionErr.Diagnostic, err)
			}
			for _, required := range []string{"safe diagnostic head", borgFailureOmissionMarker, "FINAL BORG ERROR: safe terminal exception"} {
				if !strings.Contains(executionErr.Diagnostic, required) {
					t.Fatalf("diagnostic missing %q: %q", required, executionErr.Diagnostic)
				}
			}
		})
	}
}

func TestResolveCommandPathPreservesAbsoluteAndFindsPathCommands(t *testing.T) {
	t.Parallel()

	if got := resolveCommandPath("/custom/bin/borg"); got != "/custom/bin/borg" {
		t.Fatalf("absolute path = %q", got)
	}
	if got := resolveCommandPath("sh"); got == "" || got == "sh" || !strings.Contains(got, "sh") {
		t.Fatalf("resolved sh = %q, want concrete command path", got)
	}
}

func TestBorgCommandRunnerAddsStrictSSHandLockWait(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	var capturedCommand BorgCommand
	var capturedEnv []string
	runner := BorgCommandRunner{
		Config: cfg,
		Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
			capturedCommand = command
			capturedEnv = env
			return []byte(`{}`), nil
		},
	}
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"list", "--json"}}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if strings.Join(capturedCommand.Args, " ") != "--lock-wait 5 list --json" {
		t.Fatalf("args = %#v", capturedCommand.Args)
	}
	env := strings.Join(capturedEnv, "\n")
	for _, want := range []string{
		"BORG_RSH=ssh",
		"BatchMode=yes",
		"IdentitiesOnly=yes",
		"ConnectTimeout=8",
		"ConnectionAttempts=1",
		"ServerAliveInterval=15",
		"ServerAliveCountMax=2",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q:\n%s", want, env)
		}
	}
}

func TestBorgCommandRunnerUsesGlobalRemoteLock(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	zeroWait := time.Duration(0)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	called := false
	runner := BorgCommandRunner{
		Config:         cfg,
		RemoteLockWait: &zeroWait,
		Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
			called = true
			return nil, nil
		},
	}
	if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"list", "--json"}}); err == nil {
		t.Fatal("Run returned nil error while remote lock was held")
	} else if !strings.Contains(err.Error(), ErrRemoteLockBusy.Error()) {
		t.Fatalf("err = %v, want ErrRemoteLockBusy", err)
	}
	if called {
		t.Fatal("borg exec ran while global remote lock was held")
	}
}

func TestBorgWriterRejectsOptionPrefixedAndUnsupportedCommandsBeforeExec(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	execCalls := 0
	streamCalls := 0
	runner := BorgCommandRunner{
		Config: cfg, DisableRemoteLock: true,
		Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			execCalls++
			return nil, nil
		},
		StreamExec: func(context.Context, string, BorgCommand, []string, io.Writer) error {
			streamCalls++
			return nil
		},
	}
	commands := [][]string{
		{"--umask", "0077", "prune"},
		{"--umask=0077", "compact"},
		{"--lock-wait", "5", "delete"},
		{"--remote-path", "/tmp/prune", "prune"},
		{"--debug", "compact"},
		{"mount", "::archive", "/tmp/mount"},
	}
	for _, args := range commands {
		if _, err := runner.Run(context.Background(), BorgCommand{Args: args}); err == nil {
			t.Fatalf("writer command %#v reached authorization", args)
		}
		if err := runner.RunStream(context.Background(), BorgCommand{Args: args}, io.Discard); err == nil {
			t.Fatalf("streaming writer command %#v reached authorization", args)
		}
	}
	if execCalls != 0 || streamCalls != 0 {
		t.Fatalf("denied writer commands reached Exec=%d StreamExec=%d", execCalls, streamCalls)
	}
	for _, stateDir := range []string{cfg.Snapshots.Borg.CacheDir, cfg.Snapshots.Borg.SecurityDir} {
		if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
			t.Fatalf("denied writer command prepared state directory %s: %v", stateDir, err)
		}
	}
}
