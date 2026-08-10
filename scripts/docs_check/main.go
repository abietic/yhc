package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	inlineLinkRE     = regexp.MustCompile(`\]\(([^)\n]+)\)`)
	labeledLinkRE    = regexp.MustCompile(`\[([^]\n]+)\]\(([^)\n]+)\)`)
	headingRE        = regexp.MustCompile(`^#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	levelOneRE       = regexp.MustCompile(`(?m)^#[ \t]+\S.+$`)
	statusRE         = regexp.MustCompile(`(?m)^\*\*Status:\*\*[ \t]+([^\r\n]+)$`)
	ownershipRE      = regexp.MustCompile(`(?m)^>?[ \t]*\*\*Ownership:\*\*`)
	closedGapsRE     = regexp.MustCompile(`(?m)^\*\*Closed gaps:\*\*[ \t]+([^\r\n]+)$`)
	closedGapAnyRE   = regexp.MustCompile(`(?m)^\*\*Closed gaps:\*\*`)
	closedGapIDRE    = regexp.MustCompile(`^G([1-9][0-9]*)$`)
	openGapRowRE     = regexp.MustCompile(`(?m)^\|[ \t]*(G[1-9][0-9]*)[ \t]*\|`)
	lowerKebabNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	lineAnchorRE     = regexp.MustCompile(`^L([0-9]+)(?:-L?([0-9]+))?$`)
	htmlTagRE        = regexp.MustCompile(`<[^>]+>`)
	planLinkRE       = regexp.MustCompile(`\[[^]\n]+\]\(([^)\n]+)\)`)
	skillNameRE      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

const requiredSubSkillMarker = "**For agentic workers:** REQUIRED SUB-SKILL:"

var allowedStatuses = map[string]struct{}{
	"current":            {},
	"active-plan":        {},
	"gap-inventory":      {},
	"reference-snapshot": {},
	"verification":       {},
	"historical":         {},
}

type checkResult struct {
	markdownFiles int
	links         int
	reachable     int
	errs          []error
}

type closedGapOwner struct {
	id   string
	path string
}

type planLifecycle string

const (
	planLifecycleHistorical planLifecycle = "historical"
	planLifecycleActive     planLifecycle = "active"
)

type planIndexEntry struct {
	path                  string
	lifecycle             planLifecycle
	requiresRequiredSkill bool
}

func main() {
	result := checkRepository(".")
	if len(result.errs) > 0 {
		for _, err := range result.errs {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	fmt.Printf("docs valid: %d markdown files, %d local links, %d reachable from docs/README.md\n",
		result.markdownFiles, result.links, result.reachable)
}

func checkRepository(root string) checkResult {
	root, err := filepath.Abs(root)
	if err != nil {
		return checkResult{errs: []error{err}}
	}

	docFiles, err := collectMarkdown(filepath.Join(root, "docs"))
	if err != nil {
		return checkResult{errs: []error{err}}
	}
	allFiles := append([]string(nil), docFiles...)
	for _, extra := range []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "PROJECT_DIRECTION.md"),
	} {
		if info, statErr := os.Stat(extra); statErr == nil && !info.IsDir() {
			allFiles = append(allFiles, extra)
		}
	}

	graph := make(map[string]map[string]struct{}, len(docFiles))
	for _, file := range docFiles {
		graph[file] = make(map[string]struct{})
	}

	result := checkResult{markdownFiles: len(docFiles)}
	contentCache := make(map[string][]byte, len(allFiles))
	headingCache := make(map[string]map[string]struct{})
	lineCache := make(map[string][]string)
	for _, source := range allFiles {
		data, readErr := readFileConfined(root, source)
		if readErr != nil {
			result.errs = append(result.errs, fmt.Errorf("%s: %w", displayPath(root, source), readErr))
			continue
		}
		contentCache[source] = bytes.Clone(data)
		if _, isDoc := graph[source]; isDoc {
			result.errs = append(result.errs, validateDocumentMetadata(root, source, data)...)
		}
		result.errs = append(result.errs, validateAgentRuntimeOwnership(root, source, data)...)
		for _, match := range inlineLinkRE.FindAllStringSubmatchIndex(string(data), -1) {
			raw := string(data[match[2]:match[3]])
			destination, fragment, local := parseDestination(raw)
			if !local {
				continue
			}
			result.links++
			line := 1 + strings.Count(string(data[:match[0]]), "\n")
			target := source
			if destination != "" {
				var resolveErr error
				target, resolveErr = resolveTarget(root, source, destination)
				if resolveErr != nil {
					result.errs = append(result.errs, fmt.Errorf("%s:%d: %w", displayPath(root, source), line, resolveErr))
					continue
				}
			}
			if fragment != "" {
				if anchorErr := validateAnchor(root, target, fragment, headingCache, lineCache); anchorErr != nil {
					result.errs = append(result.errs, fmt.Errorf("%s:%d: %w", displayPath(root, source), line, anchorErr))
				}
			}

			if _, ok := graph[source]; !ok {
				continue
			}
			if _, ok := graph[target]; ok {
				graph[source][target] = struct{}{}
			}
		}
		result.errs = append(result.errs, validateLocalLinkLabels(root, source, data)...)
	}
	result.errs = append(result.errs, validateClosedGapTraceability(root, docFiles, contentCache)...)
	result.errs = append(result.errs, validatePlanLifecycle(root, docFiles, contentCache)...)
	result.errs = append(result.errs, validateRequiredProjectSkills(root, docFiles, contentCache)...)

	home := filepath.Join(root, "docs", "README.md")
	if _, ok := graph[home]; !ok {
		result.errs = append(result.errs, errors.New("docs/README.md: documentation entrypoint is missing"))
		return result
	}
	reachable := walkGraph(home, graph)
	result.reachable = len(reachable)
	for _, file := range docFiles {
		if _, ok := reachable[file]; !ok {
			result.errs = append(result.errs, fmt.Errorf("%s: not reachable from docs/README.md", displayPath(root, file)))
		}
	}
	return result
}

func validatePlanLifecycle(root string, docFiles []string, contentCache map[string][]byte) []error {
	indexPath := filepath.Join(root, "docs", "superpowers", "plans", "README.md")
	plansRoot := filepath.Join(root, "docs", "superpowers", "plans") + string(filepath.Separator)
	var planFiles []string
	for _, path := range docFiles {
		if !strings.HasPrefix(path, plansRoot) || filepath.Base(path) == "README.md" {
			continue
		}
		planFiles = append(planFiles, path)
	}
	if len(planFiles) == 0 {
		return nil
	}
	indexData, ok := contentCache[indexPath]
	if !ok {
		return []error{fmt.Errorf("docs/superpowers/plans/README.md: missing Implementation Plan Index")}
	}
	entries, errs := parsePlanIndex(root, indexPath, indexData)
	for _, path := range planFiles {
		matches := entries[path]
		switch len(matches) {
		case 0:
			errs = append(errs, fmt.Errorf("%s: has no Implementation Plan Index entry", displayPath(root, path)))
			continue
		case 1:
		default:
			errs = append(errs, fmt.Errorf("%s: has multiple Implementation Plan Index entries", displayPath(root, path)))
			continue
		}

		status := statusRE.FindStringSubmatch(firstDocumentLines(contentCache[path]))
		if status == nil {
			errs = append(errs, fmt.Errorf("%s: linked plan is missing Status metadata", displayPath(root, path)))
			continue
		}
		switch matches[0].lifecycle {
		case planLifecycleHistorical:
			if strings.TrimSpace(status[1]) != "historical" {
				errs = append(errs, fmt.Errorf("%s: executed plan index entry requires Status historical", displayPath(root, path)))
			}
		case planLifecycleActive:
			if strings.TrimSpace(status[1]) != "active-plan" {
				errs = append(errs, fmt.Errorf("%s: active plan index entry requires Status active-plan", displayPath(root, path)))
			}
		}
	}
	return errs
}

func parsePlanIndex(root, indexPath string, data []byte) (map[string][]planIndexEntry, []error) {
	entries := make(map[string][]planIndexEntry)
	var errs []error
	inIndex := false
	for lineNumber, line := range strings.Split(string(markdownOutsideFences(data)), "\n") {
		cells := markdownTableCells(line)
		if !inIndex {
			inIndex = isPlanIndexHeader(cells)
			continue
		}
		if len(cells) != 3 {
			inIndex = false
			continue
		}
		if !strings.HasPrefix(cells[0], "[") {
			continue
		}
		match := planLinkRE.FindStringSubmatch(cells[0])
		if match == nil {
			continue
		}
		target, err := resolveTarget(root, indexPath, match[1])
		if err != nil {
			errs = append(errs, fmt.Errorf("docs/superpowers/plans/README.md:%d: %w", lineNumber+1, err))
			continue
		}
		lifecycle, ok := normalizePlanLifecycle(cells[2])
		if !ok {
			errs = append(errs, fmt.Errorf("docs/superpowers/plans/README.md:%d: unknown Implementation Plan Index state %q", lineNumber+1, cells[2]))
			continue
		}
		entries[target] = append(entries[target], planIndexEntry{
			path:                  target,
			lifecycle:             lifecycle,
			requiresRequiredSkill: isAcceptedPlanIndexState(cells[2]),
		})
	}
	return entries, errs
}

func isAcceptedPlanIndexState(state string) bool {
	fields := strings.Fields(strings.TrimSpace(state))
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimRight(fields[0], ";:,")) {
	case "accepted", "accepted-design":
		return true
	default:
		return false
	}
}

