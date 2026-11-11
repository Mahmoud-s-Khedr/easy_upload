package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"easy_upload/server/internal/config"
	"easy_upload/server/internal/domain"
	"easy_upload/server/internal/postgres"
)

const maxFinalizationAttempts = 10

// uploadRepository is the subset of postgres.Repository used by Service.
// Declared as an interface to allow unit testing with in-memory fakes.
type uploadRepository interface {
	CreateUpload(ctx context.Context, params postgres.CreateUploadParams) (*domain.Upload, error)
	GetUpload(ctx context.Context, id string) (*domain.Upload, error)
	LockUpload(ctx context.Context, id string) (postgres.LockedUploadTxn, error)
	ListUploadParts(ctx context.Context, uploadID string) ([]domain.UploadPart, error)
	ReconcileUpload(ctx context.Context, id string, remote domain.RemoteState, now time.Time) (*domain.Upload, error)
	ListPendingFinalizations(ctx context.Context, limit int, now time.Time) ([]domain.Upload, error)
	ListExpiredUploads(ctx context.Context, limit int, now time.Time) ([]domain.Upload, error)
	MarkCompleted(ctx context.Context, uploadID, artifactKey, checksum string, now time.Time) error
	MarkFailed(ctx context.Context, uploadID, code, message string, now, nextAttempt time.Time, terminal bool) error
	MarkExpired(ctx context.Context, uploadID string, now time.Time) error
}

type Service struct {
	repo    uploadRepository
	adapter domain.BlobAdapter
	cfg     config.Config
	logger  *slog.Logger
	now     func() time.Time
}

type CreateUploadInput struct {
	DeclaredLength int64
	Metadata       map[string]string
}

type AppendUploadInput struct {
	UploadID       string
	ExpectedOffset int64
	Payload        []byte
	ChunkChecksum  string
}

