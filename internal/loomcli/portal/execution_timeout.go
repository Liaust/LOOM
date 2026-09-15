package portal

import (
	"strings"
	"time"
)

const defaultPortalExecutionTimeout = 60 * time.Second

func portalActionExecutionTimeout(action PortalAction) time.Duration {
	switch action.Executor.Kind {
	case PortalExecutorBoxLaneSend:
		return 4 * time.Hour
	default:
		return defaultPortalExecutionTimeout
	}
}

func portalCommandExecutionTimeout(tokens []string) time.Duration {
	lower := lowerTokens(tokens)
	if len(lower) >= 2 && lower[0] == "lane" {
		switch strings.TrimSpace(lower[1]) {
		case "send":
			return 4 * time.Hour
		case "publish", "repair":
			return 45 * time.Minute
		}
	}
	return defaultPortalExecutionTimeout
}
