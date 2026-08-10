package statemigration

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/internal/statepath"
)

type directoryModePolicy func(os.FileInfo) bool

type directoryAnchor interface {
	rootHandle() *os.Root
	revalidate() error
}

type pinnedDirectory struct {
	path string
	root *os.Root
	info os.FileInfo
	mode directoryModePolicy
}

type pinnedDirectoryStep struct {
	name string
	info os.FileInfo
}

type pinnedRelativeDirectory struct {
	base    directoryAnchor
	root    *os.Root
	info    os.FileInfo
	mode    directoryModePolicy
	steps   []pinnedDirectoryStep
	handles []*os.Root
	shared  bool
}

func validateStateRoots(roots statepath.Roots) error {
	if !validAbsoluteRoot(roots.Canonical) ||
		!validAbsoluteRoot(roots.Legacy) ||
		filepath.Clean(roots.Canonical) == filepath.Clean(roots.Legacy) {
		return errMigrationUnsafe
	}
	return nil
}

func validAbsoluteRoot(value string) bool {
	return value != "" &&
		!strings.ContainsRune(value, '\x00') &&
		filepath.IsAbs(value) &&
		filepath.Clean(value) == value &&
		filepath.Base(value) != "." &&
		filepath.Base(value) != string(filepath.Separator)
}

