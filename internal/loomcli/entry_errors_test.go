package loomcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/localclient"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/version"
)

var cliEntryBinary string

// Build once per test process, including count/race runs. Never skip this gate.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "loom-cli-entry-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cliEntryBinary = filepath.Join(dir, "loom")
	build := exec.Command("go", "build", "-mod=readonly", "-buildvcs=false", "-ldflags", "-X loom.local/loom/internal/version.Version=entry-fixture -X loom.local/loom/internal/version.Commit=fixture-commit -X loom.local/loom/internal/version.BuildDate=fixture-date", "-o", cliEntryBinary, "./cmd/loom")
	build.Dir = filepath.Join("..", "..")
	build.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "GOPROXY=off", "GOSUMDB=off", "GOWORK=off"}
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "entry fixture build: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func cliEntryProcess(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	root := t.TempDir()
	guard := filepath.Join(root, "config-directory")
	if err := os.Mkdir(guard, 0700); err != nil {
		t.Fatal(err)
	}
	all := append([]string{"--config", guard, "--socket", filepath.Join(root, "absent.sock"), "--correlation-id", "entry-fixture"}, args...)
	cmd := exec.Command(cliEntryBinary, all...)
	cmd.Dir = root
	cmd.Env = []string{"HOME=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + root, "PATH=/usr/bin:/bin", "NO_COLOR=1", "TERM=dumb"}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	status := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = exit.ExitCode()
	}
	return out.String(), stderr.String(), status
}

func cliEntryEnvelope(t *testing.T, out string) response.ErrorEnvelope {
	t.Helper()
	if len(out) > 4096 || !utf8.ValidString(out) {
		t.Fatalf("fallback JSON bound/encoding: %d", len(out))
	}
	d := json.NewDecoder(strings.NewReader(out))
	var envelope response.ErrorEnvelope
	if err := d.Decode(&envelope); err != nil {
		t.Fatalf("JSON error: %v %q", err, out)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		t.Fatalf("multiple output documents: %q", out)
	}
	if envelope.OK || envelope.Error.Domain != "cli" || !strings.Contains(envelope.Error.Hint, "--help") || !strings.Contains(envelope.Error.Hint, "Usage:") {
		t.Fatalf("unhelpful fallback: %s", out)
	}
	if envelope.Meta.Source != "local-cli" {
		t.Fatalf("entry error claims a remote source: %s", out)
	}
	return envelope
}

func TestCLIEntryProcessMatrix(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code string
	}{
		{"unknown", []string{"entry-unknown"}, "cli.usage"},
		{"nested", []string{"project", "entry-unknown"}, "cli.usage"},
		{"alias", []string{"projects", "entry-unknown"}, "cli.usage"},
		{"deep", []string{"storage", "mount-policy", "entry-unknown"}, "cli.usage"},
		{"root_flag", []string{"--entry-unknown"}, "cli.usage"},
		{"nested_flag", []string{"version", "--entry-unknown"}, "cli.usage"},
		{"context_arg", []string{"project", "context"}, "cli.usage"},
		{"plan_arg", []string{"project", "plan"}, "cli.usage"},
		{"operation_arg", []string{"project", "operation"}, "cli.usage"},
		{"required", []string{"project", "apply", "fixture"}, "cli.usage"},
		{"missing_value", []string{"version", "--config"}, "cli.usage"},
		{"invalid_bool", []string{"version", "--json=invalid"}, "cli.usage"},
		{"extra", []string{"project", "context", "fixture", "extra"}, "cli.usage"},
		{"nested_version", []string{"project", "apply", "--version"}, "cli.usage"},
		{"prefix_nested_version", []string{"--version", "project", "apply", "fixture"}, "cli.usage"},
		{"guard", []string{"storage", "mount-policy", "status"}, "cli.command_failed"},
	}
	for _, mode := range []string{"default", "plain", "json"} {
		for _, tc := range cases {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				args := append([]string(nil), tc.args...)
				if mode != "default" {
					args = append([]string{"--" + mode}, args...)
				}
				out, stderr, status := cliEntryProcess(t, args...)
				if status == 0 {
					t.Fatalf("invalid input succeeded: %s %s", out, stderr)
				}
				if mode == "json" {
					env := cliEntryEnvelope(t, out)
					if stderr != "" || env.Error.Code != tc.code || env.Meta.CorrelationID != "entry-fixture" {
						t.Fatalf("wrong fallback: %s %s", out, stderr)
					}
				} else if out != "" || !strings.Contains(stderr, tc.code) || !strings.Contains(stderr, "Usage:") || len(stderr) > 2048 || strings.Count(stderr, "\n") > 8 {
					t.Fatalf("human error: %q %q", out, stderr)
				}
			})
		}
	}
	for _, args := range [][]string{nil, {"project"}, {"projects"}, {"storage", "mount-policy"}, {"--help"}, {"project", "context", "--help"}, {"--json", "project", "--help"}, {"completion", "bash"}} {
		out, stderr, status := cliEntryProcess(t, args...)
		if status != 0 || out == "" || stderr != "" {
			t.Fatalf("help/completion changed %v: %d %s %s", args, status, out, stderr)
		}
	}
}

