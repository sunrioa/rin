package hostscaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

const incompleteMarker = ".rin-scaffold.incomplete"

// Generate renders and writes a project below the process working directory.
func Generate(options Options) (Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Result{}, fmt.Errorf("read current directory: %w", err)
	}
	return GenerateAt(cwd, options)
}

// GenerateAt is equivalent to Generate with an explicit trusted working
// directory. The requested Output must still be relative and contained by it.
func GenerateAt(cwd string, options Options) (Result, error) {
	plan, err := Render(options)
	if err != nil {
		return Result{}, err
	}
	return plan.writeAt(cwd)
}

// ValidateTargetAt performs the same path, collision, and symlink checks used
// by Write without creating anything.
func (plan *Plan) ValidateTargetAt(cwd string) (resultErr error) {
	location, err := openOutputLocation(cwd, plan.options.Output)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, location.close())
	}()
	if err := validateAbsolutePlanPaths(location.absolute, plan.files); err != nil {
		return err
	}
	return location.verifyAncestors()
}

func (plan *Plan) writeAt(cwd string) (result Result, resultErr error) {
	return plan.writeAtWithHooks(cwd, writeHooks{})
}

type writeHooks struct {
	afterTargetOpen func(target string) error
	beforeFile      func(target, relative string, index int) error
}

func (plan *Plan) writeAtWithHooks(
	cwd string,
	hooks writeHooks,
) (result Result, resultErr error) {
	location, err := openOutputLocation(cwd, plan.options.Output)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, location.close())
	}()
	if err := validateAbsolutePlanPaths(location.absolute, plan.files); err != nil {
		return Result{}, err
	}
	if err := location.verifyAncestors(); err != nil {
		return Result{}, fmt.Errorf("verify output ancestors before creation: %w", err)
	}
	if err := location.parent.Mkdir(location.name, 0o700); err != nil {
		return Result{}, fmt.Errorf("create output directory: %w", err)
	}
	targetRoot, targetIdentity, err := openCreatedDirectory(
		location.parent, location.name)
	if err != nil {
		return Result{}, fmt.Errorf("open created output directory: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, targetRoot.Close())
	}()
	if hooks.afterTargetOpen != nil {
		if err := hooks.afterTargetOpen(location.absolute); err != nil {
			return Result{}, err
		}
	}
	markerIdentity, err := writeRootFile(targetRoot, incompleteMarker, 0o644, []byte(
		"This directory is incomplete. Do not build or install it.\n",
	))
	if err != nil {
		return Result{}, fmt.Errorf("create incomplete marker: %w", err)
	}

	files := append([]renderedFile(nil), plan.files...)
	sort.SliceStable(files, func(left, right int) bool {
		if files[left].Path == manifestPath {
			return false
		}
		if files[right].Path == manifestPath {
			return true
		}
		return files[left].Path < files[right].Path
	})
	created := make(map[string]fs.FileInfo, len(files))
	for index, file := range files {
		parentRoot, closeParent, err := openOrCreateFileParent(
			targetRoot, path.Dir(file.Path))
		if err != nil {
			return Result{}, fmt.Errorf("prepare %s: %w", file.Path, err)
		}
		if hooks.beforeFile != nil {
			err = hooks.beforeFile(location.absolute, file.Path, index)
		}
		var identity fs.FileInfo
		if err == nil {
			identity, err = writeRootFile(
				parentRoot, path.Base(file.Path), file.Mode, file.Data)
		}
		if closeParent {
			err = errors.Join(err, parentRoot.Close())
		}
		if err != nil {
			return Result{}, fmt.Errorf("write %s: %w", file.Path, err)
		}
		created[file.Path] = identity
	}
	for _, file := range files {
		if err := verifyCreatedFile(targetRoot, file.Path, created[file.Path]); err != nil {
			return Result{}, fmt.Errorf("verify %s: %w", file.Path, err)
		}
	}
	if err := location.verifyAncestors(); err != nil {
		return Result{}, fmt.Errorf("verify output ancestors before finalization: %w", err)
	}
	if err := verifyDirectoryBinding(
		location.parent, location.name, targetRoot, targetIdentity); err != nil {
		return Result{}, fmt.Errorf("verify output directory: %w", err)
	}
	if err := chmodRootDirectory(targetRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("set output directory mode: %w", err)
	}
	if err := verifyCreatedFile(
		targetRoot, incompleteMarker, markerIdentity); err != nil {
		return Result{}, fmt.Errorf("verify incomplete marker: %w", err)
	}
	if err := targetRoot.Remove(incompleteMarker); err != nil {
		return Result{}, fmt.Errorf("finalize scaffold: %w", err)
	}
	finalBindingErr := location.verifyAncestors()
	if finalBindingErr == nil {
		finalBindingErr = verifyDirectoryBinding(
			location.parent, location.name, targetRoot, targetIdentity)
	}
	if finalBindingErr != nil {
		_, markerErr := writeRootFile(targetRoot, incompleteMarker, 0o644, []byte(
			"This directory is incomplete. Do not build or install it.\n",
		))
		return Result{}, fmt.Errorf(
			"verify finalized output directory: %w",
			errors.Join(finalBindingErr, markerErr),
		)
	}
	return Result{
		Root: location.absolute, Host: cloneHost(plan.options.HostDescriptor),
		ID: plan.options.ID, Name: plan.options.Name, Version: plan.options.Version,
		FileCount: len(plan.files),
	}, nil
}