func isPlanIndexHeader(cells []string) bool {
	return len(cells) == 3 &&
		cells[0] == "Plan" &&
		cells[1] == "Owning accepted contract" &&
		cells[2] == "State"
}

func markdownTableCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	parts := strings.Split(line[1:len(line)-1], "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func normalizePlanLifecycle(state string) (planLifecycle, bool) {
	fields := strings.Fields(strings.TrimSpace(state))
	if len(fields) == 0 {
		return "", false
	}
	prefix := strings.ToLower(strings.TrimRight(fields[0], ";:,"))
	switch prefix {
	case "executed", "historical":
		return planLifecycleHistorical, true
	case "active", "ready", "queued", "draft", "accepted", "accepted-design":
		return planLifecycleActive, true
	default:
		return "", false
	}
}

func validateRequiredProjectSkills(root string, docFiles []string, contentCache map[string][]byte) []error {
	plansRoot := filepath.Join(root, "docs", "superpowers", "plans") + string(filepath.Separator)
	indexPath := filepath.Join(root, "docs", "superpowers", "plans", "README.md")
	entries, _ := parsePlanIndex(root, indexPath, contentCache[indexPath])
	var errs []error
	for _, path := range docFiles {
		if !strings.HasPrefix(path, plansRoot) || filepath.Base(path) == "README.md" {
			continue
		}
		instruction := firstRequiredSubSkillBlock(contentCache[path])
		status := statusRE.FindStringSubmatch(firstDocumentLines(contentCache[path]))
		requiresInstruction := len(entries[path]) == 1 &&
			entries[path][0].requiresRequiredSkill &&
			status != nil && strings.TrimSpace(status[1]) == "active-plan"
		if instruction == "" {
			if requiresInstruction {
				errs = append(errs, fmt.Errorf("%s: accepted plan requires REQUIRED SUB-SKILL in its first top-level blockquote", displayPath(root, path)))
			}
			continue
		}
		if status != nil && strings.TrimSpace(status[1]) == "historical" {
			errs = append(errs, fmt.Errorf("%s: historical plan must not contain a live REQUIRED SUB-SKILL instruction", displayPath(root, path)))
			continue
		}
		names, valid, legacy := requiredSkillNames(root, instruction)
		if legacy {
			errs = append(errs, fmt.Errorf("%s: legacy required project skill is not allowed", displayPath(root, path)))
			continue
		}
		if !valid {
			errs = append(errs, fmt.Errorf("%s: invalid required project skill token", displayPath(root, path)))
			continue
		}
		for _, name := range names {
			skillPath := filepath.Join(root, ".agents", "skills", name, "SKILL.md")
			if _, err := readFileConfined(root, skillPath); err != nil {
				errs = append(errs, fmt.Errorf("%s: required project skill $%s does not exist", displayPath(root, path), name))
			}
		}
	}
	return errs
}

