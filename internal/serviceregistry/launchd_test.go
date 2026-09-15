package serviceregistry

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestLaunchdStatusClassifiesOnlyExpectedMissingAndUnavailable(t *testing.T) {
	record := testAllowlistRecord()
	request := ManagerRequest{AllowlistKey: record.Key, Operation: OperationStatus}
	tests := []struct {
		name      string
		err       error
		wantState ObservedProcessState
		wantError bool
	}{
		{"unloaded label", fmt.Errorf("manager command failed: Could not find service %q in domain for user gui: 501", record.Unit), ProcessStateStopped, false},
		{"no login session", errors.New("manager command failed: Could not find domain for user gui: 501"), ProcessStateUnavailable, false},
		{"timeout", context.DeadlineExceeded, ProcessStateUnknown, true},
		{"permission", errors.New("manager command failed: Operation not permitted"), ProcessStateUnknown, true},
		{"missing executable", errors.New("exec: launchctl: executable file not found"), ProcessStateUnknown, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := LaunchdUserRunner{UserID: func() int { return 501 }, Command: func(context.Context, int, string, ...string) (string, int, error) {
				return "", 1, test.err
			}}
			result, err := runner.Run(context.Background(), record, request)
			if (err != nil) != test.wantError {
				t.Fatalf("err=%v wantError=%t", err, test.wantError)
			}
			if !test.wantError && result.ProcessState != test.wantState {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}
