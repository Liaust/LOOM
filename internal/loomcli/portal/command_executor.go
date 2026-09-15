package portal

import (
	"context"
	"time"
)

func ExecutePortalCommand(ctx context.Context, runner CommandRunner, request CommandRequest) (CommandResult, error) {
	preview := applyMainAvailabilityToCommandPreview(BuildCommandPreview(request.Input), request.MainAvailability)
	request.Tokens = append([]string{}, preview.CanonicalTokens...)
	request.CanonicalTokens = append([]string{}, preview.CanonicalTokens...)
	request.Classification = preview.Classification
	request.ExecutionDependency = preview.ExecutionDependency
	if reason := commandBlockedReason(preview); reason != "" {
		return CommandResult{
			Input:               request.Input,
			CanonicalTokens:     append([]string{}, request.CanonicalTokens...),
			CanonicalCommand:    commandString(request.CanonicalTokens),
			Classification:      preview.Classification,
			ExecutionDependency: preview.ExecutionDependency,
			MainAvailability:    request.MainAvailability,
			Status:              ActionLifecycleFailed,
			Summary:             "Command is blocked.",
			ErrorCode:           "portal.command_blocked",
			ErrorMessage:        reason,
			StartedAt:           time.Now().UTC(),
			CompletedAt:         time.Now().UTC(),
		}, nil
	}
	if preview.Classification == CommandClassDangerous || preview.Classification == CommandClassBlocked {
		return CommandResult{
			Input:               request.Input,
			CanonicalTokens:     append([]string{}, request.CanonicalTokens...),
			CanonicalCommand:    commandString(request.CanonicalTokens),
			Classification:      preview.Classification,
			ExecutionDependency: preview.ExecutionDependency,
			MainAvailability:    request.MainAvailability,
			Status:              ActionLifecycleFailed,
			Summary:             "Command is blocked.",
			ErrorCode:           "portal.command_blocked",
			ErrorMessage:        commandBlockedReason(preview),
			StartedAt:           time.Now().UTC(),
			CompletedAt:         time.Now().UTC(),
		}, nil
	}
	if request.MainAvailability.State == MainAvailabilityOffline && preview.ExecutionDependency == ExecutionDependencyMain {
		now := time.Now().UTC()
		return CommandResult{
			Input:               request.Input,
			CanonicalTokens:     append([]string{}, preview.CanonicalTokens...),
			CanonicalCommand:    preview.CanonicalCommand,
			Classification:      preview.Classification,
			ExecutionDependency: preview.ExecutionDependency,
			MainAvailability:    request.MainAvailability,
			Status:              ActionLifecycleFailed,
			Summary:             "Command is unavailable while main is offline.",
			ErrorCode:           "portal.main_offline",
			ErrorMessage:        "Main is offline. This command requires the main node.",
			StartedAt:           now,
			CompletedAt:         now,
		}, nil
	}
	if commandRequiresConfirmation(preview.Classification) && !request.Confirmed {
		return CommandResult{}, ErrCommandConfirmationRequired
	}
	if runner == nil {
		return CommandResult{}, ErrCommandRunnerMissing
	}
	result, err := runner.Execute(ctx, request)
	if result.Input == "" {
		result.Input = request.Input
	}
	if len(result.CanonicalTokens) == 0 {
		result.CanonicalTokens = append([]string{}, request.CanonicalTokens...)
	}
	if result.CanonicalCommand == "" {
		result.CanonicalCommand = commandString(result.CanonicalTokens)
	}
	if result.Classification == "" {
		result.Classification = preview.Classification
	}
	result.ExecutionDependency = preview.ExecutionDependency
	result.MainAvailability = request.MainAvailability
	return result, err
}
