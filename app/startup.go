package app

import (
	"context"
	"time"
)

// connectWithRetry stops at the startup deadline. Failed factories must release their resources.
func connectWithRetry[T any](ctx context.Context, delay time.Duration, connect func(context.Context) (T, error)) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		value, err := connect(attempt)
		cancel()
		if err == nil {
			return value, nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}
