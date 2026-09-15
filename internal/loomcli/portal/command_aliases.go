package portal

import "strings"

func ApplyCommandAlias(tokens []string) ([]string, string) {
	tokens = append([]string{}, tokens...)
	lower := lowerTokens(tokens)
	if len(tokens) == 0 {
		return tokens, ""
	}
	if len(tokens) >= 2 && lower[0] == "list" {
		switch lower[1] {
		case "capabilities", "capability":
			return append([]string{"capabilities", "list"}, tokens[2:]...), "list capabilities"
		case "providers", "provider":
			return append([]string{"providers", "list"}, tokens[2:]...), "list providers"
		case "workers", "worker":
			return append([]string{"workers", "list"}, tokens[2:]...), "list workers"
		case "objects", "object":
			return append([]string{"object", "list"}, tokens[2:]...), "list objects"
		case "schedules", "schedule":
			return append([]string{"schedules", "list"}, tokens[2:]...), "list schedules"
		case "jobs", "job":
			return append([]string{"jobs", "list"}, tokens[2:]...), "list jobs"
		case "nodes", "node":
			return append([]string{"node", "list"}, tokens[2:]...), "list nodes"
		}
	}
	if len(tokens) >= 2 && lower[0] == "show" {
		switch lower[1] {
		case "status":
			return append([]string{"status"}, tokens[2:]...), "show status"
		case "health":
			return append([]string{"health"}, tokens[2:]...), "show health"
		case "sync":
			if len(tokens) >= 3 && lower[2] == "status" {
				return append([]string{"sync", "status"}, tokens[3:]...), "show sync status"
			}
		}
	}
	if len(tokens) >= 2 && lower[0] == "inspect" {
		if len(tokens) >= 3 && lower[1] == "project" && lower[2] == "archive" {
			return append([]string{"project", "archive", "inspect"}, tokens[3:]...), "inspect project archive"
		}
		switch lower[1] {
		case "object":
			return append([]string{"object", "inspect"}, tokens[2:]...), "inspect object"
		case "worker":
			return append([]string{"worker", "inspect"}, tokens[2:]...), "inspect worker"
		case "capability":
			return append([]string{"capability", "inspect"}, tokens[2:]...), "inspect capability"
		case "provider":
			return append([]string{"provider", "inspect"}, tokens[2:]...), "inspect provider"
		case "schedule":
			return append([]string{"schedule", "inspect"}, tokens[2:]...), "inspect schedule"
		case "job":
			return append([]string{"job", "inspect"}, tokens[2:]...), "inspect job"
		case "node":
			return append([]string{"node", "inspect"}, tokens[2:]...), "inspect node"
		}
	}
	if len(tokens) >= 3 && lower[0] == "restore" && lower[1] == "project" && lower[2] == "archive" {
		return appendFlagIfMissing(append([]string{"project", "archive", "restore"}, tokens[3:]...), "--dry-run"), "restore project archive"
	}
	if len(tokens) >= 2 && lower[0] == "run" {
		switch lower[1] {
		case "worker":
			return append([]string{"worker", "run"}, tokens[2:]...), "run worker"
		case "indexer":
			return []string{"worker", "run", "main.indexer_text", "--once"}, "run indexer"
		}
	}
	if len(tokens) >= 2 && lower[0] == "push" && lower[1] == "lane" {
		return append([]string{"lane", "send"}, tokens[2:]...), "push lane"
	}
	if len(tokens) >= 2 && lower[0] == "mount" && lower[1] == "storage" {
		return append([]string{"storage", "mount-policy", "enable", "--local", "--repair"}, tokens[2:]...), "mount storage"
	}
	if len(tokens) >= 2 && lower[0] == "add" {
		switch {
		case lower[1] == "facet":
			return appendFlagIfMissing(append([]string{"project", "facet", "add"}, tokens[2:]...), "--backend"), "add facet"
		case len(tokens) >= 3 && lower[1] == "main" && lower[2] == "facet":
			return appendFlagIfMissing(append([]string{"project", "facet", "add"}, tokens[3:]...), "--backend"), "add main facet"
		case len(tokens) >= 3 && lower[1] == "project" && lower[2] == "facet":
			return appendFlagIfMissing(append([]string{"project", "facet", "add"}, tokens[3:]...), "--backend"), "add project facet"
		}
	}
	if len(tokens) >= 3 && lower[0] == "validate" && lower[1] == "main" && lower[2] == "project" {
		return appendFlagIfMissing(append([]string{"project", "validate"}, tokens[3:]...), "--backend"), "validate main project"
	}
	if len(tokens) >= 3 && lower[0] == "register" && lower[1] == "main" && lower[2] == "project" {
		return appendFlagIfMissing(append([]string{"project", "register"}, tokens[3:]...), "--backend"), "register main project"
	}
	if len(tokens) >= 2 && lower[0] == "archive" && lower[1] == "project" {
		return appendFlagIfMissing(append([]string{"project", "archive"}, tokens[2:]...), "--dry-run"), "archive project"
	}
	if len(tokens) >= 3 && lower[0] == "create" && lower[1] == "main" && lower[2] == "project" {
		result := append(append([]string{"project", "scaffold"}, tokens[3:]...), "--backend", "--owner-node", "main")
		result = appendFlagIfMissing(result, "--register")
		return result, "create main project"
	}
	if len(tokens) >= 2 && lower[0] == "create" && lower[1] == "project" {
		return append([]string{"project", "scaffold"}, tokens[2:]...), "create project"
	}
	if len(tokens) >= 3 && lower[0] == "create" && lower[1] == "support" && lower[2] == "bundle" {
		return append([]string{"support", "bundle", "create"}, tokens[3:]...), "create support bundle"
	}
	if len(tokens) >= 2 && lower[0] == "retry" {
		if lower[1] == "index" {
			return append([]string{"indexes", "retry"}, tokens[2:]...), "retry index"
		}
		if lower[1] == "failed" && len(tokens) >= 3 && strings.HasPrefix(lower[2], "index") {
			return append([]string{"indexes", "retry-failed"}, tokens[3:]...), "retry failed indexes"
		}
	}
	if len(tokens) >= 3 && lower[0] == "fire" && lower[1] == "schedule" {
		return append([]string{"schedule", "fire"}, tokens[2:]...), "fire schedule"
	}
	return tokens, ""
}

func appendFlagIfMissing(tokens []string, flag string) []string {
	for _, token := range tokens {
		if strings.EqualFold(token, flag) {
			return tokens
		}
	}
	return append(tokens, flag)
}

func lowerTokens(tokens []string) []string {
	lower := make([]string, len(tokens))
	for idx, token := range tokens {
		lower[idx] = strings.ToLower(strings.TrimSpace(token))
	}
	return lower
}
