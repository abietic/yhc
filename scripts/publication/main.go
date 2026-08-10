package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: publication <inventory|check|scan-expression|materialize|check-tree|manifest|licenses> --config PATH")
	}
	commandName := args[0]
	switch commandName {
	case "inventory", "check", "scan-expression", "materialize", "check-tree", "manifest", "licenses":
	default:
		return fmt.Errorf("unknown publication command %q", args[0])
	}
	command := flag.NewFlagSet(commandName, flag.ContinueOnError)
	command.SetOutput(io.Discard)
	configPath := command.String("config", "", "publication policy")
	var outputPath, rootPath, sourceCommit *string
	switch commandName {
	case "inventory":
		outputPath = command.String("output", "", "inventory output")
	case "scan-expression", "check-tree":
		rootPath = command.String("root", "", "publication tree root")
	case "licenses":
		rootPath = command.String("root", "", "repository root")
		outputPath = command.String("output", "", "dependency license report")
	case "materialize":
		sourceCommit = command.String("source-commit", "", "exact source commit")
		outputPath = command.String("output", "", "publication output directory")
	case "manifest":
		rootPath = command.String("root", "", "publication tree root")
		outputPath = command.String("output", "", "publication manifest output")
	}
	if err := command.Parse(args[1:]); err != nil {
		return err
	}
	if command.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", commandName)
	}
	if *configPath == "" {
		return errors.New("--config is required")
	}
	config, err := loadConfigPath(*configPath)
	if err != nil {
		return err
	}

	switch commandName {
	case "licenses":
		if *rootPath == "" {
			return errors.New("--root is required")
		}
		if *outputPath == "" {
			return errors.New("--output is required")
		}
		policyPath := filepath.Join(*rootPath, filepath.FromSlash(config.Dependencies.LicensePolicy))
		sbomPath := filepath.Join(*rootPath, filepath.FromSlash(config.Dependencies.SBOM))
		report, checkErr := checkDependencyLicenses(ctx, *rootPath, policyPath, sbomPath)
		if checkErr != nil {
			return checkErr
		}
		encoded, encodeErr := encodeJSON(report)
		if encodeErr != nil {
			return encodeErr
		}
		return writeInventory(*outputPath, encoded)
	case "scan-expression":
		if *rootPath == "" {
			return errors.New("--root is required")
		}
		report, scanErr := scanExpression(ctx, config, *rootPath)
		if scanErr != nil {
			return scanErr
		}
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if len(report.Findings) != 0 {
			return fmt.Errorf("expression scan found %d findings", len(report.Findings))
		}
		return nil
	case "materialize":
		if *sourceCommit == "" {
			return errors.New("--source-commit is required")
		}
		if *outputPath == "" {
			return errors.New("--output is required")
		}
		return materialize(ctx, config, *sourceCommit, *outputPath)
	case "check-tree":
		if *rootPath == "" {
			return errors.New("--root is required")
		}
		_, err := checkTree(ctx, config, *rootPath)
		return err
	case "manifest":
		if *rootPath == "" {
			return errors.New("--root is required")
		}
		if *outputPath == "" {
			return errors.New("--output is required")
		}
		_, err := writeReleaseManifest(ctx, config, *rootPath, *outputPath)
		return err
	}

	inventory, err := buildInventory(ctx, config)
	if err != nil {
		return err
	}
	if commandName == "check" {
		if err := checkIncludedRuleEvidence(config, inventory); err != nil {
			return err
		}
		for _, file := range inventory.Files {
			if err := checkDecision(file); err != nil {
				return err
			}
		}
		return nil
	}
	encoded, err := encodeJSON(inventory)
	if err != nil {
		return err
	}
	currentIdentityPaths, err := buildCurrentIdentityPathSet(ctx, inventory)
	if err != nil {
		return err
	}
	if *outputPath == "" {
		_, err = stdout.Write(encoded)
		return err
	}
	if err := writeInventory(*outputPath, encoded); err != nil {
		return err
	}
	identityPathOutput := filepath.Join(filepath.Dir(*outputPath), currentIdentityPathsFilename)
	return writeInventory(identityPathOutput, encodeNULPaths(currentIdentityPaths))
}

func loadConfigPath(configPath string) (Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read publication policy: %w", err)
	}
	return loadConfig(data)
}

func writeJSON(output io.Writer, value any) error {
	encoded, err := encodeJSON(value)
	if err != nil {
		return err
	}
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write publication JSON: %w", err)
	}
	return nil
}

