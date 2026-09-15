package portal

import (
	"context"
	"strings"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/workers"
)

func CompleteCommand(ctx context.Context, client Client, correlationID string, input string, cursor int) []CommandSuggestion {
	if cursor <= 0 || cursor > len(input) {
		cursor = len(input)
	}
	prefix, start, end := currentCommandToken(input, cursor)
	stages := staticCommandSuggestionStages(input, prefix, start, end)
	if client != nil {
		if dynamic := dynamicCommandSuggestions(ctx, client, correlationID, input, prefix, start, end); len(dynamic) > 0 {
			stages = append([][]CommandSuggestion{dynamic}, stages...)
		}
	}
	return rankCommandSuggestionStages(prefix, 12, stages...)
}

func staticCommandSuggestions(input string, prefix string, start int, end int) []CommandSuggestion {
	stages := staticCommandSuggestionStages(input, prefix, start, end)
	result := []CommandSuggestion{}
	for _, stage := range stages {
		result = append(result, stage...)
	}
	return result
}

func staticCommandSuggestionStages(input string, prefix string, start int, end int) [][]CommandSuggestion {
	if strings.HasPrefix(prefix, "-") {
		return [][]CommandSuggestion{commandFlagSuggestions(input, prefix, start, end)}
	}
	contextTokens := commandCompletionContextTokens(input, prefix)
	if len(contextTokens) == 0 {
		roots := commandRootSuggestions(start, end)
		aliases := commandRootAliasSuggestions(start, end)
		if commandAliasQueryLooksNatural(prefix) && !rootHasStrongCommandPrefix(prefix) {
			return [][]CommandSuggestion{aliases, roots}
		}
		return [][]CommandSuggestion{roots, aliases}
	}

	stages := [][]CommandSuggestion{}
	if aliases := commandContextAliasSuggestions(contextTokens, start, end); len(aliases) > 0 {
		stages = append(stages, aliases)
	}
	if subcommands := commandSubcommandSuggestions(contextTokens, start, end); len(subcommands) > 0 {
		stages = append(stages, subcommands)
	}
	if len(stages) == 0 {
		stages = append(stages, commandFlagSuggestions(input, prefix, start, end))
	}
	return stages
}

func commandContextAliasSuggestions(contextTokens []string, start int, end int) []CommandSuggestion {
	key := strings.Join(lowerTokens(contextTokens), " ")
	switch key {
	case "add":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("add main facet", "alias", "Add a facet to a project on the configured main backend.", "main facet ", start, end, "loom project facet add <project-ref> <facet> --backend", 111),
			commandSuggestionWithCanonical("add facet", "alias", "Add a facet to a backend project.", "facet ", start, end, "loom project facet add <project-ref> <facet> --backend", 109),
			commandSuggestionWithCanonical("add project facet", "alias", "Add a facet to a backend project.", "project facet ", start, end, "loom project facet add <project-ref> <facet> --backend", 105),
		}
	case "add main":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("add main facet", "alias", "Add a facet to a project on the configured main backend.", "facet ", start, end, "loom project facet add <project-ref> <facet> --backend", 111),
		}
	case "add project":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("add project facet", "alias", "Add a facet to a backend project.", "facet ", start, end, "loom project facet add <project-ref> <facet> --backend", 105),
		}
	case "validate":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("validate main project", "alias", "Validate a project contract on the configured main backend.", "main project ", start, end, "loom project validate <project-ref> --backend", 108),
		}
	case "validate main":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("validate main project", "alias", "Validate a project contract on the configured main backend.", "project ", start, end, "loom project validate <project-ref> --backend", 108),
		}
	case "register":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("register main project", "alias", "Register a project contract from the configured main backend.", "main project ", start, end, "loom project register <project-ref> --backend", 108),
		}
	case "register main":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("register main project", "alias", "Register a project contract from the configured main backend.", "project ", start, end, "loom project register <project-ref> --backend", 108),
		}
	case "archive":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("archive project", "alias", "Plan archive for a project without changing backend state.", "project ", start, end, "loom project archive <project-ref> --dry-run", 106),
		}
	case "inspect":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("inspect project archive", "alias", "Inspect retained archive state for a project.", "project archive ", start, end, "loom project archive inspect <project-ref>", 104),
		}
	case "inspect project":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("inspect project archive", "alias", "Inspect retained archive state for a project.", "archive ", start, end, "loom project archive inspect <project-ref>", 104),
		}
	case "restore":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("restore project archive", "alias", "Plan a dry-run restore from a project archive.", "project archive ", start, end, "loom project archive restore <project-ref> --dry-run", 104),
		}
	case "restore project":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("restore project archive", "alias", "Plan a dry-run restore from a project archive.", "archive ", start, end, "loom project archive restore <project-ref> --dry-run", 104),
		}
	case "create":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("create main project", "alias", "Create and register a project on the configured main backend.", "main project ", start, end, "loom project scaffold <name> --backend --owner-node main --register", 108),
			commandSuggestionWithCanonical("create project", "alias", "Create a local project scaffold.", "project ", start, end, "loom project scaffold <name>", 104),
		}
	case "create main":
		return []CommandSuggestion{
			commandSuggestionWithCanonical("create main project", "alias", "Create and register a project on the configured main backend.", "project ", start, end, "loom project scaffold <name> --backend --owner-node main --register", 108),
		}
	}
	return nil
}

