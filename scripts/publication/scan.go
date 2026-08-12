package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const nodePackageLockPath = "desktop/package-lock.json"

type ScanFinding struct {
	Path        string `json:"path"`
	Line        int    `json:"line"`
	RuleID      string `json:"rule_id"`
	MatchSHA256 string `json:"match_sha256"`
}

type ScanReport struct {
	SchemaVersion    int           `json:"schema_version"`
	ReviewedFindings int           `json:"reviewed_findings"`
	Findings         []ScanFinding `json:"findings"`
}

var (
	homePattern          = regexp.MustCompile(`(?i)(?:/Users/[^/\\\r\n]+/|/home/[^/\\\r\n]+/|C:[\\/]Users[\\/][^/\\\r\n]+[\\/])`)
	emailPattern         = regexp.MustCompile(`(?i)[A-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Z0-9](?:[A-Z0-9-]{0,61}[A-Z0-9])?(?:\.[A-Z0-9](?:[A-Z0-9-]{0,61}[A-Z0-9])?)+`)
	urlPattern           = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\x00\t\r\n <>"']*`)
	privateKeyPattern    = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	assignmentPattern    = regexp.MustCompile(`(?i)(?:api[ _-]?key|secret|token|password|credential|client[ _-]?secret|access[ _-]?key)\s*(?::=|[:=])\s*([^\s,;]+)`)
	providerPattern      = regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{12,}|sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16})`)
	bearerPattern        = regexp.MustCompile(`(?i)\bBearer[ \t]+([A-Za-z0-9._~+/=-]{8,})`)
	entropyPattern       = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,256}`)
	moduleVersionPattern = regexp.MustCompile(`^v?(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

// scanReadFile is a deliberately private test seam. Production always reads through os.Root.
var scanReadFile func(*os.Root, string) ([]byte, error)

// scanTreeAfterWalk is a deliberately private race seam. Production leaves it nil.
var scanTreeAfterWalk func(*os.Root, string)

func scanExpression(ctx context.Context, config Config, rootPath string) (ScanReport, error) {
	report := ScanReport{SchemaVersion: 1, Findings: make([]ScanFinding, 0)}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := validatePrivacy(config.Privacy); err != nil {
		return report, err
	}
	reviewed := make(map[ScanFinding]struct{}, len(config.Privacy.ReviewedFindings))
	for _, finding := range config.Privacy.ReviewedFindings {
		reviewed[ScanFinding{Path: finding.Path, Line: finding.Line, RuleID: finding.RuleID, MatchSHA256: finding.MatchSHA256}] = struct{}{}
	}
	matchedReviews := make(map[ScanFinding]struct{}, len(reviewed))
	root, rootInfo, err := scanOpenRoot(rootPath)
	if err != nil {
		return report, err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() {
		return report, errors.New("scan root is unsafe")
	}
	paths, err := scanPaths(ctx, rootPath, root, rootInfo)
	if err != nil {
		return report, err
	}
	if scanTreeAfterWalk != nil {
		scanTreeAfterWalk(root, "")
	}
	if err := scanRevalidateRoot(rootPath, root, rootInfo); err != nil {
		return report, err
	}
	seen := make(map[ScanFinding]struct{})
	digests := make(map[string][sha256.Size]byte, len(paths))
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		contents, err := scanOpenRegular(root, name)
		if err != nil {
			return report, err
		}
		if name == nodePackageLockPath {
			if _, err := checkNodeDependencies(rootPath, filepath.Join(rootPath, "quality", "node-dependency-licenses.yaml"), filepath.Join(rootPath, filepath.FromSlash(nodePackageLockPath))); err != nil {
				return report, fmt.Errorf("validate Node package lock: %w", err)
			}
			validated, err := scanOpenRegular(root, name)
			if err != nil || !bytes.Equal(contents, validated) {
				return report, fmt.Errorf("scan file %q changed during structured validation", name)
			}
		}
		digests[name] = sha256.Sum256(contents)
		scanContents := contents
		if name == nodePackageLockPath {
			scanContents, err = scanMaskedNodeLockfile(contents)
			if err != nil {
				return report, err
			}
		}
		for _, finding := range scanBytes(scanContents, name, config) {
			if _, exists := seen[finding]; exists {
				continue
			}
			seen[finding] = struct{}{}
			if _, exists := reviewed[finding]; exists {
				matchedReviews[finding] = struct{}{}
				report.ReviewedFindings++
				continue
			}
			report.Findings = append(report.Findings, finding)
		}
	}
	for _, name := range paths {
		contents, err := scanOpenRegular(root, name)
		if err != nil || sha256.Sum256(contents) != digests[name] {
			return ScanReport{SchemaVersion: 1, Findings: []ScanFinding{}}, fmt.Errorf("scan file %q changed after inspection", name)
		}
	}
	for finding := range reviewed {
		if _, matched := matchedReviews[finding]; !matched {
			return ScanReport{SchemaVersion: 1, Findings: []ScanFinding{}}, fmt.Errorf("reviewed finding %q line %d rule %q is stale", finding.Path, finding.Line, finding.RuleID)
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		return a.MatchSHA256 < b.MatchSHA256
	})
	if err := scanRevalidateRoot(rootPath, root, rootInfo); err != nil {
		return ScanReport{SchemaVersion: 1, Findings: []ScanFinding{}}, err
	}
	return report, nil
}

func scanMaskedNodeLockfile(contents []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var document map[string]json.RawMessage
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("decode Node package lock for expression scan failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("Node package lock has trailing data during expression scan")
	}
	packagesRaw, ok := document["packages"]
	if !ok {
		return nil, errors.New("Node package lock lacks packages during expression scan")
	}
	var packages map[string]map[string]json.RawMessage
	if err := json.Unmarshal(packagesRaw, &packages); err != nil {
		return nil, errors.New("decode Node package records for expression scan failed")
	}
	for location, pkg := range packages {
		if location == "" {
			continue
		}
		if _, ok := pkg["resolved"]; ok {
			pkg["resolved"] = json.RawMessage(`"validated-node-resolved"`)
		}
		if _, ok := pkg["integrity"]; ok {
			pkg["integrity"] = json.RawMessage(`"validated-node-integrity"`)
		}
	}
	encodedPackages, err := json.Marshal(packages)
	if err != nil {
		return nil, errors.New("encode Node package records for expression scan failed")
	}
	document["packages"] = encodedPackages
	masked, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("encode Node package lock for expression scan failed")
	}
	return masked, nil
}

func scanOpenRoot(rootPath string) (*os.Root, os.FileInfo, error) {
	before, err := os.Lstat(rootPath)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("scan root is unsafe")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open scan root: %w", err)
	}
	opened, err := root.Stat(".")
	after, afterErr := os.Lstat(rootPath)
	if err != nil || afterErr != nil || !opened.IsDir() || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		root.Close()
		return nil, nil, errors.New("scan root changed while opening")
	}
	return root, opened, nil
}

func scanRevalidateRoot(rootPath string, root *os.Root, expected os.FileInfo) error {
	opened, openErr := root.Stat(".")
	current, pathErr := os.Lstat(rootPath)
	if openErr != nil || pathErr != nil || !opened.IsDir() || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, opened) || !os.SameFile(expected, current) {
		return errors.New("scan root changed")
	}
	return nil
}

func scanPaths(ctx context.Context, rootPath string, root *os.Root, rootInfo os.FileInfo) ([]string, error) {
	if paths, usable, err := scanGitPaths(ctx, rootPath, rootInfo); usable {
		return paths, err
	}
	return scanTreePaths(ctx, root, "")
}

func scanGitPaths(ctx context.Context, rootPath string, rootInfo os.FileInfo) ([]string, bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", rootPath, "rev-parse", "--is-inside-work-tree")
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		return nil, false, nil
	}
	command = exec.CommandContext(ctx, "git", "-C", rootPath, "rev-parse", "--show-toplevel")
	output, err = command.Output()
	if err != nil {
		return nil, true, fmt.Errorf("resolve scan Git root: %w", err)
	}
	topLevel := strings.TrimSpace(string(output))
	topInfo, err := os.Lstat(topLevel)
	if err != nil || topInfo.Mode()&os.ModeSymlink != 0 || !topInfo.IsDir() || !os.SameFile(rootInfo, topInfo) {
		return nil, true, errors.New("scan root is not the Git worktree root")
	}
	command = exec.CommandContext(ctx, "git", "-C", rootPath, "ls-files", "--stage", "-z")
	output, err = command.Output()
	if err != nil {
		return nil, true, fmt.Errorf("enumerate tracked scan paths: %w", err)
	}
	paths, err := parseScanGitPaths(output)
	return paths, true, err
}

func parseScanGitPaths(output []byte) ([]string, error) {
	if len(output) > 0 && output[len(output)-1] != 0 {
		return nil, errors.New("tracked scan path stream is missing terminal NUL")
	}
	paths := make([]string, 0)
	for _, record := range bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid tracked scan entry")
		}
		fields := bytes.Fields(parts[0])
		if len(fields) != 3 || !bytes.Equal(fields[2], []byte("0")) || (!bytes.Equal(fields[0], []byte("100644")) && !bytes.Equal(fields[0], []byte("100755"))) {
			return nil, fmt.Errorf("unsafe tracked scan entry %q", parts[1])
		}
		name := string(parts[1])
		if err := validateScanPath(name); err != nil {
			return nil, err
		}
		paths = append(paths, name)
	}
	return validateScanPathSet(paths)
}

func scanTreePaths(ctx context.Context, root *os.Root, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("read scan directory: %w", err)
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, fmt.Errorf("read scan directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	paths := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		joined := name
		if prefix != "" {
			joined = prefix + "/" + name
		}
		if err := validateScanPath(joined); err != nil {
			return nil, err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return nil, fmt.Errorf("lstat scan path %q: %w", joined, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("scan path %q is a symlink", joined)
		}
		if info.IsDir() {
			child, err := root.OpenRoot(name)
			if err != nil {
				return nil, fmt.Errorf("open scan directory %q: %w", joined, err)
			}
			opened, statErr := child.Stat(".")
			if statErr != nil || !opened.IsDir() || !os.SameFile(info, opened) {
				child.Close()
				return nil, fmt.Errorf("scan directory %q changed while opening", joined)
			}
			childPaths, walkErr := scanTreePaths(ctx, child, joined)
			child.Close()
			if walkErr != nil {
				return nil, walkErr
			}
			if scanTreeAfterWalk != nil {
				scanTreeAfterWalk(root, name)
			}
			after, afterErr := root.Lstat(name)
			if afterErr != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, after) {
				return nil, fmt.Errorf("scan directory %q changed while walking", joined)
			}
			paths = append(paths, childPaths...)
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("scan path %q is not regular", joined)
		}
		paths = append(paths, joined)
	}
	return validateScanPathSet(paths)
}

func validateScanPathSet(paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, name := range paths {
		key := cases.Fold().String(norm.NFC.String(name))
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("colliding scan path %q", name)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths, nil
}

func validateScanPath(name string) error {
	if !utf8.ValidString(name) || name == "" || path.IsAbs(name) || strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." {
		return fmt.Errorf("unsafe scan path %q", name)
	}
	for _, segment := range strings.Split(name, "/") {
		lower := strings.ToLower(segment)
		base := strings.Split(lower, ".")[0]
		if segment == "" || strings.HasSuffix(segment, " ") || strings.HasSuffix(segment, ".") || strings.ContainsAny(segment, `<>:"\\|?*`) || base == "con" || base == "prn" || base == "aux" || base == "nul" || (len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9') {
			return fmt.Errorf("unsafe scan path %q", name)
		}
		if segment == ".git" || segment == ".reference" || segment == ".eino-agent" || segment == ".yhc" || segment == ".claude" {
			return fmt.Errorf("forbidden scan path %q", name)
		}
	}
	return nil
}