func TestRootVersionProcessParity(t *testing.T) {
	for _, mode := range []string{"default", "plain", "json"} {
		var flags []string
		if mode != "default" {
			flags = []string{"--" + mode}
		}
		out, stderr, status := cliEntryProcess(t, append(flags, "version")...)
		if status != 0 || stderr != "" {
			t.Fatalf("version: %d %s", status, stderr)
		}
		for _, args := range [][]string{append(append([]string(nil), flags...), "--version"), append([]string{"--version"}, flags...)} {
			got, diagnostic, exit := cliEntryProcess(t, args...)
			if exit != 0 || diagnostic != "" || got != out {
				t.Fatalf("version flag mismatch: %v: %d %q %q; want %q", args, exit, got, diagnostic, out)
			}
		}
		if mode == "json" {
			var info version.Info
			if json.Unmarshal([]byte(out), &info) != nil || info != (version.Info{Version: "entry-fixture", Commit: "fixture-commit", BuildDate: "fixture-date"}) {
				t.Fatalf("build metadata lost: %s", out)
			}
		}
	}
	for _, name := range []string{"version", "--version"} {
		out, stderr, status := cliEntryProcess(t, "--json", "--plain", name)
		var env response.ErrorEnvelope
		if status == 0 || stderr != "" || json.Unmarshal([]byte(out), &env) != nil || env.Error.Code != "output.invalid" {
			t.Fatalf("output mode conflict: %d %s %s", status, out, stderr)
		}
	}
}

func TestCLIEntryOutputRecoveryAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		args []string
		json bool
	}{
		{[]string{"entry-unknown", "--json"}, true},
		{[]string{"--entry-unknown", "--json"}, true},
		{[]string{"--json=true", "--json=false", "entry-unknown"}, false},
		{[]string{"--json=false", "--json=true", "entry-unknown"}, true},
		{[]string{"--config", "--json", "entry-unknown"}, false},
		{[]string{"--", "entry-unknown", "--json"}, false},
		{[]string{"--json", "--", "entry-unknown"}, true},
		{[]string{"project", "apply", "fixture", "--approval", "--json"}, false},
	} {
		out, stderr, status := cliEntryProcess(t, tc.args...)
		if status == 0 {
			t.Fatalf("succeeded %v", tc.args)
		}
		if tc.json {
			cliEntryEnvelope(t, out)
			if stderr != "" {
				t.Fatal(stderr)
			}
		} else if out != "" || !strings.Contains(stderr, "cli.usage") {
			t.Fatalf("wrong output mode %v: %q %q", tc.args, out, stderr)
		}
	}
	for _, token := range []string{"quote'fixture", "bad\nline\x1b[31m", "bad\u202eBIDI", strings.Repeat("界", 3000), "postgres://fixture:FAKE_PASSWORD@invalid.test/db?token=FAKE_TOKEN", "SELECT FAKE_SECRET FROM fixture"} {
		for _, jsonMode := range []bool{false, true} {
			args := []string{token}
			if jsonMode {
				args = append(args, "--json")
			}
			out, stderr, status := cliEntryProcess(t, args...)
			if status == 0 || strings.Contains(out+stderr, token) || strings.ContainsAny(out+stderr, "\x1b\u202e") {
				t.Fatalf("unsafe diagnostic: %d %q %q", status, out, stderr)
			}
			if jsonMode {
				cliEntryEnvelope(t, out)
			} else if len(stderr) > 2048 || strings.Count(stderr, "\n") > 8 || !utf8.ValidString(stderr) {
				t.Fatal("human limit")
			}
		}
	}
	for _, cid := range []string{"bad\ncorrelation", strings.Repeat("x", 129), "bad\u202ecorrelation"} {
		out, stderr, status := cliEntryProcess(t, "--correlation-id", cid, "--json", "entry-unknown")
		env := cliEntryEnvelope(t, out)
		if status == 0 || stderr != "" || env.Meta.CorrelationID == cid || !reflect.DeepEqual(env.Meta.Redactions, []string{"correlation_id_replaced"}) {
			t.Fatalf("correlation not replaced: %s %s", out, stderr)
		}
	}
}

