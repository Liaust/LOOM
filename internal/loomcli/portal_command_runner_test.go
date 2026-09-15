package loomcli

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli/portal"
	"loom.local/loom/internal/loomcli/ui"
)

func TestPortalCommandRunnerForwardsPersistentFlags(t *testing.T) {
	runner, ok := newPortalCommandRunner(&options{
		configFile:    "/tmp/loom.toml",
		socketPath:    "/tmp/loom.sock",
		correlationID: "corr_test",
		noColor:       true,
		theme:         ui.ThemeCoffee,
	}, ui.Mode{}, localclient.New("/resolved.sock")).(portalCommandRunner)
	if !ok {
		t.Fatalf("runner type = %T", runner)
	}
	args := runner.rootArgs(portal.CommandRequest{CorrelationID: "corr_request"}, []string{"health"})
	want := []string{
		"--config", "/tmp/loom.toml",
		"--socket", "/tmp/loom.sock",
		"--correlation-id", "corr_request",
		"--no-color",
		"--theme", ui.ThemeCoffee,
		"--no-interactive", "--no-animation",
		"health",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("rootArgs = %#v, want %#v", args, want)
	}

	resolved := newPortalCommandRunner(&options{}, ui.Mode{}, localclient.New("/resolved.sock")).(portalCommandRunner)
	args = resolved.rootArgs(portal.CommandRequest{}, []string{"status"})
	if containsArgPair(args, "--socket", "/resolved.sock") {
		t.Fatalf("resolved socket should not be forwarded as an explicit CLI override: %#v", args)
	}
	if resolved.client.SocketPath != "/resolved.sock" {
		t.Fatalf("resolved client socket = %q, want /resolved.sock", resolved.client.SocketPath)
	}
}

func TestPortalCommandRunnerBlocksRecursiveCommandsAndCompletesCobraTree(t *testing.T) {
	preview := portal.BuildCommandPreview("$ enter")
	if reason := portalCommandBlockedReason(preview); !strings.Contains(reason, "interactive portal") {
		t.Fatalf("blocked reason = %q", reason)
	}

	runner := portalCommandRunner{}
	capabilityCompletions := runner.cobraCompletions(portal.CommandCompletionRequest{Input: "$ capabilities ", Cursor: len("$ capabilities ")})
	if !runnerHasSuggestion(capabilityCompletions, "list") || !runnerHasSuggestion(capabilityCompletions, "search") {
		t.Fatalf("capabilities cobra completions = %#v", capabilityCompletions)
	}
	flagCompletions := runner.cobraCompletions(portal.CommandCompletionRequest{Input: "$ health --", Cursor: len("$ health --")})
	if !runnerHasSuggestion(flagCompletions, "--json") || !runnerHasSuggestion(flagCompletions, "--no-interactive") {
		t.Fatalf("persistent flag completions = %#v", flagCompletions)
	}
}

func TestPortalCommandRunnerDoesNotAdvertiseRetiredStorageExportMutations(t *testing.T) {
	runner := portalCommandRunner{}
	for _, tc := range []struct {
		input   string
		blocked []string
		allowed string
	}{
		{input: "$ storage export ", blocked: []string{"refresh", "rebuild"}, allowed: "status"},
		{input: "$ storage export r", blocked: []string{"refresh", "rebuild"}},
		{input: "$ storage repair ", blocked: []string{"export"}, allowed: "catalog"},
	} {
		suggestions, err := runner.Complete(context.Background(), portal.CommandCompletionRequest{Input: tc.input, Cursor: len(tc.input)})
		if err != nil {
			t.Fatalf("Complete(%q) returned error: %v", tc.input, err)
		}
		for _, blocked := range tc.blocked {
			if runnerHasSuggestion(suggestions, blocked) {
				t.Fatalf("Complete(%q) advertised retired %q action: %#v", tc.input, blocked, suggestions)
			}
		}
		if tc.allowed != "" && !runnerHasSuggestion(suggestions, tc.allowed) {
			t.Fatalf("Complete(%q) omitted inspection action %q: %#v", tc.input, tc.allowed, suggestions)
		}
	}

	// The compatibility diagnostic remains directly callable/classified for one
	// release; only Portal discovery of the retired mutation is suppressed.
	preview := portal.BuildCommandPreview("$ storage export refresh")
	if preview.Classification != portal.CommandClassInspect {
		t.Fatalf("compatibility classification = %q, want inspect", preview.Classification)
	}
}

func TestPortalCommandRunnerDoesNotAdvertiseRetiredBoxDropzone(t *testing.T) {
	runner := portalCommandRunner{}
	for _, input := range []string{"$ box ", "$ box d"} {
		suggestions, err := runner.Complete(context.Background(), portal.CommandCompletionRequest{Input: input, Cursor: len(input)})
		if err != nil {
			t.Fatalf("Complete(%q) returned error: %v", input, err)
		}
		if runnerHasSuggestion(suggestions, "dropzone") {
			t.Fatalf("Complete(%q) advertised retired Dropzone: %#v", input, suggestions)
		}
	}
}

func containsArgPair(args []string, key string, value string) bool {
	for idx := 0; idx+1 < len(args); idx++ {
		if args[idx] == key && args[idx+1] == value {
			return true
		}
	}
	return false
}

func runnerHasSuggestion(suggestions []portal.CommandSuggestion, value string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Label == value || suggestion.InsertText == value {
			return true
		}
	}
	return false
}
