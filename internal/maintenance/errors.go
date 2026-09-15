package maintenance

import "errors"

var (
	ErrInvalid  = errors.New("maintenance invalid input")
	ErrNotFound = errors.New("maintenance not found")
)