func TestRenderedErrorClaimsOwnershipAndRetainsTypes(t *testing.T) {
	for _, kind := range []string{"standard", "partial", "no-result"} {
		for _, machine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", kind, machine), func(t *testing.T) {
				cmd := &cobra.Command{}
				var out, stderr bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&stderr)
				request := &localclient.RequestError{Envelope: response.ErrorEnvelope{Error: response.ErrorBody{Code: "declaration.unauthorized", CorrelationID: "server"}, Meta: response.Meta{CorrelationID: "server", IdempotencyKey: "original"}}}
				failure := &localclient.DeclarationRequestError{RequestError: request, Detail: pc.DeclarationError{Code: pc.DeclarationUnauthorized, CauseCode: "permission_revoked", CompletedEffects: []string{"retained"}}}
				if kind == "partial" {
					failure.Result = &pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "durable", State: pc.DeclarationOperationPartial, Actions: []pc.DeclarationActionResult{{EffectRef: "retained"}}}
				}
				var got error
				if kind == "standard" {
					got = renderError(cmd, &options{jsonOutput: machine}, "client", fmt.Errorf("wrapped: %w", request))
				} else {
					got = renderDeclarationFailure(cmd, &options{jsonOutput: machine}, "client", fmt.Errorf("wrapped: %w", failure), nil)
				}
				var owned interface{ cliOutputOwned() }
				if !errors.As(got, &owned) {
					t.Fatalf("renderer did not claim output: %T", got)
				}
				var typed *localclient.RequestError
				if !errors.As(got, &typed) || typed != request {
					t.Fatalf("original type lost: %T", got)
				}
			})
		}
	}
}

func entryFailureFixture() *localclient.DeclarationRequestError {
	return &localclient.DeclarationRequestError{RequestError: &localclient.RequestError{StatusCode: 403, Envelope: response.ErrorEnvelope{Error: response.ErrorBody{Code: "declaration.unauthorized", Summary: "Permission prerequisite", Domain: "projects", Target: "declaration", Hint: "Inspect prerequisites", CorrelationID: "server-correlation"}, Meta: response.Meta{CorrelationID: "server-correlation", IdempotencyKey: "original-key", Source: "fixture", Freshness: "synthetic"}}}, Detail: pc.DeclarationError{Code: pc.DeclarationUnauthorized, CauseCode: "permission_revoked", CompletedEffects: []string{"retained-effect"}}}
}

