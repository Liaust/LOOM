package restoreauthority

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type requestHeader struct {
	Schema     string    `json:"schema"`
	Operation  Operation `json:"operation"`
	Kind       Kind      `json:"kind"`
	Database   string    `json:"database"`
	DumpSize   int64     `json:"dump_size"`
	DumpSHA256 string    `json:"dump_sha256"`
}

type requestTrailer struct {
	Schema    string    `json:"schema"`
	Stage     string    `json:"stage"`
	Operation Operation `json:"operation"`
	Kind      Kind      `json:"kind"`
	Database  string    `json:"database"`
}

type controlFrame struct {
	Schema    string    `json:"schema"`
	Action    string    `json:"action"`
	Operation Operation `json:"operation"`
	Kind      Kind      `json:"kind"`
	Database  string    `json:"database"`
}

type responseHeader struct {
	Schema           string       `json:"schema"`
	Status           string       `json:"status"`
	Operation        Operation    `json:"operation"`
	Kind             Kind         `json:"kind"`
	Database         string       `json:"database"`
	FailureStage     FailureStage `json:"failure_stage,omitempty"`
	ErrorCode        string       `json:"error_code,omitempty"`
	Message          string       `json:"message,omitempty"`
	CleanupAttempted bool         `json:"cleanup_attempted"`
	CleanupSucceeded bool         `json:"cleanup_succeeded"`
}

func encodeFrame(writer io.Writer, value any, limit uint32) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) == 0 || uint64(len(raw)) > uint64(limit) {
		return fmt.Errorf("protocol frame exceeds its bound")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	if _, err := writer.Write(prefix[:]); err != nil {
		return err
	}
	_, err = writer.Write(raw)
	return err
}

func decodeFrame(reader io.Reader, value any, limit uint32) error {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return fmt.Errorf("read protocol frame length: %w", err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > limit {
		return fmt.Errorf("protocol frame length is invalid")
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return fmt.Errorf("read protocol frame: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode protocol frame: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("protocol frame contains trailing JSON")
	}
	return nil
}

func operationForKind(kind Kind) (Operation, error) {
	switch kind {
	case KindOperational:
		return OperationRestoreOperational, nil
	case KindProvenance:
		return OperationRestoreProvenance, nil
	default:
		return "", fmt.Errorf("unsupported restore kind")
	}
}

func validateRequestHeader(header requestHeader, maxDumpBytes int64) error {
	if header.Schema != ProtocolSchema {
		return fmt.Errorf("unsupported protocol schema")
	}
	if err := ValidateDisposableDatabase(header.Kind, header.Database); err != nil {
		return err
	}
	switch header.Operation {
	case OperationRestoreOperational:
		if header.Kind != KindOperational {
			return fmt.Errorf("restore operation and kind do not match")
		}
	case OperationRestoreProvenance:
		if header.Kind != KindProvenance {
			return fmt.Errorf("restore operation and kind do not match")
		}
	case OperationDropDisposable:
		if header.DumpSize != 0 || header.DumpSHA256 != "" {
			return fmt.Errorf("drop request contains restore payload metadata")
		}
		return nil
	default:
		return fmt.Errorf("unsupported restore operation")
	}
	if header.DumpSize <= 0 || header.DumpSize > maxDumpBytes || !digestPattern.MatchString(header.DumpSHA256) {
		return fmt.Errorf("restore payload declaration is invalid")
	}
	return nil
}

func validateResponseHeader(response responseHeader, expected requestHeader) error {
	preIdentityFailure := response.Status == "failed" && response.Operation == "" && response.Kind == "" && response.Database == "" &&
		(response.ErrorCode == ErrorSocketIdentityInvalid || response.ErrorCode == ErrorPeerIdentityInvalid)
	if response.Schema != ProtocolSchema || (!preIdentityFailure && (response.Operation != expected.Operation || response.Kind != expected.Kind || response.Database != expected.Database)) {
		return fmt.Errorf("restore authority response identity mismatch")
	}
	if response.Status != "succeeded" && response.Status != "failed" {
		return fmt.Errorf("restore authority response status is invalid")
	}
	if response.Status == "succeeded" && (response.FailureStage != "" || response.ErrorCode != "" || response.Message != "") {
		return fmt.Errorf("successful restore authority response contains an error")
	}
	if response.Status == "succeeded" {
		if expected.Operation == OperationDropDisposable && (!response.CleanupAttempted || !response.CleanupSucceeded) {
			return fmt.Errorf("successful restore authority drop response lacks cleanup evidence")
		}
		if expected.Operation != OperationDropDisposable && (response.CleanupAttempted || response.CleanupSucceeded) {
			return fmt.Errorf("successful restore authority response contains unexpected cleanup evidence")
		}
	}
	if response.Status == "failed" {
		if err := ValidateFailure(response.FailureStage, response.ErrorCode, response.Message); err != nil {
			return err
		}
		if response.CleanupSucceeded && !response.CleanupAttempted {
			return fmt.Errorf("restore authority response contains impossible cleanup evidence")
		}
	}
	return nil
}

func validateRequestTrailer(trailer requestTrailer, expected requestHeader) error {
	if trailer.Schema != ProtocolSchema || trailer.Stage != "request_complete" ||
		trailer.Operation != expected.Operation || trailer.Kind != expected.Kind || trailer.Database != expected.Database {
		return fmt.Errorf("restore authority request trailer identity mismatch")
	}
	return nil
}

func validateControlFrame(control controlFrame, expected requestHeader) error {
	if control.Schema != ProtocolSchema || control.Action != "cancel" ||
		control.Operation != expected.Operation || control.Kind != expected.Kind || control.Database != expected.Database {
		return fmt.Errorf("restore authority control identity mismatch")
	}
	return nil
}

func validateAcceptedHeader(response responseHeader, expected requestHeader) error {
	if response.Schema != ProtocolSchema || response.Status != "accepted" ||
		response.Operation != expected.Operation || response.Kind != expected.Kind || response.Database != expected.Database {
		return fmt.Errorf("restore authority acknowledgment identity mismatch")
	}
	if response.FailureStage != "" || response.ErrorCode != "" || response.Message != "" || response.CleanupAttempted || response.CleanupSucceeded {
		return fmt.Errorf("restore authority acknowledgment contains terminal state")
	}
	return nil
}
