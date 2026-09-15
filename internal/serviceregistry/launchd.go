package serviceregistry

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type LaunchdUserRunner struct {
	Command func(context.Context, int, string, ...string) (string, int, error)
	UserID  func() int
}

func (runner LaunchdUserRunner) Run(ctx context.Context, record AllowlistRecord, request ManagerRequest) (ManagerResult, error) {
	if record.Manager != ManagerLaunchd {
		return ManagerResult{}, fmt.Errorf("launchd runner received non-launchd record")
	}
	command := runner.Command
	if command == nil {
		command = runBoundedCommand
	}
	userID := os.Getuid()
	if runner.UserID != nil {
		userID = runner.UserID()
	}
	target := "gui/" + strconv.Itoa(userID) + "/" + record.Unit
	result := ManagerResult{Operation: request.Operation, ProcessState: ProcessStateUnknown}
	if request.Operation == OperationLogs {
		predicate := "process == \"" + record.Unit + "\""
		output, _, err := command(ctx, request.LogMaxBytes, "/usr/bin/log", "show", "--style", "compact", "--last", fmt.Sprintf("%ds", request.LogMaxAgeSec), "--predicate", predicate)
		if err != nil {
			return result, err
		}
		result.Success, result.LogLines, result.Message = true, splitBoundedLines(output, request.LogLines), "bounded service logs"
		return result, nil
	}
	if request.Operation != OperationStatus {
		var args []string
		switch request.Operation {
		case OperationStart:
			args = []string{"kickstart", target}
		case OperationRestart:
			args = []string{"kickstart", "-k", target}
		case OperationStop:
			args = []string{"kill", "SIGTERM", target}
		}
		if _, _, err := command(ctx, 4096, "launchctl", args...); err != nil {
			return result, err
		}
	}
	output, _, err := command(ctx, 8192, "launchctl", "print", target)
	if err != nil {
		classification := classifyLaunchdPrintError(err, record.Unit)
		switch classification {
		case ProcessStateStopped:
			result.Success, result.ProcessState, result.Message = true, ProcessStateStopped, "manager state: unloaded"
			return result, nil
		case ProcessStateUnavailable:
			result.Success, result.ProcessState, result.Message = true, ProcessStateUnavailable, "launchd user domain is unavailable"
			return result, nil
		default:
			return result, err
		}
	}
	switch {
	case strings.Contains(output, "state = running"):
		result.ProcessState = ProcessStateRunning
	case strings.Contains(output, "last exit code =") || strings.Contains(output, "state = exited"):
		result.ProcessState = ProcessStateStopped
	default:
		result.ProcessState = ProcessStateUnknown
	}
	result.Success, result.Message = true, "manager state observed"
	return result, nil
}

func classifyLaunchdPrintError(err error, unit string) ObservedProcessState {
	if err == nil {
		return ProcessStateUnknown
	}
	message := strings.ToLower(err.Error())
	unit = strings.ToLower(unit)
	if strings.Contains(message, "could not find service") && strings.Contains(message, unit) {
		return ProcessStateStopped
	}
	for _, marker := range []string{"could not find domain", "user domain is unavailable", "login session is unavailable", "domain does not exist"} {
		if strings.Contains(message, marker) {
			return ProcessStateUnavailable
		}
	}
	return ProcessStateUnknown
}
