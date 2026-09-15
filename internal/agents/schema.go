package agents

func ValidAccessSessionStatus(value string) bool {
	switch value {
	case AccessSessionStatusActive,
		AccessSessionStatusCompleted,
		AccessSessionStatusCancelled,
		AccessSessionStatusFailed,
		AccessSessionStatusExpired,
		AccessSessionStatusRevoked:
		return true
	default:
		return false
	}
}

func ValidWorkContextStatus(value string) bool {
	switch value {
	case WorkContextStatusActive,
		WorkContextStatusCompleted,
		WorkContextStatusCancelled,
		WorkContextStatusFailed,
		WorkContextStatusExpired,
		WorkContextStatusRevoked:
		return true
	default:
		return false
	}
}

func ValidToolViewStatus(value string) bool {
	switch value {
	case ToolViewStatusActive, ToolViewStatusInvalidated, ToolViewStatusExpired:
		return true
	default:
		return false
	}
}

func ValidEntryKind(value string) bool {
	switch value {
	case EntryKindOperatingTool, EntryKindCapability:
		return true
	default:
		return false
	}
}

func ValidVisibilityState(value string) bool {
	switch value {
	case VisibilityVisible, VisibilityRequestable, VisibilityRedacted:
		return true
	default:
		return false
	}
}

func ValidSourceLayer(value string) bool {
	switch value {
	case SourceLayerOperating,
		SourceLayerHome,
		SourceLayerCurrentScope,
		SourceLayerMounted,
		SourceLayerTaskMount,
		SourceLayerGrant,
		SourceLayerSearch:
		return true
	default:
		return false
	}
}

func ValidOriginKind(value string) bool {
	switch value {
	case OriginKindLocalCLI, OriginKindRemoteAPI, OriginKindExternalClient, OriginKindService, OriginKindManual:
		return true
	default:
		return false
	}
}

func ValidAgentToolCallStatus(value string) bool {
	switch value {
	case AgentToolCallStatusPlanned,
		AgentToolCallStatusApprovalRequired,
		AgentToolCallStatusDispatched,
		AgentToolCallStatusCompleted,
		AgentToolCallStatusFailed,
		AgentToolCallStatusDenied:
		return true
	default:
		return false
	}
}

func ValidWorklogEntryKind(value string) bool {
	switch value {
	case WorklogEntryKindNote,
		WorklogEntryKindObservation,
		WorklogEntryKindDecision,
		WorklogEntryKindResult,
		WorklogEntryKindArtifactRef:
		return true
	default:
		return false
	}
}
