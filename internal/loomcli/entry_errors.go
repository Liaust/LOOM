package loomcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/response"
)

// Ownership is per error, not per command or process. Unwrap also preserves
// declaration/transport errors when callers wrap or join the returned error.
type renderedCLIError struct{ err error }

func (e *renderedCLIError) Error() string { return e.err.Error() }
func (e *renderedCLIError) Unwrap() error { return e.err }
func (*renderedCLIError) cliOutputOwned() {}

type cliWriteFailure struct {
	mu  sync.Mutex
	err error
}
type cliWriter struct {
	destination io.Writer
	failure     *cliWriteFailure
}

func (w cliWriter) Write(p []byte) (int, error) {
	n, err := w.destination.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.failure.mu.Lock()
		if w.failure.err == nil {
			w.failure.err = err
		}
		w.failure.mu.Unlock()
	}
	return n, err
}
func (f *cliWriteFailure) get() error { f.mu.Lock(); defer f.mu.Unlock(); return f.err }

// Claim before the first byte, even when the destination fails. No subsequent
// entry fallback may append a second document to a partially delivered response.
func renderOwnedCLIError(cmd *cobra.Command, original error, render func(*cobra.Command) error) error {
	var previous interface{ cliOutputOwned() }
	if errors.As(original, &previous) {
		return original
	}
	owned := &renderedCLIError{err: original}
	writes := &cliWriteFailure{}
	// A rendering-only view preserves inherited writers on the actual command.
	// It shares read-only command metadata and is never executed or mutated.
	view := *cmd
	view.SetOut(cliWriter{cmd.OutOrStdout(), writes})
	view.SetErr(cliWriter{cmd.ErrOrStderr(), writes})
	renderErr := render(&view)
	owned.err = errors.Join(original, renderErr, writes.get())
	return owned
}

type cliCallbackError struct{ error }

func (e *cliCallbackError) Unwrap() error { return e.error }

// Execute is the process entry boundary. Embedded callers can continue to use
// NewRootCommand; the process always receives fresh options and command state.
func Execute(args []string, stdout, stderr io.Writer) error {
	opts := &options{}
	return executeCLIEntry(newRootCommand(opts), opts, args, stdout, stderr)
}

func executeCLIEntry(root *cobra.Command, opts *options, args []string, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	oldOut, oldErr := root.OutOrStdout(), root.ErrOrStderr()
	writes := &cliWriteFailure{}
	root.SetOut(cliWriter{stdout, writes})
	root.SetErr(cliWriter{stderr, writes})
	defer func() { root.SetOut(oldOut); root.SetErr(oldErr) }()
	root.SetArgs(args)
	selected, remaining, findErr := root.Find(args)
	if selected == nil {
		selected = root
	}
	recovered := recoverCLIOutput(selected, args)
	var err error
	usage := false
	if findErr != nil {
		err, usage = findErr, true
	} else if !selected.Runnable() {
		// Cobra handles non-runnable groups as help before validating arguments.
		// Validate their parsed positionals here, without running any hooks.
		selected.InitDefaultHelpFlag()
		err = selected.ParseFlags(remaining)
		if err != nil {
			usage = true
		} else {
			help, _ := selected.Flags().GetBool("help")
			if !help && len(selected.Flags().Args()) > 0 {
				err = errors.New("invalid group arguments")
				usage = true
			} else {
				err = selected.Help()
			}
		}
	} else {
		// Only callback errors are execution failures. Parse/Args/required-flag
		// errors originate in Cobra and remain usage failures. Restore callbacks
		// so reusing a command never accumulates wrappers or handled state.
		restore := wrapCLIEntryCallbacks(selected)
		var called *cobra.Command
		called, err = root.ExecuteC()
		restore()
		if called != nil {
			selected = called
		}
		var callback *cliCallbackError
		usage = err != nil && !errors.As(err, &callback)
	}
	if writeErr := writes.get(); writeErr != nil {
		return &renderedCLIError{err: errors.Join(err, writeErr)}
	}
	if err == nil {
		return nil
	}
	var owned interface{ cliOutputOwned() }
	if errors.As(err, &owned) {
		return err
	}
	return renderCLIEntryFailure(selected, recovered, err, usage)
}

