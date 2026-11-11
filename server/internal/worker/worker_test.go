package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"
	"os"
)

// fakeService implements workerService with call counters.
type fakeService struct {
	finalizeCalls atomic.Int64
	sweeperCalls  atomic.Int64
	finalizeErr   error
	sweeperErr    error
}

func (f *fakeService) RunFinalizationPass(_ context.Context) error {
	f.finalizeCalls.Add(1)
	return f.finalizeErr
}

func (f *fakeService) RunExpirationPass(_ context.Context) error {
	f.sweeperCalls.Add(1)
	return f.sweeperErr
}

func newTestWorker(svc workerService) *Worker {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(svc, logger)
}

// waitForAtLeast blocks until the counter reaches n or the deadline passes.
func waitForAtLeast(t *testing.T, counter *atomic.Int64, n int64, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if counter.Load() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("timed out waiting for counter to reach %d (got %d)", n, counter.Load())
}

// TestRunFinalizer_CallsServiceOnTick verifies that RunFinalizer invokes
// RunFinalizationPass once immediately and again on each tick.
func TestRunFinalizer_CallsServiceOnTick(t *testing.T) {
	svc := &fakeService{}
	w := newTestWorker(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.RunFinalizer(ctx, 20*time.Millisecond)

	// First call is immediate; second comes after one tick.
	waitForAtLeast(t, &svc.finalizeCalls, 2, 500*time.Millisecond)
}

// TestRunFinalizer_StopsOnContextCancel verifies that the loop terminates
// promptly when the context is cancelled.
func TestRunFinalizer_StopsOnContextCancel(t *testing.T) {
	svc := &fakeService{}
	w := newTestWorker(svc)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.RunFinalizer(ctx, time.Hour) // very long interval — relies on ctx cancel
	}()

	// Allow the first call to fire.
	waitForAtLeast(t, &svc.finalizeCalls, 1, 500*time.Millisecond)

	cancel()

	select {
	case <-done:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunFinalizer did not stop after context cancel")
	}
}

// TestRunSweeper_CallsServiceOnTick verifies that RunSweeper invokes
// RunExpirationPass once immediately and again on each tick.
func TestRunSweeper_CallsServiceOnTick(t *testing.T) {
	svc := &fakeService{}
	w := newTestWorker(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.RunSweeper(ctx, 20*time.Millisecond)

	waitForAtLeast(t, &svc.sweeperCalls, 2, 500*time.Millisecond)
}

// TestRunSweeper_StopsOnContextCancel verifies that the sweeper loop terminates
// promptly when the context is cancelled.
func TestRunSweeper_StopsOnContextCancel(t *testing.T) {
	svc := &fakeService{}
	w := newTestWorker(svc)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.RunSweeper(ctx, time.Hour)
	}()

	waitForAtLeast(t, &svc.sweeperCalls, 1, 500*time.Millisecond)

	cancel()

	select {
	case <-done:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunSweeper did not stop after context cancel")
	}
}

// TestRunFinalizer_ContinuesAfterError verifies that a service error does NOT
// stop the finalization loop — it should log and continue ticking.
func TestRunFinalizer_ContinuesAfterError(t *testing.T) {
	svc := &fakeService{finalizeErr: errors.New("transient DB error")}
	w := newTestWorker(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.RunFinalizer(ctx, 20*time.Millisecond)

	// Despite repeated errors, the loop must keep running and accumulate calls.
	waitForAtLeast(t, &svc.finalizeCalls, 3, 500*time.Millisecond)
}

// TestRunSweeper_ContinuesAfterError verifies the same resilience for the sweeper.
func TestRunSweeper_ContinuesAfterError(t *testing.T) {
	svc := &fakeService{sweeperErr: errors.New("transient DB error")}
	w := newTestWorker(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.RunSweeper(ctx, 20*time.Millisecond)

	waitForAtLeast(t, &svc.sweeperCalls, 3, 500*time.Millisecond)
}