func openCreatedDirectory(
	parent *os.Root,
	name string,
) (*os.Root, fs.FileInfo, error) {
	identity, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if identity.Mode()&os.ModeSymlink != 0 || !identity.IsDir() {
		return nil, nil, errors.New("created path is not a real directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	if err := verifyDirectoryBinding(parent, name, child, identity); err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	return child, identity, nil
}

func openOrCreateFileParent(
	root *os.Root,
	directory string,
) (*os.Root, bool, error) {
	if directory == "." {
		return root, false, nil
	}
	current := root
	closeCurrent := false
	for _, segment := range strings.Split(directory, "/") {
		identity, err := current.Lstat(segment)
		if errors.Is(err, fs.ErrNotExist) {
			if err := current.Mkdir(segment, 0o700); err != nil {
				if closeCurrent {
					_ = current.Close()
				}
				return nil, false, err
			}
			identity, err = current.Lstat(segment)
		}
		if err != nil {
			if closeCurrent {
				_ = current.Close()
			}
			return nil, false, err
		}
		if identity.Mode()&os.ModeSymlink != 0 || !identity.IsDir() {
			if closeCurrent {
				_ = current.Close()
			}
			return nil, false, errors.New("template parent is not a real directory")
		}
		next, err := current.OpenRoot(segment)
		if err == nil {
			err = verifyDirectoryBinding(current, segment, next, identity)
		}
		if err == nil {
			err = chmodRootDirectory(next, 0o755)
		}
		if closeCurrent {
			err = errors.Join(err, current.Close())
		}
		if err != nil {
			if next != nil {
				_ = next.Close()
			}
			return nil, false, err
		}
		current = next
		closeCurrent = true
	}
	return current, true, nil
}

func writeRootFile(
	root *os.Root,
	name string,
	mode fs.FileMode,
	payload []byte,
) (identity fs.FileInfo, resultErr error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()
	identity, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(payload); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := file.Chmod(mode); err != nil {
		return nil, err
	}
	return identity, nil
}

func verifyCreatedFile(
	root *os.Root,
	name string,
	identity fs.FileInfo,
) (resultErr error) {
	parent, closeParent, err := openExistingFileParent(root, path.Dir(name))
	if err != nil {
		return err
	}
	if closeParent {
		defer func() {
			resultErr = errors.Join(resultErr, parent.Close())
		}()
	}
	current, err := parent.Lstat(path.Base(name))
	if err != nil {
		return err
	}
	if identity == nil ||
		current.Mode()&os.ModeSymlink != 0 ||
		!current.Mode().IsRegular() ||
		!os.SameFile(identity, current) {
		return errors.New("created file was replaced during generation")
	}
	return nil
}

func openExistingFileParent(
	root *os.Root,
	directory string,
) (*os.Root, bool, error) {
	if directory == "." {
		return root, false, nil
	}
	current := root
	closeCurrent := false
	for _, segment := range strings.Split(directory, "/") {
		next, err := openRealDirectory(current, segment)
		if closeCurrent {
			err = errors.Join(err, current.Close())
		}
		if err != nil {
			if next != nil {
				_ = next.Close()
			}
			return nil, false, err
		}
		current = next
		closeCurrent = true
	}
	return current, true, nil
}

func verifyDirectoryBinding(
	parent *os.Root,
	name string,
	child *os.Root,
	identity fs.FileInfo,
) error {
	opened, err := child.Stat(".")
	if err != nil {
		return err
	}
	current, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if identity == nil ||
		current.Mode()&os.ModeSymlink != 0 ||
		!current.IsDir() ||
		!os.SameFile(identity, opened) ||
		!os.SameFile(identity, current) {
		return errors.New("directory was replaced while it was open")
	}
	return nil
}

func chmodRootDirectory(root *os.Root, mode fs.FileMode) (resultErr error) {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, directory.Close())
	}()
	return directory.Chmod(mode)
}