type commandRootDefinition struct {
	Label       string
	Description string
	Score       int
}

func commandRootSuggestions(start int, end int) []CommandSuggestion {
	definitions := []commandRootDefinition{
		{Label: "status", Description: "Show node status", Score: 120},
		{Label: "health", Description: "Show node health", Score: 118},
		{Label: "storage", Description: "Browse and manage LOOM Main storage", Score: 116},
		{Label: "lane", Description: "Send LOOM Lane files to main", Score: 114},
		{Label: "box", Description: "Inspect the local LOOM Box", Score: 112},
		{Label: "cloud", Description: "Inspect cloud backup status", Score: 110},
		{Label: "project", Description: "Create and manage project contracts", Score: 108},
		{Label: "capabilities", Description: "List and search callable capabilities", Score: 106},
		{Label: "capability", Description: "Inspect or call one capability", Score: 104},
		{Label: "providers", Description: "List capability providers", Score: 102},
		{Label: "provider", Description: "Inspect one capability provider", Score: 100},
		{Label: "database", Description: "Inspect database and object-store state", Score: 98},
		{Label: "object", Description: "Inspect indexed objects", Score: 96},
		{Label: "search", Description: "Search indexed objects", Score: 94},
		{Label: "sync", Description: "Inspect sync state", Score: 92},
		{Label: "indexes", Description: "Inspect or repair indexing work", Score: 90},
		{Label: "workers", Description: "List background workers", Score: 88},
		{Label: "worker", Description: "Inspect or run one worker", Score: 86},
		{Label: "automations", Description: "Inspect automation state", Score: 84},
		{Label: "automation", Description: "Inspect one automation", Score: 82},
		{Label: "schedules", Description: "List schedules", Score: 80},
		{Label: "schedule", Description: "Inspect or fire one schedule", Score: 78},
		{Label: "jobs", Description: "Inspect job queues and failures", Score: 76},
		{Label: "job", Description: "Inspect one job", Score: 74},
		{Label: "node", Description: "List or inspect nodes", Score: 72},
		{Label: "watched-roots", Description: "Inspect folder watching state", Score: 70},
		{Label: "maintenance", Description: "Inspect maintenance workers", Score: 68},
		{Label: "backup", Description: "Inspect main-backed backup coverage", Score: 66},
		{Label: "support", Description: "Create redacted diagnostic support bundles", Score: 64},
		{Label: "update", Description: "Inspect update state", Score: 50},
		{Label: "setup", Description: "Inspect local setup state", Score: 48},
	}
	result := make([]CommandSuggestion, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, commandSuggestionWithScore(definition.Label, "command", definition.Description, definition.Label, start, end, definition.Score))
	}
	return result
}

type commandAliasDefinition struct {
	Label            string
	InsertText       string
	CanonicalPreview string
	Score            int
}

