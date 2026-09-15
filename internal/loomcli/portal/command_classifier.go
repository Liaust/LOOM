package portal

import "strings"

func BuildCommandPreview(input string) CommandPreview {
	parse := ParseCommandInput(input)
	preview := CommandPreview{
		Input:               input,
		CanonicalTokens:     append([]string{}, parse.CanonicalTokens...),
		CanonicalCommand:    commandString(parse.CanonicalTokens),
		AliasApplied:        parse.AliasApplied,
		ExecutionDependency: commandExecutionDependency(parse.CanonicalTokens),
	}
	if parse.Error != "" {
		preview.ParseError = parse.Error
		preview.BlockedReason = firstNonEmpty(parse.BlockedReason, parse.Error)
		preview.Classification = CommandClassBlocked
		return preview
	}
	class, reason := ClassifyCommand(parse.CanonicalTokens)
	preview.Classification = class
	preview.BlockedReason = reason
	preview.RequiresConfirmation = commandRequiresConfirmation(class)
	preview.TargetKind, preview.TargetRef = commandTarget(parse.CanonicalTokens)
	preview.TargetLabel = preview.TargetRef
	preview.EffectSummary = commandEffectSummary(parse.CanonicalTokens, class)
	return preview
}

func commandExecutionDependency(tokens []string) ExecutionDependency {
	lower := lowerTokens(tokens)
	if len(lower) == 1 && lower[0] == "version" {
		return ExecutionDependencyLocal
	}
	return ExecutionDependencyMain
}

func applyMainAvailabilityToCommandPreview(preview CommandPreview, availability MainAvailability) CommandPreview {
	preview.MainAvailability = availability
	if preview.ExecutionDependency == "" {
		preview.ExecutionDependency = commandExecutionDependency(preview.CanonicalTokens)
	}
	return preview
}

func ClassifyCommand(tokens []string) (CommandClassification, string) {
	lower := lowerTokens(tokens)
	if len(lower) == 0 {
		return CommandClassBlocked, "Enter a LOOM command after $."
	}
	if lower[0] == "enter" {
		return CommandClassBlocked, "This command opens an interactive portal and cannot be run from inside the portal."
	}
	if lower[0] == "completion" {
		return CommandClassBlocked, "Shell completion generation cannot be run from inside the portal."
	}
	if isDangerousCommand(lower) {
		return CommandClassDangerous, "Dangerous commands are blocked in portal command mode for this slice."
	}
	if isSensitiveCommand(lower) {
		return CommandClassSensitive, ""
	}
	if isSafeRunCommand(lower) {
		return CommandClassSafeRun, ""
	}
	return CommandClassInspect, ""
}

