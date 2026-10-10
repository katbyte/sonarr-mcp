// Package parallel runs a batch of jobs a few at a time: the reads a sweep
// makes of a server that answers one request at a time no faster than eight.
package parallel

import (
	"context"
	"sync"
)

// Each runs fn for each of n jobs, at most workers at a time - one when
// workers is below one - and returns the first error. That error ends the
// run: a job not yet started is not started, and the ones running are told
// through their context, whose cause it is.
//
// When no job failed and the caller's context ended the run, Each returns
// that context's cause, so a run cut short is never read as one that
// finished.
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
