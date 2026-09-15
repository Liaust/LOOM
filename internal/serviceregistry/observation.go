package serviceregistry

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/serviceobservation"
)

func DecodeManagerResult(raw json.RawMessage) (ManagerResult, error) {
	decoded, err := serviceobservation.Decode(raw)
	if err != nil {
		return ManagerResult{}, err
	}
	return ManagerResult{Operation: Operation(decoded.Operation), Success: decoded.Success, ProcessState: ObservedProcessState(decoded.ProcessState), Message: decoded.Message, LogLines: decoded.LogLines, ExitCode: decoded.ExitCode, Truncated: decoded.Truncated}, nil
}

func ProviderHealthFromManagerResult(result ManagerResult, observedAt time.Time) (capabilities.ProviderHealthInput, error) {
	return serviceobservation.ProviderHealth(serviceobservation.Result{Operation: string(result.Operation), Success: result.Success, ProcessState: string(result.ProcessState), Message: result.Message, LogLines: result.LogLines, ExitCode: result.ExitCode, Truncated: result.Truncated}, observedAt)
}
