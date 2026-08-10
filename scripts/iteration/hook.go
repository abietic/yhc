package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxHookInputBytes    = 256 << 10
	maxHookContextBytes  = 2048
	maxHookSessionID     = 512
	maxHookAgentID       = 256
	maxHookChildren      = 256
	hookLockWait         = 2 * time.Second
	hookLockPollInterval = 10 * time.Millisecond
)

type HookEventName string

const (
	HookSessionStart  HookEventName = "SessionStart"
	HookPostToolUse   HookEventName = "PostToolUse"
	HookSubagentStart HookEventName = "SubagentStart"
	HookSubagentStop  HookEventName = "SubagentStop"
	HookStop          HookEventName = "Stop"
	HookSessionEnd    HookEventName = "SessionEnd"
)

type HookInput struct {
	SessionID      string        `json:"session_id"`
	CWD            string        `json:"cwd"`
	HookEventName  HookEventName `json:"hook_event_name"`
	Source         string        `json:"source,omitempty"`
	AgentID        string        `json:"agent_id,omitempty"`
	AgentType      string        `json:"agent_type,omitempty"`
	StopHookActive bool          `json:"stop_hook_active,omitempty"`
}

type HookSessionState struct {
	SchemaVersion        int               `json:"schema_version"`
	Open                 bool              `json:"open"`
	InitialDigest        string            `json:"initial_digest"`
	CurrentDigest        string            `json:"current_digest"`
	Branch               string            `json:"branch"`
	BaseRef              string            `json:"base_ref"`
	EvidenceState        string            `json:"evidence_state"`
	Pending              []string          `json:"pending"`
	CreatedTrackedChange bool              `json:"created_tracked_change"`
	StopContinued        bool              `json:"stop_continued"`
	Incomplete           bool              `json:"incomplete"`
	Children             map[string]string `json:"children"`
}

type HookStateStore interface {
	Update(
		sessionID string,
		transition func(HookSessionState, bool) (HookSessionState, error),
	) (HookSessionState, error)
}

type HookSnapshot struct {
	Plan     Plan
	Evidence Evidence
	Branch   string
}

type fileHookStateStore struct {
	root string
}

func newFileHookStateStore(root string) *fileHookStateStore {
	return &fileHookStateStore{root: root}
}

func parseHookEvent(command string) (HookEventName, bool) {
	switch command {
	case "session-start":
		return HookSessionStart, true
	case "post-tool-use":
		return HookPostToolUse, true
	case "subagent-start":
		return HookSubagentStart, true
	case "subagent-stop":
		return HookSubagentStop, true
	case "stop":
		return HookStop, true
	case "session-end":
		return HookSessionEnd, true
	default:
		return "", false
	}
}

