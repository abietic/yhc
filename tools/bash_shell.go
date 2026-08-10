package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/containment"
	"github.com/google/uuid"
)

// ShellResult holds the output and metadata from a shell command execution.
type ShellResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	CWD       string
	Duration  time.Duration
	TimedOut  bool
	Canceled  bool
	Truncated bool
}

// ShellInfo provides metadata about a running shell session.
type ShellInfo struct {
	ID        string
	CWD       string
	Running   bool
	StartedAt time.Time
}

// PersistentShell represents a long-lived shell process that accepts
// multiple commands over its lifetime via stdin/stdout.
type PersistentShell struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdoutRC  io.ReadCloser
	stderrRC  io.ReadCloser
	stdout    *bufio.Reader
	stderr    *bufio.Reader
	mu        sync.Mutex
	cwd       string
	env       map[string]string
	running   bool
	shellID   string
	startedAt time.Time
	stderrBuf *stderrBuffer
	policy    *containment.Snapshot
	binding   *containment.Binding
	outputMax int64
}

// UpdateCWD sends a pwd command to the shell and returns the current directory.
func (s *PersistentShell) UpdateCWD() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return s.cwd
	}

	sentinel := uuid.New().String()
	startMarker := fmt.Sprintf("___SENTINEL_START_%s___", sentinel)
	endMarker := fmt.Sprintf("___SENTINEL_END_%s_", sentinel)

	cmd := fmt.Sprintf("echo '%s'; pwd; echo '%s'\"$?\"'___'\n", startMarker, endMarker)
	_, err := io.WriteString(s.stdin, cmd)
	if err != nil {
		return s.cwd
	}

	// Read until we find the end sentinel
	inOutput := false
	var output strings.Builder
	for {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\n\r")

		if strings.Contains(line, startMarker) {
			inOutput = true
			continue
		}
		if strings.Contains(line, endMarker) {
			break
		}
		if inOutput {
			output.WriteString(line)
		}
	}

	if result := strings.TrimSpace(output.String()); result != "" {
		s.cwd = result
	}
	return s.cwd
}

// ShellManager manages multiple persistent shell sessions.
type ShellManager struct {
	shells         map[string]*PersistentShell
	mu             sync.RWMutex
	defaultShell   string
	policy         *containment.Snapshot
	binding        *containment.Binding
	bindingDigest  string
	executionProof containment.ExecutionProof
	start          func(*exec.Cmd) error
	// captureRootIdentityForTest supplies a platform-neutral root identity
	// oracle to white-box tests. Production always uses Darwin containment.
	captureRootIdentityForTest func(string) (containment.RootIdentity, error)
	// beforeGuestStartForTest holds the final identity check immediately before
	// exec.Cmd.Start. Production code leaves it nil.
	beforeGuestStartForTest func()
	// beforeFirstPolicyCommitForTest lets white-box tests hold the gap between
	// the optimistic read and the authoritative write-lock commit. Production
	// code leaves it nil.
	beforeFirstPolicyCommitForTest func()
}

func (m *ShellManager) captureGuestRootIdentity(path string) (containment.RootIdentity, error) {
	if capture := m.captureRootIdentityForTest; capture != nil {
		return capture(path)
	}
	return containment.CaptureRootIdentity(path)
}

func (m *ShellManager) revalidateGuestRootIdentity(identity containment.RootIdentity) error {
	if m.captureRootIdentityForTest == nil {
		return containment.RevalidateRootIdentity(identity)
	}
	if identity.Path == "" || identity.Device == 0 || identity.Inode == 0 {
		return fmt.Errorf("guest root identity invalid")
	}
	current, err := m.captureGuestRootIdentity(identity.Path)
	if err != nil || current != identity {
		return fmt.Errorf("guest root identity changed")
	}
	return nil
}

// NewShellManager creates a new shell manager with no active shells.
func NewShellManager() *ShellManager {
	return &ShellManager{
		shells:       make(map[string]*PersistentShell),
		defaultShell: "default",
	}
}

