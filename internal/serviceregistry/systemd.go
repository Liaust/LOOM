package serviceregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type HelperClientRunner struct {
	Path          string
	AllowlistPath string
}

const (
	maximumHelperResponseBytes = 64 * 1024
	maximumHelperErrorBytes    = 4 * 1024
)

func (runner HelperClientRunner) Run(ctx context.Context, _ AllowlistRecord, request ManagerRequest) (ManagerResult, error) {
	if strings.TrimSpace(runner.Path) == "" || strings.TrimSpace(runner.AllowlistPath) == "" {
		return ManagerResult{}, fmt.Errorf("systemd helper is not configured")
	}
	payload, _ := json.Marshal(request)
	command := exec.CommandContext(ctx, runner.Path, "--allowlist", runner.AllowlistPath)
	command.Stdin = bytes.NewReader(payload)
	stdout := limitedBuffer{limit: maximumHelperResponseBytes}
	stderr := limitedBuffer{limit: maximumHelperErrorBytes}
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return ManagerResult{}, fmt.Errorf("helper rejected request: %s", redactManagerText(stderr.String(), 1024))
	}
	if stdout.truncated || stderr.truncated {
		return ManagerResult{}, fmt.Errorf("helper response exceeded bounded output limits")
	}
	result, err := DecodeManagerResult(stdout.Bytes())
	if err != nil {
		return ManagerResult{}, fmt.Errorf("helper returned malformed response")
	}
	if result.Operation != request.Operation {
		return ManagerResult{}, fmt.Errorf("helper response operation mismatch")
	}
	return result, nil
}

type SystemdCommandRunner struct{}

func (SystemdCommandRunner) Run(ctx context.Context, record AllowlistRecord, request ManagerRequest) (ManagerResult, error) {
	if record.Manager != ManagerSystemd {
		return ManagerResult{}, fmt.Errorf("systemd runner received non-systemd record")
	}
	result := ManagerResult{Operation: request.Operation, ProcessState: ProcessStateUnknown}
	if request.Operation == OperationLogs {
		args := []string{"--unit", record.Unit, "--no-pager", "--output", "short-iso", "--lines", strconv.Itoa(request.LogLines), "--since", fmt.Sprintf("-%ds", request.LogMaxAgeSec)}
		output, _, err := runBoundedCommand(ctx, request.LogMaxBytes, "journalctl", args...)
		if err != nil {
			return result, err
		}
		result.Success, result.LogLines, result.Message = true, splitBoundedLines(output, request.LogLines), "bounded service logs"
		return result, nil
	}
	if request.Operation != OperationStatus {
		verb := map[Operation]string{OperationStart: "start", OperationStop: "stop", OperationRestart: "restart"}[request.Operation]
		if _, _, err := runBoundedCommand(ctx, 4096, "systemctl", verb, record.Unit); err != nil {
			return result, err
		}
	}
	output, exitCode, err := runBoundedCommand(ctx, 4096, "systemctl", "is-active", record.Unit)
	state := strings.TrimSpace(output)
	if err != nil && exitCode != 3 {
		return result, err
	}
	switch state {
	case "active", "activating", "reloading":
		result.ProcessState = ProcessStateRunning
	case "failed":
		result.ProcessState = ProcessStateFailed
	case "inactive", "deactivating":
		result.ProcessState = ProcessStateStopped
	default:
		result.ProcessState = ProcessStateUnknown
	}
	result.Success, result.Message = err == nil || exitCode == 3, "manager state: "+state
	return result, nil
}

func runBoundedCommand(ctx context.Context, limit int, name string, args ...string) (string, int, error) {
	if limit <= 0 {
		limit = 4096
	}
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = limit, 4096
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		return stdout.String(), exitCode, fmt.Errorf("manager command failed: %s", stderr.String())
	}
	return stdout.String(), exitCode, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *limitedBuffer) Write(payload []byte) (int, error) {
	total := len(payload)
	remaining := buffer.limit - buffer.Len()
	if remaining > 0 {
		if remaining > total {
			remaining = total
		}
		_, _ = buffer.Buffer.Write(payload[:remaining])
	}
	if total > remaining {
		buffer.truncated = true
	}
	return total, nil
}
func splitBoundedLines(value string, limit int) []string {
	values := strings.Split(strings.TrimRight(value, "\n"), "\n")
	if len(values) == 1 && values[0] == "" {
		return []string{}
	}
	if limit > 0 && len(values) > limit {
		values = values[len(values)-limit:]
	}
	return values
}