func runHook(
	event HookEventName,
	input io.Reader,
	stdout io.Writer,
	repositoryRoot string,
	snapshot HookSnapshot,
	store HookStateStore,
) error {
	if input == nil || stdout == nil || store == nil {
		return errors.New("hook adapter dependency is unavailable")
	}
	hookInput, err := decodeHookInput(input)
	if err != nil {
		return err
	}
	if err := validateHookInput(event, hookInput); err != nil {
		return err
	}
	if err := validateHookCWD(repositoryRoot, hookInput.CWD); err != nil {
		return err
	}
	if err := validateHookSnapshot(snapshot); err != nil {
		return err
	}

	evidence := snapshot.Evidence
	if len(evidence.Gates) == 0 {
		evidence = initialEvidence(snapshot.Plan)
	}
	pending := pendingHookTargets(snapshot.Plan, evidence)
	blockStop := false
	state, err := store.Update(hookInput.SessionID, func(current HookSessionState, exists bool) (HookSessionState, error) {
		if !exists {
			current = initialHookSessionState(snapshot, evidence, pending)
		} else {
			previousDigest := current.CurrentDigest
			updateHookSnapshot(&current, snapshot, evidence, pending)
			if event == HookPostToolUse && previousDigest != snapshot.Plan.DiffDigest {
				current.CreatedTrackedChange = true
			}
			if event == HookSessionStart {
				current.Incomplete = false
			}
		}

		switch event {
		case HookSessionStart:
			// Initialization above is the complete transition.
		case HookPostToolUse:
		case HookSubagentStart:
			if _, present := current.Children[hookInput.AgentID]; !present && len(current.Children) >= maxHookChildren {
				return HookSessionState{}, errors.New("hook child limit exceeded")
			}
			current.Children[hookInput.AgentID] = "running"
		case HookSubagentStop:
			if _, present := current.Children[hookInput.AgentID]; !present && len(current.Children) >= maxHookChildren {
				return HookSessionState{}, errors.New("hook child limit exceeded")
			}
			current.Children[hookInput.AgentID] = "stopped"
		case HookStop:
			blockStop = current.CreatedTrackedChange && len(snapshot.Plan.Changed) > 0 &&
				evidence.State != "evidence_ready" && !current.StopContinued && !hookInput.StopHookActive
			if blockStop {
				current.StopContinued = true
			}
		case HookSessionEnd:
			current.Open = false
			current.Incomplete = len(snapshot.Plan.Changed) > 0 && evidence.State != "evidence_ready"
		default:
			return HookSessionState{}, errors.New("unsupported hook event")
		}
		return current, nil
	})
	if err != nil {
		return err
	}

	switch {
	case event == HookSessionStart && (len(snapshot.Plan.Changed) > 0 || len(state.Pending) > 0):
		return writeSessionStartOutput(stdout, state)
	case event == HookStop && blockStop:
		return renderJSON(struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}{
			Decision: "block",
			Reason:   "This session created a tracked change and current iteration evidence is stale. Run the risk-selected verification or record the blocking gate before stopping.",
		}, stdout)
	default:
		return nil
	}
}

func decodeHookInput(reader io.Reader) (HookInput, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxHookInputBytes+1))
	if err != nil {
		return HookInput{}, errors.New("read hook input")
	}
	if len(data) > maxHookInputBytes {
		return HookInput{}, errors.New("hook input exceeds 256 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var input HookInput
	if err := decoder.Decode(&input); err != nil {
		return HookInput{}, errors.New("invalid hook input")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return HookInput{}, errors.New("hook input must contain exactly one JSON document")
	}
	return input, nil
}

func validateHookInput(event HookEventName, input HookInput) error {
	if input.HookEventName != event {
		return errors.New("hook event does not match command")
	}
	if !safeHookIdentifier(input.SessionID, maxHookSessionID) {
		return errors.New("invalid hook session id")
	}
	if input.CWD == "" || len(input.CWD) > 4096 || !utf8.ValidString(input.CWD) || strings.ContainsRune(input.CWD, 0) {
		return errors.New("invalid hook working directory")
	}
	if input.Source != "" && !oneOf(input.Source, "startup", "resume", "clear", "compact") {
		return errors.New("invalid hook session source")
	}
	if (event == HookSubagentStart || event == HookSubagentStop) && !safeHookIdentifier(input.AgentID, maxHookAgentID) {
		return errors.New("invalid hook agent id")
	}
	if input.AgentType != "" && !safeHookIdentifier(input.AgentType, 128) {
		return errors.New("invalid hook agent type")
	}
	return nil
}

func safeHookIdentifier(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateHookCWD(repositoryRoot, cwd string) error {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return errors.New("resolve hook repository root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("resolve hook repository root")
	}
	workdir := cwd
	if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(root, workdir)
	}
	workdir, err = filepath.EvalSymlinks(workdir)
	if err != nil {
		return errors.New("resolve hook working directory")
	}
	info, err := os.Stat(workdir)
	if err != nil || !info.IsDir() {
		return errors.New("resolve hook working directory")
	}
	relative, err := filepath.Rel(root, workdir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("hook working directory escapes repository root")
	}
	return nil
}

func validateHookSnapshot(snapshot HookSnapshot) error {
	if snapshot.Plan.SchemaVersion != 1 || !digestPattern.MatchString(snapshot.Plan.DiffDigest) {
		return errors.New("invalid hook iteration plan")
	}
	if !safeContextValue(snapshot.Branch) || !safeContextValue(snapshot.Plan.BaseRef) {
		return errors.New("invalid hook iteration identity")
	}
	if snapshot.Evidence.SchemaVersion != 0 && snapshot.Evidence.SchemaVersion != 1 {
		return errors.New("invalid hook iteration evidence")
	}
	return nil
}

func safeContextValue(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || character == '\n' || character == '\r' {
			return false
		}
	}
	return true
}

