package workers

import "errors"

var (
	ErrInvalid   = errors.New("workers invalid input")
	ErrNotFound  = errors.New("workers not found")
	ErrConflict  = errors.New("workers conflict")
	ErrAmbiguous = errors.New("workers ambiguous reference")
)