func isSafeRunCommand(tokens []string) bool {
	if len(tokens) >= 4 && tokens[0] == "worker" && tokens[1] == "run" && hasFlag(tokens, "--once") {
		return true
	}
	if len(tokens) >= 3 && tokens[0] == "storage" && tokens[1] == "mount-policy" {
		switch tokens[2] {
		case "enable", "disable", "repair-once":
			return true
		}
	}
	if len(tokens) >= 2 && tokens[0] == "indexes" {
		switch tokens[1] {
		case "retry", "retry-failed":
			return true
		case "rebuild":
			return len(tokens) >= 4 && tokens[2] == "object"
		case "run":
			return len(tokens) >= 3 && tokens[2] == "text" && hasFlag(tokens, "--once")
		}
	}
	if len(tokens) >= 3 && tokens[0] == "schedule" && tokens[1] == "fire" {
		return true
	}
	if len(tokens) >= 2 && tokens[0] == "lane" {
		switch tokens[1] {
		case "acknowledge-pending", "acknowledge-transfer", "archive-transfer":
			return true
		}
	}
	if len(tokens) >= 4 && tokens[0] == "maintenance" && tokens[1] == "backup" && tokens[2] == "run" && hasFlag(tokens, "--once") {
		return true
	}
	if len(tokens) >= 3 && tokens[0] == "maintenance" && tokens[1] == "retention" && tokens[2] == "dry-run" {
		return true
	}
	if len(tokens) >= 2 && tokens[0] == "database" && tokens[1] == "compact" {
		return !hasFlag(tokens, "--confirm") && !hasFlag(tokens, "--dry-run=false")
	}
	if len(tokens) >= 4 && tokens[0] == "maintenance" && tokens[1] == "object-store" && tokens[2] == "scan" && hasFlag(tokens, "--sample") {
		return true
	}
	if len(tokens) >= 4 && tokens[0] == "project" && tokens[1] == "archive" && hasFlag(tokens, "--dry-run") {
		switch tokens[2] {
		case "inspect", "restore", "migrate-runtime":
			return false
		default:
			return true
		}
	}
	if len(tokens) >= 4 && tokens[0] == "project" && tokens[1] == "facet" && tokens[2] == "add" && hasFlag(tokens, "--dry-run") {
		return true
	}
	if len(tokens) >= 3 && tokens[0] == "support" && tokens[1] == "bundle" && tokens[2] == "create" && hasFlag(tokens, "--dry-run") {
		return true
	}
	if len(tokens) >= 3 && tokens[0] == "backup" && tokens[1] == "contracts" {
		switch tokens[2] {
		case "preflight", "recheck", "retry-activation":
			return true
		case "create", "enable", "disable", "delete":
			return hasFlag(tokens, "--dry-run")
		}
	}
	if len(tokens) >= 3 && tokens[0] == "support" && tokens[1] == "acceptance" && tokens[2] == "cleanup" && !hasFlag(tokens, "--yes") {
		return true
	}
	if len(tokens) >= 4 && tokens[0] == "sync" && tokens[1] == "deletion-request" && hasFlag(tokens, "--dry-run") {
		switch tokens[2] {
		case "review", "approve", "deny", "complete":
			return true
		}
	}
	return false
}

func isSensitiveCommand(tokens []string) bool {
	if len(tokens) >= 2 {
		switch strings.Join(tokens[:2], " ") {
		case "backup create", "backup restore-drill", "capability call", "script run", "object ingest", "direct-event ingest", "job cancel", "job retry", "job acknowledge", "job archive", "lane send", "lane publish", "lane repair", "project scaffold", "project register":
			return true
		}
	}
	if len(tokens) >= 4 && tokens[0] == "project" && tokens[1] == "facet" && tokens[2] == "add" {
		return !hasFlag(tokens, "--dry-run")
	}
	if len(tokens) >= 3 && tokens[0] == "integration" && tokens[1] == "auth" && tokens[2] == "create" {
		return true
	}
	if len(tokens) >= 3 && tokens[0] == "support" && tokens[1] == "bundle" && tokens[2] == "create" {
		return !hasFlag(tokens, "--dry-run")
	}
	if len(tokens) >= 3 && tokens[0] == "backup" && tokens[1] == "contracts" {
		switch tokens[2] {
		case "create", "enable", "disable", "delete":
			return !hasFlag(tokens, "--dry-run")
		}
	}
	if len(tokens) >= 3 && tokens[0] == "support" && tokens[1] == "acceptance" && tokens[2] == "cleanup" {
		return hasFlag(tokens, "--yes") && !hasFlag(tokens, "--delete-now")
	}
	if len(tokens) >= 4 && tokens[0] == "sync" && tokens[1] == "deletion-request" {
		switch tokens[2] {
		case "review", "approve", "deny", "complete":
			return !hasFlag(tokens, "--dry-run")
		}
	}
	return false
}