func wrapCLIEntryCallbacks(selected *cobra.Command) func() {
	var restore []func()
	for command := selected; command != nil; command = command.Parent() {
		for _, field := range []*func(*cobra.Command, []string) error{&command.RunE, &command.PreRunE, &command.PostRunE, &command.PersistentPreRunE, &command.PersistentPostRunE} {
			if *field == nil {
				continue
			}
			old := *field
			*field = func(cmd *cobra.Command, args []string) error {
				if err := old(cmd, args); err != nil {
					return &cliCallbackError{err}
				}
				return nil
			}
			restore = append(restore, func() { *field = old })
		}
	}
	return func() {
		for i := len(restore) - 1; i >= 0; i-- {
			restore[i]()
		}
	}
}

type cliRecoveredOutput struct {
	json        bool
	correlation string
}
type cliRecoveryValue struct {
	kind string
	set  func(string)
}

func (v cliRecoveryValue) String() string     { return "" }
func (v cliRecoveryValue) Type() string       { return v.kind }
func (v cliRecoveryValue) Set(s string) error { v.set(s); return nil }

// Let pflag consume values, boolean assignments, short flags and -- using the
// matched command's registered grammar. This read-only pass never writes back
// to command flags and ignores invalid values solely to recover output intent.
func recoverCLIOutput(cmd *cobra.Command, args []string) cliRecoveredOutput {
	var recovered cliRecoveredOutput
	flags := pflag.NewFlagSet("entry-output", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.ParseErrorsAllowlist.UnknownFlags = true
	add := func(f *pflag.Flag) {
		if flags.Lookup(f.Name) != nil {
			return
		}
		copy := *f
		copy.Changed = false
		copy.Value = cliRecoveryValue{f.Value.Type(), func(value string) {
			switch f.Name {
			case "json":
				if enabled, err := strconv.ParseBool(value); err == nil {
					recovered.json = enabled
				}
			case "correlation-id":
				recovered.correlation = value
			}
		}}
		flags.AddFlag(&copy)
	}
	cmd.Flags().VisitAll(add)
	for current := cmd; current != nil; current = current.Parent() {
		current.PersistentFlags().VisitAll(add)
	}
	_ = flags.Parse(args)
	return recovered
}

func cliEntrySafeText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) })
}
func renderCLIEntryFailure(cmd *cobra.Command, output cliRecoveredOutput, original error, usage bool) error {
	path, use := cmd.CommandPath(), cmd.UseLine()
	if !cliEntrySafeText(path, 256) || !cliEntrySafeText(use, 768) {
		path, use = "loom", "loom [command]"
	}
	required := []string{}
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if flag.Annotations[cobra.BashCompOneRequiredFlag] != nil && !flag.Changed {
			required = append(required, "--"+flag.Name)
		}
	})
	sort.Strings(required)
	hint := "Usage: " + use + ". Run " + path + " --help."
	if names := strings.Join(required, ", "); names != "" && cliEntrySafeText(names, 256) {
		hint += " Required flags: " + names + "."
	}
	code, summary := "cli.command_failed", "The command did not complete."
	if usage {
		code, summary = "cli.usage", "Command arguments are invalid."
	}
	cid := strings.TrimSpace(output.correlation)
	redacted := !cliEntrySafeText(output.correlation, 128) || output.correlation != "" && cid == ""
	if cid == "" || redacted {
		cid = correlation.New()
	}
	env := response.ErrorEnvelope{OK: false, Error: response.ErrorBody{Code: code, Summary: summary, Domain: "cli", Target: path, Hint: hint, CorrelationID: cid}, Meta: response.NewMeta(cid)}
	env.Meta.Source = "local-cli"
	if redacted {
		env.Meta.Redactions = []string{"correlation_id_replaced"}
	}
	return renderOwnedCLIError(cmd, original, func(cmd *cobra.Command) error {
		if output.json {
			data, err := json.Marshal(env)
			if err != nil {
				return err
			}
			if len(data)+1 > 4096 {
				return errors.New("CLI entry diagnostic exceeded JSON budget")
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}
		text := fmt.Sprintf("Error: %s: %s\n%s\nCorrelation: %s\n", code, summary, hint, cid)
		if redacted {
			text += "Redaction: correlation_id_replaced\n"
		}
		if len(text) > 2048 || strings.Count(text, "\n") > 8 {
			return errors.New("CLI entry diagnostic exceeded human budget")
		}
		_, err := fmt.Fprint(cmd.ErrOrStderr(), text)
		return err
	})
}