func firstRequiredSubSkillBlock(data []byte) string {
	lines := strings.Split(string(markdownOutsideFences(data)), "\n")
	var block []string
	for _, line := range lines {
		content, topLevel := topLevelBlockquoteLine(line)
		if topLevel {
			block = append(block, content)
			continue
		}
		if len(block) != 0 {
			break
		}
	}
	joined := strings.Join(block, " ")
	if strings.Contains(joined, requiredSubSkillMarker) {
		return joined
	}
	return ""
}

func requiredSkillNames(root, instruction string) ([]string, bool, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(instruction[strings.Index(instruction, requiredSubSkillMarker):], requiredSubSkillMarker))
	if strings.Contains(payload, "superpowers:") {
		return nil, false, true
	}

	names := make([]string, 0, 1)
	for index := 0; index < len(payload); index++ {
		if payload[index] != '$' {
			continue
		}
		if !hasSkillTokenBoundary(payload, index-1) {
			return nil, false, false
		}
		end := index + 1
		if end == len(payload) || payload[end] < 'a' || payload[end] > 'z' {
			return nil, false, false
		}
		for end < len(payload) && isSkillNameByte(payload[end]) {
			end++
		}
		if !hasSkillTokenBoundary(payload, end) {
			return nil, false, false
		}
		names = append(names, payload[index+1:end])
		index = end - 1
	}
	if len(names) == 0 {
		return nil, false, false
	}
	if hasBareLocalSkillReference(payload, localSkillNames(root)) {
		return nil, false, false
	}
	return names, true, false
}

func hasSkillTokenBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	return !isSkillNameByte(text[index]) && text[index] != '$' && text[index] != '\\' &&
		text[index] != '_' && text[index] != '/'
}

func isSkillNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-'
}

func localSkillNames(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, ".agents", "skills"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !skillNameRE.MatchString(name) {
			continue
		}
		if _, err := readFileConfined(root, filepath.Join(root, ".agents", "skills", name, "SKILL.md")); err == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func hasBareLocalSkillReference(payload string, names []string) bool {
	for _, name := range names {
		for start := 0; ; {
			index := strings.Index(payload[start:], name)
			if index < 0 {
				break
			}
			index += start
			end := index + len(name)
			if hasSkillNameBoundary(payload, index-1) && hasSkillNameBoundary(payload, end) &&
				!isCanonicalSkillOccurrence(payload, index, end) {
				return true
			}
			start = index + len(name)
		}
	}
	return false
}

func isCanonicalSkillOccurrence(payload string, start, end int) bool {
	return start > 0 && payload[start-1] == '$' &&
		hasSkillTokenBoundary(payload, start-2) && hasSkillTokenBoundary(payload, end)
}

func hasSkillNameBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	return !isSkillNameByte(text[index]) && text[index] != '_' && text[index] != '/'
}

func topLevelBlockquoteLine(line string) (string, bool) {
	if !strings.HasPrefix(line, ">") {
		return "", false
	}
	content := strings.TrimSpace(strings.TrimPrefix(line, ">"))
	return content, !strings.HasPrefix(content, ">")
}

