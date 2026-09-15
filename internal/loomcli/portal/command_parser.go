package portal

import (
	"fmt"
	"strings"
	"unicode"
)

const shellOperatorMessage = "Command mode only runs LOOM commands. Shell operators are not supported."

func ParseCommandInput(input string) CommandParseResult {
	result := CommandParseResult{RawInput: input}
	trimmedLeft := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(trimmedLeft, "$") {
		result.Error = "Command mode input must start with $."
		return result
	}
	commandText := strings.TrimSpace(strings.TrimPrefix(trimmedLeft, "$"))
	result.CommandText = commandText
	if commandText == "" {
		return result
	}
	if containsBlockedShellSyntax(commandText) {
		result.Error = shellOperatorMessage
		result.BlockedReason = shellOperatorMessage
		return result
	}
	tokens, err := splitCommandTokens(commandText)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(tokens) > 0 && strings.EqualFold(tokens[0], "loom") {
		tokens = tokens[1:]
	}
	if reason := blockedShellTokens(tokens); reason != "" {
		result.Error = reason
		result.BlockedReason = reason
		return result
	}
	result.Tokens = tokens
	result.CanonicalTokens, result.AliasApplied = ApplyCommandAlias(tokens)
	return result
}

func splitCommandTokens(input string) ([]string, error) {
	tokens := []string{}
	var builder strings.Builder
	var quote rune
	escaped := false
	for _, r := range input {
		if escaped {
			builder.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			if quote != 0 {
				escaped = true
				continue
			}
			builder.WriteRune(r)
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			builder.WriteRune(r)
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			if builder.Len() > 0 {
				tokens = append(tokens, builder.String())
				builder.Reset()
			}
		default:
			builder.WriteRune(r)
		}
	}
	if escaped {
		builder.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("Command has an unterminated quote.")
	}
	if builder.Len() > 0 {
		tokens = append(tokens, builder.String())
	}
	return tokens, nil
}

func containsBlockedShellSyntax(input string) bool {
	for _, fragment := range []string{"&&", "||", "$(", "`"} {
		if strings.Contains(input, fragment) {
			return true
		}
	}
	return false
}

func blockedShellTokens(tokens []string) string {
	for _, token := range tokens {
		switch token {
		case "|", ">", ">>", "<", "&&", "||", ";", "&", ")":
			return shellOperatorMessage
		}
		if strings.ContainsAny(token, "|><;`") || strings.Contains(token, "$(") {
			return shellOperatorMessage
		}
	}
	return ""
}

func commandString(tokens []string) string {
	if len(tokens) == 0 {
		return "loom"
	}
	return "loom " + strings.Join(tokens, " ")
}
