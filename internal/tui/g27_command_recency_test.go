package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func newG27EngineApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "g27-command-recency",
		TranscriptDir:     dir,
		CWD:               dir,
		CommandEntrypoint: commands.EntrypointTUI,
	})
	t.Cleanup(eng.Close)
	return New(Config{Engine: eng})
}

func selectG27PaletteCommand(t *testing.T, app *App, name string) tea.Cmd {
	t.Helper()
	app.openCommandPalette()
	app.commandPalette.query = name
	app.commandPalette.applyFilter()
	got := paletteCommandNames(app.commandPalette.filtered)
	if len(got) == 0 || got[0] != name {
		t.Fatalf("palette query %q = %v, want %q first", name, got, name)
	}
	handled, cmd := app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatalf("palette selection %q was not handled", name)
	}
	return cmd
}

func g27CommandResult(name string, status engine.CommandResultStatus) engine.QueryEvent {
	return engine.QueryEvent{
		Type: engine.EventCommandResult,
		CommandResult: &engine.CommandResultEvent{
			Command: name,
			Status:  status,
		},
	}
}

func assertG27NoRecent(t *testing.T, app *App) {
	t.Helper()
	if len(app.commandPalette.recent) != 0 {
		t.Fatalf("unexpected recent commands: %v", app.commandPalette.recent)
	}
}

func TestG27EngineResultCommitsRecentExactlyOnce(t *testing.T) {
	t.Run("single event", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		qid := app.queryID
		if pending := app.commandPaletteSubmission; pending == nil ||
			pending.command != "status" ||
			pending.queryID != qid {
			t.Fatalf("pending submission = %#v, want status bound to %d", pending, qid)
		}

		app.Update(engineEventMsg{
			queryID: qid,
			event:   g27CommandResult("status", engine.CommandResultSucceeded),
		})
		if got := app.commandPalette.recent; len(got) != 1 || got[0] != "status" {
			t.Fatalf("recent = %v, want [status]", got)
		}
		if app.commandPaletteSubmission != nil {
			t.Fatalf("successful result retained pending submission: %#v", app.commandPaletteSubmission)
		}

		app.Update(engineEventMsg{
			queryID: qid,
			event:   g27CommandResult("status", engine.CommandResultSucceeded),
		})
		if got := app.commandPalette.recent; len(got) != 1 || got[0] != "status" {
			t.Fatalf("duplicate result changed recent = %v", got)
		}
	})

	t.Run("batch event", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		qid := app.queryID
		app.Update(engineBatchMsg{
			queryID: qid,
			events: []engine.QueryEvent{
				g27CommandResult("status", engine.CommandResultSucceeded),
			},
			done: true,
		})
		if got := app.commandPalette.recent; len(got) != 1 || got[0] != "status" {
			t.Fatalf("recent = %v, want [status]", got)
		}
		if app.commandPaletteSubmission != nil {
			t.Fatalf("successful batch retained pending submission: %#v", app.commandPaletteSubmission)
		}

		app.Update(engineBatchMsg{
			queryID: qid,
			events: []engine.QueryEvent{
				g27CommandResult("status", engine.CommandResultSucceeded),
			},
			done: true,
		})
		if got := app.commandPalette.recent; len(got) != 1 || got[0] != "status" {
			t.Fatalf("duplicate batch changed recent = %v", got)
		}
	})
}