func validateDocumentMetadata(root, path string, data []byte) []error {
	var errs []error
	text := string(data)
	prefix := firstDocumentLines(data)
	if !levelOneRE.MatchString(text) {
		errs = append(errs, fmt.Errorf("%s: missing level-one title", displayPath(root, path)))
	}
	status := statusRE.FindStringSubmatch(prefix)
	if status == nil {
		errs = append(errs, fmt.Errorf("%s: missing Status metadata in first 30 lines", displayPath(root, path)))
	} else if _, ok := allowedStatuses[strings.TrimSpace(status[1])]; !ok {
		errs = append(errs, fmt.Errorf("%s: invalid Status %q", displayPath(root, path), strings.TrimSpace(status[1])))
	}
	if !ownershipRE.MatchString(prefix) {
		errs = append(errs, fmt.Errorf("%s: missing Ownership metadata in first 30 lines", displayPath(root, path)))
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	switch base {
	case "README", "GUIDELINE", "STATUS", "REMAINING", "PLAN":
	default:
		if !lowerKebabNameRE.MatchString(base) {
			errs = append(errs, fmt.Errorf("%s: filename is not lower-kebab", displayPath(root, path)))
		}
	}
	return errs
}

func firstDocumentLines(data []byte) string {
	const limit = 30
	lines := strings.Split(string(data), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n")
}

func markdownOutsideFences(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	var fence byte
	minimumLength := 0
	for index, line := range lines {
		marker, length, suffix, ok := markdownFence(line)
		if fence == 0 {
			if !ok {
				continue
			}
			fence = marker
			minimumLength = length
			lines[index] = ""
			continue
		}

		lines[index] = ""
		if ok && marker == fence && length >= minimumLength && strings.TrimSpace(suffix) == "" {
			fence = 0
			minimumLength = 0
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func markdownFence(line string) (byte, int, string, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) {
		return 0, 0, "", false
	}
	marker := line[indent]
	if marker != '`' && marker != '~' {
		return 0, 0, "", false
	}
	end := indent
	for end < len(line) && line[end] == marker {
		end++
	}
	length := end - indent
	if length < 3 {
		return 0, 0, "", false
	}
	return marker, length, line[end:], true
}

func isHistoryMarkdown(root, path string) bool {
	rel, err := filepath.Rel(filepath.Join(root, "docs", "migration", "history"), path)
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		strings.EqualFold(filepath.Ext(path), ".md")
}

func parseClosedGapMetadata(root, path string, data []byte) ([]closedGapOwner, error) {
	metadataText := markdownOutsideFences(data)
	prefix := firstDocumentLines(metadataText)
	allFields := closedGapAnyRE.FindAllIndex(metadataText, -1)
	if len(allFields) == 0 {
		return nil, nil
	}
	if len(allFields) > 1 {
		return nil, fmt.Errorf("%s: multiple Closed gaps metadata fields", displayPath(root, path))
	}
	if !closedGapAnyRE.MatchString(prefix) {
		return nil, fmt.Errorf("%s: Closed gaps metadata must appear in first 30 lines", displayPath(root, path))
	}

	matches := closedGapsRE.FindAllStringSubmatch(prefix, -1)
	if len(matches) != 1 {
		return nil, fmt.Errorf("%s: invalid Closed gaps metadata field", displayPath(root, path))
	}
	raw := strings.TrimSpace(matches[0][1])
	for index := 0; index < len(raw); index++ {
		if raw[index] != ',' {
			continue
		}
		if index == 0 || raw[index-1] == ' ' || index+2 >= len(raw) ||
			raw[index+1] != ' ' || raw[index+2] == ' ' {
			return nil, fmt.Errorf("%s: Closed gaps metadata must use comma-space separators", displayPath(root, path))
		}
	}

	parts := strings.Split(raw, ", ")
	owners := make([]closedGapOwner, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	previous := ""
	for _, part := range parts {
		match := closedGapIDRE.FindStringSubmatch(part)
		if match == nil {
			return nil, fmt.Errorf("%s: invalid closed Gap ID %q", displayPath(root, path), part)
		}
		if _, ok := seen[part]; ok {
			return nil, fmt.Errorf("%s: duplicates closed Gap %s", displayPath(root, path), part)
		}
		seen[part] = struct{}{}
		if previous != "" && compareRootGapIDs(part, previous) <= 0 {
			return nil, fmt.Errorf("%s: Closed gaps metadata must be in strictly increasing numeric order", displayPath(root, path))
		}
		previous = part
		owners = append(owners, closedGapOwner{id: part, path: path})
	}
	return owners, nil
}

func compareRootGapIDs(left, right string) int {
	leftNumber := strings.TrimPrefix(left, "G")
	rightNumber := strings.TrimPrefix(right, "G")
	if len(leftNumber) < len(rightNumber) {
		return -1
	}
	if len(leftNumber) > len(rightNumber) {
		return 1
	}
	return strings.Compare(leftNumber, rightNumber)
}

func parseOpenRootGaps(data []byte) map[string]struct{} {
	open := make(map[string]struct{})
	for _, match := range openGapRowRE.FindAllSubmatch(markdownOutsideFences(data), -1) {
		open[string(match[1])] = struct{}{}
	}
	return open
}

func validateClosedGapTraceability(root string, docFiles []string, contentCache map[string][]byte) []error {
	ownersByID := make(map[string][]closedGapOwner)
	var errs []error
	for _, path := range docFiles {
		if !isHistoryMarkdown(root, path) {
			continue
		}
		owners, err := parseClosedGapMetadata(root, path, contentCache[path])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, owner := range owners {
			ownersByID[owner.id] = append(ownersByID[owner.id], owner)
		}
	}

	open := parseOpenRootGaps(contentCache[filepath.Join(root, "docs", "migration", "REMAINING.md")])
	ids := make([]string, 0, len(ownersByID))
	for id := range ownersByID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return compareRootGapIDs(ids[i], ids[j]) < 0
	})

	for _, id := range ids {
		owners := ownersByID[id]
		if len(owners) > 1 {
			paths := make([]string, 0, len(owners))
			for _, owner := range owners {
				paths = append(paths, displayPath(root, owner.path))
			}
			sort.Strings(paths)
			errs = append(errs, fmt.Errorf("closed Gap %s has multiple historical owners: %s", id, strings.Join(paths, ", ")))
		}
		if _, ok := open[id]; ok {
			errs = append(errs, fmt.Errorf("closed Gap %s is still present in docs/migration/REMAINING.md (historical owner: %s)", id, displayPath(root, owners[0].path)))
		}
	}
	return errs
}

func hasLocalMarkdownDestination(data []byte, want string) bool {
	text := string(data)
	for _, match := range inlineLinkRE.FindAllStringSubmatchIndex(text, -1) {
		raw := text[match[2]:match[3]]
		destination, _, local := parseDestination(raw)
		if local && filepath.ToSlash(filepath.Clean(destination)) == want {
			return true
		}
	}
	return false
}

func validateAgentRuntimeOwnership(root, path string, data []byte) []error {
	if filepath.Clean(path) != filepath.Join(root, "AGENTS.md") {
		return nil
	}

	text := string(data)
	var errs []error
	for _, required := range []struct {
		kind  string
		value string
	}{
		{"runtime owner", "QueryEngine"},
		{"runtime owner", "projectGraphQueryKernel"},
	} {
		if !strings.Contains(text, required.value) {
			errs = append(errs, fmt.Errorf("%s: missing required %s %q",
				displayPath(root, path), required.kind, required.value))
		}
	}
	const architecturePath = "docs/architecture/runtime/query-engine.md"
	if !hasLocalMarkdownDestination(data, architecturePath) {
		errs = append(errs, fmt.Errorf("%s: missing required runtime architecture link %q",
			displayPath(root, path), architecturePath))
	}
	for _, command := range []string{
		"make change-plan",
		"make verify-focused",
		"make verify-merge",
		"make change-evidence",
		"make change-evidence-ready",
	} {
		if !containsStandaloneCommand(text, command) {
			errs = append(errs, fmt.Errorf("%s: missing public iteration command %q",
				displayPath(root, path), command))
		}
	}
	for _, detail := range []string{
		"retention_keep",
		"test-contract",
		"test-race",
		"test-pty",
		"test-fuzz-smoke",
		"test-e2e",
	} {
		if strings.Contains(text, detail) {
			errs = append(errs, fmt.Errorf("%s: contains iteration implementation detail %q",
				displayPath(root, path), detail))
		}
	}

	normalized := strings.Join(strings.Fields(text), " ")
	for _, retired := range []string{
		"imperative agent loop, not graph-based",
		"`engine/query.go` remains the production loop authority",
	} {
		if strings.Contains(normalized, retired) {
			errs = append(errs, fmt.Errorf("%s: contains retired runtime ownership claim %q",
				displayPath(root, path), retired))
		}
	}
	return errs
}

func containsStandaloneCommand(text, command string) bool {
	boundary := `[^[:alnum:]_-]`
	pattern := `(?:^|` + boundary + `)` + regexp.QuoteMeta(command) + `(?:$|` + boundary + `)`
	return regexp.MustCompile(pattern).FindStringIndex(text) != nil
}

func validateLocalLinkLabels(root, source string, data []byte) []error {
	var errs []error
	text := string(data)
	for _, match := range labeledLinkRE.FindAllStringSubmatchIndex(text, -1) {
		label := strings.Trim(strings.TrimSpace(text[match[2]:match[3]]), "`")
		raw := text[match[4]:match[5]]
		destination, _, local := parseDestination(raw)
		if !local || !strings.HasSuffix(strings.ToLower(label), ".md") {
			continue
		}
		target, err := resolveTarget(root, source, destination)
		if err != nil {
			continue // The main link validator reports the authoritative error.
		}
		labelPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(label)))
		if !strings.Contains(labelPath, "/") {
			if labelPath == filepath.Base(target) {
				continue
			}
		} else {
			destinationPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(destination)))
			targetRootPath := displayPath(root, target)
			targetDocsPath := strings.TrimPrefix(targetRootPath, "docs/")
			if labelPath == destinationPath || labelPath == targetRootPath || labelPath == targetDocsPath {
				continue
			}
		}
		line := 1 + strings.Count(text[:match[0]], "\n")
		errs = append(errs, fmt.Errorf("%s:%d: markdown link label %q does not match target path %q",
			displayPath(root, source), line, label, displayPath(root, target)))
	}
	return errs
}