// NewShellManagerWithGuestBinding pins an immutable Guest binding. An
// unavailable binding remains composable but fails closed on its first spawn.
func validateGuestBindingForShell(binding *containment.Binding) (containment.ExecutionProof, error) {
	if binding == nil || binding.ProcessClass() != containment.ProcessClassGuest || binding.Digest() == "" || binding.Policy() == nil {
		return containment.ExecutionProof{}, fmt.Errorf("shell guest binding must be valid")
	}
	if binding.Availability() == containment.BindingAvailable && binding.AdapterFamily() == containment.AdapterDarwinSeatbelt {
		spec := binding.Policy().Spec()
		if spec.Root.Path == "" || spec.Root.Device == 0 || spec.Root.Inode == 0 || spec.Descendants.Mode != containment.DescendantCleanupRequired || spec.Resources.WallTimeMillis <= 0 || spec.Resources.OutputBytes <= 0 {
			return containment.ExecutionProof{}, fmt.Errorf("shell guest policy is incomplete")
		}
		runtime := containment.AxisRootIdentity | containment.AxisDescendantCleanup | containment.AxisWallTime | containment.AxisOutput
		proof, err := containment.NewExecutionProof(binding, runtime)
		if err != nil || proof.RuntimeAxes != runtime || proof.Enforced != binding.AdapterAxes()|runtime {
			return containment.ExecutionProof{}, fmt.Errorf("shell guest execution proof unavailable")
		}
		return proof, nil
	}
	return containment.ExecutionProof{}, nil
}

func NewShellManagerWithGuestBinding(binding *containment.Binding) (*ShellManager, error) {
	proof, err := validateGuestBindingForShell(binding)
	if err != nil {
		return nil, err
	}
	return &ShellManager{shells: make(map[string]*PersistentShell), defaultShell: "default", policy: binding.Policy(), binding: binding, bindingDigest: binding.Digest(), executionProof: proof}, nil
}