func New(repo uploadRepository, adapter domain.BlobAdapter, cfg config.Config, logger *slog.Logger) *Service {
	return &Service{
		repo:    repo,
		adapter: adapter,
		cfg:     cfg,
		logger:  logger,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) CreateUpload(ctx context.Context, input CreateUploadInput) (*domain.Upload, error) {
	if input.DeclaredLength <= 0 {
		return nil, domain.WrapError(http.StatusBadRequest, "INVALID_UPLOAD_LENGTH", "upload length must be greater than zero", false, nil)
	}
	if input.DeclaredLength > s.cfg.MaxUploadBytes {
		return nil, domain.WrapError(http.StatusRequestEntityTooLarge, "INVALID_UPLOAD_LENGTH", "upload length exceeds service maximum", false, nil)
	}
	now := s.now()
	uploadID := domain.NewID("upl", now)
	artifactKey := buildArtifactKey(now, uploadID, input.Metadata["objectKeyHint"])
	hashState, err := newHashState()
	if err != nil {
		return nil, err
	}
	handle, err := s.adapter.Create(ctx, domain.CreateSpec{
		UploadID:       uploadID,
		DeclaredLength: input.DeclaredLength,
		Metadata:       input.Metadata,
		ArtifactKey:    artifactKey,
	})
	if err != nil {
		return nil, domain.WrapError(http.StatusServiceUnavailable, "BACKEND_UNAVAILABLE", "failed to allocate storage handle", true, err)
	}
	upload, err := s.repo.CreateUpload(ctx, postgres.CreateUploadParams{
		ID:                   uploadID,
		DeclaredLength:       input.DeclaredLength,
		Metadata:             input.Metadata,
		ChecksumAlgorithm:    domain.DefaultChecksumAlg,
		ExpectedFullChecksum: input.Metadata["sha256"],
		HashState:            hashState,
		StorageBackend:       handle.Backend,
		StorageHandle:        handle.Opaque,
		ArtifactKey:          artifactKey,
		ExpiresAt:            now.Add(s.cfg.InactivityTTL),
		CreatedAt:            now,
	})
	if err != nil {
		_ = s.adapter.Abort(ctx, handle)
		return nil, err
	}
	return upload, nil
}

func (s *Service) GetUpload(ctx context.Context, uploadID string, reconcile bool) (*domain.Upload, error) {
	upload, err := s.repo.GetUpload(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if reconcile && !upload.State.Terminal() {
		refreshed, err := s.refreshFromRemote(ctx, upload)
		if err != nil {
			return nil, err
		}
		return refreshed, nil
	}
	return upload, nil
}

func (s *Service) AppendUpload(ctx context.Context, input AppendUploadInput) (*domain.Upload, error) {
	lock, err := s.repo.LockUpload(ctx, input.UploadID)
	if err != nil {
		return nil, err
	}
	defer lock.Rollback(ctx)

	upload := lock.Upload()
	now := s.now()
	if err := s.failIfExpired(ctx, upload, now); err != nil {
		return nil, err
	}
	if upload.State.Terminal() {
		return nil, stateWriteError(upload)
	}
	if input.ExpectedOffset != upload.CommittedOffset {
		return nil, domain.WrapError(http.StatusConflict, "OFFSET_MISMATCH", "upload offset does not match committed offset", true, nil)
	}
	if len(input.Payload) == 0 {
		return nil, domain.WrapError(http.StatusBadRequest, "EMPTY_CHUNK", "chunk payload must not be empty", false, nil)
	}
	if int64(len(input.Payload)) > s.cfg.MaxChunkBytes {
		return nil, domain.WrapError(http.StatusRequestEntityTooLarge, "CHUNK_TOO_LARGE", "chunk exceeds maximum allowed size", false, nil)
	}
	newOffset := upload.CommittedOffset + int64(len(input.Payload))
	if newOffset > upload.DeclaredLength {
		return nil, domain.WrapError(http.StatusBadRequest, "INVALID_UPLOAD_LENGTH", "chunk exceeds declared upload length", false, nil)
	}

	chunkHash := sha256.Sum256(input.Payload)
	chunkChecksum := hex.EncodeToString(chunkHash[:])
	if input.ChunkChecksum != "" && !strings.EqualFold(input.ChunkChecksum, chunkChecksum) {
		return nil, domain.WrapError(http.StatusUnprocessableEntity, "CHECKSUM_MISMATCH", "chunk checksum does not match payload", false, nil)
	}

	rollingHash, err := hashFromState(upload.HashState)
	if err != nil {
		return nil, err
	}
	if _, err := rollingHash.Write(input.Payload); err != nil {
		return nil, err
	}
	hashState, err := marshalHashState(rollingHash)
	if err != nil {
		return nil, err
	}

	partNumber := upload.PartCount + 1
	appendResult, err := s.adapter.Append(ctx, s.handleForUpload(upload), domain.Segment{
		PartNumber:     partNumber,
		StartOffset:    upload.CommittedOffset,
		Size:           int64(len(input.Payload)),
		ChecksumSHA256: chunkChecksum,
	}, bytes.NewReader(input.Payload))
	if err != nil {
		return nil, domain.WrapError(http.StatusServiceUnavailable, "BACKEND_UNAVAILABLE", "failed to append upload chunk", true, err)
	}

	newState := domain.StateUploading
	if newOffset == upload.DeclaredLength {
		newState = domain.StateFinalizing
	}
	if err := lock.PersistAppend(ctx, postgres.AppendMutation{
		Part: domain.UploadPart{
			UploadID:           upload.ID,
			PartNumber:         partNumber,
			StartOffset:        upload.CommittedOffset,
			EndOffsetExclusive: newOffset,
			SizeBytes:          appendResult.Size,
			ChecksumSHA256:     chunkChecksum,
			BackendETag:        appendResult.BackendETag,
			CreatedAt:          now,
		},
		NewOffset:    newOffset,
		NewState:     newState,
		NewExpiresAt: now.Add(s.cfg.InactivityTTL),
		NewHashState: hashState,
		UpdatedAt:    now,
	}); err != nil {
		return nil, err
	}
	if err := lock.Commit(ctx); err != nil {
		return nil, err
	}
	return lock.Upload(), nil
}

func (s *Service) TerminateUpload(ctx context.Context, uploadID string) error {
	lock, err := s.repo.LockUpload(ctx, uploadID)
	if err != nil {
		return err
	}
	defer lock.Rollback(ctx)

	upload := lock.Upload()
	now := s.now()
	if err := s.failIfExpired(ctx, upload, now); err != nil {
		return err
	}
	if upload.State.Terminal() {
		if upload.State == domain.StateCompleted {
			return domain.WrapError(http.StatusConflict, "UPLOAD_COMPLETED", "completed uploads cannot be terminated", false, nil)
		}
		return nil
	}
	if err := s.adapter.Abort(ctx, s.handleForUpload(upload)); err != nil {
		return domain.WrapError(http.StatusServiceUnavailable, "BACKEND_UNAVAILABLE", "failed to abort upload in storage backend", true, err)
	}
	if err := lock.MarkAborted(ctx, now); err != nil {
		return err
	}
	return lock.Commit(ctx)
}

func (s *Service) BuildStatus(upload *domain.Upload) (*domain.StatusResponse, error) {
	var digest *string
	if upload.State == domain.StateCompleted && (upload.ExpectedFullChecksum != "" || len(upload.HashState) > 0) {
		value, err := digestFromState(upload.HashState)
		if err != nil {
			return nil, err
		}
		digest = &value
	}
	var artifactKey *string
	if upload.State == domain.StateCompleted && upload.ArtifactKey != "" {
		artifactKey = &upload.ArtifactKey
	}
	var failure *domain.Failure
	if upload.LastErrorCode != "" || upload.LastErrorMessage != "" {
		failure = &domain.Failure{
			Code:    upload.LastErrorCode,
			Message: upload.LastErrorMessage,
		}
	}
	return &domain.StatusResponse{
		ID:              upload.ID,
		State:           upload.State,
		DeclaredLength:  upload.DeclaredLength,
		CommittedOffset: upload.CommittedOffset,
		ExpiresAt:       upload.ExpiresAt,
		Metadata:        upload.Metadata,
		Checksum: domain.StatusChecksum{
			Algorithm:      upload.ChecksumAlgorithm,
			FullFileDigest: digest,
		},
		Storage: domain.StatusStorage{
			Backend:     upload.StorageBackend,
			ArtifactKey: artifactKey,
		},
		Failure:     failure,
		CreatedAt:   upload.CreatedAt,
		UpdatedAt:   upload.UpdatedAt,
		CompletedAt: upload.CompletedAt,
	}, nil
}

func (s *Service) RunFinalizationPass(ctx context.Context) error {
	now := s.now()
	uploads, err := s.repo.ListPendingFinalizations(ctx, s.cfg.FinalizeLimit, now)
	if err != nil {
		return err
	}
	for _, upload := range uploads {
		if err := s.finalizeUpload(ctx, &upload); err != nil {
			s.logger.Error("finalization pass failed", "upload_id", upload.ID, "error", err)
		}
	}
	return nil
}

func (s *Service) RunExpirationPass(ctx context.Context) error {
	now := s.now()
	uploads, err := s.repo.ListExpiredUploads(ctx, s.cfg.ExpireLimit, now)
	if err != nil {
		return err
	}
	for _, upload := range uploads {
		if err := s.adapter.DeleteExpired(ctx, s.handleForUpload(&upload)); err != nil {
			s.logger.Error("delete expired upload failed", "upload_id", upload.ID, "error", err)
			continue
		}
		if err := s.repo.MarkExpired(ctx, upload.ID, now); err != nil {
			s.logger.Error("mark expired failed", "upload_id", upload.ID, "error", err)
		}
	}
	return nil
}

func (s *Service) refreshFromRemote(ctx context.Context, upload *domain.Upload) (*domain.Upload, error) {
	remote, err := s.adapter.Inspect(ctx, s.handleForUpload(upload))
	if err != nil {
		return upload, nil
	}
	if remote.Offset > upload.CommittedOffset {
		return s.repo.ReconcileUpload(ctx, upload.ID, remote, s.now())
	}
	return upload, nil
}

func (s *Service) finalizeUpload(ctx context.Context, upload *domain.Upload) error {
	if upload.State != domain.StateFinalizing {
		return nil
	}
	if upload.CommittedOffset != upload.DeclaredLength {
		return s.repo.MarkFailed(ctx, upload.ID, "UPLOAD_INCOMPLETE", "upload has not reached declared length", s.now(), s.nextRetryAt(upload.FinalizationAttempts), true)
	}
	parts, err := s.repo.ListUploadParts(ctx, upload.ID)
	if err != nil {
		return err
	}
	// NOTE: hash_state is not updated during reconcile (see docs/technical_spec_v1.md §25).
	// If a crash occurred after adapter.Append but before the DB transaction committed,
	// hash_state may be stale. Uploads with an expected full checksum will fail here;
	// uploads without one will proceed with a digest derived from the bytes that were hashed.
	digest, err := digestFromState(upload.HashState)
	if err != nil {
		return err
	}
	if upload.ExpectedFullChecksum != "" && !strings.EqualFold(upload.ExpectedFullChecksum, digest) {
		return s.repo.MarkFailed(ctx, upload.ID, "CHECKSUM_MISMATCH", "full file checksum mismatch", s.now(), time.Time{}, true)
	}
	result, err := s.adapter.Seal(ctx, s.handleForUpload(upload), domain.SealRequest{
		ArtifactKey:          upload.ArtifactKey,
		ExpectedSize:         upload.DeclaredLength,
		ExpectedFullChecksum: upload.ExpectedFullChecksum,
		Metadata:             upload.Metadata,
		Parts:                parts,
	})
	if err != nil {
		now := s.now()
		terminal := upload.FinalizationAttempts+1 >= maxFinalizationAttempts
		return s.repo.MarkFailed(ctx, upload.ID, "BACKEND_UNAVAILABLE", err.Error(), now, s.nextRetryAt(upload.FinalizationAttempts), terminal)
	}
	if result.ChecksumSHA256 == "" {
		result.ChecksumSHA256 = digest
	}
	return s.repo.MarkCompleted(ctx, upload.ID, result.ArtifactKey, result.ChecksumSHA256, s.now())
}

func (s *Service) nextRetryAt(attempts int) time.Time {
	backoff := time.Second * time.Duration(1<<min(attempts, 6)) //nolint:predeclared
	return s.now().Add(backoff)
}

func (s *Service) handleForUpload(upload *domain.Upload) domain.Handle {
	return domain.Handle{
		Backend: upload.StorageBackend,
		Opaque:  upload.StorageHandle,
	}
}

func (s *Service) failIfExpired(ctx context.Context, upload *domain.Upload, now time.Time) error {
	if (upload.State == domain.StateCreated || upload.State == domain.StateUploading) && upload.ExpiresAt.Before(now) {
		if err := s.adapter.DeleteExpired(ctx, s.handleForUpload(upload)); err != nil {
			s.logger.Error("delete expired upload failed", "upload_id", upload.ID, "error", err)
		}
		if err := s.repo.MarkExpired(ctx, upload.ID, now); err != nil {
			return err
		}
		return domain.WrapError(http.StatusGone, "UPLOAD_EXPIRED", "upload has expired", false, nil)
	}
	switch upload.State {
	case domain.StateExpired:
		return domain.WrapError(http.StatusGone, "UPLOAD_EXPIRED", "upload has expired", false, nil)
	case domain.StateAborted:
		return domain.WrapError(http.StatusGone, "UPLOAD_ABORTED", "upload has been aborted", false, nil)
	case domain.StateFailed:
		return domain.WrapError(http.StatusGone, "UPLOAD_FAILED", "upload has failed", false, nil)
	}
	return nil
}

func stateWriteError(upload *domain.Upload) error {
	switch upload.State {
	case domain.StateExpired:
		return domain.WrapError(http.StatusGone, "UPLOAD_EXPIRED", "upload has expired", false, nil)
	case domain.StateAborted:
		return domain.WrapError(http.StatusGone, "UPLOAD_ABORTED", "upload has been aborted", false, nil)
	case domain.StateFailed:
		return domain.WrapError(http.StatusGone, "UPLOAD_FAILED", "upload has failed", false, nil)
	case domain.StateCompleted:
		return domain.WrapError(http.StatusConflict, "UPLOAD_COMPLETED", "upload has already completed", false, nil)
	default:
		return domain.WrapError(http.StatusConflict, "UPLOAD_INVALID_STATE", fmt.Sprintf("upload is in state %s", upload.State), false, nil)
	}
}

func buildArtifactKey(now time.Time, uploadID, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return fmt.Sprintf("uploads/%04d/%02d/%02d/obj_%s", now.Year(), now.Month(), now.Day(), uploadID)
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+hint), "/")
	if cleaned == "" || cleaned == "." {
		return fmt.Sprintf("uploads/%04d/%02d/%02d/obj_%s", now.Year(), now.Month(), now.Day(), uploadID)
	}
	return cleaned
}

