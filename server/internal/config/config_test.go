package config

import "testing"

func TestLoadParsesIntegerByteValues(t *testing.T) {
	t.Setenv("EASY_UPLOAD_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/easy_upload?sslmode=disable")
	t.Setenv("EASY_UPLOAD_AUTH_TOKENS", "dev-token")
	t.Setenv("EASY_UPLOAD_MAX_UPLOAD_BYTES", "5497558138880")
	t.Setenv("EASY_UPLOAD_MAX_CHUNK_BYTES", "134217728")
	t.Setenv("EASY_UPLOAD_INACTIVITY_TTL", "24h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MaxUploadBytes != 5497558138880 {
		t.Fatalf("unexpected MaxUploadBytes %d", cfg.MaxUploadBytes)
	}
	if cfg.MaxChunkBytes != 134217728 {
		t.Fatalf("unexpected MaxChunkBytes %d", cfg.MaxChunkBytes)
	}
	if cfg.InactivityTTL != defaultInactivityTTL {
		t.Fatalf("unexpected InactivityTTL %s", cfg.InactivityTTL)
	}
}

func TestLoadInvalidByteSuffixFallsBackToDefaults(t *testing.T) {
	t.Setenv("EASY_UPLOAD_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/easy_upload?sslmode=disable")
	t.Setenv("EASY_UPLOAD_AUTH_TOKENS", "dev-token")
	t.Setenv("EASY_UPLOAD_MAX_UPLOAD_BYTES", "5GiB")
	t.Setenv("EASY_UPLOAD_MAX_CHUNK_BYTES", "128MiB")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MaxUploadBytes != defaultMaxUploadBytes {
		t.Fatalf("unexpected MaxUploadBytes %d", cfg.MaxUploadBytes)
	}
	if cfg.MaxChunkBytes != defaultMaxChunkBytes {
		t.Fatalf("unexpected MaxChunkBytes %d", cfg.MaxChunkBytes)
	}
}

func TestLoadParsesAllowedOrigins(t *testing.T) {
	t.Setenv("EASY_UPLOAD_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/easy_upload?sslmode=disable")
	t.Setenv("EASY_UPLOAD_AUTH_TOKENS", "dev-token")
	t.Setenv("EASY_UPLOAD_CORS_ALLOWED_ORIGINS", "http://localhost:5173, https://example.com ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.CORSAllowedOrigins) != 2 {
		t.Fatalf("unexpected origins length %d", len(cfg.CORSAllowedOrigins))
	}
	if cfg.CORSAllowedOrigins[0] != "http://localhost:5173" {
		t.Fatalf("unexpected first origin %q", cfg.CORSAllowedOrigins[0])
	}
	if cfg.CORSAllowedOrigins[1] != "https://example.com" {
		t.Fatalf("unexpected second origin %q", cfg.CORSAllowedOrigins[1])
	}
}