func TestG27EngineNonSuccessClearsWithoutRecording(t *testing.T) {
	tests := []struct {
		name  string
		event engine.QueryEvent
	}{
		{
			name:  "failed",
			event: g27CommandResult("status", engine.CommandResultFailed),
		},
		{
			name:  "unsupported",
			event: g27CommandResult("status", engine.CommandResultUnsupported),
		},
		{
			name: "missing result",
			event: engine.QueryEvent{
				Type: engine.EventCommandResult,
			},
		},
		{
			name:  "mismatched command",
			event: g27CommandResult("context", engine.CommandResultSucceeded),
		},
		{
			name: "user interruption",
			event: engine.QueryEvent{
				Type: engine.EventUserInterruption,
			},
		},
		{
			name: "terminal",
			event: engine.QueryEvent{
				Type: engine.EventTerminal,
			},
		},
		{
			name: "max turns",
			event: engine.QueryEvent{
				Type: engine.EventMaxTurnsReached,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newG27EngineApp(t)
			if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
				t.Fatal("status selection did not start the engine")
			}
			app.Update(engineEventMsg{queryID: app.queryID, event: test.event})
			assertG27NoRecent(t, app)
			if app.commandPaletteSubmission != nil {
				t.Fatalf("non-success retained pending submission: %#v", app.commandPaletteSubmission)
			}
		})
	}

	t.Run("events done", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		app.Update(eventsDoneMsg{queryID: app.queryID})
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("eventsDone retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})

	t.Run("accepted cancellation", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("accepted cancellation retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})

	t.Run("batch done without result", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		app.Update(engineBatchMsg{queryID: app.queryID, done: true})
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("empty final batch retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})
}

func TestG27EngineSubmissionIdentityExcludesStaleManualAndReplay(t *testing.T) {
	t.Run("superseded query", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		oldQueryID := app.queryID
		app.running = false
		if cmd := app.startEngineRequest("manual prompt"); cmd == nil {
			t.Fatal("manual prompt did not start the engine")
		}
		if app.commandPaletteSubmission != nil {
			t.Fatalf("new request retained old palette submission: %#v", app.commandPaletteSubmission)
		}
		app.Update(engineEventMsg{
			queryID: oldQueryID,
			event:   g27CommandResult("status", engine.CommandResultSucceeded),
		})
		assertG27NoRecent(t, app)
	})

	t.Run("same command different query", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("first status selection did not start the engine")
		}
		firstQueryID := app.queryID
		app.running = false
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("second status selection did not start the engine")
		}
		secondQueryID := app.queryID
		if secondQueryID == firstQueryID {
			t.Fatalf("query IDs did not advance: %d", secondQueryID)
		}

		app.Update(engineEventMsg{
			queryID: firstQueryID,
			event:   g27CommandResult("status", engine.CommandResultSucceeded),
		})
		assertG27NoRecent(t, app)
		if pending := app.commandPaletteSubmission; pending == nil ||
			pending.queryID != secondQueryID {
			t.Fatalf("stale result changed current pending submission: %#v", pending)
		}

		app.Update(engineEventMsg{
			queryID: secondQueryID,
			event:   g27CommandResult("status", engine.CommandResultSucceeded),
		})
		if got := app.commandPalette.recent; len(got) != 1 || got[0] != "status" {
			t.Fatalf("current result recent = %v, want [status]", got)
		}
	})

	t.Run("manual same text", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		originalQueryID := app.queryID
		app.sendSlashCommand("/status")
		if app.commandPaletteSubmission != nil {
			t.Fatalf("manual command retained palette provenance: %#v", app.commandPaletteSubmission)
		}
		app.Update(engineEventMsg{
			queryID: originalQueryID,
			event:   g27CommandResult("status", engine.CommandResultSucceeded),
		})
		assertG27NoRecent(t, app)
	})

	t.Run("queued manual prompt", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		app.textarea.SetValue("manual follow-up")
		app.inputMode = InputNormal
		app.sendMessage()
		if app.commandPaletteSubmission != nil {
			t.Fatalf("queued manual prompt retained palette provenance: %#v", app.commandPaletteSubmission)
		}
		assertG27NoRecent(t, app)
	})

	t.Run("async hook and direct replay", func(t *testing.T) {
		app := newG27EngineApp(t)
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd == nil {
			t.Fatal("status selection did not start the engine")
		}
		event := g27CommandResult("status", engine.CommandResultSucceeded)
		app.Update(asyncHookEventMsg{event: event, open: true})
		app.handleEngineEvent(event)
		assertG27NoRecent(t, app)
		if pending := app.commandPaletteSubmission; pending == nil {
			t.Fatal("non-live projections consumed palette provenance")
		}
		app.Update(engineEventMsg{
			queryID: app.queryID,
			event: engine.QueryEvent{
				Type: engine.EventTerminal,
			},
		})
		if app.commandPaletteSubmission != nil {
			t.Fatalf("live terminal retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})
}

func TestG27LocalCommandCommitsOnlyAfterAcceptedAction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		app := New(Config{})
		if cmd := selectG27PaletteCommand(t, app, "keybindings"); cmd != nil {
			t.Fatal("keybindings unexpectedly returned asynchronous work")
		}
		if got := app.commandPalette.recent; len(got) != 1 || got[0] != "keybindings" {
			t.Fatalf("recent = %v, want [keybindings]", got)
		}
	})

	t.Run("strict dispatch failure", func(t *testing.T) {
		app := New(Config{})
		if err := app.commandRegistry.Register(&commands.Command{
			Name:           "g27-dispatch-failure",
			Description:    "Fail during strict dispatch",
			Usage:          "/g27-dispatch-failure",
			Entrypoints:    commands.EntrypointsTUI,
			ExecutionOwner: commands.ExecutionOwnerEntrypoint,
			Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				return nil, errors.New("g27 dispatch rejected")
			},
		}); err != nil {
			t.Fatal(err)
		}
		if cmd := selectG27PaletteCommand(t, app, "g27-dispatch-failure"); cmd != nil {
			t.Fatal("failed local dispatch returned asynchronous work")
		}
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("dispatch failure retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})

	t.Run("capability loss at strict dispatch", func(t *testing.T) {
		app := New(Config{})
		executed := false
		if err := app.commandRegistry.Register(&commands.Command{
			Name:           "g27-capability-loss",
			Description:    "Lose capability after palette admission",
			Usage:          "/g27-capability-loss",
			Entrypoints:    commands.EntrypointsTUI,
			ExecutionOwner: commands.ExecutionOwnerEntrypoint,
			ResolveAvailability: func(_ context.Context, cmdCtx *commands.CommandContext) (commands.AvailabilityState, string) {
				if cmdCtx != nil && cmdCtx.RawInput != "" {
					return commands.AvailabilityUnavailable, "capability changed before dispatch"
				}
				return commands.AvailabilitySupported, ""
			},
			Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				executed = true
				return &commands.CommandResult{Output: "unexpected execution"}, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		if cmd := selectG27PaletteCommand(t, app, "g27-capability-loss"); cmd != nil {
			t.Fatal("capability loss returned asynchronous work")
		}
		if executed {
			t.Fatal("capability loss reached the command handler")
		}
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("capability loss retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})

	t.Run("action application failure", func(t *testing.T) {
		app := New(Config{})
		if err := app.commandRegistry.Register(&commands.Command{
			Name:           "g27-action-failure",
			Description:    "Fail during TUI action application",
			Usage:          "/g27-action-failure",
			Entrypoints:    commands.EntrypointsTUI,
			ExecutionOwner: commands.ExecutionOwnerEntrypoint,
			Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				return &commands.CommandResult{
					Action: commands.ActionChangeTheme,
					Data:   map[string]any{"theme": "g27-missing-theme"},
				}, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		if cmd := selectG27PaletteCommand(t, app, "g27-action-failure"); cmd != nil {
			t.Fatal("failed local action returned asynchronous work")
		}
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("action failure retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})

	t.Run("asynchronous clipboard result", func(t *testing.T) {
		newApp := func(t *testing.T) *App {
			t.Helper()
			app := New(Config{})
			app.SetClipboardService(p273SuccessfulClipboardService())
			if err := app.commandRegistry.Register(&commands.Command{
				Name:           "g27-copy",
				Description:    "Copy through the typed clipboard service",
				Usage:          "/g27-copy",
				Entrypoints:    commands.EntrypointsTUI,
				ExecutionOwner: commands.ExecutionOwnerEntrypoint,
				Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
					return &commands.CommandResult{
						Action: commands.ActionCopy,
						Data:   map[string]any{"text": "g27 clipboard payload"},
					}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			return app
		}

		t.Run("confirmed success commits after result", func(t *testing.T) {
			app := newApp(t)
			cmd := selectG27PaletteCommand(t, app, "g27-copy")
			if cmd == nil || app.clipboardPending == nil {
				t.Fatalf(
					"clipboard request cmd=%v pending=%#v",
					cmd != nil,
					app.clipboardPending,
				)
			}
			assertG27NoRecent(t, app)
			p273FinishClipboardCmd(t, app, cmd)
			if got := app.commandPalette.recent; len(got) != 1 || got[0] != "g27-copy" {
				t.Fatalf("confirmed clipboard result recent = %v, want [g27-copy]", got)
			}
			if app.commandPaletteSubmission != nil {
				t.Fatalf(
					"confirmed clipboard result retained submission: %#v",
					app.commandPaletteSubmission,
				)
			}
		})

		t.Run("failure clears without recording", func(t *testing.T) {
			app := newApp(t)
			cmd := selectG27PaletteCommand(t, app, "g27-copy")
			if cmd == nil || app.clipboardPending == nil {
				t.Fatalf(
					"clipboard request cmd=%v pending=%#v",
					cmd != nil,
					app.clipboardPending,
				)
			}
			pending := *app.clipboardPending
			app.Update(clipboardResultMsg{
				requestID: pending.id,
				caller:    pending.caller,
				terminal:  clipboardTerminalFailed,
				failure:   clipboardFailureOutputFailed,
			})
			assertG27NoRecent(t, app)
			if app.commandPaletteSubmission != nil {
				t.Fatalf(
					"failed clipboard result retained submission: %#v",
					app.commandPaletteSubmission,
				)
			}
		})

		t.Run("stale result cannot settle current request", func(t *testing.T) {
			app := newApp(t)
			cmd := selectG27PaletteCommand(t, app, "g27-copy")
			if cmd == nil || app.clipboardPending == nil {
				t.Fatalf(
					"clipboard request cmd=%v pending=%#v",
					cmd != nil,
					app.clipboardPending,
				)
			}
			pending := *app.clipboardPending
			app.Update(clipboardResultMsg{
				requestID: pending.id + 1,
				caller:    pending.caller,
				terminal:  clipboardTerminalSequenceWritten,
				native:    clipboardNativeSucceeded,
			})
			assertG27NoRecent(t, app)
			if app.commandPaletteSubmission == nil {
				t.Fatal("stale clipboard result cleared current submission")
			}

			rawResult := cmd()
			result, ok := rawResult.(clipboardResultMsg)
			if !ok {
				t.Fatalf("clipboard command result = %T", rawResult)
			}
			app.Update(result)
			if got := app.commandPalette.recent; len(got) != 1 || got[0] != "g27-copy" {
				t.Fatalf("live clipboard result recent = %v, want [g27-copy]", got)
			}
		})

		t.Run("manual same text supersedes pending result", func(t *testing.T) {
			app := newApp(t)
			cmd := selectG27PaletteCommand(t, app, "g27-copy")
			if cmd == nil || app.clipboardPending == nil {
				t.Fatalf(
					"clipboard request cmd=%v pending=%#v",
					cmd != nil,
					app.clipboardPending,
				)
			}
			app.textarea.SetValue("/g27-copy")
			app.inputMode = InputCommand
			if manualCmd := app.sendMessage(); manualCmd != nil {
				t.Fatal("busy manual same-text copy unexpectedly returned work")
			}
			if app.commandPaletteSubmission != nil {
				t.Fatalf(
					"manual same-text copy retained palette submission: %#v",
					app.commandPaletteSubmission,
				)
			}

			rawResult := cmd()
			result, ok := rawResult.(clipboardResultMsg)
			if !ok {
				t.Fatalf("clipboard command result = %T", rawResult)
			}
			app.Update(result)
			assertG27NoRecent(t, app)
		})
	})

	t.Run("missing engine", func(t *testing.T) {
		app := New(Config{})
		if cmd := selectG27PaletteCommand(t, app, "status"); cmd != nil {
			t.Fatal("missing engine unexpectedly returned work")
		}
		assertG27NoRecent(t, app)
		if app.commandPaletteSubmission != nil {
			t.Fatalf("missing engine retained pending submission: %#v", app.commandPaletteSubmission)
		}
	})
}
