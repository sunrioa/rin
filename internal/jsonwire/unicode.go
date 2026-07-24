// Package jsonwire contains strict checks needed before Go's JSON decoder
// normalizes malformed wire text.
package jsonwire

import (
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// Valid reports whether payload is syntactically valid JSON encoded as UTF-8
// and every escaped UTF-16 surrogate is a well-formed pair. encoding/json
// intentionally replaces malformed UTF-8 and unpaired surrogates with U+FFFD;
// wire boundaries use this check first so malformed input cannot be silently
// rewritten into a different valid value.
func Valid(payload []byte) bool {
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
