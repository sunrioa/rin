package hostscaffold

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sunrioa/rin/internal/portablepath"
	"github.com/sunrioa/rin/release"
)

const (
	defaultProjectVersion      = "0.1.0"
	maxProjectVersionBytes     = len("65534.65534.65534")
	maxProjectVersionComponent = uint64(65534)
	maxDisplayRunes            = 80
	maxAuthorRunes             = 120
)

var (
	hostIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// Options controls a deterministic Host scaffold render.
type Options struct {
	Host    string
	Runtime string
	ID      string
	Name    string
	Author  string
	Version string
	Output  string
}

type normalizedOptions struct {
	Options
	HostDescriptor HostDescriptor
	RinVersion     string
}

func normalizeOptions(options Options) (normalizedOptions, error) {
	host, err := lookupHost(options.Host)
	if err != nil {
		return normalizedOptions{}, err
	}
	runtimeLanguages := map[string]string{
		"go": "Go", "javascript": "JavaScript", "python": "Python",
		"csharp": "C#", "java": "Java", "lua": "Lua",
	}
	language, ok := runtimeLanguages[options.Runtime]
	if !ok {
		return normalizedOptions{}, errors.New(
			"-runtime must be go, javascript, python, csharp, java, or lua")
	}
	host.Language = language
	host.RuntimePins = []RuntimePin{{Name: "runtime", Version: options.Runtime}}
	if len(options.ID) < 2 || len(options.ID) > 64 || !hostIDPattern.MatchString(options.ID) {
		return normalizedOptions{}, errors.New(
			"-id must be 2-64 lowercase ASCII letters, digits, or single underscores and start with a letter")
	}
	if portablepath.IsWindowsReservedName(options.ID) {
		return normalizedOptions{}, fmt.Errorf("-id %q is reserved on Windows", options.ID)
	}
	if options.Name == "" {
		options.Name = options.ID
	}
	if err := validateDisplayValue("-name", options.Name, maxDisplayRunes); err != nil {
		return normalizedOptions{}, err
	}
	if options.Author != "" {
		if err := validateDisplayValue("-author", options.Author, maxAuthorRunes); err != nil {
			return normalizedOptions{}, err
		}
	}
	if options.Version == "" {
		options.Version = defaultProjectVersion
	}
	if err := validateProjectVersion(options.Version); err != nil {
		return normalizedOptions{}, err
	}
	if options.Output == "" {
		options.Output = options.ID
	}
	return normalizedOptions{
		Options: options, HostDescriptor: host, RinVersion: release.Version,
	}, nil
}

func validateProjectVersion(version string) error {
	if len(version) > maxProjectVersionBytes {
		return fmt.Errorf("-version must contain at most %d ASCII characters", maxProjectVersionBytes)
	}
	if !versionPattern.MatchString(version) {
		return errors.New("-version must use numeric major.minor.patch form")
	}
	for _, component := range strings.Split(version, ".") {
		value, err := strconv.ParseUint(component, 10, 16)
		if err != nil || value > maxProjectVersionComponent {
			return fmt.Errorf("-version components must be between 0 and %d", maxProjectVersionComponent)
		}
	}
	return nil
}

func validateDisplayValue(flagName, value string, maximum int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", flagName)
	}
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("%s must be non-empty and have no leading or trailing whitespace", flagName)
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s must contain at most %d Unicode characters", flagName, maximum)
	}
	for _, character := range value {
		if character == 0 || character == '\n' || character == '\r' ||
			unicode.IsControl(character) || unicode.Is(unicode.Zl, character) ||
			unicode.Is(unicode.Zp, character) {
			return fmt.Errorf("%s must not contain control characters or line breaks", flagName)
		}
	}
	return nil
}
