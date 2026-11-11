package worker

import (
	"context"
	"log/slog"
	"time"
)

// workerService is the subset of service.Service used by Worker.
// Declared as an interface to allow unit testing with in-memory fakes.
type workerService interface {
	RunFinalizationPass(ctx context.Context) error
	RunExpirationPass(ctx context.Context) error
}

type Worker struct {
	service workerService
	logger  *slog.Logger
}

func New(svc workerService, logger *slog.Logger) *Worker {
	return &Worker{
		service: svc,
		logger:  logger,
	}
}

func (w *Worker) RunFinalizer(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.service.RunFinalizationPass(ctx); err != nil {
			w.logger.Error("finalization worker pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.service.RunExpirationPass(ctx); err != nil {
			w.logger.Error("expiration worker pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
