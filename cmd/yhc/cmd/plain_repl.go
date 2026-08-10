package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func drivePlainREPL(
	ctx context.Context,
	eng *engine.QueryEngine,
	cmdRegistry *commands.Registry,
	input *plainInputBroker,
	plainPrompt engine.PermissionPromptFn,
	stdout io.Writer,
	stderr io.Writer,
) (returnErr error) {
	if eng == nil {
		return errors.New("plain REPL engine is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	stopAttempted := false
	defer func() {
		if returnErr == nil || stopAttempted {
			return
		}
		mode := engine.RuntimeStopGraceful
		reason := "plain_runtime_failed"
		if errors.Is(returnErr, context.Canceled) ||
			errors.Is(returnErr, context.DeadlineExceeded) {
			mode = engine.RuntimeStopImmediate
			reason = "plain_context_cancelled"
		}
		if stopErr := eng.RequestStop(mode, reason); stopErr != nil {
			returnErr = errors.Join(returnErr, stopErr)
		}
	}()
	goalWake := eng.SubscribeGoalContinuations()
	for {
		fmt.Fprint(stdout, "> ")
		selection := waitForPlainIdleWork(ctx, input, goalWake)
		if selection.hasInput {
			exit, eofAfterInput, err := drivePlainInput(
				ctx,
				eng,
				cmdRegistry,
				plainPrompt,
				stdout,
				stderr,
				selection.input,
			)
			if err != nil {
				return err
			}
			if exit || eofAfterInput {
				stopAttempted = true
				if err := eng.RequestStop(
					engine.RuntimeStopGraceful,
					"plain_input_closed",
				); err != nil {
					return err
				}
				return nil
			}
			continue
		}
		if !selection.goalWake {
			continue
		}
		fmt.Fprintln(stdout)

		if _, pending := eng.PendingProjectGraphPermissionRequest(); pending {
			if err := drivePlainPendingProjectGraphPermission(
				ctx,
				eng,
				plainPrompt,
				stdout,
				stderr,
			); err != nil {
				return err
			}
			fmt.Fprintln(stdout)
			continue
		}
		if completedInput, ok := input.tryNext(); ok {
			exit, eofAfterInput, err := drivePlainInput(
				ctx,
				eng,
				cmdRegistry,
				plainPrompt,
				stdout,
				stderr,
				completedInput,
			)
			if err != nil {
				return err
			}
			if exit || eofAfterInput {
				stopAttempted = true
				if err := eng.RequestStop(
					engine.RuntimeStopGraceful,
					"plain_input_closed",
				); err != nil {
					return err
				}
				return nil
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		started, err := drivePlainGoalContinuation(
			ctx,
			eng,
			plainPrompt,
			stdout,
			stderr,
		)
		if err != nil {
			return err
		}
		if started {
			fmt.Fprintln(stdout)
		}
	}
}

func drivePlainInput(
	ctx context.Context,
	eng *engine.QueryEngine,
	cmdRegistry *commands.Registry,
	plainPrompt engine.PermissionPromptFn,
	stdout io.Writer,
	stderr io.Writer,
	readResult plainInputResult,
) (bool, bool, error) {
	if readResult.err != nil && !errors.Is(readResult.err, io.EOF) {
		if errors.Is(readResult.err, context.Canceled) ||
			errors.Is(readResult.err, context.DeadlineExceeded) {
			return false, false, readResult.err
		}
		return false, false, fmt.Errorf(
			"read plain REPL input: %w",
			readResult.err,
		)
	}
	eofAfterInput := errors.Is(readResult.err, io.EOF)
	if len(readResult.line) == 0 && eofAfterInput {
		return false, true, nil
	}
	prompt := strings.TrimSpace(readResult.line)
	if prompt == "/exit" || prompt == "/quit" || prompt == "exit" {
		fmt.Fprintln(stdout, "Goodbye.")
		return true, eofAfterInput, nil
	}
	if prompt == "" {
		return false, eofAfterInput, nil
	}

	if commands.IsCommand(prompt) {
		if plainCommandRunsThroughEngine(cmdRegistry, prompt) {
			outcome, commandErr := runPlainEngineCommand(ctx, eng, prompt)
			if commandErr != nil {
				fmt.Fprintf(stdout, "Command error: %v\n", commandErr)
				return false, eofAfterInput, nil
			}
			if outcome.Output != "" {
				fmt.Fprintln(stdout, outcome.Output)
			}
			if outcome.FollowUpPrompt != "" &&
				outcome.Status == engine.CommandResultSucceeded {
				prompt = outcome.FollowUpPrompt
			} else {
				return false, eofAfterInput, nil
			}
		} else {
			cmdCtx := eng.CommandContext()
			result, dispatchErr := cmdRegistry.Dispatch(
				ctx,
				commands.EntrypointPlain,
				cmdCtx,
				prompt,
			)
			if dispatchErr != nil {
				fmt.Fprintf(stdout, "Command error: %v\n", dispatchErr)
				return false, eofAfterInput, nil
			}
			if result == nil {
				return false, eofAfterInput, nil
			}
			if result.Output != "" {
				fmt.Fprintln(stdout, result.Output)
			}
			action := plainREPLHandleAction(result, stdout)
			if action == "quit" {
				return true, eofAfterInput, nil
			}
			if action == "prompt" {
				prompt = result.Output
			} else {
				return false, eofAfterInput, nil
			}
		}
	}

	events, _ := eng.SubmitMessage(ctx, prompt)
	if driveErr := drivePlainQueryEvents(
		ctx,
		eng,
		plainPrompt,
		stdout,
		stderr,
		events,
	); driveErr != nil {
		return false, eofAfterInput, fmt.Errorf(
			"drive plain query events: %w",
			driveErr,
		)
	}
	fmt.Fprintln(stdout)
	return false, eofAfterInput, nil
}

func drivePlainGoalContinuation(
	ctx context.Context,
	eng *engine.QueryEngine,
	plainPrompt engine.PermissionPromptFn,
	stdout io.Writer,
	stderr io.Writer,
) (bool, error) {
	events, ok, err := claimPlainGoalContinuation(ctx, eng, stdout)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := drivePlainQueryEvents(
		ctx,
		eng,
		plainPrompt,
		stdout,
		stderr,
		events,
	); err != nil {
		return true, fmt.Errorf("drive Plain Goal continuation: %w", err)
	}
	return true, nil
}

type plainGoalContinuationRuntime interface {
	ClaimNextGoalContinuation() (engine.RuntimeItem, bool, error)
	GoalSnapshot() (*engine.GoalSnapshot, bool)
	SubmitGoalContinuation(
		context.Context,
		engine.RuntimeItem,
	) (<-chan engine.QueryEvent, engine.Terminal)
}

func claimPlainGoalContinuation(
	ctx context.Context,
	runtime plainGoalContinuationRuntime,
	stdout io.Writer,
) (<-chan engine.QueryEvent, bool, error) {
	if runtime == nil {
		return nil, false, errors.New("plain Goal runtime is unavailable")
	}
	item, ok, err := runtime.ClaimNextGoalContinuation()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	objective := "active Goal"
	if snapshot, available := runtime.GoalSnapshot(); available {
		if normalized := strings.Join(strings.Fields(snapshot.Objective), " "); normalized != "" {
			objective = normalized
		}
	}
	fmt.Fprintf(stdout, "[Goal continuation] %s\n", objective)
	events, _ := runtime.SubmitGoalContinuation(ctx, item)
	return events, true, nil
}
