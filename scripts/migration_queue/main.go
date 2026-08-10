package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	generatedBegin = "<!-- migration-queue:begin -->"
	generatedEnd   = "<!-- migration-queue:end -->"
	readyLimit     = 1
)

var (
	sliceIDPattern  = regexp.MustCompile(`^P[0-9]+(?:\.[0-9A-Za-z]+)*$`)
	gapIDPattern    = regexp.MustCompile(`^G[0-9]+$`)
	stableIDPattern = regexp.MustCompile(
		`^[a-z0-9]+(?:-[a-z0-9]+)*$`,
	)
	gapInventoryRowPattern = regexp.MustCompile(`(?m)^\|\s*(G[0-9]+)\s*\|`)
)

type queueFile struct {
	Version    int                `yaml:"version"`
	Updated    string             `yaml:"updated"`
	ReadyLimit int                `yaml:"ready_limit"`
	Slices     []queueSlice       `yaml:"slices"`
	Deferred   []deferredDecision `yaml:"deferred"`
}

type queueSlice struct {
	ID        string    `yaml:"id"`
	State     string    `yaml:"state"`
	Priority  int       `yaml:"priority"`
	Gaps      []string  `yaml:"gaps"`
	DependsOn []string  `yaml:"depends_on"`
	BlockedBy []string  `yaml:"blocked_by"`
	Promotion promotion `yaml:"promotion"`
	Contract  string    `yaml:"contract"`
	Outcome   string    `yaml:"outcome"`
}

type promotion struct {
	ID    string `yaml:"id"`
	State string `yaml:"state"`
	Label string `yaml:"label"`
	Link  string `yaml:"link"`
}

type deferredDecision struct {
	ID     string   `yaml:"id"`
	Gaps   []string `yaml:"gaps"`
	Gate   string   `yaml:"gate"`
	Reason string   `yaml:"reason"`
}

