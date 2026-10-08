package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStartupRetriesTransientFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	value, err := connectWithRetry(ctx, time.Millisecond, func(ctx context.Context) (int, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("attempt has no deadline")
		}
		if calls < 3 {
			return 0, errors.New("unavailable")
		}
		return 42, nil
	})
	if err != nil || value != 42 || calls != 3 {
		t.Fatalf("value=%d calls=%d err=%v", value, calls, err)
	}
}
func TestStartupStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := connectWithRetry(ctx, time.Hour, func(context.Context) (int, error) {
		calls++
		cancel()
		return 0, errors.New("private connection details")
	})
	if !errors.Is(err, context.Canceled) || calls != 1 || strings.Contains(err.Error(), "private") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestStartupDeadlineBoundsPermanentFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := connectWithRetry(ctx, time.Millisecond, func(context.Context) (int, error) { return 0, errors.New("offline") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestMigrationFailureStopsStartup(t *testing.T) {
	calls := 0
	err := applyMigrations(context.Background(), "ch", func(string) error { calls++; return errors.New("private SQL details") }, "ClickHouse")
	if err == nil || calls != 1 || strings.Contains(err.Error(), "private") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	if err := applyMigrations(ctx, "pg", func(string) error { calls++; return nil }, "PostgreSQL"); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
