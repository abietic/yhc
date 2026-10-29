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
	out := make([]int, len(input))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var first sync.Once
	var firstErr error
	workers := min(limit, len(input))
	for range workers {
		wg.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				value, err := work(ctx, input[i])
				if err != nil {
					first.Do(func() { firstErr = err; cancel() })
					continue
				}
				out[i] = value
			}
		})
	}
dispatch:
	for i := range input {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