func initialHookSessionState(snapshot HookSnapshot, evidence Evidence, pending []string) HookSessionState {
	return HookSessionState{
		SchemaVersion: 1,
		Open:          true,
		InitialDigest: snapshot.Plan.DiffDigest,
		CurrentDigest: snapshot.Plan.DiffDigest,
		Branch:        snapshot.Branch,
		BaseRef:       snapshot.Plan.BaseRef,
		EvidenceState: evidence.State,
		Pending:       append([]string(nil), pending...),
		Children:      make(map[string]string),
	}
}

func updateHookSnapshot(state *HookSessionState, snapshot HookSnapshot, evidence Evidence, pending []string) {
	state.SchemaVersion = 1
	state.Open = true
	state.CurrentDigest = snapshot.Plan.DiffDigest
	state.Branch = snapshot.Branch
	state.BaseRef = snapshot.Plan.BaseRef
	state.EvidenceState = evidence.State
	state.Pending = append(state.Pending[:0], pending...)
	if state.Children == nil {
		state.Children = make(map[string]string)
	}
}

func pendingHookTargets(plan Plan, evidence Evidence) []string {
	if len(plan.Changed) == 0 {
		return nil
	}
	var pending []string
	for _, level := range []VerifyLevel{VerifyFocused, VerifyMerge} {
		for _, target := range expectedTargets(plan, level) {
			gate := gateFor(evidence, target, string(level))
			if gate == nil || gate.Status != GatePass && gate.Status != GateNotApplicable {
				pending = append(pending, string(level)+"/"+target)
			}
		}
	}
	slices.Sort(pending)
	return unique(pending)
}

