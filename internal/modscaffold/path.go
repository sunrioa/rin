package modscaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxPortablePathSegmentUTF16 = 255
	maxPortableAbsoluteUTF16    = 240
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

func validateTemplatePath(templatePath string) error {
	if !utf8.ValidString(templatePath) {
		return errors.New("template path must be valid UTF-8")
	}
	if templatePath == "" || strings.Contains(templatePath, `\`) {
		return errors.New("template path must be a non-empty slash-separated relative path")
	}
	if strings.HasPrefix(templatePath, "/") || path.IsAbs(templatePath) ||
		path.Clean(templatePath) != templatePath {
		return fmt.Errorf("unsafe template path %q", templatePath)
	}
	segments := strings.Split(templatePath, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe template path %q", templatePath)
		}
		if err := validatePortablePathSegment(segment); err != nil {
			return fmt.Errorf("unsafe template path %q: %w", templatePath, err)
		}
	}
	return nil
}

func validatePortablePathSegment(segment string) error {
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
	if isWindowsReservedName(segment) {
		return fmt.Errorf("%q is a Windows reserved device name", segment)
	}
	if utf16Length(segment) > maxPortablePathSegmentUTF16 {
		return fmt.Errorf(
			"path segment exceeds the portable %d UTF-16 code-unit limit",
			maxPortablePathSegmentUTF16,
		)
	}
	return nil
}

func isWindowsReservedName(name string) bool {
	base := name
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	_, reserved := windowsReservedNames[strings.ToUpper(base)]
	return reserved
}

type outputLocation struct {
	absolute    string
	name        string
	parent      *os.Root
	cwdPath     string
	cwdIdentity fs.FileInfo
	roots       []*os.Root
	ancestors   []directoryBinding
}

type directoryBinding struct {
	parent   *os.Root
	name     string
	child    *os.Root
	identity fs.FileInfo
}

func (location *outputLocation) close() (resultErr error) {
	if location == nil {
		return nil
	}
	for index := len(location.roots) - 1; index >= 0; index-- {
		resultErr = errors.Join(resultErr, location.roots[index].Close())
	}
	location.roots = nil
	location.parent = nil
	return resultErr
}

func (location *outputLocation) verifyAncestors() (resultErr error) {
	freshCWD, err := os.OpenRoot(location.cwdPath)
	if err != nil {
		return fmt.Errorf("reopen current directory: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, freshCWD.Close())
	}()
	currentCWD, err := freshCWD.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect current directory: %w", err)
	}
	if location.cwdIdentity == nil ||
		!os.SameFile(location.cwdIdentity, currentCWD) {
		return errors.New("current directory was replaced during generation")
	}
	for _, binding := range location.ancestors {
		if err := verifyDirectoryBinding(
			binding.parent,
			binding.name,
			binding.child,
			binding.identity,
		); err != nil {
			return fmt.Errorf("output ancestor %q: %w", binding.name, err)
		}
	}
	return nil
}

func openOutputLocation(cwd, output string) (
	location *outputLocation,
	resultErr error,
) {
	if output == "" {
		return nil, errors.New("output path is required")
	}
	if !utf8.ValidString(output) {
		return nil, errors.New("-output must be valid UTF-8")
	}
	if strings.Contains(output, `\`) {
		return nil, errors.New("-output must use forward slashes on every platform")
	}
	if filepath.IsAbs(output) || path.IsAbs(output) || filepath.VolumeName(output) != "" {
		return nil, errors.New("-output must be relative to the current directory")
	}
	normalizedInput := output
	if strings.HasPrefix(normalizedInput, "./") {
		normalizedInput = strings.TrimPrefix(normalizedInput, "./")
	}
	if normalizedInput == "" {
		return nil, errors.New("-output must name a new directory")
	}
	for _, segment := range strings.Split(normalizedInput, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, errors.New("-output must not contain empty, dot, or parent segments")
		}
	}
	cleanSlash := path.Clean(normalizedInput)
	if cleanSlash == "." || cleanSlash == ".." || strings.HasPrefix(cleanSlash, "../") {
		return nil, errors.New("-output must stay inside the current directory")
	}
	segments := strings.Split(cleanSlash, "/")
	for _, segment := range segments {
		if err := validatePortablePathSegment(segment); err != nil {
			return nil, fmt.Errorf("invalid -output: %w", err)
		}
	}
	cwdAbsolute, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve current directory: %w", err)
	}
	if !utf8.ValidString(cwdAbsolute) {
		return nil, errors.New("current directory must be valid UTF-8")
	}
	target := filepath.Join(cwdAbsolute, filepath.FromSlash(cleanSlash))
	if utf16Length(target) > maxPortableAbsoluteUTF16 {
		return nil, fmt.Errorf(
			"absolute output path exceeds the portable %d UTF-16 code-unit budget",
			maxPortableAbsoluteUTF16,
		)
	}

	current, err := os.OpenRoot(cwdAbsolute)
	if err != nil {
		return nil, fmt.Errorf("open current directory root: %w", err)
	}
	cwdIdentity, err := current.Stat(".")
	if err != nil {
		_ = current.Close()
		return nil, fmt.Errorf("inspect current directory root: %w", err)
	}
	roots := []*os.Root{current}
	var ancestors []directoryBinding
	defer func() {
		if resultErr != nil {
			for index := len(roots) - 1; index >= 0; index-- {
				resultErr = errors.Join(resultErr, roots[index].Close())
			}
		}
	}()
	for _, segment := range segments[:len(segments)-1] {
		next, identity, openErr := openBoundDirectory(current, segment)
		if openErr != nil {
			return nil, fmt.Errorf("output parent %q: %w", segment, openErr)
		}
		roots = append(roots, next)
		ancestors = append(ancestors, directoryBinding{
			parent: current, name: segment, child: next, identity: identity,
		})
		current = next
	}

	entries, err := fs.ReadDir(current.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read output parent: %w", err)
	}
	targetName := segments[len(segments)-1]
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), targetName) {
			return nil, fmt.Errorf("output %q already exists or collides by case", output)
		}
	}
	location = &outputLocation{
		absolute:    target,
		name:        targetName,
		parent:      current,
		cwdPath:     cwdAbsolute,
		cwdIdentity: cwdIdentity,
		roots:       roots,
		ancestors:   ancestors,
	}
	return location, nil
}

func openRealDirectory(parent *os.Root, name string) (*os.Root, error) {
	child, _, err := openBoundDirectory(parent, name)
	return child, err
}

func openBoundDirectory(
	parent *os.Root,
	name string,
) (*os.Root, fs.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("symbolic link ancestors are not allowed")
	}
	if !before.IsDir() {
		return nil, nil, errors.New("ancestor is not a directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	after, err := parent.Lstat(name)
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 ||
		!after.IsDir() ||
		!os.SameFile(before, opened) ||
		!os.SameFile(after, opened) {
		_ = child.Close()
		return nil, nil, errors.New("output ancestor changed while it was opened")
	}
	return child, before, nil
}

func validateAbsolutePlanPaths(target string, files []renderedFile) error {
	for _, file := range files {
		candidate := filepath.Join(target, filepath.FromSlash(file.Path))
		if !utf8.ValidString(candidate) {
			return fmt.Errorf("generated path %q is not valid UTF-8", file.Path)
		}
		if utf16Length(candidate) > maxPortableAbsoluteUTF16 {
			return fmt.Errorf(
				"generated path %q exceeds the portable %d UTF-16 code-unit absolute-path budget",
				file.Path,
				maxPortableAbsoluteUTF16,
			)
		}
	}
	return nil
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}