func collectMarkdown(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		files = append(files, abs)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func parseDestination(raw string) (destination, fragment string, local bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "<") {
		if end := strings.Index(raw, ">"); end > 0 {
			raw = raw[1:end]
		}
	} else if fields := strings.Fields(raw); len(fields) > 0 {
		raw = fields[0]
	}
	if raw == "" {
		return "", "", false
	}
	if strings.HasPrefix(raw, "#") {
		fragment := strings.TrimPrefix(raw, "#")
		return "", fragment, fragment != ""
	}
	lower := strings.ToLower(raw)
	for _, prefix := range []string{"http://", "https://", "mailto:", "data:", "app://"} {
		if strings.HasPrefix(lower, prefix) {
			return "", "", false
		}
	}
	pathPart, fragment, _ := strings.Cut(raw, "#")
	pathPart, _, _ = strings.Cut(pathPart, "?")
	unescaped, err := url.PathUnescape(pathPart)
	if err != nil {
		return pathPart, fragment, true
	}
	return unescaped, fragment, true
}

func resolveTarget(root, source, destination string) (string, error) {
	if destination == "" {
		return "", errors.New("empty local link target")
	}
	var target string
	if filepath.IsAbs(destination) {
		target = filepath.Clean(destination)
	} else {
		target = filepath.Clean(filepath.Join(filepath.Dir(source), filepath.FromSlash(destination)))
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("local link escapes repository: %s", destination)
	}
	info, err := statConfined(root, target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("local link target does not exist: %s", destination)
		}
		return "", fmt.Errorf("local link target is not confined to repository: %s: %w", destination, err)
	}
	if info.IsDir() {
		readme := filepath.Join(target, "README.md")
		if _, err := statConfined(root, readme); err != nil {
			return "", fmt.Errorf("linked directory has no README.md: %s", destination)
		}
		target = readme
	}
	return target, nil
}

