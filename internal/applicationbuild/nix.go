package applicationbuild

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	sr "loom.local/loom/internal/serviceregistry"
)

const maxCommandBytes = 2 * 1024 * 1024

type Command struct {
	Path string
	Args []string
	Dir  string
}

// A runner must respect ctx and bound output. No shell or caller-selected
// executable is passed through Input. Errors/output are not logged or exposed.
type CommandRunner interface {
	Run(context.Context, Command) ([]byte, error)
}

type CommandRunnerFunc func(context.Context, Command) ([]byte, error)

func (f CommandRunnerFunc) Run(ctx context.Context, command Command) ([]byte, error) {
	return f(ctx, command)
}

type ExecRunner struct{}

// Details stay in scoped build-job logs, never in routine error strings.
type CommandFailure struct{ Detail string }

func (e *CommandFailure) Error() string { return ErrCommand.Error() }
func (e *CommandFailure) Unwrap() error { return ErrCommand }

func (ExecRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = []string{"PATH=/run/current-system/sw/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C", "HOME=/var/empty"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	output := &boundedOutput{cancel: cancel}
	detail := &tailOutput{}
	cmd.Stdout, cmd.Stderr = output, detail
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || output.overflow {
		return nil, &CommandFailure{Detail: string(detail.data)}
	}
	return output.buffer.Bytes(), nil
}

type tailOutput struct{ data []byte }

func (t *tailOutput) Write(raw []byte) (int, error) {
	n := len(raw)
	if n >= 32768 {
		t.data = append(t.data[:0], raw[n-32768:]...)
	} else {
		if len(t.data)+n > 32768 {
			t.data = append([]byte{}, t.data[len(t.data)+n-32768:]...)
		}
		t.data = append(t.data, raw...)
	}
	return n, nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > maxCommandBytes-b.buffer.Len() {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return 0, ErrCommand
	}
	return b.buffer.Write(p)
}

type realization struct {
	StoreRoot  string
	Executable string
	Closure    []sr.ApplicationClosurePath
}

func (s *Service) command(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args = append([]string{"--extra-experimental-features", "nix-command"}, args...)
	raw, err := s.options.Runner.Run(ctx, Command{Path: s.options.NixBinary, Args: args, Dir: dir})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) > maxCommandBytes {
		if err != nil {
			return nil, err
		}
		return nil, ErrCommand
	}
	return raw, nil
}

func (s *Service) realize(ctx context.Context, stage string) (realization, error) {
	raw, err := s.command(ctx, stage, "build", "--no-link", "--json", "--file", filepath.Join(stage, "build.nix"))
	if err != nil {
		return realization{}, err
	}
	var builds []struct {
		Outputs map[string]string `json:"outputs"`
	}
	if json.Unmarshal(raw, &builds) != nil || len(builds) != 1 || len(builds[0].Outputs) != 1 || !storePattern.MatchString(builds[0].Outputs["out"]) {
		return realization{}, ErrRealization
	}
	return s.inspect(ctx, builds[0].Outputs["out"])
}

func (s *Service) inspect(ctx context.Context, root string) (realization, error) {
	raw, err := s.command(ctx, "", "--offline", "path-info", "--json", "--recursive", root)
	if err != nil {
		return realization{}, err
	}
	type entry struct {
		Path       string   `json:"path"`
		NARHash    string   `json:"narHash"`
		References []string `json:"references"`
	}
	var entries map[string]entry
	// Nix has emitted both keyed objects and arrays across CLI versions.
	if json.Unmarshal(raw, &entries) != nil {
		var list []entry
		if json.Unmarshal(raw, &list) != nil {
			return realization{}, ErrRealization
		}
		entries = map[string]entry{}
		for _, item := range list {
			if _, duplicate := entries[item.Path]; duplicate {
				return realization{}, ErrRealization
			}
			entries[item.Path] = item
		}
	}
	if len(entries) == 0 || len(entries) > 2048 {
		return realization{}, ErrRealization
	}
	if _, exists := entries[root]; !exists {
		return realization{}, ErrRealization
	}
	result := realization{StoreRoot: root, Closure: []sr.ApplicationClosurePath{}}
	for name, item := range entries {
		hash, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(item.NARHash, "sha256-"))
		if !storePattern.MatchString(name) || (item.Path != "" && item.Path != name) || !strings.HasPrefix(item.NARHash, "sha256-") || err != nil || len(hash) != 32 || item.NARHash != "sha256-"+base64.StdEncoding.EncodeToString(hash) {
			return realization{}, ErrRealization
		}
		for _, reference := range item.References {
			if _, exists := entries[reference]; !exists {
				return realization{}, ErrRealization
			}
		}
		result.Closure = append(result.Closure, sr.ApplicationClosurePath{Path: name, NARHash: item.NARHash})
	}
	sort.Slice(result.Closure, func(i, j int) bool { return result.Closure[i].Path < result.Closure[j].Path })
	// This fixed expression reads one realized immutable store file. Pure
	// expression evaluation does not admit a bare store path without context.
	raw, err = s.command(ctx, "", "--offline", "eval", "--impure", "--raw", "--expr", "builtins.readFile "+strconv.Quote(root+"/share/loom/runtime-executable"))
	if err != nil {
		return realization{}, err
	}
	result.Executable = strings.TrimSpace(string(raw))
	if result.Executable == "" || len(result.Executable) > 4096 || strings.ContainsAny(result.Executable, "\x00\r\n") || filepath.Clean(result.Executable) != result.Executable {
		return realization{}, ErrRealization
	}
	owned := false
	for _, entry := range result.Closure {
		owned = owned || strings.HasPrefix(result.Executable, entry.Path+"/")
	}
	if !owned {
		return realization{}, ErrRealization
	}
	return result, nil
}
