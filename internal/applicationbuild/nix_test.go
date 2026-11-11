package applicationbuild

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestClosureDiscoveryAcceptsNixFormats(t *testing.T) {
	s, _, runner := fixture(t)
	root := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-app"
	runner.pathInfo = []byte(`[{"path":"` + fixtureDependency + `","narHash":"` + fixtureNAR + `"},{"path":"` + root + `","narHash":"` + fixtureNAR + `","references":["` + fixtureDependency + `"]}]`)
	result, err := s.inspect(t.Context(), root)
	if err != nil || len(result.Closure) != 2 || result.Closure[0].Path != root {
		t.Fatalf("array closure: %#v %v", result, err)
	}
	for _, raw := range []string{
		`null`, `{}`, `[]`, `{"bad":"shape"}`,
		`{"` + root + `":{"narHash":"sha256:not-sri"}}`,
		`{"` + root + `":{"narHash":"` + fixtureNAR + `","references":["` + fixtureDependency + `"]}}`,
		`{"` + fixtureDependency + `":{"narHash":"` + fixtureNAR + `"}}`,
		`[{"path":"` + root + `","narHash":"` + fixtureNAR + `"},{"path":"` + root + `","narHash":"` + fixtureNAR + `"}]`,
		`{"` + root + `":{"narHash":"` + fixtureNAR + `","path":"` + fixtureDependency + `"}}`,
	} {
		runner.pathInfo = []byte(raw)
		if _, err := s.inspect(t.Context(), root); !errors.Is(err, ErrRealization) {
			t.Fatalf("bad closure accepted %s: %v", raw, err)
		}
	}
}

func TestBuildRejectsMalformedRealizationAndUnboundedRunner(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `[{}]`, `[{"outputs":{"out":"/tmp/fabricated"}}]`,
		`[{"outputs":{"out":"` + fixtureDependency + `","dev":"` + fixtureDependency + `"}}]`,
	} {
		s, input, _ := fixture(t)
		s.options.Runner = CommandRunnerFunc(func(context.Context, Command) ([]byte, error) { return []byte(raw), nil })
		if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrRealization) || len(s.cache) != 0 {
			t.Fatalf("invalid build accepted: %v", err)
		}
	}
	s, input, _ := fixture(t)
	s.options.Runner = CommandRunnerFunc(func(context.Context, Command) ([]byte, error) {
		return []byte(strings.Repeat("x", maxCommandBytes+1)), nil
	})
	if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrCommand) {
		t.Fatalf("unbounded injected output accepted: %v", err)
	}
}

func TestReuseRefusesChangedNAR(t *testing.T) {
	s, input, runner := fixture(t)
	first, err := s.Build(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	nar := "sha256-BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	runner.pathInfo, _ = json.Marshal(map[string]any{first.Descriptor.StoreRoot: map[string]string{"narHash": nar}})
	if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrRealization) || runner.builds != 1 {
		t.Fatalf("changed NAR reused: %v", err)
	}
}

func TestCommandOutputLimitCannotBypassWrite(t *testing.T) {
	cancelled := false
	output := &boundedOutput{cancel: func() { cancelled = true }}
	_, err := io.Copy(output, strings.NewReader(strings.Repeat("x", maxCommandBytes+1)))
	if !errors.Is(err, ErrCommand) || !cancelled || output.buffer.Len() > maxCommandBytes {
		t.Fatalf("output bound bypassed: %v", err)
	}
}

func TestExecRunnerCancelsProcessGroup(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = (ExecRunner{}).Run(ctx, Command{Path: sh, Args: []string{"-c", "sleep 30 & wait"}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("process group cancellation: %v", err)
	}
}