// BindGuestBinding atomically pins the first Guest binding and rejects replacement.
func (m *ShellManager) BindGuestBinding(binding *containment.Binding) error {
	proof, err := validateGuestBindingForShell(binding)
	if m == nil || err != nil {
		return fmt.Errorf("shell guest binding must be valid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.binding != nil && m.binding.Digest() != binding.Digest() {
		return fmt.Errorf("shell guest binding replacement rejected")
	}
	if m.policy != nil && m.policy.Digest() != binding.PolicyDigest() {
		return fmt.Errorf("shell execution policy mismatch")
	}
	m.binding, m.bindingDigest, m.policy, m.executionProof = binding, binding.Digest(), binding.Policy(), proof
	return nil
}

func (m *ShellManager) GuestExecutionProof() containment.ExecutionProof {
	if m == nil {
		return containment.ExecutionProof{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executionProof
}

func (m *ShellManager) GuestBindingDigest() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bindingDigest
}

// NewShellManagerWithExecutionPolicy creates a manager pinned to one immutable
// execution identity before any persistent process can start.
func NewShellManagerWithExecutionPolicy(policy *containment.Snapshot) *ShellManager {
	if policy == nil || policy.Digest() == "" {
		panic("tools: shell execution policy must be valid")
	}
	return &ShellManager{
		shells:       make(map[string]*PersistentShell),
		defaultShell: "default",
		policy:       policy,
	}
}

// BindExecutionPolicy freezes the manager identity. A different digest is
// rejected even before first spawn so two roots cannot share and replace it.
func (m *ShellManager) BindExecutionPolicy(policy *containment.Snapshot) error {
	if m == nil || policy == nil || policy.Digest() == "" {
		return fmt.Errorf("shell execution policy must be valid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.policy != nil && m.policy.Digest() != policy.Digest() {
		return fmt.Errorf("shell execution policy replacement rejected")
	}
	if m.policy == nil {
		m.policy = policy
	}
	if m.policy.Digest() != policy.Digest() {
		return fmt.Errorf("shell execution policy replacement rejected")
	}
	return nil
}

// ExecutionPolicyDigest returns the manager's immutable execution identity.
func (m *ShellManager) ExecutionPolicyDigest() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.policy == nil {
		return ""
	}
	return m.policy.Digest()
}

// GetOrCreate returns an existing shell by ID or creates a new one.
func (m *ShellManager) GetOrCreate(id string) (*PersistentShell, error) {
	return m.GetOrCreateAt(id, "")
}

// GetOrCreateAt returns an existing shell by ID or creates a new one whose
// process starts in cwd. The working directory is only applied at creation so
// subsequent shell-local cd commands remain persistent.
func (m *ShellManager) GetOrCreateAt(id, cwd string) (*PersistentShell, error) {
	return m.getOrCreateAt(id, cwd, nil)
}

func (m *ShellManager) getOrCreateAt(
	id, cwd string,
	requested *containment.Snapshot,
) (*PersistentShell, error) {
	if id == "" {
		id = m.defaultShell
	}

	m.mu.RLock()
	policyUnbound := m.policy == nil
	beforePolicyCommit := m.beforeFirstPolicyCommitForTest
	if m.policy != nil && requested != nil && m.policy.Digest() != requested.Digest() {
		m.mu.RUnlock()
		return nil, fmt.Errorf("shell execution policy mismatch")
	}
	if shell, ok := m.shells[id]; ok {
		m.mu.RUnlock()
		return shell, nil
	}
	m.mu.RUnlock()
	if policyUnbound && beforePolicyCommit != nil {
		beforePolicyCommit()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.policy == nil {
		if requested == nil {
			requested = containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointEmbedded)
		}
		if requested == nil || requested.Digest() == "" {
			return nil, fmt.Errorf("shell execution policy unavailable")
		}
		m.policy = requested
	} else if requested != nil && m.policy.Digest() != requested.Digest() {
		return nil, fmt.Errorf("shell execution policy mismatch")
	}

	// Double-check after acquiring write lock
	if shell, ok := m.shells[id]; ok {
		return shell, nil
	}

	shell, err := m.startShell(id, cwd)
	if err != nil {
		return nil, fmt.Errorf("failed to start shell %q: %w", id, err)
	}
	m.shells[id] = shell
	return shell, nil
}

// startShell spawns a new bash process and wires up its stdin/stdout/stderr.
func (m *ShellManager) startShell(id, cwd string) (*PersistentShell, error) {
	var cmd *exec.Cmd
	var binding *containment.Binding
	var outputMax int64
	if m.binding != nil {
		binding = m.binding
		if binding.Availability() != containment.BindingAvailable {
			return nil, fmt.Errorf("shell guest binding unavailable")
		}
		spec := binding.Policy().Spec()
		if cwd == "" {
			cwd = spec.CWD
		}
		executable := "bash"
		if binding.AdapterFamily() == containment.AdapterDarwinSeatbelt {
			if err := m.revalidateGuestRootIdentity(spec.Root); err != nil {
				return nil, fmt.Errorf("shell guest root identity invalid")
			}
			requestedRoot, err := m.captureGuestRootIdentity(cwd)
			if err != nil || requestedRoot != spec.Root {
				return nil, fmt.Errorf("shell guest working directory rejected")
			}
			cwd = spec.CWD
			executable = "/bin/bash"
			outputMax = spec.Resources.OutputBytes
		}
		env := append([]string(nil), os.Environ()...)
		request := containment.SpawnRequest{Binding: binding, Executable: executable, Args: []string{"--noediting", "--noprofile", "--norc"}, Dir: cwd, Env: env}
		spawn, err := binding.Prepare(context.Background(), request)
		if err != nil || spawn.BindingDigest != m.bindingDigest || spawn.Dir != cwd || !equalStringSlices(spawn.Env, env) {
			return nil, fmt.Errorf("shell guest binding preparation rejected")
		}
		// Persistent shells outlive individual tool calls. Their lifecycle is
		// owned explicitly by Kill/KillAll, so do not bind them to a request
		// context that could kill only the process-group leader.
		cmd = exec.CommandContext(context.Background(), spawn.Path, spawn.Args...)
		cmd.Dir, cmd.Env = spawn.Dir, append([]string(nil), spawn.Env...)
	} else {
		cmd = exec.CommandContext(context.Background(), "bash", "--noediting", "--noprofile", "--norc")
		if cwd != "" {
			cmd.Dir = cwd
		}
	}
	setProcessGroup(cmd)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if binding != nil {
		if beforeStart := m.beforeGuestStartForTest; beforeStart != nil {
			beforeStart()
		}
		if m.binding != binding || binding.Digest() != m.bindingDigest {
			return nil, fmt.Errorf("shell guest binding mismatch")
		}
		if binding.AdapterFamily() == containment.AdapterDarwinSeatbelt {
			if err := m.revalidateGuestRootIdentity(binding.Policy().Spec().Root); err != nil {
				return nil, fmt.Errorf("shell guest root identity invalid")
			}
		}
	}
	start := m.start
	if start == nil {
		start = (*exec.Cmd).Start
	}
	if err := start(cmd); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	shell := &PersistentShell{
		cmd:       cmd,
		stdin:     stdinPipe,
		stdoutRC:  stdoutPipe,
		stderrRC:  stderrPipe,
		stdout:    bufio.NewReaderSize(stdoutPipe, 64*1024),
		stderr:    bufio.NewReaderSize(stderrPipe, 64*1024),
		cwd:       cwd,
		env:       make(map[string]string),
		running:   true,
		shellID:   id,
		policy:    m.policy,
		binding:   binding,
		outputMax: outputMax,
		startedAt: time.Now(),
		stderrBuf: &stderrBuffer{max: outputMax},
	}

	// Drain stderr in background to prevent blocking
	go shell.drainStderr()

	// Guest creation is already pinned to policy CWD; avoid an extra unbounded
	// protocol round trip before the WallTime clock exists.
	if binding == nil {
		shell.cwd = shell.getCWDUnsafe()
	}

	return shell, nil
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ExecutionPolicyDigest returns the identity pinned before this shell spawned.
func (s *PersistentShell) ExecutionPolicyDigest() string {
	if s == nil || s.policy == nil {
		return ""
	}
	return s.policy.Digest()
}

// getCWDUnsafe reads the current working directory without locking.
// Caller must hold shell.mu or ensure exclusive access.
func (s *PersistentShell) getCWDUnsafe() string {
	sentinel := uuid.New().String()
	startMarker := fmt.Sprintf("___SENTINEL_START_%s___", sentinel)
	endMarker := fmt.Sprintf("___SENTINEL_END_%s_", sentinel)

	cmd := fmt.Sprintf("echo '%s'; pwd; echo '%s'\"0\"'___'\n", startMarker, endMarker)
	_, err := io.WriteString(s.stdin, cmd)
	if err != nil {
		return ""
	}

	inOutput := false
	var output strings.Builder
	for {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\n\r")
		if strings.Contains(line, startMarker) {
			inOutput = true
			continue
		}
		if strings.Contains(line, endMarker) {
			break
		}
		if inOutput {
			output.WriteString(line)
		}
	}
	return strings.TrimSpace(output.String())
}

// stderrBuffer is a byte-bounded collector; it deliberately does not retain a
// line slice per fragment, so empty-line floods cannot grow its metadata.
type stderrBuffer struct {
	mu        sync.Mutex
	max       int64
	data      strings.Builder
	truncated bool
}

func (b *stderrBuffer) append(fragment string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		b.data.WriteString(fragment)
		return
	}
	remaining := b.max - int64(b.data.Len())
	if remaining <= 0 {
		b.truncated = true
		return
	}
	if int64(len(fragment)) > remaining {
		fragment = fragment[:remaining]
		b.truncated = true
	}
	b.data.WriteString(fragment)
}

func (b *stderrBuffer) drain() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, truncated := b.data.String(), b.truncated
	b.data.Reset()
	b.truncated = false
	return result, truncated
}

// drainStderr reads stderr in background to prevent pipe blocking.
func (s *PersistentShell) drainStderr() {
	for {
		fragment, err := readShellFragment(s.stderr)
		if fragment != "" {
			s.stderrBuf.append(fragment)
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			break
		}
	}
}

// getStderrBuffer returns collected stderr for a shell.
func (s *PersistentShell) getStderrBuffer() (string, bool) {
	return s.stderrBuf.drain()
}

// readShellFragment never asks bufio to allocate an unbounded line. ErrBufferFull
// returns a bounded fragment; callers continue until the protocol sentinel.
func readShellFragment(reader *bufio.Reader) (string, error) {
	fragment, err := reader.ReadSlice('\n')
	return string(fragment), err
}

func appendBoundedShellOutput(output *strings.Builder, max int64, fragment string, truncated *bool) {
	if fragment == "" {
		return
	}
	if max <= 0 {
		output.WriteString(fragment)
		return
	}
	remaining := max - int64(output.Len())
	if remaining <= 0 {
		*truncated = true
		return
	}
	if int64(len(fragment)) > remaining {
		fragment = fragment[:remaining]
		*truncated = true
	}
	output.WriteString(fragment)
}

// normalizeShellOutput preserves the historical line-oriented Bash result:
// CRLF becomes LF and exactly one terminal line delimiter is omitted.
func normalizeShellOutput(raw string) string {
	parts := strings.Split(raw, "\n")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for index := range parts {
		parts[index] = strings.TrimRight(parts[index], "\r")
	}
	return strings.Join(parts, "\n")
}

const maxShellProtocolBytes = 16 * 1024

func readShellProtocol(reader *bufio.Reader, initial, endMarker string) (string, int, error) {
	protocol := initial
	for {
		if index := strings.Index(protocol, endMarker); index >= 0 {
			rest := protocol[index+len(endMarker):]
			if terminator := strings.Index(rest, "___"); terminator >= 0 {
				return protocol[:index], parseExitCode(endMarker+rest[:terminator+3], endMarker), nil
			}
		}
		if len(protocol) >= maxShellProtocolBytes {
			return "", 0, fmt.Errorf("shell terminal protocol exceeded limit")
		}
		fragment, err := readShellFragment(reader)
		if len(protocol)+len(fragment) > maxShellProtocolBytes {
			return "", 0, fmt.Errorf("shell terminal protocol exceeded limit")
		}
		protocol += fragment
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return "", 0, fmt.Errorf("shell terminal protocol incomplete")
		}
	}
}

