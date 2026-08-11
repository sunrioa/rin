package cognition

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	ErrProviderNotFound = errors.New("cognition provider record not found")
	ErrProviderConflict = errors.New("cognition provider record conflicts with existing state")
	ErrProviderCapacity = errors.New("cognition provider capacity exceeded")
)

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
var providerDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxProviderWireInteger = 9_007_199_254_740_991

type ProviderHealth struct {
	Available bool   `json:"available"`
	Degraded  bool   `json:"degraded"`
	Code      string `json:"code,omitempty"`
}

func validateProviderID(field, value string) error {
	if len(value) == 0 || len(value) > 96 || !providerIDPattern.MatchString(value) {
		return fmt.Errorf("%s must be a lowercase safe identifier of at most 96 bytes", field)
	}
	return nil
}

func validateProviderText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be valid UTF-8 without NUL", field)
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s must contain at most %d characters", field, maximum)
	}
	return nil
}

func normalizeProviderIDs(field string, values []string, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("%s must contain at most %d values", field, maximum)
	}
	values = append([]string(nil), values...)
	slices.Sort(values)
	for index, value := range values {
		if err := validateProviderID(fmt.Sprintf("%s[%d]", field, index), value); err != nil {
			return nil, err
		}
		if index > 0 && values[index-1] == value {
			return nil, fmt.Errorf("%s must not contain duplicates", field)
		}
	}
	return values, nil
}

func normalizeProviderTexts(
	field string,
	values []string,
	maximumValues, maximumCharacters int,
) ([]string, error) {
	if len(values) > maximumValues {
		return nil, fmt.Errorf("%s must contain at most %d values", field, maximumValues)
	}
	values = append([]string(nil), values...)
	for index, value := range values {
		if err := validateProviderText(
			fmt.Sprintf("%s[%d]", field, index),
			value,
			maximumCharacters,
			true,
		); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func providerKey(id, version string) string {
	return id + "\x00" + version
}
