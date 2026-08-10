package tools

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const configSettingsFile = "settings.json"

type configDirResolution struct {
	path             string
	canonicalDefault bool
}

type pinnedConfigStore struct {
	path             string
	root             *os.Root
	info             os.FileInfo
	canonicalDefault bool
}

func openConfigStore(
	resolution configDirResolution,
	create bool,
) (*pinnedConfigStore, bool, error) {
	if !validConfigStorePath(resolution.path) {
		return nil, false, errors.New("config root is invalid")
	}
	if resolution.canonicalDefault {
		return openCanonicalConfigStore(resolution.path, create)
	}
	if create {
		if err := os.MkdirAll(resolution.path, 0o700); err != nil {
			return nil, false, errors.New("config root is unavailable")
		}
	}
	return openPinnedConfigStore(resolution.path, false)
}

func openCanonicalConfigStore(
	rootPath string,
	create bool,
) (*pinnedConfigStore, bool, error) {
	store, exists, err := openPinnedConfigStore(rootPath, true)
	if err != nil || exists || !create {
		return store, exists, err
	}

	parentPath := filepath.Dir(rootPath)
	name := filepath.Base(rootPath)
	if name == "." || name == string(filepath.Separator) || strings.ContainsRune(name, '\x00') {
		return nil, false, errors.New("config root is invalid")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, false, errors.New("config root is unavailable")
	}
	defer parent.Close() //nolint:errcheck
	parentInfo, err := parent.Stat(".")
	currentParent, currentErr := os.Stat(parentPath)
	if err != nil || currentErr != nil || !parentInfo.IsDir() ||
		!os.SameFile(parentInfo, currentParent) {
		return nil, false, errors.New("config root is invalid")
	}
	if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, false, errors.New("config root is unavailable")
	}
	created, err := parent.Lstat(name)
	if err != nil || !canonicalConfigDirectory(created) {
		return nil, false, errors.New("config root is invalid")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, false, errors.New("config root is unavailable")
	}
	opened, openErr := root.Stat(".")
	current, currentErr := parent.Lstat(name)
	if openErr != nil || currentErr != nil || !os.SameFile(created, opened) ||
		!os.SameFile(created, current) {
		_ = root.Close()
		return nil, false, errors.New("config root is invalid")
	}
	store = &pinnedConfigStore{
		path:             rootPath,
		root:             root,
		info:             opened,
		canonicalDefault: true,
	}
	if err := store.revalidate(); err != nil {
		_ = store.Close()
		return nil, false, err
	}
	return store, true, nil
}

func openPinnedConfigStore(
	rootPath string,
	canonicalDefault bool,
) (*pinnedConfigStore, bool, error) {
	info, err := configStorePathInfo(rootPath, canonicalDefault)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !validConfigDirectory(info, canonicalDefault) {
		return nil, false, errors.New("config root is invalid")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, false, errors.New("config root is unavailable")
	}
	store := &pinnedConfigStore{
		path:             rootPath,
		root:             root,
		info:             info,
		canonicalDefault: canonicalDefault,
	}
	if err := store.revalidate(); err != nil {
		_ = root.Close()
		return nil, false, err
	}
	return store, true, nil
}

func (store *pinnedConfigStore) readSettings() ([]byte, bool, error) {
	if err := store.revalidate(); err != nil {
		return nil, false, err
	}
	expected, err := store.root.Lstat(configSettingsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !safeConfigFileInfo(expected) {
		return nil, false, errors.New("settings file is invalid")
	}
	file, err := store.root.Open(configSettingsFile)
	if err != nil {
		return nil, false, errors.New("settings file is invalid")
	}
	opened, openErr := file.Stat()
	current, currentErr := store.root.Lstat(configSettingsFile)
	if openErr != nil || currentErr != nil || !safeConfigFileInfo(opened) ||
		!safeConfigFileInfo(current) || !os.SameFile(expected, opened) ||
		!os.SameFile(expected, current) || !configFileHasSingleLink(file) {
		_ = file.Close()
		return nil, false, errors.New("settings file is invalid")
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || store.revalidate() != nil {
		return nil, false, errors.New("settings file is invalid")
	}
	current, err = store.root.Lstat(configSettingsFile)
	if err != nil || !os.SameFile(expected, current) {
		return nil, false, errors.New("settings file is invalid")
	}
	return data, true, nil
}

func (store *pinnedConfigStore) writeSettings(data []byte) error {
	if err := store.revalidate(); err != nil {
		return err
	}
	expected, err := store.root.Lstat(configSettingsFile)
	flags := os.O_WRONLY
	if errors.Is(err, os.ErrNotExist) {
		expected = nil
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil || !safeConfigFileInfo(expected) {
		return errors.New("settings file is invalid")
	}
	file, err := store.root.OpenFile(configSettingsFile, flags, 0o600)
	if err != nil {
		return errors.New("settings file is invalid")
	}
	opened, openErr := file.Stat()
	current, currentErr := store.root.Lstat(configSettingsFile)
	validIdentity := openErr == nil && currentErr == nil &&
		safeConfigFileInfo(opened) && safeConfigFileInfo(current) &&
		os.SameFile(opened, current) && configFileHasSingleLink(file)
	if expected != nil {
		validIdentity = validIdentity && os.SameFile(expected, opened)
	}
	if !validIdentity || store.revalidate() != nil {
		_ = file.Close()
		return errors.New("settings file is invalid")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return errors.New("settings file is invalid")
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return errors.New("settings file is invalid")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return errors.New("settings file is invalid")
	}
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(data) || syncErr != nil || closeErr != nil ||
		store.revalidate() != nil {
		return errors.New("settings file is invalid")
	}
	current, err = store.root.Lstat(configSettingsFile)
	if err != nil || !safeConfigFileInfo(current) || !os.SameFile(opened, current) {
		return errors.New("settings file is invalid")
	}
	return nil
}

func (store *pinnedConfigStore) revalidate() error {
	if store == nil || store.root == nil || store.info == nil {
		return errors.New("config root is invalid")
	}
	opened, openErr := store.root.Stat(".")
	current, currentErr := configStorePathInfo(store.path, store.canonicalDefault)
	if openErr != nil || currentErr != nil ||
		!validConfigDirectory(opened, store.canonicalDefault) ||
		!validConfigDirectory(current, store.canonicalDefault) ||
		!os.SameFile(store.info, opened) || !os.SameFile(store.info, current) {
		return errors.New("config root is invalid")
	}
	return nil
}

func (store *pinnedConfigStore) Close() error {
	if store == nil || store.root == nil {
		return nil
	}
	err := store.root.Close()
	store.root = nil
	return err
}

func configStorePathInfo(path string, canonicalDefault bool) (os.FileInfo, error) {
	if canonicalDefault {
		return os.Lstat(path)
	}
	return os.Stat(path)
}

func validConfigDirectory(info os.FileInfo, canonicalDefault bool) bool {
	if info == nil || !info.IsDir() {
		return false
	}
	return !canonicalDefault || canonicalConfigDirectory(info)
}

func canonicalConfigDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().Perm()&0o077 == 0
}

func safeConfigFileInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func validConfigStorePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value &&
		!strings.ContainsRune(value, '\x00')
}