// Execute runs a command in the specified shell and returns the result.
// It uses sentinel markers to delimit command output and extract the exit code.
func (m *ShellManager) Execute(ctx context.Context, shellID, command string, timeout time.Duration) (*ShellResult, error) {
	return m.ExecuteAt(ctx, shellID, "", command, timeout)
}

// ExecuteAt runs a command in a persistent shell whose first generation starts
// in cwd. It never mutates the process-wide current directory.
func (m *ShellManager) ExecuteAt(ctx context.Context, shellID, cwd, command string, timeout time.Duration) (*ShellResult, error) {
	requested, _ := containment.FromContext(ctx)
	if shellID == "" {
		shellID = m.defaultShell
	}

	var shell *PersistentShell
	for {
		var err error
		shell, err = m.getOrCreateAt(shellID, cwd, requested)
		if err != nil {
			return nil, err
		}

		shell.mu.Lock()
		if shell.running && m.isCurrent(shellID, shell) {
			break
		}
		shell.mu.Unlock()
	}
	defer shell.mu.Unlock()
	if shell.binding != nil {
		if shell.binding.Digest() != m.GuestBindingDigest() {
			m.retireLocked(shellID, shell)
			return nil, fmt.Errorf("shell guest binding mismatch")
		}
		if shell.binding.AdapterFamily() == containment.AdapterDarwinSeatbelt && m.revalidateGuestRootIdentity(shell.binding.Policy().Spec().Root) != nil {
			m.retireLocked(shellID, shell)
			return nil, fmt.Errorf("shell guest root identity invalid")
		}
	}

	// Create a context with timeout if specified
	if shell.binding != nil && shell.binding.AdapterFamily() == containment.AdapterDarwinSeatbelt {
		limit := time.Duration(shell.binding.Policy().Spec().Resources.WallTimeMillis) * time.Millisecond
		if timeout <= 0 || timeout > limit {
			timeout = limit
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	start := time.Now()

	// Generate unique sentinel markers for this execution
	sentinel := uuid.New().String()
	startMarker := fmt.Sprintf("___SENTINEL_START_%s___", sentinel)
	cwdMarker := fmt.Sprintf("___SENTINEL_CWD_%s___", sentinel)
	endMarker := fmt.Sprintf("___SENTINEL_END_%s_", sentinel)

	// Drain any existing stderr before our command
	_, _ = shell.getStderrBuffer()

	// Keep stderr merged with stdout as before. The terminal protocol carries
	// both the persistent Bash CWD and exit status, so Guest execution never
	// needs an unbounded follow-up pwd round trip.
	wrappedCmd := fmt.Sprintf("echo '%s'; %s 2>&1; __eino_status=$?; printf '%s%%s%s%%s___\\n' \"$PWD\" \"$__eino_status\"\n",
		startMarker, command, cwdMarker, endMarker)

	_, err := io.WriteString(shell.stdin, wrappedCmd)
	if err != nil {
		m.retireLocked(shellID, shell)
		return nil, fmt.Errorf("write to shell: %w", err)
	}

	// Read output between sentinels using a dedicated goroutine that
	// terminates cleanly once the end sentinel is found.
	type readResult struct {
		output    string
		cwd       string
		exitCode  int
		truncated bool
		err       error
	}
	resultCh := make(chan readResult, 1)

	// Extract progress callback for streaming shell output to TUI
	progressFn, _ := ctx.Value(progressKeyType{}).(func(ToolProgressEvent))

	go func() {
		var output strings.Builder
		truncated := false
		inOutput := false
		terminalWindow := ""
		var recentLines []string
		lineCount := 0
		partialLine := false
		lastEmit := time.Now()
		const emitInterval = 500 * time.Millisecond
		const maxRecent = 5

		observeProgress := func(fragment string) {
			if fragment == "" {
				return
			}
			lineCount += strings.Count(fragment, "\n")
			partialLine = !strings.HasSuffix(fragment, "\n")
			progressLine := strings.TrimRight(fragment, "\n\r")
			if len(progressLine) > 4*1024 {
				progressLine = progressLine[len(progressLine)-4*1024:] + "[fragment truncated]"
			}
			if len(recentLines) >= maxRecent {
				recentLines = recentLines[1:]
			}
			recentLines = append(recentLines, progressLine)
			if progressFn != nil && time.Since(lastEmit) >= emitInterval {
				visibleLines := lineCount
				if partialLine {
					visibleLines++
				}
				progressFn(ToolProgressEvent{
					ToolName: "Bash",
					Content:  fmt.Sprintf("[%d lines] %s", visibleLines, strings.Join(recentLines, "\n")),
				})
				lastEmit = time.Now()
			}
		}

		for {
			fragment, readErr := readShellFragment(shell.stdout)
			if !inOutput {
				if strings.Contains(fragment, startMarker) {
					inOutput = true
					continue
				}
				if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
					resultCh <- readResult{err: readErr}
					return
				}
				continue
			}

			combined := terminalWindow + fragment
			if index := strings.Index(combined, cwdMarker); index >= 0 {
				prefix := combined[:index]
				appendBoundedShellOutput(&output, shell.outputMax, prefix, &truncated)
				observeProgress(prefix)
				commandCWD, exitCode, err := readShellProtocol(shell.stdout, combined[index+len(cwdMarker):], endMarker)
				if err != nil {
					resultCh <- readResult{err: err}
					return
				}
				normalized := normalizeShellOutput(output.String())
				if truncated {
					normalized += "\n[stdout truncated]"
				}
				resultCh <- readResult{output: normalized, cwd: commandCWD, exitCode: exitCode, truncated: truncated}
				return
			}

			keep := len(cwdMarker) - 1
			safe := len(combined) - keep
			if safe > 0 {
				prefix := combined[:safe]
				appendBoundedShellOutput(&output, shell.outputMax, prefix, &truncated)
				observeProgress(prefix)
				terminalWindow = combined[safe:]
			} else {
				terminalWindow = combined
			}
			if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
				resultCh <- readResult{err: readErr}
				return
			}
		}
	}()

	// Wait for either result or context cancellation.
	var output string
	var commandCWD string
	var exitCode int
	truncated := false
	timedOut := false
	canceled := false

	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			timedOut = true
			output = "(command timed out)"
		} else {
			canceled = true
			output = "(command canceled)"
		}
		m.retireLocked(shellID, shell)
		<-resultCh
	case res := <-resultCh:
		if res.err != nil {
			m.retireLocked(shellID, shell)
			return nil, fmt.Errorf("read from shell: %w", res.err)
		}
		output = res.output
		commandCWD = res.cwd
		exitCode = res.exitCode
		truncated = res.truncated
	}

	duration := time.Since(start)

	// The terminal protocol captures the persistent shell CWD inside the same
	// wall-time-bounded read as command output.
	currentCWD := shell.cwd
	if !timedOut && shell.running && commandCWD != "" {
		currentCWD = commandCWD
		shell.cwd = currentCWD
	}

	// Collect stderr
	stderr, stderrTruncated := shell.getStderrBuffer()
	if stderrTruncated {
		stderr += "[stderr truncated]"
	}

	return &ShellResult{
		Stdout:    output,
		Stderr:    stderr,
		ExitCode:  exitCode,
		CWD:       currentCWD,
		Duration:  duration,
		TimedOut:  timedOut,
		Canceled:  canceled,
		Truncated: truncated || stderrTruncated,
	}, nil
}

