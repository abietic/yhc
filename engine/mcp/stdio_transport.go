package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/containment"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioGracePeriod bounds each graceful shutdown stage. It is intentionally
// short: a disconnected MCP server must not retain an engine session.
const stdioGracePeriod = 250 * time.Millisecond

// stdioProcessTransport owns a command and every process it starts through an
// OS-specific process-tree owner. It deliberately does not use the SDK command
// transport because that transport owns only the direct child.
type stdioProcessTransport struct {
	cmd                   *exec.Cmd
	policy                *containment.Snapshot
	binding               *containment.Binding
	preparedBindingDigest string

	mu       sync.Mutex
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	tree     stdioProcessTree
	waitDone chan struct{}

	closeOnce sync.Once
	closeErr  error
}

var _ sdkmcp.Transport = (*stdioProcessTransport)(nil)

func newStdioProcessTransport(config ServerConfig) (*stdioProcessTransport, error) {
	if err := stdioPlatformSupported(); err != nil {
		return nil, err
	}
	if config.Command == "" {
		return nil, errors.New("mcp: stdio transport requires a command")
	}
	if !filepath.IsAbs(config.Command) {
		return nil, errors.New("mcp: stdio transport requires an absolute command")
	}
	if config.ExecutionPolicy == nil {
		config.ExecutionPolicy = containment.DisabledCompatibilitySnapshot(
			config.CWD,
			containment.EntrypointEmbedded,
		)
	}
	if config.ExecutionPolicy.Digest() == "" {
		return nil, errors.New("mcp: stdio transport requires an execution policy")
	}

	environment := inheritedEnvironmentWithOverlay(os.Environ(), config.Env)
	path, args, dir, preparedBindingDigest := config.Command, config.Args, config.CWD, ""
	if config.ExecutionBinding != nil {
		if !validStdioExecutionBinding(config.ExecutionBinding) || config.ExecutionBinding.PolicyDigest() != config.ExecutionPolicy.Digest() {
			return nil, errors.New("mcp: stdio execution binding mismatch")
		}
		spec, err := config.ExecutionBinding.Prepare(context.Background(), containment.SpawnRequest{Binding: config.ExecutionBinding, Executable: path, Args: args, Dir: dir, Env: environment})
		if err != nil || spec.BindingDigest != config.ExecutionBinding.Digest() {
			return nil, errors.New("mcp: stdio execution binding unavailable")
		}
		path, args, dir, environment = spec.Path, spec.Args, spec.Dir, spec.Env
		preparedBindingDigest = spec.BindingDigest
	}
	// Do not bind only the direct child to request cancellation: this transport
	// owns and terminates the complete process group or Job Object itself.
	//nolint:noctx // Direct-child CommandContext cleanup would violate tree ownership.
	cmd := exec.Command(path, args...)
	cmd.Env = environment
	cmd.Dir = dir
	if err := configureStdioProcess(cmd); err != nil {
		return nil, err
	}
	return &stdioProcessTransport{cmd: cmd, policy: config.ExecutionPolicy, binding: config.ExecutionBinding, preparedBindingDigest: preparedBindingDigest}, nil
}

func validStdioExecutionBinding(binding *containment.Binding) bool {
	if binding == nil || binding.ProcessClass() != containment.ProcessClassStdioMCP || binding.Availability() != containment.BindingAvailable || binding.AdapterFamily() != containment.AdapterAmbientHost {
		return false
	}
	diagnostic := binding.Policy().Diagnostic()
	return diagnostic.Profile == containment.ProfileDangerFullAccess && diagnostic.State == containment.StateDisabled
}

func (t *stdioProcessTransport) executionPolicyDigest() string {
	if t == nil || t.policy == nil {
		return ""
	}
	return t.policy.Digest()
}

// Connect starts exactly one command and returns the SDK's newline-delimited
// JSON connection. Callers must close the returned connection or this transport.
func (t *stdioProcessTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stdin != nil || t.waitDone != nil {
		return nil, errors.New("mcp: stdio transport already connected")
	}
	if t.binding != nil && (t.binding.Digest() == "" || t.preparedBindingDigest != t.binding.Digest()) {
		return nil, errors.New("mcp: stdio execution binding unavailable")
	}

	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("mcp: stdio process setup failed")
	}
	stdin, err := t.cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, errors.New("mcp: stdio process setup failed")
	}
	if err := t.cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, errors.New("mcp: stdio process start failed")
	}

	t.stdin = stdin
	t.stdout = stdout
	t.waitDone = make(chan struct{})
	go func() {
		_ = t.cmd.Wait() // Wait is owned here and is called exactly once.
		close(t.waitDone)
	}()

	tree, err := attachStdioProcessTree(t.cmd)
	if err != nil {
		// This is startup cleanup, never a direct-child lifecycle fallback.
		_ = t.cmd.Process.Kill()
		<-t.waitDone
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	t.tree = tree

	if err := ctx.Err(); err != nil {
		go func() { _ = t.Close() }()
		return nil, err
	}
	return (&sdkmcp.IOTransport{
		Reader: stdioLifecycleReader{ReadCloser: stdout, transport: t},
		Writer: stdioLifecycleWriter{WriteCloser: stdin, transport: t},
	}).Connect(ctx)
}

// Close is safe to call concurrently and always attempts the full tree cleanup,
// including when the direct child already exited but descendants remain alive.
func (t *stdioProcessTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		stdin, stdout, tree, waitDone := t.stdin, t.stdout, t.tree, t.waitDone
		t.mu.Unlock()
		if waitDone == nil {
			return
		}

		if stdin != nil {
			_ = stdin.Close()
		}
		// Do not use direct-child exit as proof that the process group/job is
		// empty: descendants may still hold the process tree alive.
		time.Sleep(stdioGracePeriod)
		if tree != nil {
			_ = tree.terminate()
		}
		time.Sleep(stdioGracePeriod)
		if tree != nil {
			_ = tree.kill()
			_ = tree.close()
		}
		if stdout != nil {
			_ = stdout.Close()
		}
		select {
		case <-waitDone:
		case <-time.After(stdioGracePeriod):
			t.closeErr = errors.New("mcp: stdio process did not exit")
		}
	})
	return t.closeErr
}

type stdioLifecycleReader struct {
	io.ReadCloser
	transport *stdioProcessTransport
}

func (r stdioLifecycleReader) Close() error {
	return r.transport.Close()
}

type stdioLifecycleWriter struct {
	io.WriteCloser
	transport *stdioProcessTransport
}

func (w stdioLifecycleWriter) Close() error { return w.transport.Close() }

// inheritedEnvironmentWithOverlay replaces inherited keys once and appends new
// keys in lexical order, avoiding map-order-dependent duplicate environments.
func inheritedEnvironmentWithOverlay(inherited []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return append([]string(nil), inherited...)
	}
	keys := make([]string, 0, len(overlay))
	for key := range overlay {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	overlaid := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		overlaid[CanonicalEnvironmentKey(key)] = struct{}{}
	}
	result := make([]string, 0, len(inherited)+len(keys))
	for _, entry := range inherited {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := overlaid[CanonicalEnvironmentKey(key)]; replace {
				continue
			}
		}
		result = append(result, entry)
	}
	for _, key := range keys {
		result = append(result, fmt.Sprintf("%s=%s", key, overlay[key]))
	}
	return result
}