func TestCLIEntryPreservesRenderedPayloads(t *testing.T) {
	for _, kind := range []string{"partial", "no-result", "request", "raw", "progress", "joined"} {
		for _, machine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", kind, machine), func(t *testing.T) {
				opts := &options{jsonOutput: machine}
				failure := entryFailureFixture()
				if kind == "partial" {
					failure.Result = &pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "durable-operation", PlanID: "reviewed-plan", State: pc.DeclarationOperationPartial, Actions: []pc.DeclarationActionResult{{EffectRef: "retained-effect"}}, Errors: []pc.DeclarationError{failure.Detail}}
				}
				original := errors.New("raw FAKE_SECRET SQL private/path")
				root := &cobra.Command{Use: "fixture", SilenceErrors: true, SilenceUsage: true}
				root.Flags().BoolVar(&opts.jsonOutput, "json", false, "")
				root.RunE = func(cmd *cobra.Command, args []string) error {
					switch kind {
					case "raw":
						return original
					case "progress":
						stream := cmd.OutOrStdout()
						if machine {
							stream = cmd.ErrOrStderr()
						}
						fmt.Fprintln(stream, "Progress: fixture")
						return original
					case "request":
						return renderError(cmd, opts, "client", fmt.Errorf("wrapped: %w", failure.RequestError))
					default:
						err := renderDeclarationFailure(cmd, opts, "client", fmt.Errorf("wrapped: %w", failure), nil)
						if kind == "joined" {
							return fmt.Errorf("outer: %w", errors.Join(err, original))
						}
						return err
					}
				}
				var out, stderr bytes.Buffer
				args := []string{}
				if machine {
					args = []string{"--json"}
				}
				err := executeCLIEntry(root, opts, args, &out, &stderr)
				if err == nil {
					t.Fatal("exit status lost")
				}
				if kind == "raw" || kind == "progress" {
					if !errors.Is(err, original) || strings.Contains(out.String()+stderr.String(), "FAKE_SECRET") {
						t.Fatal("raw error identity/redaction")
					}
					if machine {
						env := cliEntryEnvelope(t, out.String())
						if env.Error.Code != "cli.command_failed" {
							t.Fatal(env)
						}
					} else if strings.Count(stderr.String(), "Error:") != 1 {
						t.Fatal(stderr.String())
					}
					if kind == "progress" && !strings.Contains(out.String()+stderr.String(), "Progress:") {
						t.Fatal("progress disappeared")
					}
					return
				}
				var request *localclient.RequestError
				if !errors.As(err, &request) || request != failure.RequestError {
					t.Fatal("transport type/identity lost")
				}
				if kind != "request" {
					var typed *localclient.DeclarationRequestError
					if !errors.As(err, &typed) || typed != failure {
						t.Fatal("declaration type/identity lost")
					}
				}
				if machine {
					var expected bytes.Buffer
					switch kind {
					case "partial":
						_ = json.NewEncoder(&expected).Encode(failure.Result)
					case "request":
						_ = json.NewEncoder(&expected).Encode(failure.Envelope)
					default:
						_ = json.NewEncoder(&expected).Encode(struct {
							response.ErrorEnvelope
							Detail pc.DeclarationError `json:"declaration_error"`
						}{failure.Envelope, failure.Detail})
					}
					if out.String() != expected.String() || stderr.Len() != 0 {
						t.Fatalf("wire output changed/duplicated: %q %q", out.String(), stderr.String())
					}
				} else if strings.Count(stderr.String(), "Error:") != 1 || !strings.Contains(stderr.String(), "original-key") || !strings.Contains(stderr.String(), "server-correlation") {
					t.Fatalf("human metadata duplicated/lost: %s", stderr.String())
				}
			})
		}
	}
}

type cliFailOnceWriter struct {
	calls int
	limit int
	err   error
	bytes.Buffer
}

func (w *cliFailOnceWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		n := min(w.limit, len(p))
		_, _ = w.Buffer.Write(p[:n])
		return n, w.err
	}
	return w.Buffer.Write(p)
}