func writeSessionStartOutput(writer io.Writer, state HookSessionState) error {
	context := hookSessionContext(state)
	output := struct {
		HookSpecificOutput struct {
			HookEventName     HookEventName `json:"hookEventName"`
			AdditionalContext string        `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	output.HookSpecificOutput.HookEventName = HookSessionStart
	output.HookSpecificOutput.AdditionalContext = context
	return renderJSON(output, writer)
}

func hookSessionContext(state HookSessionState) string {
	branch := sanitizeContextAtom(state.Branch, 256)
	base := sanitizeContextAtom(state.BaseRef, 256)
	evidenceState := sanitizeContextAtom(state.EvidenceState, 64)
	prefix := fmt.Sprintf("Iteration: branch=%s base=%s state=%s", branch, base, evidenceState)
	detail := " pending=none"
	if len(state.Pending) > 0 {
		detail = " pending=" + strings.Join(state.Pending, ",")
	}
	if len(prefix)+len(detail) <= maxHookContextBytes {
		return prefix + detail
	}
	remaining := maxHookContextBytes - len(prefix)
	if remaining <= 0 {
		return truncateUTF8(prefix, maxHookContextBytes)
	}
	return prefix + truncateUTF8(detail, remaining)
}

func sanitizeContextAtom(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return '_'
		}
		return character
	}, value)
	return truncateUTF8(value, limit)
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	data := []byte(value)[:limit]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}

func (store *fileHookStateStore) Update(
	sessionID string,
	transition func(HookSessionState, bool) (HookSessionState, error),
) (HookSessionState, error) {
	if !safeHookIdentifier(sessionID, maxHookSessionID) || transition == nil {
		return HookSessionState{}, errors.New("invalid hook state request")
	}
	repository, err := os.OpenRoot(store.root)
	if err != nil {
		return HookSessionState{}, errors.New("open hook repository root")
	}
	defer repository.Close()
	directory, err := openStrictDir(repository, filepath.ToSlash(filepath.Join("build", "iteration", "hooks")), true)
	if err != nil {
		return HookSessionState{}, errors.New("open hook state directory")
	}
	defer directory.Close()
	info, err := directory.Stat(".")
	if err != nil || info.Mode().Perm() != 0o700 {
		return HookSessionState{}, errors.New("hook state directory mode is not 0700")
	}

	name := hookStateName(sessionID)
	release, err := store.acquire(directory, name+".lock")
	if err != nil {
		return HookSessionState{}, err
	}
	defer release()

	current, exists, err := readHookState(directory, name)
	if err != nil {
		return HookSessionState{}, err
	}
	next, err := transition(current, exists)
	if err != nil {
		return HookSessionState{}, err
	}
	if err := validateHookState(next); err != nil {
		return HookSessionState{}, err
	}
	if err := writeJSONAtomically(directory, name, next, nil); err != nil {
		return HookSessionState{}, errors.New("persist hook state")
	}
	return next, nil
}

func hookStateName(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func (store *fileHookStateStore) acquire(directory *os.Root, name string) (func(), error) {
	file, err := directory.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errors.New("open hook state lock")
	}
	info, err := directory.Lstat(name)
	fileInfo, fileErr := file.Stat()
	if err != nil || fileErr != nil || info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !os.SameFile(info, fileInfo) {
		_ = file.Close()
		return nil, errors.New("invalid hook state lock")
	}
	deadline := time.Now().Add(hookLockWait)
	for {
		locked, lockErr := tryLockHookFile(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, errors.New("lock hook state")
		}
		if locked {
			return func() {
				_ = unlockHookFile(file)
				_ = file.Close()
			}, nil
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, errors.New("hook state lock timed out")
		}
		time.Sleep(hookLockPollInterval)
	}
}

func readHookState(directory *os.Root, name string) (HookSessionState, bool, error) {
	info, err := directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return HookSessionState{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 || info.Size() > maxHookInputBytes {
		return HookSessionState{}, false, errors.New("invalid hook state file")
	}
	file, err := directory.Open(name)
	if err != nil {
		return HookSessionState{}, false, errors.New("read hook state file")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxHookInputBytes+1))
	decoder.DisallowUnknownFields()
	var state HookSessionState
	if err := decoder.Decode(&state); err != nil {
		return HookSessionState{}, false, errors.New("invalid hook state document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return HookSessionState{}, false, errors.New("invalid hook state document")
	}
	if err := validateHookState(state); err != nil {
		return HookSessionState{}, false, err
	}
	return state, true, nil
}

func validateHookState(state HookSessionState) error {
	if state.SchemaVersion != 1 || !digestPattern.MatchString(state.InitialDigest) || !digestPattern.MatchString(state.CurrentDigest) {
		return errors.New("invalid hook state document")
	}
	if !safeContextValue(state.Branch) || !safeContextValue(state.BaseRef) ||
		!oneOf(state.EvidenceState, "planned", "changed", "focused_verified", "merge_verified", "evidence_ready") {
		return errors.New("invalid hook state document")
	}
	if len(state.Children) > maxHookChildren || len(state.Pending) > 1024 {
		return errors.New("invalid hook state document")
	}
	for id, status := range state.Children {
		if !safeHookIdentifier(id, maxHookAgentID) || !oneOf(status, "running", "stopped") {
			return errors.New("invalid hook state document")
		}
	}
	for _, target := range state.Pending {
		if !safeContextValue(target) {
			return errors.New("invalid hook state document")
		}
	}
	return nil
}

func commandBranchName(ctx context.Context, root string) (string, error) {
	output, err := (commandGitSource{root: root}).runUnchecked(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", errors.New("resolve hook branch")
	}
	branch := strings.TrimSpace(string(output))
	if !safeContextValue(branch) {
		return "", errors.New("resolve hook branch")
	}
	return branch, nil
}
