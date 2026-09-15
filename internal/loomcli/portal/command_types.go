package portal

import (
	"context"
	"errors"
	"strings"
	"time"
)

type CommandClassification string

const (
	CommandClassInspect   CommandClassification = "inspect"
	CommandClassSafeRun   CommandClassification = "safe_run"
	CommandClassSensitive CommandClassification = "sensitive"
	CommandClassDangerous CommandClassification = "dangerous"
	CommandClassBlocked   CommandClassification = "blocked"
)

var ErrCommandConfirmationRequired = errors.New("portal command confirmation is required")

type CommandModeState struct {
	Active             bool
	Input              string
	Cursor             int
	Parse              CommandParseResult
	Preview            CommandPreview
	Suggestions        []CommandSuggestion
	SelectedSuggestion int
	CompletionOpen     bool
	CompletionCache    map[string]CommandCompletionCacheEntry
	History            []CommandHistoryEntry
	HistoryIndex       int
	RawDetails         bool
}

type CommandCompletionCacheEntry struct {
	Suggestions []CommandSuggestion
	LoadedAt    time.Time
}

type CommandPanelState struct {
	Lifecycle  PortalActionLifecycle
	Preview    CommandPreview
	Result     CommandResult
	RawDetails bool
}

type CommandParseResult struct {
	RawInput        string
	CommandText     string
	Tokens          []string
	CanonicalTokens []string
	AliasApplied    string
	BlockedReason   string
	Error           string
}

type CommandPreview struct {
	Input                string
	CanonicalTokens      []string
	CanonicalCommand     string
	Classification       CommandClassification
	TargetKind           string
	TargetRef            string
	TargetLabel          string
	EffectSummary        string
	RequiresConfirmation bool
	BlockedReason        string
	ParseError           string
	AliasApplied         string
	ExecutionDependency  ExecutionDependency
	MainAvailability     MainAvailability
}

type CommandSuggestion struct {
	Label            string
	Kind             string
	Description      string
	InsertText       string
	ReplacementStart int
	ReplacementEnd   int
	CanonicalPreview string
	Score            int
}

type CommandHistoryEntry struct {
	Input            string
	CanonicalCommand string
	Status           PortalActionLifecycle
	At               time.Time
}

type CommandResult struct {
	Input               string
	CanonicalTokens     []string
	CanonicalCommand    string
	Classification      CommandClassification
	Status              PortalActionLifecycle
	Summary             string
	Stdout              string
	Stderr              string
	ErrorCode           string
	ErrorMessage        string
	StartedAt           time.Time
	CompletedAt         time.Time
	RefreshScreen       string
	ExecutionDependency ExecutionDependency
	MainAvailability    MainAvailability
}

type CommandRunner interface {
	Preview(context.Context, CommandRequest) (CommandPreview, error)
	Complete(context.Context, CommandCompletionRequest) ([]CommandSuggestion, error)
	Execute(context.Context, CommandRequest) (CommandResult, error)
}

type CommandRequest struct {
	Input               string
	Tokens              []string
	CanonicalTokens     []string
	Classification      CommandClassification
	Confirmed           bool
	CorrelationID       string
	CurrentScreen       string
	SelectedRecord      CommandSelectedRecord
	ExecutionDependency ExecutionDependency
	MainAvailability    MainAvailability
}

type CommandCompletionRequest struct {
	Input          string
	Cursor         int
	CorrelationID  string
	CurrentScreen  string
	SelectedRecord CommandSelectedRecord
}

type CommandSelectedRecord struct {
	Kind  string
	Ref   string
	Label string
}

func (c CommandClassification) portalRisk() PortalActionRisk {
	switch c {
	case CommandClassInspect:
		return ActionRiskInspect
	case CommandClassSafeRun:
		return ActionRiskSafeRun
	case CommandClassSensitive:
		return ActionRiskSensitive
	case CommandClassDangerous:
		return ActionRiskDangerous
	case CommandClassBlocked:
		return ActionRiskBlocked
	default:
		return ActionRiskInspect
	}
}

func isCommandModeInput(input string) bool {
	return strings.HasPrefix(strings.TrimLeft(input, " \t"), "$")
}

func commandRequiresConfirmation(class CommandClassification) bool {
	return class == CommandClassSafeRun || class == CommandClassSensitive
}

func commandBlockedReason(preview CommandPreview) string {
	if preview.ParseError != "" {
		return preview.ParseError
	}
	if preview.BlockedReason != "" {
		return preview.BlockedReason
	}
	if preview.Classification == CommandClassDangerous {
		return "Dangerous commands are blocked in portal command mode for this slice."
	}
	if preview.Classification == CommandClassBlocked {
		return "This command is blocked in portal command mode."
	}
	return ""
}
