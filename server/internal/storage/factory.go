package storage

import (
	"context"
	"fmt"

	"easy_upload/server/internal/config"
	"easy_upload/server/internal/domain"
)

func NewAdapter(ctx context.Context, cfg config.Config) (domain.BlobAdapter, error) {
	switch cfg.StorageBackend {
	case "local":
		return NewLocalAdapter(cfg.LocalRoot)
	case "s3":
		return NewS3Adapter(ctx, S3Config{
			Region:       cfg.S3Region,
			Endpoint:     cfg.S3Endpoint,
			Bucket:       cfg.S3Bucket,
			AccessKey:    cfg.S3AccessKey,
			SecretKey:    cfg.S3SecretKey,
			UsePathStyle: cfg.S3UsePathStyle,
		})
	default:
		return nil, fmt.Errorf("unsupported storage backend %q", cfg.StorageBackend)
	}
}