type sliceDescription struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	State         string `json:"state"`
	Contract      string `json:"contract"`
	Outcome       string `json:"outcome"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migration_queue", flag.ContinueOnError)
	flags.SetOutput(stderr)
	queuePath := flags.String("queue", "docs/migration/queue.yaml", "queue data path")
	planPath := flags.String("plan", "docs/migration/PLAN.md", "rendered plan path")
	sliceID := flags.String("slice-id", "", "active slice identifier for describe")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: migration_queue [flags] check|render|print|describe")
		return 2
	}
	if flags.Arg(0) == "describe" && strings.TrimSpace(*sliceID) == "" {
		fmt.Fprintln(stderr, "usage: migration_queue --slice-id <ID> describe")
		return 2
	}

	repositoryRoot, err := os.OpenRoot(".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer repositoryRoot.Close()

	queueRoot, err := repositoryRoot.OpenRoot(filepath.Dir(*queuePath))
	if err == nil {
		defer queueRoot.Close()
	}
	var queue queueFile
	if err == nil {
		queue, err = loadQueue(queueRoot, filepath.Base(*queuePath))
	}
	if err == nil {
		err = validateQueue(queue, queueRoot)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fragment := renderQueue(queue)
	switch flags.Arg(0) {
	case "check":
		plan, readErr := repositoryRoot.ReadFile(*planPath)
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 1
		}
		current, extractErr := extractGeneratedBlock(string(plan))
		if extractErr != nil {
			fmt.Fprintln(stderr, extractErr)
			return 1
		}
		if current != fragment {
			fmt.Fprintln(stderr, "generated migration queue in PLAN.md is stale; run `go run ./scripts/migration_queue render`")
			return 1
		}
		fmt.Fprintf(stdout, "validated %d active slices and %d deferred decisions\n", len(queue.Slices), len(queue.Deferred))
	case "render":
		plan, readErr := repositoryRoot.ReadFile(*planPath)
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 1
		}
		updated, replaceErr := replaceGeneratedBlock(string(plan), fragment)
		if replaceErr != nil {
			fmt.Fprintln(stderr, replaceErr)
			return 1
		}
		if writeErr := repositoryRoot.WriteFile(*planPath, []byte(updated), 0o644); writeErr != nil {
			fmt.Fprintln(stderr, writeErr)
			return 1
		}
		fmt.Fprintf(stdout, "rendered %s from %s\n", *planPath, *queuePath)
	case "print":
		fmt.Fprint(stdout, fragment)
	case "describe":
		slice, ok := findActiveSlice(queue, *sliceID)
		if !ok {
			fmt.Fprintf(stderr, "active executable slice %q not found\n", *sliceID)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(sliceDescription{
			SchemaVersion: 1,
			ID:            slice.ID,
			State:         slice.State,
			Contract:      slice.Contract,
			Outcome:       slice.Outcome,
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", flags.Arg(0))
		return 2
	}
	return 0
}

func findActiveSlice(queue queueFile, id string) (queueSlice, bool) {
	for _, slice := range queue.Slices {
		if slice.ID == id {
			return slice, true
		}
	}
	return queueSlice{}, false
}

func loadQueue(root *os.Root, path string) (queueFile, error) {
	data, err := root.ReadFile(path)
	if err != nil {
		return queueFile{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var queue queueFile
	if err := decoder.Decode(&queue); err != nil {
		return queueFile{}, fmt.Errorf("decode queue: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return queueFile{}, fmt.Errorf("decode trailing queue document: %w", err)
		}
		return queueFile{}, errors.New("decode queue: multiple YAML documents are not allowed")
	}
	return queue, nil
}

func validateQueue(queue queueFile, root *os.Root) error {
	var problems []string
	gapInventory, gapInventoryErr := loadGapInventory(root)
	if gapInventoryErr != nil {
		problems = append(problems, "gap inventory: "+gapInventoryErr.Error())
	}
	if queue.Version != 1 {
		problems = append(problems, "version must be 1")
	}
	if _, err := time.Parse(time.DateOnly, queue.Updated); err != nil {
		problems = append(problems, "updated must use YYYY-MM-DD")
	}
	if queue.ReadyLimit != readyLimit {
		problems = append(problems, "ready_limit must be 1")
	}
	byID := make(map[string]queueSlice, len(queue.Slices))
	priorities := make(map[int]string, len(queue.Slices))
	readyCount := 0
	for _, slice := range queue.Slices {
		if !sliceIDPattern.MatchString(slice.ID) {
			problems = append(problems, fmt.Sprintf("invalid slice id %q", slice.ID))
		}
		if _, exists := byID[slice.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate slice id %s", slice.ID))
		}
		byID[slice.ID] = slice
		if slice.Priority <= 0 {
			problems = append(problems, fmt.Sprintf("%s priority must be positive", slice.ID))
		}
		if other, exists := priorities[slice.Priority]; exists {
			problems = append(problems, fmt.Sprintf("%s and %s share priority %d", other, slice.ID, slice.Priority))
		}
		priorities[slice.Priority] = slice.ID
		if !containsString([]string{"ready", "queued", "blocked"}, slice.State) {
			problems = append(problems, fmt.Sprintf("%s has invalid state %q", slice.ID, slice.State))
		}
		if slice.State == "ready" {
			readyCount++
			if slice.Promotion.State != "satisfied" {
				problems = append(problems, fmt.Sprintf("%s is ready with an unsatisfied promotion gate", slice.ID))
			}
			if len(slice.DependsOn) != 0 || len(slice.BlockedBy) != 0 {
				problems = append(problems, fmt.Sprintf("%s is ready but still has dependencies or blockers", slice.ID))
			}
		}
		if slice.State == "blocked" && len(slice.BlockedBy) == 0 {
			problems = append(problems, fmt.Sprintf("%s is blocked without blocked_by", slice.ID))
		}
		if len(slice.Gaps) == 0 {
			problems = append(problems, fmt.Sprintf("%s must own at least one gap", slice.ID))
		}
		for _, gap := range slice.Gaps {
			if !gapIDPattern.MatchString(gap) {
				problems = append(problems, fmt.Sprintf("%s has invalid gap %q", slice.ID, gap))
			} else if gapInventoryErr == nil {
				if _, exists := gapInventory[gap]; !exists {
					problems = append(problems, fmt.Sprintf("%s references gap %s missing from REMAINING.md", slice.ID, gap))
				}
			}
		}
		if !stableIDPattern.MatchString(slice.Promotion.ID) {
			problems = append(problems, fmt.Sprintf("%s has invalid promotion id %q", slice.ID, slice.Promotion.ID))
		}
		if !containsString([]string{"pending", "satisfied"}, slice.Promotion.State) {
			problems = append(problems, fmt.Sprintf("%s has invalid promotion state %q", slice.ID, slice.Promotion.State))
		}
		if strings.TrimSpace(slice.Promotion.Label) == "" || strings.TrimSpace(slice.Outcome) == "" {
			problems = append(problems, fmt.Sprintf("%s requires promotion label and outcome", slice.ID))
		}
		for _, link := range []string{slice.Contract, slice.Promotion.Link} {
			if err := validateRelativeLink(root, link); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", slice.ID, err))
			}
		}
	}
	if readyCount > readyLimit {
		problems = append(problems, fmt.Sprintf("ready slices %d exceed limit %d", readyCount, readyLimit))
	}

	if cycle := dependencyCycle(byID); len(cycle) > 0 {
		problems = append(problems, "dependency cycle: "+strings.Join(cycle, " -> "))
	}
	for _, slice := range queue.Slices {
		seen := make(map[string]struct{}, len(slice.DependsOn))
		for _, dependency := range slice.DependsOn {
			if dependency == slice.ID {
				problems = append(problems, fmt.Sprintf("%s depends on itself", slice.ID))
				continue
			}
			if _, exists := seen[dependency]; exists {
				problems = append(problems, fmt.Sprintf("%s repeats dependency %s", slice.ID, dependency))
			}
			seen[dependency] = struct{}{}
			owner, exists := byID[dependency]
			if !exists {
				problems = append(problems, fmt.Sprintf("%s depends on unknown active slice %s", slice.ID, dependency))
				continue
			}
			if owner.Priority >= slice.Priority {
				problems = append(problems, fmt.Sprintf("priority order is not topological: %s must precede %s", dependency, slice.ID))
			}
		}
	}

	allIDs := make(map[string]struct{}, len(byID)+len(queue.Deferred))
	for id := range byID {
		allIDs[id] = struct{}{}
	}
	for _, decision := range queue.Deferred {
		if !sliceIDPattern.MatchString(decision.ID) {
			problems = append(problems, fmt.Sprintf("invalid deferred id %q", decision.ID))
		}
		if _, exists := allIDs[decision.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate queue/deferred id %s", decision.ID))
		}
		allIDs[decision.ID] = struct{}{}
		if len(decision.Gaps) == 0 || strings.TrimSpace(decision.Reason) == "" {
			problems = append(problems, fmt.Sprintf("%s requires gaps and reason", decision.ID))
		}
		for _, gap := range decision.Gaps {
			if !gapIDPattern.MatchString(gap) {
				problems = append(problems, fmt.Sprintf("%s has invalid gap %q", decision.ID, gap))
			} else if gapInventoryErr == nil {
				if _, exists := gapInventory[gap]; !exists {
					problems = append(problems, fmt.Sprintf("%s references gap %s missing from REMAINING.md", decision.ID, gap))
				}
			}
		}
		if err := validateRelativeLink(root, decision.Gate); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", decision.ID, err))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}

func loadGapInventory(root *os.Root) (map[string]struct{}, error) {
	data, err := root.ReadFile("REMAINING.md")
	if err != nil {
		return nil, fmt.Errorf("read REMAINING.md: %w", err)
	}
	matches := gapInventoryRowPattern.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return nil, errors.New("REMAINING.md has no gap inventory rows")
	}
	gaps := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		gap := string(match[1])
		if _, exists := gaps[gap]; exists {
			return nil, fmt.Errorf("REMAINING.md repeats gap %s", gap)
		}
		gaps[gap] = struct{}{}
	}
	return gaps, nil
}

func validateRelativeLink(root *os.Root, link string) error {
	path := strings.SplitN(link, "#", 2)[0]
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("invalid relative link %q", link)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("link escapes migration root: %q", link)
	}
	info, err := root.Stat(clean)
	if err != nil {
		return fmt.Errorf("link target %q: %w", link, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("link target %q is not a regular file", link)
	}
	return nil
}

func dependencyCycle(byID map[string]queueSlice) []string {
	const (
		unseen = iota
		visiting
		done
	)
	state := make(map[string]int, len(byID))
	var stack []string
	var visit func(string) []string
	visit = func(id string) []string {
		state[id] = visiting
		stack = append(stack, id)
		for _, dependency := range byID[id].DependsOn {
			if _, exists := byID[dependency]; !exists {
				continue
			}
			switch state[dependency] {
			case unseen:
				if cycle := visit(dependency); len(cycle) > 0 {
					return cycle
				}
			case visiting:
				for index, candidate := range stack {
					if candidate == dependency {
						return append(append([]string{}, stack[index:]...), dependency)
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
		return nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == unseen {
			if cycle := visit(id); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}

func renderQueue(queue queueFile) string {
	slices := append([]queueSlice(nil), queue.Slices...)
	sort.Slice(slices, func(i, j int) bool {
		return slices[i].Priority < slices[j].Priority
	})
	ready, queued, blocked := 0, 0, 0
	for _, slice := range slices {
		switch slice.State {
		case "ready":
			ready++
		case "queued":
			queued++
		case "blocked":
			blocked++
		}
	}

	var output strings.Builder
	output.WriteString(generatedBegin + "\n")
	output.WriteString("> Generated from [`queue.yaml`](queue.yaml). Run `go run ./scripts/migration_queue render` after changing queue data; `make docs-check` rejects drift.\n\n")
	fmt.Fprintf(&output, "**Snapshot:** %s; %d `Ready`, %d `Queued`, %d `Blocked`, %d deferred decisions.\n\n", queue.Updated, ready, queued, blocked, len(queue.Deferred))
	output.WriteString("```mermaid\n")
	output.WriteString("flowchart LR\n")
	output.WriteString("    accTitle: Active evolution promotion topology\n")
	output.WriteString("    accDescr: Each active slice follows its promotion gate. Solid gate edges are satisfied; dotted gate edges are pending. Future hard slice dependencies are rendered as solid slice-to-slice edges.\n")
	if len(slices) == 0 {
		output.WriteString("    no_active[\"No accepted active slices\"]\n")
	} else {
		for _, slice := range slices {
			nodeID := mermaidID(slice.ID)
			gateID := "gate_" + nodeID
			gateState := "pending"
			edge := "-.->"
			if slice.Promotion.State == "satisfied" {
				gateState = "satisfied"
				edge = "-->"
			}
			fmt.Fprintf(&output, "    %s[\"Gate %s: %s\"] %s %s[\"%s %s\"]\n", gateID, gateState, mermaidLabel(slice.Promotion.Label), edge, nodeID, slice.ID, stateLabel(slice.State))
		}
		for _, slice := range slices {
			for _, dependency := range slice.DependsOn {
				fmt.Fprintf(&output, "    %s --> %s\n", mermaidID(dependency), mermaidID(slice.ID))
			}
		}
		output.WriteString("    classDef ready stroke-width:3px\n")
		for _, slice := range slices {
			if slice.State == "ready" {
				fmt.Fprintf(&output, "    class %s ready\n", mermaidID(slice.ID))
			}
		}
	}
	output.WriteString("```\n\n")
	if len(slices) == 0 {
		output.WriteString("There is no accepted incomplete slice. Open gaps remain in `REMAINING.md` until intake accepts a successor; they do not become queue rows automatically.\n")
	} else {
		output.WriteString("The diagram is a gate/dependency view, not a schedule. The table below is its text equivalent and adds risk priority.\n\n")
		output.WriteString("| Priority | Slice and state | Hard dependencies | Promotion gate | Gap | Accepted outcome |\n")
		output.WriteString("|---:|---|---|---|---|---|\n")
		for _, slice := range slices {
			dependencies := "—"
			if len(slice.DependsOn) > 0 {
				dependencies = strings.Join(slice.DependsOn, ", ")
			}
			fmt.Fprintf(
				&output,
				"| %d | [%s](%s) `%s` | %s | [%s](%s) `%s` | [%s](REMAINING.md#verified-current-implementation-gaps) | %s |\n",
				slice.Priority,
				slice.ID,
				slice.Contract,
				stateLabel(slice.State),
				dependencies,
				slice.Promotion.Label,
				slice.Promotion.Link,
				stateLabel(slice.Promotion.State),
				strings.Join(slice.Gaps, ", "),
				slice.Outcome,
			)
		}
	}
	if len(queue.Deferred) > 0 {
		output.WriteString("\n### Deferred decisions\n\n")
		output.WriteString("Deferred entries are reproduced gaps without an executable queue row.\n\n")
		output.WriteString("| Decision | Gap | Re-entry gate | Reason |\n")
		output.WriteString("|---|---|---|---|\n")
		for _, decision := range queue.Deferred {
			fmt.Fprintf(&output, "| %s | [%s](REMAINING.md#verified-current-implementation-gaps) | [evidence gate](%s) | %s |\n", decision.ID, strings.Join(decision.Gaps, ", "), decision.Gate, decision.Reason)
		}
	}
	output.WriteString(generatedEnd + "\n")
	return output.String()
}

func extractGeneratedBlock(plan string) (string, error) {
	begin := strings.Index(plan, generatedBegin)
	end := strings.Index(plan, generatedEnd)
	if begin < 0 || end < 0 || end < begin {
		return "", errors.New("PLAN.md is missing one ordered migration queue marker pair")
	}
	end += len(generatedEnd)
	if end < len(plan) && plan[end] == '\n' {
		end++
	}
	return plan[begin:end], nil
}

func replaceGeneratedBlock(plan, fragment string) (string, error) {
	current, err := extractGeneratedBlock(plan)
	if err != nil {
		return "", err
	}
	return strings.Replace(plan, current, fragment, 1), nil
}

func mermaidID(id string) string {
	return "slice_" + strings.NewReplacer(".", "_", "-", "_").Replace(strings.ToLower(id))
}

func mermaidLabel(value string) string {
	return strings.NewReplacer("\"", "'", "\n", " ").Replace(value)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stateLabel(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