func commandRootAliasSuggestions(start int, end int) []CommandSuggestion {
	definitions := []commandAliasDefinition{
		{Label: "add main facet", InsertText: "add main facet ", CanonicalPreview: "loom project facet add <project-ref> <facet> --backend", Score: 116},
		{Label: "add facet", InsertText: "add facet ", CanonicalPreview: "loom project facet add <project-ref> <facet> --backend", Score: 114},
		{Label: "validate main project", InsertText: "validate main project ", CanonicalPreview: "loom project validate <project-ref> --backend", Score: 113},
		{Label: "register main project", InsertText: "register main project ", CanonicalPreview: "loom project register <project-ref> --backend", Score: 112},
		{Label: "archive project", InsertText: "archive project ", CanonicalPreview: "loom project archive <project-ref> --dry-run", Score: 111},
		{Label: "inspect project archive", InsertText: "inspect project archive ", CanonicalPreview: "loom project archive inspect <project-ref>", Score: 110},
		{Label: "restore project archive", InsertText: "restore project archive ", CanonicalPreview: "loom project archive restore <project-ref> --dry-run", Score: 109},
		{Label: "push lane", InsertText: "push lane", CanonicalPreview: "loom lane send", Score: 112},
		{Label: "mount storage", InsertText: "mount storage", CanonicalPreview: "loom storage mount-policy enable --local --repair", Score: 110},
		{Label: "cloud status", InsertText: "cloud status", CanonicalPreview: "loom cloud status", Score: 108},
		{Label: "create support bundle", InsertText: "create support bundle ", CanonicalPreview: "loom support bundle create", Score: 107},
		{Label: "create project", InsertText: "create project ", CanonicalPreview: "loom project scaffold <name>", Score: 106},
		{Label: "create main project", InsertText: "create main project ", CanonicalPreview: "loom project scaffold <name> --backend --owner-node main --register", Score: 105},
		{Label: "list capabilities", InsertText: "list capabilities", CanonicalPreview: "loom capabilities list", Score: 92},
		{Label: "list providers", InsertText: "list providers", CanonicalPreview: "loom providers list", Score: 90},
		{Label: "list workers", InsertText: "list workers", CanonicalPreview: "loom workers list", Score: 88},
		{Label: "list objects", InsertText: "list objects", CanonicalPreview: "loom object list", Score: 86},
		{Label: "list schedules", InsertText: "list schedules", CanonicalPreview: "loom schedules list", Score: 84},
		{Label: "list jobs", InsertText: "list jobs", CanonicalPreview: "loom jobs list", Score: 82},
		{Label: "list nodes", InsertText: "list nodes", CanonicalPreview: "loom node list", Score: 80},
		{Label: "show status", InsertText: "show status", CanonicalPreview: "loom status", Score: 78},
		{Label: "show health", InsertText: "show health", CanonicalPreview: "loom health", Score: 76},
		{Label: "show sync status", InsertText: "show sync status ", CanonicalPreview: "loom sync status --node <node>", Score: 74},
		{Label: "inspect object", InsertText: "inspect object ", CanonicalPreview: "loom object inspect <object-ref>", Score: 72},
		{Label: "inspect worker", InsertText: "inspect worker ", CanonicalPreview: "loom worker inspect <worker-ref>", Score: 70},
		{Label: "inspect capability", InsertText: "inspect capability ", CanonicalPreview: "loom capability inspect <capability-ref>", Score: 68},
		{Label: "inspect provider", InsertText: "inspect provider ", CanonicalPreview: "loom provider inspect <provider-ref>", Score: 66},
		{Label: "inspect schedule", InsertText: "inspect schedule ", CanonicalPreview: "loom schedule inspect <schedule-ref>", Score: 64},
		{Label: "inspect job", InsertText: "inspect job ", CanonicalPreview: "loom job inspect <job-ref>", Score: 62},
		{Label: "run indexer", InsertText: "run indexer", CanonicalPreview: "loom worker run main.indexer_text --once", Score: 60},
	}
	result := make([]CommandSuggestion, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, CommandSuggestion{
			Label:            definition.Label,
			Kind:             "alias",
			Description:      "Friendly alias",
			InsertText:       definition.InsertText,
			ReplacementStart: 0,
			ReplacementEnd:   end,
			CanonicalPreview: definition.CanonicalPreview,
			Score:            definition.Score,
		})
	}
	return result
}

func commandAliasQueryLooksNatural(prefix string) bool {
	query := strings.ToLower(strings.TrimSpace(prefix))
	if query == "" {
		return false
	}
	for _, suggestion := range commandRootAliasSuggestions(0, 0) {
		firstWord := strings.Fields(suggestion.Label)
		if len(firstWord) == 0 {
			continue
		}
		if strings.HasPrefix(firstWord[0], query) || strings.HasPrefix(query, firstWord[0]) {
			return true
		}
	}
	return false
}

func rootHasStrongCommandPrefix(prefix string) bool {
	query := strings.ToLower(strings.TrimSpace(prefix))
	if query == "" {
		return true
	}
	for _, suggestion := range commandRootSuggestions(0, 0) {
		label := strings.ToLower(suggestion.Label)
		if strings.HasPrefix(label, query) {
			return true
		}
	}
	return false
}

