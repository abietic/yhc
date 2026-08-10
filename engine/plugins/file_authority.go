package plugins

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/skills"
)

type pluginDirectoryIdentity struct {
	configuredRoot string
	entryName      string
	info           os.FileInfo
}

type materializedPluginCommand struct {
	spec     PluginCommand
	command  *commands.Command
	material string
	err      error
}

type pluginSourceAuthority struct {
	configuredRoot string
	root           *os.Root
}

type pluginFileAuthority struct {
	root        *os.Root
	displayDir  string
	identity    pluginDirectoryIdentity
	directoryID os.FileInfo
}

func openPluginSourceAuthority(configuredRoot string) (*pluginSourceAuthority, error) {
	root, err := os.OpenRoot(configuredRoot)
	if err != nil {
		return nil, err
	}
	return &pluginSourceAuthority{
		configuredRoot: configuredRoot,
		root:           root,
	}, nil
}

func (a *pluginSourceAuthority) Close() error {
	if a == nil || a.root == nil {
		return nil
	}
	return a.root.Close()
}

func (a *pluginSourceAuthority) readDir() ([]fs.DirEntry, error) {
	if a == nil || a.root == nil {
		return nil, fmt.Errorf("plugins: source authority is closed")
	}
	return fs.ReadDir(a.root.FS(), ".")
}

func (a *pluginSourceAuthority) openPlugin(entryName string) (*pluginFileAuthority, error) {
	return a.openPluginWithExpectedIdentity(entryName, nil)
}

func (a *pluginSourceAuthority) openPluginWithExpectedIdentity(
	entryName string,
	expected os.FileInfo,
) (*pluginFileAuthority, error) {
	if a == nil || a.root == nil {
		return nil, fmt.Errorf("plugins: source authority is closed")
	}
	root, err := a.root.OpenRoot(filepath.FromSlash(entryName))
	if err != nil {
		return nil, err
	}
	authority, err := newPluginFileAuthority(
		root,
		filepath.Join(a.configuredRoot, entryName),
		pluginDirectoryIdentity{
			configuredRoot: a.configuredRoot,
			entryName:      entryName,
		},
	)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if expected != nil && !os.SameFile(expected, authority.directoryID) {
		_ = authority.Close()
		return nil, fmt.Errorf(
			"plugins: plugin directory %q changed during discovery",
			entryName,
		)
	}
	return authority, nil
}

func openStandalonePluginAuthority(dir string) (*pluginFileAuthority, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	cleanDir := filepath.Clean(dir)
	authority, err := newPluginFileAuthority(
		root,
		dir,
		pluginDirectoryIdentity{
			configuredRoot: filepath.Dir(cleanDir),
			entryName:      filepath.Base(cleanDir),
		},
	)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return authority, nil
}

func newPluginFileAuthority(
	root *os.Root,
	displayDir string,
	identity pluginDirectoryIdentity,
) (*pluginFileAuthority, error) {
	info, err := rootDirectoryInfo(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plugins: plugin root is not a directory")
	}
	identity.info = info
	return &pluginFileAuthority{
		root:        root,
		displayDir:  displayDir,
		identity:    identity,
		directoryID: info,
	}, nil
}

func (a *pluginFileAuthority) Close() error {
	if a == nil || a.root == nil {
		return nil
	}
	return a.root.Close()
}

func (a *pluginFileAuthority) openPath(
	declared string,
) (*os.File, string, error) {
	normalized, err := normalizePluginLocalPath(declared)
	if err != nil {
		return nil, "", err
	}
	file, err := a.root.Open(filepath.FromSlash(normalized))
	if err != nil {
		return nil, normalized, err
	}
	return file, normalized, nil
}

func (a *pluginFileAuthority) openRegularFile(
	declared string,
) (*os.File, error) {
	file, _, err := a.openPath(declared)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf(
			"plugins: path %q is not a regular file",
			declared,
		)
	}
	return file, nil
}