func isDangerousCommand(tokens []string) bool {
	if len(tokens) >= 2 {
		switch strings.Join(tokens[:2], " ") {
		case "backup restore", "bootstrap ssh", "integration disable", "integration revoke", "schedule disable", "module disable", "grant revoke", "provider-advertisement approve", "provider-advertisement reject", "setup apply", "update apply", "update rollback", "storage restore", "storage safe-delete":
			return true
		}
		if tokens[0] == "database" && tokens[1] == "compact" {
			return hasFlag(tokens, "--confirm") || hasFlag(tokens, "--dry-run=false")
		}
	}
	if len(tokens) >= 3 {
		switch strings.Join(tokens[:3], " ") {
		case "integration auth revoke", "maintenance retention apply", "storage cleanup apply", "storage cloud-offload apply", "cloud snapshot push", "cloud snapshot fetch", "cloud snapshot restore-drill":
			return true
		}
		if strings.Join(tokens[:3], " ") == "support acceptance cleanup" && hasFlag(tokens, "--delete-now") {
			return true
		}
		if len(tokens) >= 4 && strings.Join(tokens[:4], " ") == "cloud snapshot backend init" {
			return true
		}
		if tokens[0] == "project" && tokens[1] == "archive" {
			if hasFlag(tokens, "--dry-run") {
				return false
			}
			switch tokens[2] {
			case "inspect", "restore", "migrate-runtime":
				return false
			default:
				return true
			}
		}
	}
	return false
}

func commandTarget(tokens []string) (string, string) {
	lower := lowerTokens(tokens)
	if len(lower) >= 4 && lower[0] == "sync" && lower[1] == "deletion-request" {
		switch lower[2] {
		case "inspect", "review", "approve", "deny", "complete":
			return "deletion-request", tokens[3]
		}
	}
	if len(lower) >= 3 {
		switch strings.Join(lower[:2], " ") {
		case "worker run", "worker inspect":
			return "worker", tokens[2]
		case "object inspect", "capability inspect", "provider inspect", "schedule inspect", "schedule fire", "job inspect", "job logs", "job outputs", "job cancel", "job retry", "job acknowledge", "job archive", "node inspect":
			return lower[0], tokens[2]
		case "project validate", "project plan", "project register":
			return "project", tokens[2]
		case "project archive":
			if len(lower) >= 4 {
				switch lower[2] {
				case "inspect", "restore", "migrate-runtime":
					return "project", tokens[3]
				}
			}
			return "project", tokens[2]
		case "indexes retry":
			return "index", tokens[2]
		case "backup contracts":
			if len(lower) >= 4 {
				switch lower[2] {
				case "inspect", "create", "enable", "disable", "recheck", "retry-activation", "delete":
					return "backup-contract", tokens[3]
				}
			}
		}
	}
	if len(lower) >= 5 && lower[0] == "project" && lower[1] == "facet" && lower[2] == "add" {
		return "project", tokens[3]
	}
	if len(lower) >= 4 && lower[0] == "indexes" && lower[1] == "rebuild" && lower[2] == "object" {
		return "object", tokens[3]
	}
	return "", ""
}