func validArtifactRelative(value string) bool {
	if value == "." || len(value) > 4096 ||
		strings.ContainsRune(value, '\x00') ||
		strings.Contains(value, `\`) ||
		!fs.ValidPath(value) ||
		path.Clean(value) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if len(component) > 255 {
			return false
		}
	}
	return value != "." &&
		!strings.ContainsRune(value, '\x00') &&
		!strings.Contains(value, `\`) &&
		fs.ValidPath(value) &&
		path.Clean(value) == value
}

func openPinnedDirectory(
	rootPath string,
	mode directoryModePolicy,
) (*pinnedDirectory, bool, error) {
	info, err := os.Lstat(rootPath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !mode(info) {
		return nil, false, errMigrationUnsafe
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, false, errMigrationUnsafe
	}
	pinned := &pinnedDirectory{path: rootPath, root: root, info: info, mode: mode}
	if err := pinned.revalidate(); err != nil {
		_ = root.Close()
		return nil, false, errMigrationUnsafe
	}
	return pinned, true, nil
}

func ensureCanonicalDirectory(rootPath string) (*pinnedDirectory, error) {
	if pinned, exists, err := openPinnedDirectory(rootPath, canonicalDirectoryMode); err != nil {
		return nil, err
	} else if exists {
		return pinned, nil
	}

	parentPath := filepath.Dir(rootPath)
	base := filepath.Base(rootPath)
	if !safeNativeSegment(base) {
		return nil, errMigrationUnsafe
	}
	parent, exists, err := openPinnedDirectory(parentPath, ordinaryDirectoryMode)
	if err != nil || !exists {
		return nil, errMigrationUnsafe
	}
	defer parent.Close() //nolint:errcheck
	if err := parent.revalidate(); err != nil {
		return nil, errMigrationUnsafe
	}
	if err := parent.root.Mkdir(base, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, errMigrationUnsafe
	}
	created, err := parent.root.Lstat(base)
	if err != nil || !canonicalDirectoryMode(created) {
		return nil, errMigrationUnsafe
	}
	if err := syncRootDirectory(parent.root); err != nil {
		return nil, errMigrationUnsafe
	}
	if err := parent.revalidate(); err != nil {
		return nil, errMigrationUnsafe
	}
	pinned, exists, err := openPinnedDirectory(rootPath, canonicalDirectoryMode)
	if err != nil || !exists || !os.SameFile(created, pinned.info) {
		if pinned != nil {
			_ = pinned.Close()
		}
		return nil, errMigrationUnsafe
	}
	return pinned, nil
}

func (pinned *pinnedDirectory) rootHandle() *os.Root {
	return pinned.root
}

func (pinned *pinnedDirectory) revalidate() error {
	if pinned == nil || pinned.root == nil {
		return errMigrationUnsafe
	}
	opened, openErr := pinned.root.Stat(".")
	current, pathErr := os.Lstat(pinned.path)
	if openErr != nil || pathErr != nil ||
		!pinned.mode(opened) || !pinned.mode(current) ||
		!os.SameFile(pinned.info, opened) || !os.SameFile(pinned.info, current) {
		return errMigrationUnsafe
	}
	return nil
}

func (pinned *pinnedDirectory) Close() error {
	if pinned == nil || pinned.root == nil {
		return nil
	}
	err := pinned.root.Close()
	pinned.root = nil
	return err
}

func openRelativeDirectory(
	base directoryAnchor,
	relative string,
	create bool,
	mode directoryModePolicy,
) (*pinnedRelativeDirectory, bool, error) {
	if base == nil || (relative != "." && !validArtifactRelative(relative)) {
		return nil, false, errMigrationUnsafe
	}
	if err := base.revalidate(); err != nil {
		return nil, false, errMigrationUnsafe
	}
	if relative == "." {
		info, err := base.rootHandle().Stat(".")
		if err != nil || !mode(info) {
			return nil, false, errMigrationUnsafe
		}
		return &pinnedRelativeDirectory{
			base: base, root: base.rootHandle(), info: info, mode: mode, shared: true,
		}, true, nil
	}

	current := base.rootHandle()
	steps := make([]pinnedDirectoryStep, 0)
	handles := make([]*os.Root, 0)
	closeHandles := func() {
		for index := len(handles) - 1; index >= 0; index-- {
			_ = handles[index].Close()
		}
	}
	for _, name := range strings.Split(relative, "/") {
		if !safeNativeSegment(name) {
			closeHandles()
			return nil, false, errMigrationUnsafe
		}
		info, err := current.Lstat(filepath.FromSlash(name))
		if os.IsNotExist(err) && create {
			if err := current.Mkdir(filepath.FromSlash(name), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				closeHandles()
				return nil, false, errMigrationUnsafe
			}
			if err := syncRootDirectory(current); err != nil {
				closeHandles()
				return nil, false, errMigrationUnsafe
			}
			info, err = current.Lstat(filepath.FromSlash(name))
		}
		if os.IsNotExist(err) {
			closeHandles()
			return nil, false, nil
		}
		if err != nil || !mode(info) {
			closeHandles()
			return nil, false, errMigrationUnsafe
		}
		child, err := current.OpenRoot(filepath.FromSlash(name))
		if err != nil {
			closeHandles()
			return nil, false, errMigrationUnsafe
		}
		opened, err := child.Stat(".")
		if err != nil || !mode(opened) || !os.SameFile(info, opened) {
			_ = child.Close()
			closeHandles()
			return nil, false, errMigrationUnsafe
		}
		steps = append(steps, pinnedDirectoryStep{name: name, info: info})
		handles = append(handles, child)
		current = child
	}
	pinned := &pinnedRelativeDirectory{
		base: base, root: current, info: steps[len(steps)-1].info,
		mode: mode, steps: steps, handles: handles,
	}
	if err := pinned.revalidate(); err != nil {
		pinned.Close() //nolint:errcheck
		return nil, false, errMigrationUnsafe
	}
	return pinned, true, nil
}

func (pinned *pinnedRelativeDirectory) rootHandle() *os.Root {
	return pinned.root
}

func (pinned *pinnedRelativeDirectory) revalidate() error {
	if pinned == nil || pinned.base == nil || pinned.root == nil {
		return errMigrationUnsafe
	}
	if err := pinned.base.revalidate(); err != nil {
		return errMigrationUnsafe
	}
	current := pinned.base.rootHandle()
	temporary := make([]*os.Root, 0, len(pinned.steps))
	defer func() {
		for index := len(temporary) - 1; index >= 0; index-- {
			_ = temporary[index].Close()
		}
	}()
	for _, step := range pinned.steps {
		info, err := current.Lstat(filepath.FromSlash(step.name))
		if err != nil || !pinned.mode(info) || !os.SameFile(step.info, info) {
			return errMigrationUnsafe
		}
		child, err := current.OpenRoot(filepath.FromSlash(step.name))
		if err != nil {
			return errMigrationUnsafe
		}
		opened, err := child.Stat(".")
		if err != nil || !pinned.mode(opened) || !os.SameFile(step.info, opened) {
			_ = child.Close()
			return errMigrationUnsafe
		}
		temporary = append(temporary, child)
		current = child
	}
	opened, err := pinned.root.Stat(".")
	if err != nil || !pinned.mode(opened) || !os.SameFile(pinned.info, opened) {
		return errMigrationUnsafe
	}
	return nil
}

func (pinned *pinnedRelativeDirectory) Close() error {
	if pinned == nil || pinned.shared {
		return nil
	}
	var closeErr error
	for index := len(pinned.handles) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, pinned.handles[index].Close())
	}
	pinned.handles = nil
	pinned.root = nil
	return closeErr
}

func inspectCanonicalTarget(canonicalPath, targetRelative string) (bool, error) {
	canonical, exists, err := openPinnedDirectory(canonicalPath, canonicalDirectoryMode)
	if err != nil || !exists {
		return false, err
	}
	defer canonical.Close() //nolint:errcheck
	parent, exists, err := openRelativeDirectory(
		canonical,
		path.Dir(targetRelative),
		false,
		canonicalDirectoryMode,
	)
	if err != nil || !exists {
		return false, err
	}
	defer parent.Close() //nolint:errcheck
	if err := parent.revalidate(); err != nil {
		return false, errMigrationUnsafe
	}
	_, err = parent.root.Lstat(filepath.FromSlash(path.Base(targetRelative)))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, errMigrationUnsafe
}

func ordinaryDirectoryMode(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func legacyDirectoryMode(info os.FileInfo) bool {
	if !ordinaryDirectoryMode(info) {
		return false
	}
	permissions := info.Mode().Perm()
	return permissions&0o077 == 0 && permissions&0o500 == 0o500
}

func canonicalDirectoryMode(info os.FileInfo) bool {
	return ordinaryDirectoryMode(info) && info.Mode().Perm() == 0o700
}

func legacyRegularMode(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	permissions := info.Mode().Perm()
	return permissions&0o077 == 0 && permissions&0o111 == 0 && permissions&0o400 != 0
}

func legacyOwnerControlledDirectoryMode(info os.FileInfo) bool {
	if !ordinaryDirectoryMode(info) ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	permissions := info.Mode().Perm()
	return permissions&0o022 == 0 && permissions&0o500 == 0o500
}

func legacyOwnerControlledRegularMode(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() ||
		info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	permissions := info.Mode().Perm()
	return permissions&0o022 == 0 && permissions&0o111 == 0 && permissions&0o400 != 0
}

func legacyModePolicies(
	mode LegacyMode,
) (directoryModePolicy, func(os.FileInfo) bool, error) {
	switch mode {
	case LegacyPrivate:
		return legacyDirectoryMode, legacyRegularMode, nil
	case LegacyOwnerControlled:
		return legacyOwnerControlledDirectoryMode, legacyOwnerControlledRegularMode, nil
	default:
		return nil, nil, errMigrationUnsafe
	}
}

func canonicalRegularMode(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o600
}

func safeNativeSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsRune(value, '\x00') &&
		!strings.ContainsAny(value, `/\`)
}

func syncRootDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return errMigrationUnsafe
	}
	defer directory.Close() //nolint:errcheck
	if err := syncDirectoryFile(directory); err != nil {
		return errMigrationUnsafe
	}
	return nil
}