func (a *pluginFileAuthority) readRegularFile(
	declared string,
) ([]byte, error) {
	file, err := a.openRegularFile(declared)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (a *pluginFileAuthority) openDirectory(
	declared string,
	expected os.FileInfo,
) (*os.Root, error) {
	normalized, err := normalizePluginLocalPath(declared)
	if err != nil {
		return nil, err
	}
	root, err := a.root.OpenRoot(filepath.FromSlash(normalized))
	if err != nil {
		return nil, err
	}
	actual, err := rootDirectoryInfo(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if expected != nil && !os.SameFile(expected, actual) {
		_ = root.Close()
		return nil, fmt.Errorf(
			"plugins: directory %q changed while opening",
			declared,
		)
	}
	return root, nil
}

func reopenPluginAuthority(plugin *Plugin) (*pluginFileAuthority, error) {
	if plugin == nil || plugin.directoryIdentity.info == nil {
		return nil, fmt.Errorf("plugins: plugin directory authority is unavailable")
	}
	source, err := openPluginSourceAuthority(
		plugin.directoryIdentity.configuredRoot,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"plugins: reopen source for %s: %w",
			plugin.Name,
			err,
		)
	}
	defer source.Close()
	authority, err := source.openPlugin(plugin.directoryIdentity.entryName)
	if err != nil {
		return nil, fmt.Errorf(
			"plugins: reopen directory for %s: %w",
			plugin.Name,
			err,
		)
	}
	if !os.SameFile(plugin.directoryIdentity.info, authority.directoryID) {
		_ = authority.Close()
		return nil, fmt.Errorf(
			"plugins: plugin directory identity changed for %s",
			plugin.Name,
		)
	}
	return authority, nil
}

func rootDirectoryInfo(root *os.Root) (os.FileInfo, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

func normalizePluginLocalPath(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("plugins: local path is empty")
	}
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("plugins: local path %q contains NUL", value)
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(normalized, "//") {
		return "", fmt.Errorf("plugins: local path %q is UNC-qualified", value)
	}
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("plugins: local path %q is absolute", value)
	}
	if len(normalized) >= 2 &&
		((normalized[0] >= 'a' && normalized[0] <= 'z') ||
			(normalized[0] >= 'A' && normalized[0] <= 'Z')) &&
		normalized[1] == ':' {
		return "", fmt.Errorf("plugins: local path %q is drive-qualified", value)
	}
	normalized = path.Clean(normalized)
	if normalized == "." || normalized == ".." ||
		strings.HasPrefix(normalized, "../") ||
		path.IsAbs(normalized) ||
		!fs.ValidPath(normalized) {
		return "", fmt.Errorf(
			"plugins: local path %q escapes plugin directory or is invalid",
			value,
		)
	}
	return normalized, nil
}

func collectPluginSkills(plugin *Plugin) (skills.Snapshot, error) {
	authority, err := reopenPluginAuthority(plugin)
	if err != nil {
		return skills.Snapshot{}, err
	}
	defer authority.Close()

	var result skills.Snapshot
	loaded := make(map[string]bool)
	for _, declared := range plugin.Skills {
		if declared.FilePath == "" {
			continue
		}
		snapshot, normalized, err := collectExplicitSkillPath(
			authority,
			plugin.Directory,
			declared.FilePath,
		)
		if err != nil {
			return skills.Snapshot{}, fmt.Errorf(
				"plugins: load skill path %q for %s: %w",
				declared.FilePath,
				plugin.Name,
				err,
			)
		}
		appendSkillSnapshot(&result, snapshot)
		loaded[normalized] = true
	}
	if !loaded["skills"] {
		snapshot, found, err := collectDefaultSkillDirectory(
			authority,
			plugin.Directory,
		)
		if err != nil {
			return skills.Snapshot{}, fmt.Errorf(
				"plugins: load skills directory for %s: %w",
				plugin.Name,
				err,
			)
		}
		if found {
			appendSkillSnapshot(&result, snapshot)
		}
	}
	return result, nil
}

func collectExplicitSkillPath(
	authority *pluginFileAuthority,
	displayRoot string,
	declared string,
) (skills.Snapshot, string, error) {
	file, normalized, err := authority.openPath(declared)
	if err != nil {
		return skills.Snapshot{}, normalized, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return skills.Snapshot{}, normalized, err
	}
	if info.IsDir() {
		_ = file.Close()
		root, err := authority.openDirectory(normalized, info)
		if err != nil {
			return skills.Snapshot{}, normalized, err
		}
		defer root.Close()
		snapshot, err := walkSkillDirectory(
			root,
			displayRoot,
			normalized,
			"directory",
			[]os.FileInfo{info},
		)
		return snapshot, normalized, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return skills.Snapshot{}, normalized, fmt.Errorf(
			"plugins: skill path %q is not a regular file or directory",
			declared,
		)
	}
	data, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		return skills.Snapshot{}, normalized, err
	}
	displayPath := filepath.Join(
		displayRoot,
		filepath.FromSlash(normalized),
	)
	skill, err := skills.ParseSkillData(displayPath, data)
	if err != nil {
		return skills.Snapshot{}, normalized, err
	}
	return skills.Snapshot{Skills: []*skills.Skill{skill}}, normalized, nil
}