func encodeJSON(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode publication JSON: %w", err)
	}
	return append(encoded, '\n'), nil
}

func checkDecision(file FileDecision) error {
	if file.Decision != "include" {
		return fmt.Errorf("tracked path %q has non-publishable decision %q", file.Path, file.Decision)
	}
	if file.Class == "private-operational" || file.Class == "proprietary-or-reconstructable" {
		return fmt.Errorf("tracked path %q has forbidden included class %q", file.Path, file.Class)
	}
	if file.Class == "reference-informed-independent" && !file.Mapped {
		return fmt.Errorf("tracked path %q is included reference-informed material without a source mapping", file.Path)
	}
	if file.Class == "license-compatible-third-party" && file.License == "" {
		return fmt.Errorf("tracked path %q is included third-party material without a license", file.Path)
	}
	return nil
}

func checkIncludedRuleEvidence(config Config, inventory Inventory) error {
	tracked := make(map[string]FileDecision, len(inventory.Files))
	for _, file := range inventory.Files {
		tracked[file.Path] = file
	}
	for _, rule := range config.Rules {
		if rule.Decision != "include" || rule.Class != "license-compatible-third-party" {
			continue
		}
		if !validLicenseID(rule.License) {
			return fmt.Errorf("publication rule %q has an invalid third-party license ID", rule.ID)
		}
		hasLicenseEvidence := false
		for _, evidence := range rule.Evidence {
			if validateRepositoryPath(evidence) != nil {
				continue
			}
			file, ok := tracked[evidence]
			if !ok || file.Decision != "include" {
				continue
			}
			base := strings.ToUpper(filepath.Base(evidence))
			if strings.Contains(base, "LICENSE") || strings.Contains(base, "NOTICE") ||
				strings.Contains(base, "COPYING") {
				hasLicenseEvidence = true
				break
			}
		}
		if !hasLicenseEvidence {
			return fmt.Errorf("publication rule %q lacks included license or notice evidence", rule.ID)
		}
	}
	return nil
}

func validLicenseID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '.' || character == '+' {
			continue
		}
		return false
	}
	return true
}

func writeInventory(outputPath string, contents []byte) error {
	directory := filepath.Dir(outputPath)
	base := filepath.Base(outputPath)
	if err := validateInventoryOutputPath(outputPath, directory, base); err != nil {
		return err
	}
	outputDirectory, err := openInventoryOutputDirectory(directory)
	if err != nil {
		return err
	}
	defer outputDirectory.Close()
	root := outputDirectory.root
	initialTarget, err := lstatRegularOrAbsent(root, base)
	if err != nil {
		return err
	}
	temporary, file, err := createInventoryTemp(root)
	if err != nil {
		return err
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = root.Remove(temporary)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write inventory output: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync inventory output: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close inventory output: %w", err)
	}
	if err := outputDirectory.revalidate(); err != nil {
		return err
	}
	if err := verifyInventoryTarget(root, base, initialTarget); err != nil {
		return err
	}
	if err := root.Rename(temporary, base); err != nil {
		return fmt.Errorf("promote inventory output: %w", err)
	}
	promoted = true
	if err := outputDirectory.revalidate(); err != nil {
		return err
	}
	directoryFile, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open inventory output directory for sync: %w", err)
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return fmt.Errorf("sync inventory output directory: %w", err)
	}
	return nil
}

type inventoryDirectoryIdentity struct {
	path   string
	name   string
	info   os.FileInfo
	parent *os.Root
	child  *os.Root
}

type inventoryOutputDirectory struct {
	root        *os.Root
	directories []inventoryDirectoryIdentity
	roots       []*os.Root
}

type inventoryDirectoryHook func(stage, path string)

func openInventoryOutputDirectory(
	directory string,
) (*inventoryOutputDirectory, error) {
	return openInventoryOutputDirectoryWithHook(directory, nil)
}

