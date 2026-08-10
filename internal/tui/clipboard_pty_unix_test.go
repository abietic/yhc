//go:build unix

package tui

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	p273ClipboardPTYModeEnv = "YHC_P273_CLIPBOARD_PTY_MODE"
	p273RendererPacketBegin = "P273_RENDERER_PACKET_BEGIN:"
	p273RendererPacketEnd   = ":P273_RENDERER_PACKET_END"
	p273ClipboardPTYSource  = "clipboard-pty-source"
)

func TestP273ClipboardPTYSerializesRendererAndOSC52(t *testing.T) {
	if mode := os.Getenv(p273ClipboardPTYModeEnv); mode != "" {
		runP273ClipboardPTYHelper(t, mode)
		return
	}

	for _, mode := range []string{"direct", "tmux", "screen"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestP273ClipboardPTYSerializesRendererAndOSC52$",
			)
			command.Env = append(os.Environ(), p273ClipboardPTYModeEnv+"="+mode)
			terminal, err := pty.StartWithSize(
				command,
				&pty.Winsize{Cols: 80, Rows: 24},
			)
			if err != nil {
				t.Fatalf("start clipboard PTY helper: %v", err)
			}
			defer terminal.Close() //nolint:errcheck

			var output bytes.Buffer
			var outputMu sync.Mutex
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				buffer := make([]byte, 8192)
				for {
					count, readErr := terminal.Read(buffer)
					if count > 0 {
						outputMu.Lock()
						_, _ = output.Write(buffer[:count])
						outputMu.Unlock()
					}
					if readErr != nil {
						return
					}
				}
			}()

			waitDone := make(chan error, 1)
			go func() { waitDone <- command.Wait() }()
			select {
			case waitErr := <-waitDone:
				if waitErr != nil {
					outputMu.Lock()
					captured := output.String()
					outputMu.Unlock()
					t.Fatalf("clipboard PTY helper failed: %v\n%s", waitErr, captured)
				}
			case <-time.After(15 * time.Second):
				_ = command.Process.Kill()
				<-waitDone
				t.Fatal("clipboard PTY helper timed out")
			}
			_ = terminal.Close()
			select {
			case <-readDone:
			case <-time.After(time.Second):
				t.Fatal("clipboard PTY reader did not finish")
			}

			outputMu.Lock()
			captured := append([]byte(nil), output.Bytes()...)
			outputMu.Unlock()
			rendererPacket := p273RendererPacket()
			if bytes.Count(captured, rendererPacket) != 1 {
				t.Fatalf("renderer packet was missing or interleaved in PTY output")
			}
			environment := clipboardEnvironment{}
			switch mode {
			case "tmux":
				environment.tmux = true
			case "screen":
				environment.screen = true
			}
			oscPacket := clipboardOSC52Packet(
				[]byte(p273ClipboardPTYSource),
				environment,
			)
			if bytes.Count(captured, oscPacket) != 1 {
				t.Fatalf(
					"%s OSC packet was missing or interleaved:\npacket=%q",
					mode,
					oscPacket,
				)
			}
		})
	}
}

func runP273ClipboardPTYHelper(t *testing.T, mode string) {
	t.Helper()
	output, err := NewTerminalOutput(os.Stdout)
	if err != nil {
		t.Fatalf("NewTerminalOutput: %v", err)
	}
	defer output.Close() //nolint:errcheck

	environment := clipboardEnvironment{goos: "other"}
	switch mode {
	case "direct":
	case "tmux":
		environment.tmux = true
	case "screen":
		environment.screen = true
	default:
		t.Fatalf("unknown PTY mode %q", mode)
	}
	service := p273Service(
		context.Background(),
		output,
		environment,
		p273MissingLookPath,
		nil,
		time.Second,
	)

	start := make(chan struct{})
	renderDone := make(chan error, 1)
	clipboardDone := make(chan clipboardResultMsg, 1)
	go func() {
		<-start
		_, writeErr := output.Write(p273RendererPacket())
		renderDone <- writeErr
	}()
	go func() {
		<-start
		clipboardDone <- service.deliver(
			1,
			ClipboardCallerActionCopy,
			p273ClipboardPTYSource,
		)
	}()
	close(start)

	if writeErr := <-renderDone; writeErr != nil {
		t.Fatalf("renderer write: %v", writeErr)
	}
	result := <-clipboardDone
	if result.terminal != clipboardTerminalSequenceWritten ||
		result.native != clipboardNativeUnavailable {
		t.Fatalf("clipboard result = %#v", result)
	}
	if closeErr := output.Close(); closeErr != nil {
		t.Fatalf("close terminal output: %v", closeErr)
	}
}

func p273RendererPacket() []byte {
	return []byte(
		p273RendererPacketBegin +
			string(bytes.Repeat([]byte{'R'}, 64<<10)) +
			p273RendererPacketEnd,
	)
}