func TestRenderedErrorWriteFailureOwnsOutput(t *testing.T) {
	for _, kind := range []string{"partial", "no-result", "request", "fallback", "ignored-write"} {
		for _, limit := range []int{0, 17} {
			t.Run(fmt.Sprintf("%s/%d", kind, limit), func(t *testing.T) {
				writeErr := errors.New("fixture write failure")
				writer := &cliFailOnceWriter{limit: limit, err: writeErr}
				opts := &options{jsonOutput: true}
				failure := entryFailureFixture()
				if kind == "partial" {
					failure.Result = &pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "durable", State: pc.DeclarationOperationPartial}
				}
				original := errors.New("raw fixture error")
				root := &cobra.Command{Use: "fixture", SilenceErrors: true, SilenceUsage: true}
				root.Flags().BoolVar(&opts.jsonOutput, "json", false, "")
				root.RunE = func(cmd *cobra.Command, args []string) error {
					switch kind {
					case "fallback":
						return original
					case "ignored-write":
						fmt.Fprintln(cmd.OutOrStdout(), "synthetic partial output")
						return nil
					case "request":
						return renderError(cmd, opts, "client", failure.RequestError)
					default:
						return renderDeclarationFailure(cmd, opts, "client", failure, nil)
					}
				}
				var stderr bytes.Buffer
				err := executeCLIEntry(root, opts, []string{"--json"}, writer, &stderr)
				var owned interface{ cliOutputOwned() }
				if !errors.As(err, &owned) || !errors.Is(err, writeErr) || writer.calls != 1 || stderr.Len() != 0 {
					t.Fatalf("write failure lost/second response: err=%v calls=%d stderr=%q", err, writer.calls, stderr.String())
				}
				if kind == "partial" || kind == "no-result" {
					var typed *localclient.DeclarationRequestError
					if !errors.As(err, &typed) || typed != failure {
						t.Fatal("failure discarded typed result")
					}
				}
				if kind == "fallback" && !errors.Is(err, original) {
					t.Fatal("fallback lost original failure")
				}
			})
		}
	}
	for _, args := range [][]string{{"entry-unknown"}, {"--help"}, {"--version"}} {
		writeErr := errors.New("stream failed")
		out, stderr := &cliFailOnceWriter{err: writeErr}, &cliFailOnceWriter{err: writeErr}
		if err := Execute(args, out, stderr); !errors.Is(err, writeErr) {
			t.Fatalf("stream failure became success: %v %v", args, err)
		}
	}
}

func TestCLIEntryReusedAndConcurrentRoots(t *testing.T) {
	opts := &options{}
	root := &cobra.Command{Use: "fixture", SilenceErrors: true, SilenceUsage: true}
	first := true
	original := errors.New("fixture error")
	child := &cobra.Command{Use: "child", RunE: func(cmd *cobra.Command, args []string) error {
		if first {
			first = false
			return renderError(cmd, opts, "first", original)
		}
		return original
	}}
	root.AddCommand(child)
	for i := 0; i < 2; i++ {
		var out, stderr bytes.Buffer
		if err := executeCLIEntry(root, opts, []string{"child"}, &out, &stderr); !errors.Is(err, original) {
			t.Fatal(err)
		}
		if strings.Count(stderr.String(), "Error:") != 1 {
			t.Fatalf("previous root/writer ownership leaked: %q", stderr.String())
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, stderr bytes.Buffer
			err := Execute([]string{"--json", "entry-unknown"}, &out, &stderr)
			if err == nil || stderr.Len() != 0 || !strings.Contains(out.String(), "cli.usage") {
				t.Errorf("concurrent ownership/mode leak: %v %q %q", err, out.String(), stderr.String())
			}
		}()
	}
	wg.Wait()
}

func TestRenderedErrorWrappingDoesNotReprint(t *testing.T) {
	cmd := &cobra.Command{}
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	failure := entryFailureFixture()
	first := renderDeclarationFailure(cmd, &options{jsonOutput: true}, "client", failure, nil)
	before := out.String()
	joined := fmt.Errorf("outer: %w", errors.Join(first, errors.New("additional failure")))
	second := renderError(cmd, &options{jsonOutput: true}, "client", joined)
	if out.String() != before || stderr.Len() != 0 || !errors.Is(second, first) {
		t.Fatal("wrapped ownership rendered twice")
	}
}