// isCurrent reports whether shell is still the manager's active generation for id.
func (m *ShellManager) isCurrent(id string, shell *PersistentShell) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.shells[id] == shell
}

// retireLocked removes and terminates a shell generation. The caller must hold
// shell.mu so no other command can use the process while it is being retired.
func (m *ShellManager) retireLocked(id string, shell *PersistentShell) {
	m.mu.Lock()
	if m.shells[id] == shell {
		delete(m.shells, id)
	}
	m.mu.Unlock()

	if !shell.running {
		return
	}
	shell.running = false
	_ = shell.stdin.Close()
	killProcessGroup(shell.cmd)
	_ = shell.stdoutRC.Close()
	_ = shell.stderrRC.Close()
	_ = shell.cmd.Wait()
}

// parseExitCode extracts the exit code from the end sentinel line.
// Expected format: ___SENTINEL_END_<uuid>_<exitcode>___
func parseExitCode(line, endMarker string) int {
	idx := strings.Index(line, endMarker)
	if idx < 0 {
		return -1
	}
	// endMarker is "___SENTINEL_END_<uuid>_", remaining is "<exitcode>___"
	rest := line[idx+len(endMarker):]
	// Strip trailing "___"
	rest = strings.TrimSuffix(rest, "___")
	rest = strings.TrimSpace(rest)

	code := 0
	for _, ch := range rest {
		if ch >= '0' && ch <= '9' {
			code = code*10 + int(ch-'0')
		} else {
			break
		}
	}
	return code
}

