package hostscaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/internal/portablepath"
)

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
		if err := portablepath.ValidateSegment(segment); err != nil {
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
	if portablepath.UTF16Length(target) > portablepath.MaxAbsoluteUTF16 {
		return nil, fmt.Errorf(
			"absolute output path exceeds the portable %d UTF-16 code-unit budget",
			portablepath.MaxAbsoluteUTF16,
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
		if err := portablepath.ValidateProjectPath(target, file.Path); err != nil {
			return err
		}
	}
	return nil
}
