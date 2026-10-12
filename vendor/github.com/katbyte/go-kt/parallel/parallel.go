// Package parallel runs a batch of jobs a few at a time, as many as a server answers at once.
package parallel

import (
	"context"
	"sync"
)

// Each runs fn for each of n jobs, at most workers at a time, and returns the first error, which ends the run: jobs not started stay so, and running
// ones see it as their context's cause. A run the caller's context cut short returns that cause, never nil.
func Each(ctx context.Context, n, workers int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)
	slots := make(chan struct{}, max(workers, 1))
	for i := range n {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					first = err
					cancel(err)
				})
			}
		})
	}
	wg.Wait()
	if first != nil {
		return first
	}

	// only the caller's context can have ended this one
	return context.Cause(ctx)
}