func scanOpenRegular(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe scan file %q", name)
	}
	file, openErr := root.Open(name)
	if openErr != nil {
		return nil, fmt.Errorf("open scan file %q: %w", name, openErr)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		file.Close()
		return nil, fmt.Errorf("scan file %q changed while opening", name)
	}
	contents, err := io.ReadAll(file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("read scan file %q: %w", name, err)
	}
	if scanReadFile != nil {
		if _, err := scanReadFile(root, name); err != nil {
			return nil, fmt.Errorf("scan read hook for %q: %w", name, err)
		}
	}
	after, err := root.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("scan file %q changed while reading", name)
	}
	return contents, nil
}

func scanBytes(contents []byte, name string, config Config) []ScanFinding {
	findings := make([]ScanFinding, 0)
	add := func(rule string, start int, match []byte) {
		sum := sha256.Sum256(match)
		findings = append(findings, ScanFinding{Path: name, Line: bytes.Count(contents[:start], []byte{'\n'}) + 1, RuleID: rule, MatchSHA256: hex.EncodeToString(sum[:])})
	}
	sentinel := func(candidate []byte) bool {
		for _, value := range config.Privacy.TestSentinels {
			if string(candidate) == value {
				return true
			}
		}
		return false
	}
	for _, loc := range homePattern.FindAllIndex(contents, -1) {
		segment := contents[loc[0]:loc[1]]
		user := scanHomeUser(segment)
		if !scanPlaceholderUser(user) {
			add("home-path", loc[0], segment)
		}
	}
	for _, loc := range emailPattern.FindAllIndex(contents, -1) {
		candidate := contents[loc[0]:loc[1]]
		if !scanAllowedEmail(candidate, config.Privacy.AllowedEmails) && !scanGoModuleVersion(candidate) {
			add("private-email", loc[0], candidate)
		}
	}
	urlRanges := urlPattern.FindAllIndex(contents, -1)
	lexicalRanges := scanGoLexicalRanges(contents, name)
	for _, loc := range urlRanges {
		candidate := bytes.TrimRight(contents[loc[0]:loc[1]], ".,;:!?)\"")
		parsed, err := url.Parse(string(candidate))
		if err == nil && parsed.Hostname() != "" && !scanAllowedHost(parsed.Hostname(), config.Privacy.AllowedURLHosts) {
			add("private-url", loc[0], candidate)
		}
	}
	for _, loc := range privateKeyPattern.FindAllIndex(contents, -1) {
		add("private-key", loc[0], contents[loc[0]:loc[1]])
	}
	for _, loc := range assignmentPattern.FindAllSubmatchIndex(contents, -1) {
		if path.Ext(name) == ".go" && !rangeContainedBy(loc[2:4], lexicalRanges.text) {
			continue
		}
		value := bytes.Trim(contents[loc[2]:loc[3]], "\"'")
		if len(value) >= 8 && !sentinel(value) {
			add("credential-assignment", loc[2], value)
		}
	}
	for _, loc := range providerPattern.FindAllIndex(contents, -1) {
		candidate := contents[loc[0]:loc[1]]
		if !sentinel(candidate) {
			add("provider-token", loc[0], candidate)
		}
	}
	for _, loc := range bearerPattern.FindAllSubmatchIndex(contents, -1) {
		candidate := contents[loc[2]:loc[3]]
		if !sentinel(candidate) {
			add("bearer-token", loc[2], candidate)
		}
	}
	for _, loc := range entropyPattern.FindAllIndex(contents, -1) {
		candidate := contents[loc[0]:loc[1]]
		if !rangeContainedBy(loc, lexicalRanges.identifiers) && scanEntropyCandidate(contents, loc, candidate, urlRanges) && !sentinel(candidate) {
			add("high-entropy-token", loc[0], candidate)
		}
	}
	return findings
}

