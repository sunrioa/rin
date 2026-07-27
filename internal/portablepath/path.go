// Package portablepath validates relative project paths against the common
// filesystem subset used by Rin-generated hosts on macOS, Linux, and Windows.
package portablepath

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	MaxSegmentUTF16  = 255
	MaxAbsoluteUTF16 = 240
)

var windowsReservedNames = func() map[string]struct{} {
	names := map[string]struct{}{
		"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	}
	for index := 1; index <= 9; index++ {
		names[fmt.Sprintf("COM%d", index)] = struct{}{}
		names[fmt.Sprintf("LPT%d", index)] = struct{}{}
	}
	for _, suffix := range []string{"¹", "²", "³"} {
		names["COM"+suffix] = struct{}{}
		names["LPT"+suffix] = struct{}{}
	}
	return names
}()

// ValidateRelative checks a non-empty slash-separated project path.
func ValidateRelative(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("path must be valid UTF-8")
	}
	if value == "" || strings.Contains(value, `\`) {
		return errors.New("path must be a non-empty slash-separated relative path")
	}
	if strings.HasPrefix(value, "/") || path.IsAbs(value) ||
		path.Clean(value) != value {
		return fmt.Errorf("unsafe relative path %q", value)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe relative path %q", value)
		}
		if err := ValidateSegment(segment); err != nil {
			return fmt.Errorf("unsafe relative path %q: %w", value, err)
		}
	}
	return nil
}

// ValidateProjectPath also applies the conservative Windows absolute-path
// budget used by the scaffold generator.
func ValidateProjectPath(root string, relative string) error {
	if err := ValidateRelative(relative); err != nil {
		return err
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	if !utf8.ValidString(candidate) {
		return fmt.Errorf("project path %q is not valid UTF-8", relative)
	}
	if UTF16Length(candidate) > MaxAbsoluteUTF16 {
		return fmt.Errorf(
			"project path %q exceeds the portable %d UTF-16 code-unit absolute-path budget",
			relative,
			MaxAbsoluteUTF16,
		)
	}
	return nil
}

// ValidateSegment checks one path component against Windows device-name,
// character, trailing-character, and UTF-16 limits.
func ValidateSegment(segment string) error {
	if !utf8.ValidString(segment) {
		return errors.New("path segment must be valid UTF-8")
	}
	if strings.TrimRight(segment, " .") != segment {
		return errors.New("path segment must not end in a space or period")
	}
	if strings.ContainsAny(segment, `<>:"/\|?*`) {
		return errors.New("path segment contains a character invalid on Windows")
	}
	for _, character := range segment {
		if character < 32 {
			return errors.New("path segment contains a control character")
		}
	}
	if IsWindowsReservedName(segment) {
		return fmt.Errorf("%q is a Windows reserved device name", segment)
	}
	if UTF16Length(segment) > MaxSegmentUTF16 {
		return fmt.Errorf(
			"path segment exceeds the portable %d UTF-16 code-unit limit",
			MaxSegmentUTF16,
		)
	}
	return nil
}

func IsWindowsReservedName(name string) bool {
	base := name
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	_, reserved := windowsReservedNames[strings.ToUpper(base)]
	return reserved
}

func UTF16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}