func validateAnchor(root, target, fragment string, headings map[string]map[string]struct{}, lines map[string][]string) error {
	decoded, err := url.PathUnescape(fragment)
	if err == nil {
		fragment = decoded
	}
	if match := lineAnchorRE.FindStringSubmatch(fragment); match != nil {
		fileLines, ok := lines[target]
		if !ok {
			fileLines, err = readLines(root, target)
			if err != nil {
				return err
			}
			lines[target] = fileLines
		}
		start, _ := strconv.Atoi(match[1])
		end := start
		if match[2] != "" {
			end, _ = strconv.Atoi(match[2])
		}
		if start < 1 || end < start || end > len(fileLines) {
			return fmt.Errorf("line anchor #%s is outside %s (1-%d)", fragment, filepath.ToSlash(target), len(fileLines))
		}
		if strings.TrimSpace(fileLines[start-1]) == "" {
			return fmt.Errorf("line anchor #%s lands on a blank line in %s", fragment, filepath.ToSlash(target))
		}
		return nil
	}
	if !strings.EqualFold(filepath.Ext(target), ".md") {
		return nil
	}
	targetHeadings, ok := headings[target]
	if !ok {
		targetHeadings, err = markdownHeadings(root, target)
		if err != nil {
			return err
		}
		headings[target] = targetHeadings
	}
	if _, ok := targetHeadings[fragment]; !ok {
		return fmt.Errorf("markdown anchor #%s does not exist in %s", fragment, filepath.ToSlash(target))
	}
	return nil
}

