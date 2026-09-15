package loomcli

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli/portal"
	"loom.local/loom/internal/loomcli/ui"
)

type portalCommandRunner struct {
	configFile    string
	socketPath    string
	correlationID string
	noColor       bool
	theme         string
	mode          ui.Mode
	client        localclient.Client
}

func newPortalCommandRunner(opts *options, mode ui.Mode, resolvedClient localclient.Client) portal.CommandRunner {
	if opts == nil {
		opts = &options{}
	}
	return portalCommandRunner{
		configFile:    opts.configFile,
		socketPath:    opts.socketPath,
		correlationID: opts.correlationID,
		noColor:       opts.noColor,
		theme:         opts.theme,
		mode:          mode,
		client:        resolvedClient,
	}
}

func (r portalCommandRunner) Preview(_ context.Context, request portal.CommandRequest) (portal.CommandPreview, error) {
	return portal.BuildCommandPreview(request.Input), nil
}

func (r portalCommandRunner) Complete(ctx context.Context, request portal.CommandCompletionRequest) ([]portal.CommandSuggestion, error) {
	correlationID := firstNonEmpty(request.CorrelationID, r.correlationID)
	suggestions := []portal.CommandSuggestion{}
	if r.client.BaseURL != "" || r.client.SocketPath != "" {
		suggestions = append(suggestions, portal.CompleteCommand(ctx, r.client, correlationID, request.Input, request.Cursor)...)
	}
	suggestions = append(suggestions, r.cobraCompletions(request)...)
	return filterPortalCompatibilityCompletions(request, uniqueRunnerCommandSuggestions(suggestions)), nil
}

// filterPortalCompatibilityCompletions keeps the one-release compatibility
// commands directly callable without presenting retired export mutations as
// normal Portal actions. Shell completion outside the Portal remains an exact
// reflection of the Cobra tree for operators maintaining older integrations.
func filterPortalCompatibilityCompletions(request portal.CommandCompletionRequest, suggestions []portal.CommandSuggestion) []portal.CommandSuggestion {
	tokens := portal.ParseCommandInput(request.Input).CanonicalTokens
	if len(tokens) < 2 {
		return suggestions
	}
	context := strings.ToLower(strings.Join(tokens[:2], " "))
	blocked := map[string]bool{}
	switch context {
	case "storage export":
		blocked["refresh"] = true
		blocked["rebuild"] = true
	case "storage repair":
		blocked["export"] = true
	default:
		return suggestions
	}
	filtered := make([]portal.CommandSuggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if blocked[strings.ToLower(strings.TrimSpace(suggestion.Label))] || blocked[strings.ToLower(strings.TrimSpace(suggestion.InsertText))] {
			continue
		}
		filtered = append(filtered, suggestion)
	}
	return filtered
}