func openInventoryOutputDirectoryWithHook(
	directory string,
	hook inventoryDirectoryHook,
) (*inventoryOutputDirectory, error) {
	root, err := os.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("open inventory working directory: %w", err)
	}
	roots := []*os.Root{root}
	closeRoots := func() {
		for index := len(roots) - 1; index >= 0; index-- {
			_ = roots[index].Close()
		}
	}
	cwd, err := root.Stat(".")
	if err != nil || !cwd.IsDir() {
		closeRoots()
		return nil, errors.New("inventory working directory is unsafe")
	}
	directories := make([]inventoryDirectoryIdentity, 0)
	prefix := ""
	for _, component := range strings.Split(directory, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			closeRoots()
			return nil, errors.New("invalid inventory output directory")
		}
		prefix = filepath.Join(prefix, component)
		parent := roots[len(roots)-1]
		info, statErr := parent.Lstat(component)
		if os.IsNotExist(statErr) {
			if hook != nil {
				hook("before-mkdir", prefix)
			}
			if err := parent.Mkdir(component, 0o700); err != nil {
				closeRoots()
				return nil, fmt.Errorf("create inventory directory %q: %w", prefix, err)
			}
			info, statErr = parent.Lstat(component)
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			closeRoots()
			return nil, fmt.Errorf("inventory output directory %q is unsafe", prefix)
		}
		if hook != nil {
			hook("before-open", prefix)
		}
		child, err := parent.OpenRoot(component)
		if err != nil {
			closeRoots()
			return nil, fmt.Errorf("open inventory output directory %q: %w", prefix, err)
		}
		opened, err := child.Stat(".")
		if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
			_ = child.Close()
			closeRoots()
			return nil, fmt.Errorf("inventory output directory %q changed while opening", prefix)
		}
		roots = append(roots, child)
		directories = append(directories, inventoryDirectoryIdentity{
			path:   prefix,
			name:   component,
			info:   info,
			parent: parent,
			child:  child,
		})
	}
	output := &inventoryOutputDirectory{
		root:        roots[len(roots)-1],
		directories: directories,
		roots:       roots,
	}
	if hook != nil {
		hook("before-chmod", directory)
	}
	directoryFile, err := output.root.Open(".")
	if err != nil {
		closeRoots()
		return nil, fmt.Errorf("open inventory output directory for chmod: %w", err)
	}
	if err := directoryFile.Chmod(0o700); err != nil {
		_ = directoryFile.Close()
		closeRoots()
		return nil, fmt.Errorf("set inventory directory mode: %w", err)
	}
	if err := directoryFile.Close(); err != nil {
		closeRoots()
		return nil, fmt.Errorf("close inventory output directory after chmod: %w", err)
	}
	if err := output.revalidate(); err != nil {
		closeRoots()
		return nil, err
	}
	return output, nil
}

func (output *inventoryOutputDirectory) revalidate() error {
	for _, directory := range output.directories {
		current, err := directory.parent.Lstat(directory.name)
		if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
			!os.SameFile(directory.info, current) {
			return fmt.Errorf("inventory output directory %q changed", directory.path)
		}
		opened, err := directory.child.Stat(".")
		if err != nil || !opened.IsDir() || !os.SameFile(directory.info, opened) {
			return fmt.Errorf("inventory output directory %q changed", directory.path)
		}
	}
	return nil
}

func (output *inventoryOutputDirectory) Close() error {
	var errs []error
	for index := len(output.roots) - 1; index >= 0; index-- {
		if err := output.roots[index].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validateInventoryOutputPath(outputPath, directory, base string) error {
	if outputPath == "" || filepath.IsAbs(outputPath) || filepath.Clean(outputPath) != outputPath || directory == "." || base == "." || base == string(filepath.Separator) || strings.Contains(base, string(filepath.Separator)) {
		return errors.New("invalid inventory output path")
	}
	return nil
}

func lstatRegularOrAbsent(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("inventory output target is unsafe")
	}
	return info, nil
}

func createInventoryTemp(root *os.Root) (string, *os.File, error) {
	for attempt := 0; attempt < 128; attempt++ {
		name := fmt.Sprintf(".inventory-%d-%d.tmp", os.Getpid(), attempt)
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create inventory staging file: %w", err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return "", nil, fmt.Errorf("set inventory staging mode: %w", err)
		}
		return name, file, nil
	}
	return "", nil, errors.New("create inventory staging file: exhausted unique names")
}

func verifyInventoryTarget(root *os.Root, name string, initial os.FileInfo) error {
	current, err := lstatRegularOrAbsent(root, name)
	if err != nil {
		return err
	}
	if initial == nil && current == nil {
		return nil
	}
	if initial != nil && current != nil && os.SameFile(initial, current) {
		return nil
	}
	return errors.New("inventory output target changed before promotion")
}
