package portal

import (
	"errors"
	"fmt"
	"strings"
)

var ErrMissingClient = errors.New("portal client is required")

func ErrMissingClientFor(operation string) error {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return ErrMissingClient
	}
	return fmt.Errorf("%w: %s", ErrMissingClient, operation)
}

var ErrCommandRunnerMissing = errors.New("portal command runner is required")

var ErrActionConfirmationRequired = errors.New("portal action confirmation is required")

type StartupError struct {
	Cause error
}

func (e *StartupError) Error() string {
	if e == nil || e.Cause == nil {
		return "portal application failed to start"
	}
	return e.Cause.Error()
}

func (e *StartupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsStartupError(err error) bool {
	var startupErr *StartupError
	return errors.As(err, &startupErr)
}

func ErrActionNotFound(actionID string) error {
	return fmt.Errorf("portal action %q is not registered", actionID)
}

func ErrActionNotFoundOnScreen(actionID string, screen string) error {
	screen = NormalizeScreen(screen)
	if strings.TrimSpace(screen) == "" {
		return ErrActionNotFound(actionID)
	}
	return fmt.Errorf("portal action %q is not registered for screen %q", actionID, screen)
}
