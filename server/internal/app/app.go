package app

import (
	"context"
	"log/slog"
	"net/http"

	"easy_upload/server/internal/config"
	"easy_upload/server/internal/httpapi"
	"easy_upload/server/internal/postgres"
	"easy_upload/server/internal/service"
	"easy_upload/server/internal/storage"
	"easy_upload/server/internal/worker"
)

type App struct {
	Server *http.Server
	Pool   interface{ Close() }
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	adapter, err := storage.NewAdapter(ctx, cfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	repo := postgres.New(pool)
	svc := service.New(repo, adapter, cfg, logger)
	handler := httpapi.NewHandler(svc, cfg)
	workers := worker.New(svc, logger)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	go workers.RunFinalizer(ctx, cfg.FinalizeInterval)
	go workers.RunSweeper(ctx, cfg.SweepInterval)

	return &App{
		Server: server,
		Pool:   pool,
	}, nil
}