func (r portalCommandRunner) Execute(ctx context.Context, request portal.CommandRequest) (portal.CommandResult, error) {
	started := time.Now().UTC()
	preview := portal.BuildCommandPreview(request.Input)
	if len(request.CanonicalTokens) > 0 {
		preview.CanonicalTokens = append([]string{}, request.CanonicalTokens...)
		preview.CanonicalCommand = commandStringForRunner(request.CanonicalTokens)
		preview.Classification = request.Classification
	}
	result := portal.CommandResult{
		Input:            request.Input,
		CanonicalTokens:  append([]string{}, preview.CanonicalTokens...),
		CanonicalCommand: preview.CanonicalCommand,
		Classification:   preview.Classification,
		StartedAt:        started,
	}
	if reason := portalCommandBlockedReason(preview); reason != "" {
		result.Status = portal.ActionLifecycleFailed
		result.Summary = "Command is blocked."
		result.ErrorCode = "portal.command_blocked"
		result.ErrorMessage = reason
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(r.rootArgs(request, preview.CanonicalTokens))
	err := cmd.ExecuteContext(ctx)

	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.CompletedAt = time.Now().UTC()
	if err != nil {
		result.Status = portal.ActionLifecycleFailed
		result.Summary = "Command failed."
		result.ErrorCode = "portal.command_failed"
		result.ErrorMessage = err.Error()
		return result, nil
	}
	result.Status = portal.ActionLifecycleSucceeded
	result.Summary = "Command completed."
	if preview.Classification == portal.CommandClassSafeRun || preview.Classification == portal.CommandClassSensitive {
		result.RefreshScreen = request.CurrentScreen
	}
	return result, nil
}

func (r portalCommandRunner) rootArgs(request portal.CommandRequest, tokens []string) []string {
	args := []string{}
	if r.configFile != "" {
		args = append(args, "--config", r.configFile)
	}
	if r.socketPath != "" {
		args = append(args, "--socket", r.socketPath)
	}
	correlationID := firstNonEmpty(request.CorrelationID, r.correlationID)
	if correlationID != "" {
		args = append(args, "--correlation-id", correlationID)
	}
	if r.noColor || !r.mode.CanColor() {
		args = append(args, "--no-color")
	}
	if r.theme != "" {
		args = append(args, "--theme", r.theme)
	}
	args = append(args, "--no-interactive", "--no-animation")
	args = append(args, tokens...)
	return args
}

func commandStringForRunner(tokens []string) string {
	if len(tokens) == 0 {
		return "loom"
	}
	return "loom " + strings.Join(tokens, " ")
}

func portalCommandBlockedReason(preview portal.CommandPreview) string {
	if preview.ParseError != "" {
		return preview.ParseError
	}
	if preview.BlockedReason != "" {
		return preview.BlockedReason
	}
	if preview.Classification == portal.CommandClassDangerous {
		return "Dangerous commands are blocked in portal command mode for this slice."
	}
	if preview.Classification == portal.CommandClassBlocked {
		return "This command is blocked in portal command mode."
	}
	return ""
}

func (r portalCommandRunner) cobraCompletions(request portal.CommandCompletionRequest) []portal.CommandSuggestion {
	prefix, start, end := runnerCurrentCommandToken(request.Input, request.Cursor)
	parse := portal.ParseCommandInput(request.Input)
	if parse.Error != "" {
		return nil
	}
	tokens := append([]string{}, parse.CanonicalTokens...)
	if len(tokens) > 0 && prefix != "" && !runnerInputEndsWithSpace(request.Input) && strings.EqualFold(tokens[len(tokens)-1], prefix) {
		tokens = tokens[:len(tokens)-1]
	}
	cmd := NewRootCommand()
	current := cmd
	for _, token := range tokens {
		next := runnerFindChildCommand(current, token)
		if next == nil {
			break
		}
		current = next
	}
	if strings.HasPrefix(prefix, "-") {
		return runnerFlagCompletions(current, start, end)
	}
	return runnerSubcommandCompletions(current, start, end)
}

func runnerSubcommandCompletions(cmd *cobra.Command, start int, end int) []portal.CommandSuggestion {
	result := []portal.CommandSuggestion{}
	for _, child := range cmd.Commands() {
		if child.Hidden {
			continue
		}
		name := child.Name()
		result = append(result, portal.CommandSuggestion{
			Label:            name,
			Kind:             "subcommand",
			Description:      firstNonEmpty(child.Short, "LOOM subcommand"),
			InsertText:       name,
			ReplacementStart: start,
			ReplacementEnd:   end,
			CanonicalPreview: "loom " + name,
			Score:            85,
		})
	}
	return result
}

func runnerFlagCompletions(cmd *cobra.Command, start int, end int) []portal.CommandSuggestion {
	result := []portal.CommandSuggestion{}
	addFlags := func(flags *pflag.FlagSet) {
		if flags == nil {
			return
		}
		flags.VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			name := "--" + flag.Name
			result = append(result, portal.CommandSuggestion{
				Label:            name,
				Kind:             "flag",
				Description:      firstNonEmpty(flag.Usage, "CLI flag"),
				InsertText:       name,
				ReplacementStart: start,
				ReplacementEnd:   end,
				CanonicalPreview: name,
				Score:            80,
			})
		})
	}
	addFlags(cmd.LocalFlags())
	addFlags(cmd.InheritedFlags())
	return result
}

func runnerFindChildCommand(cmd *cobra.Command, token string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Hidden {
			continue
		}
		if child.Name() == token {
			return child
		}
		for _, alias := range child.Aliases {
			if alias == token {
				return child
			}
		}
	}
	return nil
}

func runnerCurrentCommandToken(input string, cursor int) (string, int, int) {
	if cursor <= 0 || cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 {
		ch := input[start-1]
		if ch == ' ' || ch == '\t' || ch == '$' {
			break
		}
		start--
	}
	end := cursor
	for end < len(input) {
		ch := input[end]
		if ch == ' ' || ch == '\t' {
			break
		}
		end++
	}
	return input[start:cursor], start, end
}

func runnerInputEndsWithSpace(input string) bool {
	trimmed := strings.TrimRight(input, " \t")
	return len(trimmed) != len(input)
}

func uniqueRunnerCommandSuggestions(suggestions []portal.CommandSuggestion) []portal.CommandSuggestion {
	seen := map[string]bool{}
	result := []portal.CommandSuggestion{}
	for _, suggestion := range suggestions {
		key := suggestion.Kind + ":" + suggestion.InsertText
		if strings.TrimSpace(suggestion.InsertText) == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, suggestion)
	}
	if len(result) > 12 {
		return result[:12]
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
