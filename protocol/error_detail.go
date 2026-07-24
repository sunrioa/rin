package protocol

import "strings"

// NewErrorDetail constructs an ErrorDetail that is safe to expose on every
// OpenAPI response path, including successful async-job envelopes.
func NewErrorDetail(code, message, field string) *ErrorDetail {
	code = boundedErrorText(code, ErrorCodeMaxLength)
	if !validErrorCode(code) {
		code = "internal_error"
	}
	return &ErrorDetail{
		Code:    code,
		Message: boundedErrorText(message, ErrorMessageMaxLength),
		Field:   boundedErrorText(field, ErrorFieldMaxLength),
	}
}

func validErrorCode(code string) bool {
	if code == "" || code[0] < 'a' || code[0] > 'z' {
		return false
	}
	for _, value := range []byte(code[1:]) {
		if (value < 'a' || value > 'z') &&
			(value < '0' || value > '9') &&
			value != '_' {
			return false
		}
	}
	return true
}

func boundedErrorText(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum])
	}
	return value
}
