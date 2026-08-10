//go:build unix

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	g9eTablePTYHelperEnv = "YHC_G9E_TABLE_PTY_HELPER"
	g9eTablePTYWidthEnv  = "YHC_G9E_TABLE_PTY_WIDTH"
	g9eTablePTYBegin     = "G9E_TABLE_BEGIN"
	g9eTablePTYEnd       = "G9E_TABLE_END"
)

func TestG9ESemanticTablePTYGeometry(t *testing.T) {
	if os.Getenv(g9eTablePTYHelperEnv) == "1" {
		runG9ETablePTYHelper(t)
		return
	}

	for _, width := range []uint16{32, 48, 72} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestG9ESemanticTablePTYGeometry$",
			)
			command.Env = append(
				os.Environ(),
				g9eTablePTYHelperEnv+"=1",
				fmt.Sprintf("%s=%d", g9eTablePTYWidthEnv, width),
				"TERM=xterm-256color",
				"COLORTERM=truecolor",
			)
			terminal, err := pty.StartWithSize(
				command,
				&pty.Winsize{Cols: width, Rows: 24},
			)
			if err != nil {
				t.Fatalf("start G9.E table PTY: %v", err)
			}
			defer terminal.Close() //nolint:errcheck

			output := newLockedPTYOutput(int(width), 24)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				buffer := make([]byte, 8192)
				for {
					count, readErr := terminal.Read(buffer)
					if count > 0 {
						output.append(buffer[:count])
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
					t.Fatalf(
						"G9.E table PTY helper failed: %v\n%s",
						waitErr,
						output.raw(),
					)
				}
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-waitDone
				t.Fatalf("G9.E table PTY helper timed out\n%s", output.raw())
			}
			_ = terminal.Close()
			select {
			case <-readDone:
			case <-time.After(time.Second):
				t.Fatal("G9.E table PTY reader did not finish")
			}

			rendered := g9ePTYRenderedTable(t, output.raw())
			if strings.Contains(rendered, "| Item |") {
				t.Fatalf("PTY retained literal table source:\n%s", rendered)
			}
			assertTableAlignment(t, rendered)
			profile := DefaultDisplayCellProfile()
			for index, line := range strings.Split(rendered, "\n") {
				if cells := profile.width(line); cells > int(width) {
					t.Fatalf(
						"PTY line %d width=%d exceeds columns=%d: %q",
						index,
						cells,
						width,
						line,
					)
				}
				assertWidthProfileControlStateClosed(t, profile, line)
			}
		})
	}
}

func runG9ETablePTYHelper(t *testing.T) {
	t.Helper()
	width, err := strconv.Atoi(os.Getenv(g9eTablePTYWidthEnv))
	if err != nil || width <= 0 {
		t.Fatalf("invalid G9.E PTY width: %q", os.Getenv(g9eTablePTYWidthEnv))
	}
	source := "| Item | Value |\n" +
		"| --- | --- |\n" +
		"| CJK | 终端宽度 |\n" +
		"| Emoji | 👨‍👩‍👧‍👦 🏷 |\n" +
		"| Indic | क्ष |\n" +
		"| Flag | 🇺🇸 |\n"
	stream := &StreamingMarkdown{}
	stream.Finalize(source)
	rendered := stream.Render(source, width, ThemePolarNight)
	fmt.Printf("%s\n%s\n%s\n", g9eTablePTYBegin, rendered, g9eTablePTYEnd)
}

func g9ePTYRenderedTable(t *testing.T, raw string) string {
	t.Helper()
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	start := strings.Index(raw, g9eTablePTYBegin+"\n")
	if start < 0 {
		t.Fatalf("PTY output missing begin marker: %q", raw)
	}
	start += len(g9eTablePTYBegin) + 1
	end := strings.Index(raw[start:], "\n"+g9eTablePTYEnd)
	if end < 0 {
		t.Fatalf("PTY output missing end marker: %q", raw)
	}
	return raw[start : start+end]
}