func collectDefaultSkillDirectory(
	authority *pluginFileAuthority,
	displayRoot string,
) (skills.Snapshot, bool, error) {
	file, normalized, err := authority.openPath("skills")
	if err != nil {
		if os.IsNotExist(err) {
			return skills.Snapshot{}, false, nil
		}
		return skills.Snapshot{}, false, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return skills.Snapshot{}, false, err
	}
	if !info.IsDir() {
		_ = file.Close()
		return skills.Snapshot{}, false, nil
	}
	_ = file.Close()
	root, err := authority.openDirectory(normalized, info)
	if err != nil {
		return skills.Snapshot{}, false, err
	}
	defer root.Close()
	snapshot, err := walkSkillDirectory(
		root,
		displayRoot,
		normalized,
		"directory",
		[]os.FileInfo{info},
	)
	return snapshot, true, err
}

func walkSkillDirectory(
	root *os.Root,
	displayRoot string,
	relativeDir string,
	source string,
	visited []os.FileInfo,
) (skills.Snapshot, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return skills.Snapshot{}, err
	}
	var result skills.Snapshot
	for _, entry := range entries {
		name := entry.Name()
		relativePath := path.Join(relativeDir, name)
		isMarkdown := strings.HasSuffix(strings.ToLower(name), ".md")
		if !entry.IsDir() &&
			entry.Type()&fs.ModeSymlink == 0 &&
			!isMarkdown {
			continue
		}

		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return skills.Snapshot{}, err
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return skills.Snapshot{}, err
		}
		if info.IsDir() {
			_ = file.Close()
			if seenDirectory(visited, info) {
				continue
			}
			child, err := root.OpenRoot(filepath.FromSlash(name))
			if err != nil {
				return skills.Snapshot{}, err
			}
			childInfo, err := rootDirectoryInfo(child)
			if err != nil {
				_ = child.Close()
				return skills.Snapshot{}, err
			}
			if !os.SameFile(info, childInfo) {
				_ = child.Close()
				return skills.Snapshot{}, fmt.Errorf(
					"plugins: skill directory %q changed while opening",
					relativePath,
				)
			}
			childSnapshot, err := walkSkillDirectory(
				child,
				displayRoot,
				relativePath,
				source,
				append(append([]os.FileInfo(nil), visited...), info),
			)
			_ = child.Close()
			if err != nil {
				return skills.Snapshot{}, err
			}
			appendSkillSnapshot(&result, childSnapshot)
			continue
		}
		if !isMarkdown {
			_ = file.Close()
			continue
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			return skills.Snapshot{}, fmt.Errorf(
				"plugins: skill path %q is not a regular file",
				relativePath,
			)
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			return skills.Snapshot{}, err
		}
		displayPath := filepath.Join(
			displayRoot,
			filepath.FromSlash(relativePath),
		)
		skill, parseErr := skills.ParseSkillData(displayPath, data)
		if parseErr != nil {
			result.Diagnostics = append(
				result.Diagnostics,
				skills.Diagnostic{
					Source:   source,
					FilePath: displayPath,
					Message:  parseErr.Error(),
				},
			)
			continue
		}
		skill.Source = source
		result.Skills = append(result.Skills, skill)
	}
	return result, nil
}

func seenDirectory(visited []os.FileInfo, candidate os.FileInfo) bool {
	for _, current := range visited {
		if os.SameFile(current, candidate) {
			return true
		}
	}
	return false
}

func appendSkillSnapshot(target *skills.Snapshot, source skills.Snapshot) {
	target.Skills = append(target.Skills, source.Skills...)
	target.Diagnostics = append(target.Diagnostics, source.Diagnostics...)
}
