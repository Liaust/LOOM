package loomerrors

import "fmt"

type Error struct {
	Code    string
	Summary string
	Domain  string
	Target  string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Summary, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Summary)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func New(code, domain, target, summary string) *Error {
	return &Error{
		Code:    code,
		Domain:  domain,
		Target:  target,
		Summary: summary,
	}
}

func Wrap(code, domain, target, summary string, cause error) *Error {
	return &Error{
		Code:    code,
		Domain:  domain,
		Target:  target,
		Summary: summary,
		Cause:   cause,
	}
}
