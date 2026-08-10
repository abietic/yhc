package cmd

import (
	"bufio"
	"context"
	"io"
)

type plainInputResult struct {
	line string
	err  error
}

// plainInputBroker is the process-lifetime owner of Plain stdin reads. A
// cancelled consumer leaves this one reader in place so a later prompt cannot
// race an abandoned ReadString goroutine for the next line.
type plainInputBroker struct {
	results <-chan plainInputResult
}

func newPlainInputBroker(reader *bufio.Reader) *plainInputBroker {
	results := make(chan plainInputResult, 1)
	broker := &plainInputBroker{results: results}
	go func() {
		defer close(results)
		if reader == nil {
			results <- plainInputResult{err: io.EOF}
			return
		}
		for {
			line, err := reader.ReadString('\n')
			results <- plainInputResult{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()
	return broker
}

func (b *plainInputBroker) next(ctx context.Context) plainInputResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.results == nil {
		return plainInputResult{err: io.EOF}
	}
	select {
	case <-ctx.Done():
		return plainInputResult{err: ctx.Err()}
	case result, ok := <-b.results:
		if !ok {
			return plainInputResult{err: io.EOF}
		}
		return result
	}
}

func (b *plainInputBroker) tryNext() (plainInputResult, bool) {
	if b == nil || b.results == nil {
		return plainInputResult{err: io.EOF}, true
	}
	select {
	case result, ok := <-b.results:
		if !ok {
			return plainInputResult{err: io.EOF}, true
		}
		return result, true
	default:
		return plainInputResult{}, false
	}
}

type plainIdleSelection struct {
	input    plainInputResult
	hasInput bool
	goalWake bool
}

// waitForPlainIdleWork gives already-completed input deterministic precedence
// over a coalesced Goal wake. The caller rechecks input and permission state
// again immediately before claiming a Goal cursor.
func waitForPlainIdleWork(
	ctx context.Context,
	input *plainInputBroker,
	goalWake <-chan struct{},
) plainIdleSelection {
	if result, ok := input.tryNext(); ok {
		return plainIdleSelection{input: result, hasInput: true}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return plainIdleSelection{
			input:    plainInputResult{err: ctx.Err()},
			hasInput: true,
		}
	case result, ok := <-input.results:
		if !ok {
			result = plainInputResult{err: io.EOF}
		}
		return plainIdleSelection{input: result, hasInput: true}
	case <-goalWake:
		if result, ok := input.tryNext(); ok {
			return plainIdleSelection{input: result, hasInput: true}
		}
		return plainIdleSelection{goalWake: true}
	}
}
