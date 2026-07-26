package modscaffold

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	sdkassets "github.com/sunrioa/rin/sdk"
)

const (
	defaultProjectVersion      = "0.1.0"
	maxProjectVersionBytes     = len("65534.65534.65534")
	maxProjectVersionComponent = uint64(65534)
	maxDisplayRunes            = 80
	maxAuthorRunes             = 120
	maxNamespaceBytes          = 200
)

var (
	modIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	namespaceSegment = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	luantiAuthor     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	versionPattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

var javaKeywords = map[string]struct{}{
	"_": {}, "abstract": {}, "assert": {}, "boolean": {}, "break": {},
	"byte": {}, "case": {}, "catch": {}, "char": {}, "class": {},
	"const": {}, "continue": {}, "default": {}, "do": {}, "double": {},
	"else": {}, "enum": {}, "exports": {}, "extends": {}, "final": {},
	"finally": {}, "float": {}, "for": {}, "goto": {}, "if": {},
	"implements": {}, "import": {}, "instanceof": {}, "int": {},
	"interface": {}, "long": {}, "module": {}, "native": {}, "new": {},
	"non-sealed": {}, "open": {}, "opens": {}, "package": {}, "permits": {},
	"private": {}, "protected": {}, "provides": {}, "public": {},
	"record": {}, "requires": {}, "return": {}, "sealed": {}, "short": {},
	"static": {}, "strictfp": {}, "super": {}, "switch": {},
	"synchronized": {}, "this": {}, "throw": {}, "throws": {},
	"to": {}, "transient": {}, "transitive": {}, "try": {}, "uses": {},
	"var": {}, "void": {}, "volatile": {}, "when": {}, "while": {},
	"with": {}, "yield": {},
}

// Options controls a deterministic Mod scaffold render.
type Options struct {
	Host      string
	ID        string
	Name      string
	Namespace string
	Author    string
	Version   string
	Output    string
}

type normalizedOptions struct {
	Options
	HostDescriptor HostDescriptor
	RinVersion     string
	JavaPackage    string
	CodeName       string
	PluginGUID     string
	CommandName    string
}

func normalizeOptions(options Options) (normalizedOptions, error) {
	host, err := lookupHost(options.Host)
	if err != nil {
		return normalizedOptions{}, err
	}
	if len(options.ID) < 2 || len(options.ID) > 64 || !modIDPattern.MatchString(options.ID) {
		return normalizedOptions{}, errors.New(
			"-id must be 2-64 lowercase ASCII letters, digits, or single underscores and start with a letter")
	}
	if isWindowsReservedName(options.ID) {
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
		if host.ID == HostLuanti && !luantiAuthor.MatchString(options.Author) {
			return normalizedOptions{}, errors.New(
				"-author for Luanti must be a 1-64 character ContentDB username using only ASCII letters, digits, underscores, or hyphens")
		}
	}
	if options.Version == "" {
		options.Version = defaultProjectVersion
	}
	if err := validateProjectVersion(options.Version); err != nil {
		return normalizedOptions{}, err
	}
	namespace := options.Namespace
	if host.RequiresNamespace {
		if err := validateNamespace(namespace, options.ID); err != nil {
			return normalizedOptions{}, err
		}
	} else if namespace != "" {
		return normalizedOptions{}, fmt.Errorf("-namespace is not used by host %q", host.ID)
	}
	if options.Output == "" {
		options.Output = options.ID
	}
	codeName := pascalIdentifier(options.ID)
	result := normalizedOptions{
		Options:        options,
		HostDescriptor: host,
		RinVersion:     sdkassets.Version,
		CodeName:       codeName,
		CommandName:    strings.ReplaceAll(options.ID, "_", "-"),
	}
	switch host.ID {
	case HostFabric:
		result.JavaPackage = namespace + "." + options.ID
	case HostBepInExMono, HostBepInExIL2CPP:
		result.PluginGUID = namespace + "." + strings.ReplaceAll(options.ID, "_", "-")
	}
	return result, nil
}

func validateProjectVersion(version string) error {
	if len(version) > maxProjectVersionBytes {
		return fmt.Errorf("-version must contain at most %d ASCII characters",
			maxProjectVersionBytes)
	}
	if !versionPattern.MatchString(version) {
		return errors.New("-version must use numeric major.minor.patch form")
	}
	for _, component := range strings.Split(version, ".") {
		value, err := strconv.ParseUint(component, 10, 16)
		if err != nil || value > maxProjectVersionComponent {
			return fmt.Errorf("-version components must be between 0 and %d",
				maxProjectVersionComponent)
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

func validateNamespace(namespace, id string) error {
	if namespace == "" {
		return errors.New("-namespace is required for this host")
	}
	if len(namespace) > maxNamespaceBytes {
		return fmt.Errorf("-namespace must contain at most %d bytes", maxNamespaceBytes)
	}
	segments := strings.Split(namespace, ".")
	if len(segments) < 2 {
		return errors.New("-namespace must contain at least two lowercase reverse-domain segments")
	}
	for _, segment := range append(append([]string(nil), segments...), id) {
		if !namespaceSegment.MatchString(segment) {
			return errors.New(
				"-namespace segments must start with a lowercase ASCII letter and contain only lowercase letters, digits, or underscores")
		}
		if _, reserved := javaKeywords[segment]; reserved {
			return fmt.Errorf("Java package segment %q is reserved", segment)
		}
		if isWindowsReservedName(segment) {
			return fmt.Errorf("package segment %q is reserved on Windows", segment)
		}
	}
	return nil
}

func pascalIdentifier(id string) string {
	var builder strings.Builder
	for _, part := range strings.Split(id, "_") {
		builder.WriteString(strings.ToUpper(part[:1]))
		builder.WriteString(part[1:])
	}
	return builder.String()
}
