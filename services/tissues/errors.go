package tissues

import "errors"

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid operation")
	ErrConflict = errors.New("conflict")
	ErrInternal = errors.New("persistence failure")
	ErrTooLarge = errors.New("payload too large")
)
