package host

import (
	"errors"
	"fmt"
)

// ValidationError identifies the contract field that failed validation.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// IsValidationError reports whether err contains a ValidationError.
func IsValidationError(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