func scanGoModuleVersion(candidate []byte) bool {
	separator := bytes.LastIndexByte(candidate, '@')
	if separator <= 0 || separator == len(candidate)-1 {
		return false
	}
	return moduleVersionPattern.Match(candidate[separator+1:])
}

type goLexicalRangeSet struct {
	identifiers [][]int
	text        [][]int
}

func scanGoLexicalRanges(contents []byte, name string) goLexicalRangeSet {
	if path.Ext(name) != ".go" {
		return goLexicalRangeSet{}
	}
	files := token.NewFileSet()
	file := files.AddFile(name, -1, len(contents))
	var lexical scanner.Scanner
	lexical.Init(file, contents, nil, scanner.ScanComments)
	ranges := goLexicalRangeSet{identifiers: make([][]int, 0), text: make([][]int, 0)}
	for {
		position, kind, literal := lexical.Scan()
		if kind == token.EOF {
			break
		}
		start := file.Offset(position)
		switch kind {
		case token.IDENT:
			ranges.identifiers = append(ranges.identifiers, []int{start, start + len(literal)})
		case token.STRING, token.COMMENT:
			ranges.text = append(ranges.text, []int{start, start + len(literal)})
		}
	}
	return ranges
}

func rangeContainedBy(candidate []int, containers [][]int) bool {
	for _, container := range containers {
		if candidate[0] >= container[0] && candidate[1] <= container[1] {
			return true
		}
	}
	return false
}