// Kill terminates a specific shell session.
func (m *ShellManager) Kill(id string) error {
	if id == "" {
		id = m.defaultShell
	}

	m.mu.Lock()
	shell, ok := m.shells[id]
	if ok {
		delete(m.shells, id)
	}
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("shell %q not found", id)
	}

	return killShell(shell)
}

// KillAll terminates all active shell sessions.
func (m *ShellManager) KillAll() error {
	m.mu.Lock()
	shells := make([]*PersistentShell, 0, len(m.shells))
	for _, shell := range m.shells {
		shells = append(shells, shell)
	}
	m.shells = make(map[string]*PersistentShell)
	m.mu.Unlock()

	var firstErr error
	for _, shell := range shells {
		if err := killShell(shell); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// killShell closes stdin and sends SIGKILL to the shell process.
func killShell(shell *PersistentShell) error {
	shell.mu.Lock()
	defer shell.mu.Unlock()

	if !shell.running {
		return nil
	}
	shell.running = false

	// Close stdin to signal EOF
	_ = shell.stdin.Close()

	// Kill the process group
	killProcessGroup(shell.cmd)
	_ = shell.stdoutRC.Close()
	_ = shell.stderrRC.Close()

	_ = shell.cmd.Wait()

	return nil
}

// ListShells returns metadata about all active shell sessions.
func (m *ShellManager) ListShells() []ShellInfo {
	m.mu.RLock()
	shells := make([]*PersistentShell, 0, len(m.shells))
	for _, shell := range m.shells {
		shells = append(shells, shell)
	}
	m.mu.RUnlock()

	infos := make([]ShellInfo, 0, len(shells))
	for _, shell := range shells {
		shell.mu.Lock()
		infos = append(infos, ShellInfo{
			ID:        shell.shellID,
			CWD:       shell.cwd,
			Running:   shell.running,
			StartedAt: shell.startedAt,
		})
		shell.mu.Unlock()
	}
	return infos
}
