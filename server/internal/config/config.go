package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddr              = ":8080"
	defaultMaxUploadBytes    = int64(5 * 1024 * 1024 * 1024 * 1024)
	defaultMaxChunkBytes     = int64(128 * 1024 * 1024)
	defaultInactivityTTL     = 24 * time.Hour
	defaultSweepInterval     = 15 * time.Minute
	defaultFinalizeInterval  = 5 * time.Second
	defaultFinalizeLimit     = 100
	defaultExpireLimit       = 100
	defaultReadHeaderTimeout = 10 * time.Second
	defaultWriteTimeout      = 15 * time.Minute
	defaultIdleTimeout       = 2 * time.Minute
)

type Config struct {
	Addr               string
	DatabaseURL        string
	AuthTokens         []string
	CORSAllowedOrigins []string
	StorageBackend     string
	LocalRoot          string
	S3Bucket           string
	S3Region           string
	S3Endpoint         string
	S3AccessKey        string
	S3SecretKey        string
	S3UsePathStyle     bool
	MaxUploadBytes     int64
	MaxChunkBytes      int64
	InactivityTTL      time.Duration
	SweepInterval      time.Duration
	FinalizeInterval   time.Duration
	FinalizeLimit      int
	ExpireLimit        int
	ReadHeaderTimeout  time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Addr:               getenv("EASY_UPLOAD_ADDR", defaultAddr),
		DatabaseURL:        strings.TrimSpace(os.Getenv("EASY_UPLOAD_DATABASE_URL")),
		StorageBackend:     getenv("EASY_UPLOAD_STORAGE_BACKEND", "local"),
		LocalRoot:          getenv("EASY_UPLOAD_LOCAL_ROOT", "./data"),
		S3Bucket:           strings.TrimSpace(os.Getenv("EASY_UPLOAD_S3_BUCKET")),
		S3Region:           getenv("EASY_UPLOAD_S3_REGION", "auto"),
		S3Endpoint:         strings.TrimSpace(os.Getenv("EASY_UPLOAD_S3_ENDPOINT")),
		S3AccessKey:        strings.TrimSpace(os.Getenv("EASY_UPLOAD_S3_ACCESS_KEY")),
		S3SecretKey:        strings.TrimSpace(os.Getenv("EASY_UPLOAD_S3_SECRET_KEY")),
		S3UsePathStyle:     getenvBool("EASY_UPLOAD_S3_USE_PATH_STYLE", false),
		MaxUploadBytes:     getenvInt64("EASY_UPLOAD_MAX_UPLOAD_BYTES", defaultMaxUploadBytes),
		MaxChunkBytes:      getenvInt64("EASY_UPLOAD_MAX_CHUNK_BYTES", defaultMaxChunkBytes),
		InactivityTTL:      getenvDuration("EASY_UPLOAD_INACTIVITY_TTL", defaultInactivityTTL),
		SweepInterval:      getenvDuration("EASY_UPLOAD_SWEEP_INTERVAL", defaultSweepInterval),
		FinalizeInterval:   getenvDuration("EASY_UPLOAD_FINALIZE_INTERVAL", defaultFinalizeInterval),
		FinalizeLimit:      getenvInt("EASY_UPLOAD_FINALIZE_LIMIT", defaultFinalizeLimit),
		ExpireLimit:        getenvInt("EASY_UPLOAD_EXPIRE_LIMIT", defaultExpireLimit),
		CORSAllowedOrigins: splitComma(os.Getenv("EASY_UPLOAD_CORS_ALLOWED_ORIGINS")),
		ReadHeaderTimeout:  getenvDuration("EASY_UPLOAD_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout),
		WriteTimeout:       getenvDuration("EASY_UPLOAD_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:        getenvDuration("EASY_UPLOAD_IDLE_TIMEOUT", defaultIdleTimeout),
	}
	cfg.AuthTokens = splitComma(os.Getenv("EASY_UPLOAD_AUTH_TOKENS"))

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("EASY_UPLOAD_DATABASE_URL is required")
	}
	if len(cfg.AuthTokens) == 0 {
		return Config{}, fmt.Errorf("EASY_UPLOAD_AUTH_TOKENS is required")
	}
	switch cfg.StorageBackend {
	case "local":
	case "s3":
		if cfg.S3Bucket == "" {
			return Config{}, fmt.Errorf("EASY_UPLOAD_S3_BUCKET is required when storage backend is s3")
		}
	default:
		return Config{}, fmt.Errorf("unsupported EASY_UPLOAD_STORAGE_BACKEND %q", cfg.StorageBackend)
	}
	if cfg.MaxChunkBytes <= 0 {
		return Config{}, fmt.Errorf("EASY_UPLOAD_MAX_CHUNK_BYTES must be > 0")
	}
	if cfg.MaxUploadBytes <= 0 {
		return Config{}, fmt.Errorf("EASY_UPLOAD_MAX_UPLOAD_BYTES must be > 0")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getenvInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func getenvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitComma(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