func scanHomeUser(value []byte) []byte {
	parts := bytes.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return nil
}

func scanPlaceholderUser(user []byte) bool {
	value := strings.ToLower(string(user))
	return value == "user" || value == "username" || value == "example" || value == "$user" || value == "${user}" || value == "<user>"
}

func scanAllowedEmail(candidate []byte, allowed []string) bool {
	for _, value := range allowed {
		if strings.EqualFold(string(candidate), value) {
			return true
		}
	}
	return false
}

func scanAllowedHost(host string, allowed []string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, value := range allowed {
		if host == strings.TrimSuffix(strings.ToLower(value), ".") {
			return true
		}
	}
	return false
}

func scanEntropyCandidate(contents []byte, candidateRange []int, candidate []byte, urlRanges [][]int) bool {
	start := candidateRange[0]
	if start >= 3 && (bytes.Equal(contents[start-3:start], []byte("h1:")) || bytes.Equal(contents[start-3:start], []byte("zh:"))) || start >= 7 && bytes.Equal(contents[start-7:start], []byte("sha256:")) {
		return false
	}
	if scanRepositoryPathLiteral(contents, candidateRange, candidate) {
		return false
	}
	for _, urlRange := range urlRanges {
		if candidateRange[0] >= urlRange[0] && candidateRange[1] <= urlRange[1] {
			return false
		}
	}
	letters, digits, nonHex := false, false, false
	counts := [256]int{}
	for _, c := range candidate {
		counts[c]++
		letters = letters || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digits = digits || c >= '0' && c <= '9'
		nonHex = nonHex || !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'))
	}
	if !letters || !digits || !nonHex {
		return false
	}
	entropy := 0.0
	for _, count := range counts {
		if count > 0 {
			p := float64(count) / float64(len(candidate))
			entropy -= p * math.Log2(p)
		}
	}
	return entropy >= 4.25
}

func scanRepositoryPathLiteral(contents []byte, candidateRange []int, candidate []byte) bool {
	separator := bytes.IndexByte(candidate, '/')
	if separator <= 0 {
		return false
	}
	switch string(candidate[:separator]) {
	case "agents", "codex", "github", "cmd", "docs", "engine", "internal", "quality", "scripts", "server", "third_party", "tools":
	default:
		return false
	}
	if candidateRange[1] >= len(contents) {
		return false
	}
	remainder := contents[candidateRange[1]:]
	for _, extension := range []string{".go", ".md", ".yaml", ".yml", ".json", ".sh", ".txt", ".toml", ".lock", ".mod", ".sum", ".golden", ".xml", ".html", ".csv", ".tsv", ".proto", ".tmpl", ".ps1", ".bat", ".svg", ".sql"} {
		if bytes.HasPrefix(remainder, []byte(extension)) {
			return true
		}
	}
	return false
}