func readLines(root, path string) ([]string, error) {
	data, err := readFileConfined(root, path)
	if err != nil {
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func readFileConfined(root, path string) ([]byte, error) {
	rel, err := confinedRelativePath(root, path)
	if err != nil {
		return nil, err
	}
	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer rootDir.Close()
	data, err := rootDir.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("read path confined to repository root %s: %w", path, err)
	}
	return data, nil
}

func statConfined(root, path string) (os.FileInfo, error) {
	rel, err := confinedRelativePath(root, path)
	if err != nil {
		return nil, err
	}
	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer rootDir.Close()
	return rootDir.Stat(rel)
}

func confinedRelativePath(root, path string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository root: %s", path)
	}
	return rel, nil
}

func markdownHeadings(root, path string) (map[string]struct{}, error) {
	lines, err := readLines(root, path)
	if err != nil {
		return nil, err
	}
	headings := make(map[string]struct{})
	counts := make(map[string]int)
	for _, line := range lines {
		match := headingRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		base := githubSlug(match[1])
		if base == "" {
			continue
		}
		slug := base
		if count := counts[base]; count > 0 {
			slug = fmt.Sprintf("%s-%d", base, count)
		}
		counts[base]++
		headings[slug] = struct{}{}
	}
	return headings, nil
}

func githubSlug(text string) string {
	text = htmlTagRE.ReplaceAllString(text, "")
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	lastDash := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r) || r == '-':
			if b.Len() > 0 && !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func walkGraph(start string, graph map[string]map[string]struct{}) map[string]struct{} {
	seen := map[string]struct{}{start: {}}
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for next := range graph[current] {
			if _, ok := seen[next]; ok {
				continue
			}
			seen[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	return seen
}

func displayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
