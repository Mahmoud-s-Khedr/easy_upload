package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"easy_upload/server/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

type CreateUploadParams struct {
	ID                   string
	DeclaredLength       int64
	Metadata             map[string]string
	ChecksumAlgorithm    string
	ExpectedFullChecksum string
	HashState            []byte
	StorageBackend       string
	StorageHandle        []byte
	ArtifactKey          string
	ExpiresAt            time.Time
	CreatedAt            time.Time
}

type AppendMutation struct {
	Part         domain.UploadPart
	NewOffset    int64
	NewState     domain.UploadState
	NewExpiresAt time.Time
	NewHashState []byte
	UpdatedAt    time.Time
}

// LockedUploadTxn is the interface for a pessimistically-locked upload transaction.
// Using an interface here allows service-layer unit tests to substitute in-memory fakes.
type LockedUploadTxn interface {
	Upload() *domain.Upload
	PersistAppend(ctx context.Context, mutation AppendMutation) error
	MarkAborted(ctx context.Context, now time.Time) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type LockedUpload struct {
	repo   *Repository
	tx     pgx.Tx
	upload *domain.Upload
	closed bool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Pool returns the underlying connection pool.
// Intended for use in tests that need to run raw SQL assertions.
func (r *Repository) Pool() *pgxpool.Pool {
	return r.pool
}

func (r *Repository) CreateUpload(ctx context.Context, params CreateUploadParams) (*domain.Upload, error) {
	metadataJSON, err := json.Marshal(params.Metadata)
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO uploads (
			id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state,
			storage_backend, storage_handle, artifact_key, expires_at, created_at,
			updated_at, version, part_count, finalization_attempts, next_finalization_at
		) VALUES (
			$1, $2, $3, 0, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, 1, 0, 0, $12
		)
		RETURNING id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
	`, params.ID, domain.StateCreated, params.DeclaredLength, metadataJSON, params.ChecksumAlgorithm,
		params.ExpectedFullChecksum, params.HashState, params.StorageBackend, params.StorageHandle,
		params.ArtifactKey, params.ExpiresAt, params.CreatedAt)
	return scanUpload(row)
}

func (r *Repository) GetUpload(ctx context.Context, id string) (*domain.Upload, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
		FROM uploads
		WHERE id = $1
	`, id)
	return scanUpload(row)
}

func (r *Repository) LockUpload(ctx context.Context, id string) (LockedUploadTxn, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		SELECT id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
		FROM uploads
		WHERE id = $1
		FOR UPDATE
	`, id)
	upload, err := scanUpload(row)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &LockedUpload{
		repo:   r,
		tx:     tx,
		upload: upload,
	}, nil
}

func (r *Repository) ListUploadParts(ctx context.Context, uploadID string) ([]domain.UploadPart, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT upload_id, part_number, start_offset, end_offset_exclusive, size_bytes, checksum_sha256, backend_etag, created_at
		FROM upload_parts
		WHERE upload_id = $1
		ORDER BY part_number ASC
	`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parts []domain.UploadPart
	for rows.Next() {
		var part domain.UploadPart
		if err := rows.Scan(&part.UploadID, &part.PartNumber, &part.StartOffset, &part.EndOffsetExclusive, &part.SizeBytes, &part.ChecksumSHA256, &part.BackendETag, &part.CreatedAt); err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, rows.Err()
}

func (r *Repository) ReconcileUpload(ctx context.Context, id string, remote domain.RemoteState, now time.Time) (*domain.Upload, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		SELECT id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
		FROM uploads
		WHERE id = $1
		FOR UPDATE
	`, id)
	upload, err := scanUpload(row)
	if err != nil {
		return nil, err
	}
	if remote.Offset <= upload.CommittedOffset {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return upload, nil
	}

	if _, err := tx.Exec(ctx, `DELETE FROM upload_parts WHERE upload_id = $1`, id); err != nil {
		return nil, err
	}
	var offset int64
	for _, part := range remote.Parts {
		start := offset
		offset += part.Size
		if _, err := tx.Exec(ctx, `
			INSERT INTO upload_parts (upload_id, part_number, start_offset, end_offset_exclusive, size_bytes, checksum_sha256, backend_etag, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, id, part.PartNumber, start, offset, part.Size, part.ChecksumSHA256, part.BackendETag, now); err != nil {
			return nil, err
		}
	}

	newState := upload.State
	if remote.Offset > 0 && newState == domain.StateCreated {
		newState = domain.StateUploading
	}
	if remote.Offset >= upload.DeclaredLength && !newState.Terminal() && newState != domain.StateCompleted {
		newState = domain.StateFinalizing
	}
	row = tx.QueryRow(ctx, `
		UPDATE uploads
		SET committed_offset = $2,
			state = $3,
			part_count = $4,
			updated_at = $5,
			version = version + 1,
			next_finalization_at = CASE WHEN $3 = 'finalizing' THEN $5 ELSE next_finalization_at END
		WHERE id = $1
		RETURNING id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
	`, id, remote.Offset, newState, len(remote.Parts), now)
	upload, err = scanUpload(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return upload, nil
}

func (r *Repository) ListPendingFinalizations(ctx context.Context, limit int, now time.Time) ([]domain.Upload, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
		FROM uploads
		WHERE state = 'finalizing' AND next_finalization_at <= $1
		ORDER BY next_finalization_at ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUploads(rows)
}

func (r *Repository) ListExpiredUploads(ctx context.Context, limit int, now time.Time) ([]domain.Upload, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
		FROM uploads
		WHERE state IN ('created', 'uploading') AND expires_at < $1
		ORDER BY expires_at ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUploads(rows)
}

func (r *Repository) MarkCompleted(ctx context.Context, uploadID, artifactKey, checksum string, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE uploads
		SET state = 'completed',
			artifact_key = $2,
			updated_at = $3,
			completed_at = $3,
			last_error_code = '',
			last_error_message = '',
			version = version + 1
		WHERE id = $1 AND state = 'finalizing'
		RETURNING id, declared_length, storage_backend, metadata_json
	`, uploadID, artifactKey, now)
	var (
		id             string
		declaredLength int64
		backend        string
		metadataJSON   []byte
	)
	if err := row.Scan(&id, &declaredLength, &backend, &metadataJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	var metadata map[string]string
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		return err
	}
	eventID := domain.NewID("evt", now)
	payload, err := json.Marshal(domain.OutboxEvent{
		EventID:        eventID,
		EventType:      domain.EventUploadCompleted,
		UploadID:       id,
		ArtifactKey:    artifactKey,
		Backend:        backend,
		DeclaredLength: declaredLength,
		ChecksumSHA256: checksum,
		Metadata:       metadata,
		OccurredAt:     now,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, upload_id, artifact_key, backend, declared_length, checksum_sha256, metadata_json, occurred_at, payload_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, eventID, domain.EventUploadCompleted, id, artifactKey, backend, declaredLength, checksum, metadataJSON, now, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) MarkFailed(ctx context.Context, uploadID, code, message string, now, nextAttempt time.Time, terminal bool) error {
	state := domain.StateFinalizing
	if terminal {
		state = domain.StateFailed
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE uploads
		SET state = $2,
			last_error_code = $3,
			last_error_message = $4,
			updated_at = $5,
			version = version + 1,
			finalization_attempts = finalization_attempts + 1,
			next_finalization_at = $6
		WHERE id = $1 AND state NOT IN ('completed', 'aborted', 'expired', 'failed')
		RETURNING id, declared_length, storage_backend, metadata_json
	`, uploadID, state, code, message, now, nextAttempt)
	var (
		id             string
		declaredLength int64
		backend        string
		metadataJSON   []byte
	)
	if err := row.Scan(&id, &declaredLength, &backend, &metadataJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !terminal {
		return tx.Commit(ctx)
	}
	eventID := domain.NewID("evt", now)
	payload, err := json.Marshal(domain.OutboxEvent{
		EventID:        eventID,
		EventType:      domain.EventUploadFailed,
		UploadID:       id,
		Backend:        backend,
		DeclaredLength: declaredLength,
		OccurredAt:     now,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, upload_id, artifact_key, backend, declared_length, checksum_sha256, metadata_json, occurred_at, payload_json)
		VALUES ($1, $2, $3, '', $4, $5, '', $6, $7, $8)
	`, eventID, domain.EventUploadFailed, id, backend, declaredLength, metadataJSON, now, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) MarkExpired(ctx context.Context, uploadID string, now time.Time) error {
	return r.markTerminal(ctx, uploadID, domain.StateExpired, domain.EventUploadExpired, now)
}

func (r *Repository) markTerminal(ctx context.Context, uploadID string, state domain.UploadState, eventType string, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE uploads
		SET state = $2, updated_at = $3, version = version + 1
		WHERE id = $1 AND state IN ('created', 'uploading', 'finalizing')
		RETURNING id, declared_length, storage_backend, metadata_json
	`, uploadID, state, now)
	var (
		id             string
		declaredLength int64
		backend        string
		metadataJSON   []byte
	)
	if err := row.Scan(&id, &declaredLength, &backend, &metadataJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	eventID := domain.NewID("evt", now)
	payload, err := json.Marshal(domain.OutboxEvent{
		EventID:        eventID,
		EventType:      eventType,
		UploadID:       id,
		Backend:        backend,
		DeclaredLength: declaredLength,
		OccurredAt:     now,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, upload_id, artifact_key, backend, declared_length, checksum_sha256, metadata_json, occurred_at, payload_json)
		VALUES ($1, $2, $3, '', $4, $5, '', $6, $7, $8)
	`, eventID, eventType, id, backend, declaredLength, metadataJSON, now, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (l *LockedUpload) Upload() *domain.Upload {
	return l.upload
}

func (l *LockedUpload) PersistAppend(ctx context.Context, mutation AppendMutation) error {
	if _, err := l.tx.Exec(ctx, `
		INSERT INTO upload_parts (upload_id, part_number, start_offset, end_offset_exclusive, size_bytes, checksum_sha256, backend_etag, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, mutation.Part.UploadID, mutation.Part.PartNumber, mutation.Part.StartOffset, mutation.Part.EndOffsetExclusive,
		mutation.Part.SizeBytes, mutation.Part.ChecksumSHA256, mutation.Part.BackendETag, mutation.Part.CreatedAt); err != nil {
		return err
	}
	row := l.tx.QueryRow(ctx, `
		UPDATE uploads
		SET committed_offset = $2,
			state = $3,
			expires_at = $4,
			hash_state = $5,
			updated_at = $6,
			version = version + 1,
			part_count = part_count + 1,
			next_finalization_at = CASE WHEN $3 = 'finalizing' THEN $6 ELSE next_finalization_at END
		WHERE id = $1
		RETURNING id, state, declared_length, committed_offset, metadata_json,
			checksum_algorithm, expected_full_checksum, hash_state, storage_backend,
			storage_handle, artifact_key, expires_at, last_error_code, last_error_message,
			created_at, updated_at, completed_at, version, part_count, finalization_attempts, next_finalization_at
	`, l.upload.ID, mutation.NewOffset, mutation.NewState, mutation.NewExpiresAt, mutation.NewHashState, mutation.UpdatedAt)
	upload, err := scanUpload(row)
	if err != nil {
		return err
	}
	l.upload = upload
	return nil
}

func (l *LockedUpload) MarkAborted(ctx context.Context, now time.Time) error {
	row := l.tx.QueryRow(ctx, `
		UPDATE uploads
		SET state = 'aborted',
			updated_at = $2,
			version = version + 1
		WHERE id = $1
		RETURNING id, declared_length, storage_backend, metadata_json
	`, l.upload.ID, now)
	var (
		id             string
		declaredLength int64
		backend        string
		metadataJSON   []byte
	)
	if err := row.Scan(&id, &declaredLength, &backend, &metadataJSON); err != nil {
		return err
	}
	eventID := domain.NewID("evt", now)
	payload, err := json.Marshal(domain.OutboxEvent{
		EventID:        eventID,
		EventType:      domain.EventUploadAborted,
		UploadID:       id,
		Backend:        backend,
		DeclaredLength: declaredLength,
		OccurredAt:     now,
	})
	if err != nil {
		return err
	}
	if _, err := l.tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, upload_id, artifact_key, backend, declared_length, checksum_sha256, metadata_json, occurred_at, payload_json)
		VALUES ($1, $2, $3, '', $4, $5, '', $6, $7, $8)
	`, eventID, domain.EventUploadAborted, id, backend, declaredLength, metadataJSON, now, payload); err != nil {
		return err
	}
	return nil
}

func (l *LockedUpload) Commit(ctx context.Context) error {
	if l.closed {
		return nil
	}
	l.closed = true
	return l.tx.Commit(ctx)
}

func (l *LockedUpload) Rollback(ctx context.Context) error {
	if l.closed {
		return nil
	}
	l.closed = true
	return l.tx.Rollback(ctx)
}

func scanUpload(row interface{ Scan(dest ...any) error }) (*domain.Upload, error) {
	var (
		upload       domain.Upload
		metadataJSON []byte
		state        string
	)
	err := row.Scan(
		&upload.ID, &state, &upload.DeclaredLength, &upload.CommittedOffset,
		&metadataJSON, &upload.ChecksumAlgorithm, &upload.ExpectedFullChecksum,
		&upload.HashState, &upload.StorageBackend, &upload.StorageHandle,
		&upload.ArtifactKey, &upload.ExpiresAt, &upload.LastErrorCode,
		&upload.LastErrorMessage, &upload.CreatedAt, &upload.UpdatedAt,
		&upload.CompletedAt, &upload.Version, &upload.PartCount,
		&upload.FinalizationAttempts, &upload.NextFinalizationAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.WrapError(404, "UPLOAD_NOT_FOUND", "upload not found", false, err)
		}
		return nil, err
	}
	upload.State = domain.UploadState(state)
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &upload.Metadata); err != nil {
			return nil, err
		}
	}
	if upload.Metadata == nil {
		upload.Metadata = map[string]string{}
	}
	return &upload, nil
}

func scanUploads(rows pgx.Rows) ([]domain.Upload, error) {
	var uploads []domain.Upload
	for rows.Next() {
		upload, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, *upload)
	}
	return uploads, rows.Err()
}

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
