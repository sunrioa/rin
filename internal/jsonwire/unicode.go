// Package jsonwire contains strict checks needed before Go's JSON decoder
// normalizes malformed wire text.
package jsonwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// Valid reports whether payload is one unambiguous JSON value. In addition to
// syntax and UTF validation, object names must be unique at every nesting
// level. This prevents different JSON implementations from interpreting the
// same wire value with first-wins, last-wins, or reject semantics.
func Valid(payload []byte) bool {
	return Validate(payload) == nil
}

// Validate returns a descriptive error when payload is not one unambiguous
// JSON value suitable for a protocol boundary.
func Validate(payload []byte) error {
	if !utf8.Valid(payload) || !json.Valid(payload) {
		return errors.New("must be valid UTF-8 JSON")
	}
	if !validEscapedUnicode(payload) {
		return errors.New("must contain well-formed escaped Unicode")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := walkValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("must contain exactly one JSON value")
		}
		return fmt.Errorf("has trailing data: %w", err)
	}
	return nil
}

func validEscapedUnicode(payload []byte) bool {
	if !utf8.Valid(payload) || !json.Valid(payload) {
		return false
	}
	inString := false
	for index := 0; index < len(payload); index++ {
		switch payload[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			index++
			if index >= len(payload) {
				return false
			}
			if payload[index] != 'u' {
				continue
			}
			first, ok := escapedCodeUnit(payload, index+1)
			if !ok {
				return false
			}
			index += 4
			if utf16.IsSurrogate(rune(first)) {
				if first < 0xD800 || first > 0xDBFF {
					return false
				}
				if index+6 >= len(payload) ||
					payload[index+1] != '\\' ||
					payload[index+2] != 'u' {
					return false
				}
				second, secondOK := escapedCodeUnit(payload, index+3)
				if !secondOK || second < 0xDC00 || second > 0xDFFF {
					return false
				}
				index += 6
			}
		}
	}
	return !inString
}

func walkValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		names := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok {
				return errors.New("object name must be a string")
			}
			if _, duplicate := names[name]; duplicate {
				return fmt.Errorf("duplicate object name %q", name)
			}
			names[name] = struct{}{}
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		return consumeDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		return consumeDelimiter(decoder, ']')
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func consumeDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != expected {
		return errors.New("JSON container is not closed")
	}
	return nil
}

func escapedCodeUnit(payload []byte, start int) (uint16, bool) {
	if start+4 > len(payload) {
		return 0, false
	}
	var result uint16
	for _, value := range payload[start : start+4] {
		result <<= 4
		switch {
		case value >= '0' && value <= '9':
			result |= uint16(value - '0')
		case value >= 'a' && value <= 'f':
			result |= uint16(value-'a') + 10
		case value >= 'A' && value <= 'F':
			result |= uint16(value-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}