func commandCompletionContextTokens(input string, prefix string) []string {
	parsed := ParseCommandInput(input)
	tokens := append([]string{}, parsed.CanonicalTokens...)
	if len(tokens) == 0 {
		return nil
	}
	trimmedRight := strings.TrimRight(input, " \t")
	inputEndsWithSpace := len(trimmedRight) != len(input)
	if !inputEndsWithSpace && prefix != "" && strings.EqualFold(tokens[len(tokens)-1], prefix) {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

func commandSubcommandSuggestions(contextTokens []string, start int, end int) []CommandSuggestion {
	key := longestCommandContextKey(contextTokens)
	if key == "" {
		return nil
	}
	subcommandsByContext := map[string][]string{
		"box":                      {"status", "path", "init", "repair", "watch-plan", "watch-apply", "watch-status"},
		"setup":                    {"plan", "apply", "status", "doctor", "repair", "cleanup", "manifest", "uninstall", "workspace"},
		"setup cleanup":            {"plan", "apply"},
		"lane":                     {"status", "send", "publish", "repair", "acknowledge-pending", "acknowledge-transfer", "archive-transfer"},
		"storage":                  {"status", "export", "mount-policy", "cloud-status", "tree", "list", "inspect", "inspect-path", "verify", "failures", "transfers", "fetch", "retention", "main-documents", "inventory", "doctor", "repair", "mount-status", "cleanup", "archive", "restore", "safe-to-delete", "safe-delete", "cloud-offload", "cloud-fetch", "benchmark"},
		"storage export":           {"status"},
		"storage cleanup":          {"plan", "apply"},
		"storage repair":           {"catalog", "retention"},
		"storage mount-policy":     {"status", "enable", "disable", "repair-once"},
		"storage cloud-offload":    {"plan", "apply"},
		"cloud":                    {"status", "doctor", "snapshot"},
		"cloud snapshot":           {"status", "backend", "push", "list", "verify", "fetch", "restore-drill", "retention"},
		"cloud snapshot backend":   {"status", "doctor", "init"},
		"project":                  {"scaffold", "facet", "validate", "plan", "diff", "watch-plan", "workflows", "repos", "register", "status", "doctor", "activate", "deactivate", "archive", "apply-watch-policy", "sync-status", "backup-status"},
		"project facet":            {"add"},
		"project scaffold":         {"cleanup"},
		"project workflows":        {"list", "inspect"},
		"project repos":            {"list", "inspect", "status"},
		"project archive":          {"inspect", "restore", "migrate-runtime"},
		"capabilities":             {"list", "search"},
		"providers":                {"list"},
		"provider":                 {"inspect", "health"},
		"capability":               {"inspect", "call"},
		"object":                   {"list", "inspect", "ingest"},
		"sync":                     {"status", "batches", "conflicts", "replicas", "private-backups", "deletion-requests", "deletion-request"},
		"sync deletion-request":    {"inspect", "review", "approve", "deny", "complete"},
		"indexes":                  {"status", "queue", "failures", "explain", "retry", "retry-failed", "rebuild", "run"},
		"indexes queue":            {"list", "show"},
		"indexes explain":          {"object"},
		"indexes rebuild":          {"object"},
		"indexes run":              {"text"},
		"worker":                   {"inspect", "runs", "run"},
		"workers":                  {"list"},
		"schedule":                 {"inspect", "fire", "fires", "enable", "disable"},
		"schedules":                {"list", "create", "status"},
		"job":                      {"inspect", "logs", "outputs", "cancel", "retry", "acknowledge", "archive", "events"},
		"jobs":                     {"status", "queue", "failures", "run"},
		"node":                     {"list", "inspect", "health"},
		"database":                 {"status", "doctor", "compact", "objects", "indexes"},
		"watched-roots":            {"status", "findings", "failures", "backups"},
		"watched-roots backups":    {"status", "batches", "items"},
		"backup":                   {"status", "list", "create", "verify", "restore-drill", "coverage", "contracts"},
		"backup contracts":         {"status", "list", "inspect", "preflight", "create", "enable", "disable", "recheck", "retry-activation", "delete", "plan"},
		"support":                  {"bundle", "acceptance"},
		"support acceptance":       {"cleanup"},
		"support bundle":           {"create"},
		"maintenance":              {"status", "backup", "object-store", "retention"},
		"maintenance backup":       {"status", "run"},
		"maintenance object-store": {"status", "scan"},
		"maintenance retention":    {"dry-run", "apply"},
	}
	subcommands := subcommandsByContext[key]
	result := make([]CommandSuggestion, 0, len(subcommands))
	for idx, subcommand := range subcommands {
		canonicalTokens := append([]string{}, strings.Fields(key)...)
		canonicalTokens = append(canonicalTokens, subcommand)
		result = append(result, CommandSuggestion{
			Label:            subcommand,
			Kind:             "subcommand",
			Description:      "LOOM subcommand",
			InsertText:       subcommand,
			ReplacementStart: start,
			ReplacementEnd:   end,
			CanonicalPreview: commandString(canonicalTokens),
			Score:            100 - idx,
		})
	}
	return result
}

func commandFlagSuggestions(input string, _ string, start int, end int) []CommandSuggestion {
	contextTokens := commandCompletionContextTokens(input, "")
	flags := []string{"--json", "--plain", "--verbose", "--no-color", "--no-interactive", "--correlation-id"}
	key := strings.Join(contextTokens, " ")
	switch {
	case strings.HasPrefix(key, "worker run"):
		flags = append([]string{"--once", "--reason", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "lane send"):
		flags = append([]string{"--dry-run", "--keep-local", "--resume", "--path", "--profile", "--source-node", "--source-box-id", "--main-host", "--remote-root"}, flags...)
	case strings.HasPrefix(key, "lane publish"), strings.HasPrefix(key, "lane repair"):
		flags = append([]string{"--dry-run", "--path", "--profile", "--main-host", "--remote-root"}, flags...)
	case strings.HasPrefix(key, "lane acknowledge-pending"), strings.HasPrefix(key, "lane acknowledge-transfer"), strings.HasPrefix(key, "lane archive-transfer"):
		flags = append([]string{"--path", "--profile", "--note"}, flags...)
	case strings.HasPrefix(key, "lane status"):
		flags = append([]string{"--path", "--profile", "--include-fidelity"}, flags...)
	case strings.HasPrefix(key, "box"):
		flags = append([]string{"--path", "--profile", "--dry-run"}, flags...)
	case strings.HasPrefix(key, "storage mount-policy enable"):
		flags = append([]string{"--local", "--repair"}, flags...)
	case strings.HasPrefix(key, "storage mount-policy disable"):
		flags = append([]string{"--local", "--keep-mounted"}, flags...)
	case strings.HasPrefix(key, "storage mount-policy repair-once"):
		flags = append([]string{"--local", "--force"}, flags...)
	case strings.HasPrefix(key, "storage mount-policy"):
		flags = append([]string{"--local"}, flags...)
	case strings.HasPrefix(key, "cloud status"):
		flags = append([]string{"--live", "--force-live", "--cached", "--local", "--cloud-config"}, flags...)
	case strings.HasPrefix(key, "cloud doctor"):
		flags = append([]string{"--live", "--force-live", "--local", "--cloud-config"}, flags...)
	case strings.HasPrefix(key, "cloud snapshot status"):
		flags = append([]string{"--live", "--force-live", "--local", "--node-id", "--cloud-config"}, flags...)
	case strings.HasPrefix(key, "project scaffold cleanup"):
		flags = append([]string{"--dry-run"}, flags...)
	case strings.HasPrefix(key, "project scaffold"):
		flags = append([]string{"--backend", "--register", "--slug", "--owner-node", "--preset", "--facets", "--directory", "--force", "--dry-run"}, flags...)
	case strings.HasPrefix(key, "project facet add"):
		flags = append([]string{"--backend", "--dry-run", "--force", "--no-register"}, flags...)
	case strings.HasPrefix(key, "project validate"):
		flags = append([]string{"--backend", "--strict"}, flags...)
	case strings.HasPrefix(key, "project plan"):
		flags = append([]string{"--backend", "--include-diagnostics"}, flags...)
	case strings.HasPrefix(key, "project register"):
		flags = append([]string{"--backend", "--strict", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "project repos list"), strings.HasPrefix(key, "project repos status"):
		flags = append([]string{"--limit", "--after"}, flags...)
	case strings.HasPrefix(key, "project archive restore"):
		flags = append([]string{"--dry-run", "--to-node"}, flags...)
	case strings.HasPrefix(key, "project archive migrate-runtime"):
		flags = append([]string{"--dry-run", "--to-node"}, flags...)
	case strings.HasPrefix(key, "project archive"):
		flags = append([]string{"--dry-run", "--source", "--to", "--reason", "--skip-storage-archive", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "indexes run text"):
		flags = append([]string{"--once"}, flags...)
	case strings.HasPrefix(key, "indexes retry-failed"):
		flags = append([]string{"--limit", "--object", "--project", "--scope", "--index-type"}, flags...)
	case strings.HasPrefix(key, "sync status"):
		flags = append([]string{"--node"}, flags...)
	case strings.HasPrefix(key, "capabilities list"):
		flags = append([]string{"--provider", "--class", "--node", "--scope", "--form", "--status", "--risk"}, flags...)
	case strings.HasPrefix(key, "providers list"):
		flags = append([]string{"--node", "--scope", "--type", "--status", "--health"}, flags...)
	case strings.HasPrefix(key, "object list"):
		flags = append([]string{"--project", "--scope", "--limit"}, flags...)
	case strings.HasPrefix(key, "support bundle create"):
		flags = append([]string{"--output", "--profile", "--include-logs", "--include-live", "--include-projects", "--project", "--include-absolute-paths", "--max-items", "--max-bytes", "--timeout", "--dry-run"}, flags...)
	case strings.HasPrefix(key, "support acceptance cleanup"):
		flags = append([]string{"--root", "--archive-root", "--yes", "--delete-now"}, flags...)
	case strings.HasPrefix(key, "maintenance retention dry-run"):
		flags = append([]string{"--recent-success-days", "--out", "--max-rows-per-batch", "--max-total-rows"}, flags...)
	case strings.HasPrefix(key, "maintenance retention apply"):
		flags = append([]string{"--yes", "--plan", "--plan-hash", "--max-rows-per-batch", "--max-total-rows", "--reason"}, flags...)
	case strings.HasPrefix(key, "database compact"):
		flags = append([]string{"--dry-run", "--confirm", "--recent-success-days", "--out", "--plan", "--plan-hash", "--max-rows-per-batch", "--max-total-rows", "--reason"}, flags...)
	case strings.HasPrefix(key, "backup contracts create"):
		flags = append([]string{"--path", "--node", "--owner-node", "--preflight", "--target-scope", "--display-name", "--include", "--exclude", "--backup-mode", "--max-file-bytes", "--max-batch-bytes", "--replace", "--dry-run", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "backup contracts status"):
		flags = append([]string{"--status", "--node", "--limit"}, flags...)
	case strings.HasPrefix(key, "backup contracts preflight"):
		flags = append([]string{"--node", "--path", "--include", "--exclude", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "backup contracts enable"):
		flags = append([]string{"--dry-run", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "backup contracts disable"):
		flags = append([]string{"--dry-run", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "backup contracts recheck"):
		flags = append([]string{"--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "backup contracts retry-activation"):
		flags = append([]string{"--reason", "--idempotency-key"}, flags...)
	case strings.HasPrefix(key, "backup contracts delete"):
		flags = append([]string{"--dry-run", "--yes", "--idempotency-key"}, flags...)
	}
	result := make([]CommandSuggestion, 0, len(flags))
	for _, flag := range flags {
		result = append(result, commandSuggestion(flag, "flag", "CLI flag", flag, start, end))
	}
	return result
}

func longestCommandContextKey(tokens []string) string {
	for length := len(tokens); length > 0; length-- {
		key := strings.Join(lowerTokens(tokens[:length]), " ")
		switch key {
		case "box", "lane", "setup", "setup cleanup", "storage", "storage export",
			"storage cleanup", "storage repair", "storage mount-policy", "storage cloud-offload",
			"cloud", "cloud snapshot", "cloud snapshot backend", "project", "project facet",
			"project facet add", "project workflows", "project repos",
			"project archive", "capabilities", "providers", "provider", "capability", "object", "sync",
			"indexes", "indexes queue", "indexes explain", "indexes rebuild", "indexes run",
			"worker", "workers", "schedule", "schedules", "job", "jobs", "node", "database", "database compact",
			"watched-roots", "watched-roots backups", "backup", "backup contracts", "backup contracts status", "backup contracts preflight", "backup contracts create", "backup contracts enable", "backup contracts disable", "backup contracts recheck", "backup contracts retry-activation", "backup contracts delete", "support", "support acceptance", "support bundle",
			"maintenance", "maintenance backup", "maintenance object-store", "maintenance retention":
			return key
		}
	}
	return ""
}

func dynamicCommandSuggestions(ctx context.Context, client Client, correlationID string, input string, prefix string, start int, end int) []CommandSuggestion {
	parse := ParseCommandInput(input)
	tokens := parse.CanonicalTokens
	lower := lowerTokens(tokens)
	kind := dynamicCompletionKind(lower)
	if kind == "" {
		return nil
	}
	switch kind {
	case "worker":
		envelope, err := client.ListWorkers(ctx, correlationID, workers.WorkerFilter{Limit: 50})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, worker := range envelope.Data {
			ref := firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID)
			result = append(result, commandSuggestion(ref, "worker", workerDisplayName(worker), ref, start, end))
		}
		return result
	case "capability":
		envelope, err := client.ListCapabilities(ctx, correlationID, capabilities.CapabilityFilter{Limit: 50, Status: capabilities.EndpointStatusActive})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, capability := range envelope.Data {
			ref := firstNonEmpty(capability.CapabilityEndpoint.CompactAddress, capability.CapabilityEndpoint.CapabilityEndpointID)
			result = append(result, commandSuggestion(ref, "capability", capabilityLabel(capability), ref, start, end))
		}
		return result
	case "provider":
		envelope, err := client.ListProviders(ctx, correlationID, capabilities.ProviderFilter{Limit: 50, Status: capabilities.ProviderStatusActive, RequireActiveEndpoint: true})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, provider := range envelope.Data {
			ref := firstNonEmpty(provider.Provider.CompactAddress, provider.Provider.ProviderID)
			result = append(result, commandSuggestion(ref, "provider", firstNonEmpty(provider.Provider.DisplayName, ref), ref, start, end))
		}
		return result
	case "object":
		envelope, err := client.ListObjects(ctx, correlationID, objects.ListFilter{Limit: 50})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, object := range envelope.Data {
			ref := firstNonEmpty(object.ObjectID, object.Name)
			result = append(result, commandSuggestion(ref, "object", firstNonEmpty(object.Name, ref), ref, start, end))
		}
		return result
	case "schedule":
		envelope, err := client.ListSchedules(ctx, correlationID, automation.ScheduleFilter{Limit: 50})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, schedule := range envelope.Data {
			ref := firstNonEmpty(schedule.ScheduleID, schedule.ScheduleKey)
			result = append(result, commandSuggestion(ref, "schedule", firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, ref), ref, start, end))
		}
		return result
	case "job":
		envelope, err := client.ListJobs(ctx, correlationID, jobs.ListFilter{Limit: 50})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, job := range envelope.Data {
			result = append(result, commandSuggestion(job.JobID, "job", firstNonEmpty(job.JobType, job.Status), job.JobID, start, end))
		}
		return result
	case "node":
		envelope, err := client.ListNodes(ctx, correlationID, 50)
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, node := range envelope.Data {
			ref := firstNonEmpty(node.NodeKey, node.NodeID)
			result = append(result, commandSuggestion(ref, "node", node.NodeID, ref, start, end))
		}
		return result
	case "project":
		envelope, err := client.ListProjects(ctx, correlationID, 50)
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, project := range envelope.Data {
			ref := projectRef(project)
			result = append(result, commandSuggestion(ref, "project", firstNonEmpty(project.Name, project.Status), ref, start, end))
		}
		return result
	case "index":
		envelope, err := client.ListIndexFailures(ctx, correlationID, search.StatusFilter{Limit: 50})
		if err != nil {
			return nil
		}
		result := []CommandSuggestion{}
		for _, status := range envelope.Data {
			ref := firstNonEmpty(status.IndexStatusID, status.ObjectID)
			result = append(result, commandSuggestion(ref, "index", firstNonEmpty(status.LastErrorCode, status.Status), ref, start, end))
		}
		return result
	default:
		return nil
	}
}

func dynamicCompletionKind(tokens []string) string {
	if len(tokens) < 2 {
		return ""
	}
	if len(tokens) >= 2 {
		switch strings.Join(tokens[:2], " ") {
		case "worker run", "worker inspect":
			return "worker"
		case "capability inspect", "capability call":
			return "capability"
		case "provider inspect":
			return "provider"
		case "object inspect":
			return "object"
		case "project validate", "project plan", "project register", "project archive":
			return "project"
		case "schedule inspect", "schedule fire":
			return "schedule"
		case "job inspect", "job logs", "job outputs", "job retry", "job cancel", "job acknowledge", "job archive":
			return "job"
		case "node inspect":
			return "node"
		case "indexes retry":
			return "index"
		}
	}
	if len(tokens) >= 3 {
		switch strings.Join(tokens[:3], " ") {
		case "project facet add", "project archive inspect", "project archive restore", "project archive migrate-runtime":
			return "project"
		case "project repos list", "project repos status":
			if len(tokens) <= 4 {
				return "project"
			}
		case "project repos inspect":
			if len(tokens) <= 4 {
				return "project"
			}
		}
	}
	return ""
}

func currentCommandToken(input string, cursor int) (string, int, int) {
	if cursor > len(input) {
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

func commandSuggestion(label, kind, description, insert string, start int, end int) CommandSuggestion {
	return commandSuggestionWithScore(label, kind, description, insert, start, end, 50)
}

func commandSuggestionWithScore(label, kind, description, insert string, start int, end int, score int) CommandSuggestion {
	return CommandSuggestion{Label: label, Kind: kind, Description: description, InsertText: insert, ReplacementStart: start, ReplacementEnd: end, CanonicalPreview: commandString(ParseCommandInput("$ " + insert).CanonicalTokens), Score: score}
}

func commandSuggestionWithCanonical(label, kind, description, insert string, start int, end int, canonical string, score int) CommandSuggestion {
	suggestion := commandSuggestionWithScore(label, kind, description, insert, start, end, score)
	suggestion.CanonicalPreview = canonical
	return suggestion
}

func rankCommandSuggestions(prefix string, suggestions []CommandSuggestion, limit int) []CommandSuggestion {
	candidates := make([]ui.Candidate, 0, len(suggestions))
	byID := map[string]CommandSuggestion{}
	for _, suggestion := range suggestions {
		id := suggestion.Kind + ":" + suggestion.Label + ":" + suggestion.InsertText
		byID[id] = suggestion
		candidates = append(candidates, ui.Candidate{ID: id, Title: suggestion.Label, Description: suggestion.Description, Domain: suggestion.Kind, Keywords: []string{suggestion.InsertText, suggestion.CanonicalPreview}, Attention: suggestion.Score})
	}
	ranked := ui.RankFuzzy(prefix, candidates, limit)
	result := []CommandSuggestion{}
	seen := map[string]bool{}
	for _, item := range ranked {
		suggestion := byID[item.Candidate.ID]
		key := suggestion.Kind + ":" + suggestion.InsertText
		if seen[key] {
			continue
		}
		seen[key] = true
		suggestion.Score = item.Score
		result = append(result, suggestion)
	}
	return result
}

func rankCommandSuggestionStages(prefix string, limit int, stages ...[]CommandSuggestion) []CommandSuggestion {
	if limit <= 0 {
		limit = 12
	}
	result := []CommandSuggestion{}
	seen := map[string]bool{}
	for _, stage := range stages {
		if len(stage) == 0 {
			continue
		}
		ranked := rankCommandSuggestions(prefix, stage, limit)
		for _, suggestion := range ranked {
			key := suggestion.Kind + ":" + suggestion.InsertText
			if strings.TrimSpace(suggestion.InsertText) == "" || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, suggestion)
			if len(result) >= limit {
				return result
			}
		}
	}
	return result
}

func applyCommandSuggestion(input string, suggestion CommandSuggestion) string {
	start := suggestion.ReplacementStart
	end := suggestion.ReplacementEnd
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(input) {
		end = len(input)
	}
	insertText := suggestion.InsertText
	if start == 0 && isCommandModeInput(input) && !isCommandModeInput(insertText) {
		insertText = "$ " + insertText
	}
	return input[:start] + insertText + input[end:]
}

func selectedRecordCommandSuggestion(input string, prefix string, start int, end int, selected CommandSelectedRecord) CommandSuggestion {
	ref := strings.TrimSpace(selected.Ref)
	if ref == "" {
		return CommandSuggestion{}
	}
	kind := dynamicCompletionKind(lowerTokens(ParseCommandInput(input).CanonicalTokens))
	if kind == "" || normalizeCommandRecordKind(selected.Kind) != kind {
		return CommandSuggestion{}
	}
	label := firstNonEmpty(selected.Label, ref)
	if prefix != "" && !strings.Contains(strings.ToLower(ref), strings.ToLower(prefix)) && !strings.Contains(strings.ToLower(label), strings.ToLower(prefix)) {
		return CommandSuggestion{}
	}
	return CommandSuggestion{
		Label:            label,
		Kind:             kind,
		Description:      "Selected record",
		InsertText:       ref,
		ReplacementStart: start,
		ReplacementEnd:   end,
		CanonicalPreview: commandString(append(commandCompletionContextTokens(input, prefix), ref)),
		Score:            100,
	}
}

func normalizeCommandRecordKind(kind string) string {
	switch kind {
	case "worker":
		return "worker"
	case "capability", "capability_endpoint":
		return "capability"
	case "provider":
		return "provider"
	case "object", "search_result":
		return "object"
	case "schedule", "direct_endpoint":
		return "schedule"
	case "job":
		return "job"
	case "node":
		return "node"
	case "index_status", "index_queue", "index_failure":
		return "index"
	default:
		return kind
	}
}

func uniqueCommandSuggestions(suggestions []CommandSuggestion) []CommandSuggestion {
	seen := map[string]bool{}
	result := []CommandSuggestion{}
	for _, suggestion := range suggestions {
		key := suggestion.Kind + ":" + suggestion.InsertText
		if strings.TrimSpace(suggestion.InsertText) == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, suggestion)
	}
	return result
}