func commandEffectSummary(tokens []string, class CommandClassification) string {
	lower := lowerTokens(tokens)
	if len(lower) == 0 {
		return ""
	}
	if class == CommandClassInspect {
		return "Reads LOOM state and prints command output."
	}
	if len(lower) >= 2 {
		if len(lower) >= 3 && strings.Join(lower[:3], " ") == "project scaffold cleanup" {
			return "Removes untouched generated scaffold example packages from a project."
		}
		switch strings.Join(lower[:2], " ") {
		case "lane send":
			return "Transfers pending LOOM Lane files to main and clears verified local Lane files unless keep-local is set."
		case "lane publish", "lane repair":
			return "Repairs imports promotion or cataloging, idempotently cleans transport staging, and completes the reviewed local cleanup when safe without re-uploading payload bytes."
		case "lane acknowledge-pending":
			return "Marks the current pending Lane item state as acknowledged without deleting, moving, or sending the file."
		case "lane acknowledge-transfer", "lane archive-transfer":
			return "Clears active attention for a failed Lane transfer while keeping the batch history."
		case "maintenance retention":
			return "Builds a database retention dry-run report without deleting rows."
		case "storage export":
			return "Shows a deprecated read-only diagnostic; the generated export is retired and no files are changed."
		case "storage mount-policy":
			return "Changes the local LOOM Main storage mount desired state."
		case "project scaffold":
			if hasFlag(lower, "--backend") {
				return "Creates a project scaffold on the configured backend."
			}
			return "Creates a local project scaffold on disk."
		case "project validate":
			if hasFlag(lower, "--backend") {
				return "Validates a project contract on the configured backend without writing."
			}
			return "Validates a local project contract without writing."
		case "project plan":
			if hasFlag(lower, "--backend") {
				return "Builds a read-only registration plan on the configured backend."
			}
			return "Builds a read-only local project registration plan."
		case "project register":
			if hasFlag(lower, "--backend") {
				return "Registers the backend project contract snapshot."
			}
			return "Registers a local project contract snapshot with the backend."
		case "project archive":
			if len(lower) >= 3 {
				switch lower[2] {
				case "inspect":
					return "Reads project archive state and retained runtime manifest details."
				case "restore":
					return "Plans project archive restore without applying file or runtime changes."
				case "migrate-runtime":
					return "Plans successor runtime ownership without rewriting archived runtime URLs."
				}
			}
			if hasFlag(lower, "--dry-run") {
				return "Plans project archive without changing backend state."
			}
			return "Archives project storage and disables project-owned runtime surfaces."
		case "cloud status":
			return "Reads cloud backup status."
		case "worker run":
			return "Requests one bounded manual worker run."
		case "indexes retry":
			return "Requeues selected indexing work."
		case "indexes retry-failed":
			return "Requeues failed indexing work."
		case "schedule fire":
			return "Requests a manual schedule fire."
		case "capability call":
			return "Calls a capability through the router."
		case "script run":
			return "Queues or runs a registered script."
		case "job cancel":
			return "Requests cancellation for a job."
		case "job retry":
			return "Requests a job retry."
		case "support bundle":
			if len(lower) >= 3 && lower[2] == "create" {
				if hasFlag(lower, "--dry-run") {
					return "Shows the bounded support-bundle collection plan without writing an archive."
				}
				return "Writes a redacted diagnostic archive for handoff. It does not upload the archive."
			}
		case "backup contracts":
			if len(lower) >= 3 {
				switch lower[2] {
				case "preflight":
					return "Runs a bounded read-only folder check on the selected owner node."
				case "create":
					if hasFlag(lower, "--dry-run") {
						return "Validates a Box backup contract and shows the watched-root plan without writing."
					}
					return "Writes a Box backup contract on the main daemon and applies the Box watch policy."
				case "enable":
					if hasFlag(lower, "--dry-run") {
						return "Shows the protected-folder enable operation without writing."
					}
					return "Enables a protected folder and queues desired state for its owner node."
				case "disable":
					if hasFlag(lower, "--dry-run") {
						return "Shows the backup contract disable operation without writing."
					}
					return "Disables a Box backup contract YAML file and reapplies the Box watch policy."
				case "recheck":
					return "Queues a new bounded folder preflight on the protected folder's owner node."
				case "retry-activation":
					return "Queues another desired-state activation attempt without claiming it applied."
				case "delete":
					if hasFlag(lower, "--dry-run") {
						return "Shows the backup contract deletion without removing the YAML file."
					}
					return "Removes a Box backup contract YAML file and reapplies the Box watch policy."
				}
			}
		case "job acknowledge":
			return "Acknowledges failed-job attention."
		case "job archive":
			return "Archives failed-job attention."
		case "sync deletion-request":
			if len(lower) >= 3 {
				switch lower[2] {
				case "inspect":
					return "Inspects a sync deletion request."
				case "review":
					return "Marks a sync deletion request for review."
				case "approve":
					return "Approves deletion request semantics without deleting files."
				case "deny":
					return "Denies a sync deletion request."
				case "complete":
					return "Marks a sync deletion request complete."
				}
			}
		}
	}
	if len(lower) >= 3 && strings.Join(lower[:3], " ") == "project facet add" {
		if hasFlag(lower, "--dry-run") {
			return "Plans backend project facet additions without writing."
		}
		return "Adds missing project facet files and contract entries."
	}
	return "Runs a LOOM command inside the portal."
}

func hasFlag(tokens []string, flag string) bool {
	for _, token := range tokens {
		if token == flag {
			return true
		}
	}
	return false
}
