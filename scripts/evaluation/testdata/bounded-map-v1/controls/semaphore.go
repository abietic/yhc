package fixture

import (
	"context"
	"errors"
	"sync"
)

func Map(parent context.Context, input []int, limit int, work func(context.Context, int) (int, error)) ([]int, error) {
	if limit < 1 {
		return nil, errors.New("invalid limit")
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	permits := make(chan struct{}, limit)
	out := make([]int, len(input))
	var wg sync.WaitGroup
	var first sync.Once
	var firstErr error
dispatch:
	for i := range input {
		select {
		case permits <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		wg.Go(func() {
			defer func() { <-permits }()
			if ctx.Err() != nil {
				return
			}
			value, err := work(ctx, input[i])
			if err != nil {
				first.Do(func() { firstErr = err; cancel() })
				return
			}
			out[i] = value
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
