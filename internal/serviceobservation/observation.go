package serviceobservation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"loom.local/loom/internal/capabilities"
)

const (
	StateUnknown     = "unknown"
	StateRunning     = "running"
	StateStopped     = "stopped"
	StateFailed      = "failed"
	StateUnavailable = "unavailable"
)

type Result struct {
	Operation    string   `json:"operation"`
	Success      bool     `json:"success"`
	ProcessState string   `json:"process_state"`
	Message      string   `json:"message,omitempty"`
	LogLines     []string `json:"log_lines,omitempty"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

func StandardOperation(value string) bool {
	switch value {
	case "status", "start", "stop", "restart", "logs":
		return true
	default:
		return false
	}
}

func Decode(raw json.RawMessage) (Result, error) {
	var result Result
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Result{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Result{}, fmt.Errorf("manager result must contain one JSON object")
	}
	if !StandardOperation(result.Operation) {
		return Result{}, fmt.Errorf("invalid service operation %q", result.Operation)
	}
	if !ValidProcessState(result.ProcessState) {
		return Result{}, fmt.Errorf("invalid observed process state %q", result.ProcessState)
	}
	return result, nil
}

func ValidProcessState(value string) bool {
	switch value {
	case StateUnknown, StateRunning, StateStopped, StateFailed, StateUnavailable:
		return true
	default:
		return false
	}
}

func ProviderHealth(result Result, observedAt time.Time) (capabilities.ProviderHealthInput, error) {
	if !StandardOperation(result.Operation) || !ValidProcessState(result.ProcessState) {
		return capabilities.ProviderHealthInput{}, fmt.Errorf("invalid service manager observation")
	}
	observedAt = observedAt.UTC()
	if observedAt.IsZero() {
		return capabilities.ProviderHealthInput{}, fmt.Errorf("observation timestamp is required")
	}
	health, availability := capabilities.HealthStatusUnknown, capabilities.AvailabilityStatusUnknown
	switch result.ProcessState {
	case StateRunning:
		health, availability = capabilities.HealthStatusOK, capabilities.AvailabilityStatusAvailable
	case StateStopped:
		health, availability = capabilities.HealthStatusDegraded, capabilities.AvailabilityStatusUnavailable
	case StateFailed:
		health, availability = capabilities.HealthStatusUnhealthy, capabilities.AvailabilityStatusUnavailable
	case StateUnavailable:
		health, availability = capabilities.HealthStatusOffline, capabilities.AvailabilityStatusUnavailable
	}
	details, _ := json.Marshal(map[string]any{
		"operation": result.Operation, "process_state": result.ProcessState,
		"observed_at": observedAt.Format(time.RFC3339Nano), "success": result.Success, "truncated": result.Truncated,
	})
	input := capabilities.ProviderHealthInput{
		HealthStatus: health, AvailabilityStatus: availability,
		Message:     fmt.Sprintf("Service process observed as %s after %s.", result.ProcessState, result.Operation),
		DetailsJSON: details, LastCheckedAt: &observedAt,
	}
	if result.ProcessState == StateRunning {
		input.LastOKAt = &observedAt
	}
	return input, nil
}
