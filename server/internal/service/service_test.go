package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easy_upload/server/internal/config"
	"easy_upload/server/internal/domain"
	"easy_upload/server/internal/postgres"
)

// ---------------------------------------------------------------------------
// Fake storage adapter
// ---------------------------------------------------------------------------

type fakeAdapter struct {
	createErr  error
	appendErr  error
	sealErr    error
	abortErr   error
	appendSize int64
}

func (f *fakeAdapter) Create(_ context.Context, _ domain.CreateSpec) (domain.Handle, error) {
	if f.createErr != nil {
		return domain.Handle{}, f.createErr
	}
	return domain.Handle{Backend: "fake", Opaque: []byte(`{}`)}, nil
}

func (f *fakeAdapter) Append(_ context.Context, _ domain.Handle, seg domain.Segment, _ io.Reader) (domain.AppendResult, error) {
	if f.appendErr != nil {
		return domain.AppendResult{}, f.appendErr
	}
	size := f.appendSize
	if size == 0 {
		size = seg.Size
	}
	return domain.AppendResult{Size: size}, nil
}

func (f *fakeAdapter) Inspect(_ context.Context, _ domain.Handle) (domain.RemoteState, error) {
	return domain.RemoteState{}, nil
}

func (f *fakeAdapter) Seal(_ context.Context, _ domain.Handle, req domain.SealRequest) (domain.SealResult, error) {
	if f.sealErr != nil {
		return domain.SealResult{}, f.sealErr
	}
	return domain.SealResult{ArtifactKey: req.ArtifactKey}, nil
}

func (f *fakeAdapter) Abort(_ context.Context, _ domain.Handle) error { return f.abortErr }

func (f *fakeAdapter) DeleteExpired(_ context.Context, _ domain.Handle) error { return nil }

// ---------------------------------------------------------------------------
// Fake locked upload (satisfies postgres.LockedUploadTxn)
// ---------------------------------------------------------------------------

type fakeLock struct {
	upload       *domain.Upload
	persistErr   error
	abortedErr   error
	commitErr    error
	persistCalls int
}

func (l *fakeLock) Upload() *domain.Upload { return l.upload }

func (l *fakeLock) PersistAppend(_ context.Context, mutation postgres.AppendMutation) error {
	if l.persistErr != nil {
		return l.persistErr
	}
	l.persistCalls++
	l.upload.CommittedOffset = mutation.NewOffset
	l.upload.State = mutation.NewState
	l.upload.PartCount++
	l.upload.HashState = mutation.NewHashState
	l.upload.ExpiresAt = mutation.NewExpiresAt
	return nil
}

func (l *fakeLock) MarkAborted(_ context.Context, _ time.Time) error { return l.abortedErr }
func (l *fakeLock) Commit(_ context.Context) error                   { return l.commitErr }
func (l *fakeLock) Rollback(_ context.Context) error                 { return nil }

// ---------------------------------------------------------------------------
// Fake repository
// ---------------------------------------------------------------------------

type fakeRepo struct {
	uploads     map[string]*domain.Upload
	parts       map[string][]domain.UploadPart
	lockErr     error
	createErr   error
	markExpired map[string]bool
	markFailed  map[string]bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		uploads:     make(map[string]*domain.Upload),
		parts:       make(map[string][]domain.UploadPart),
		markExpired: make(map[string]bool),
		markFailed:  make(map[string]bool),
	}
}

func (r *fakeRepo) CreateUpload(_ context.Context, p postgres.CreateUploadParams) (*domain.Upload, error) {
	if r.createErr != nil {
		return nil, r.createErr
	}
	hashState, _ := newHashState()
	upload := &domain.Upload{
		ID:                   p.ID,
		State:                domain.StateCreated,
		DeclaredLength:       p.DeclaredLength,
		CommittedOffset:      0,
		Metadata:             p.Metadata,
		ChecksumAlgorithm:    p.ChecksumAlgorithm,
		ExpectedFullChecksum: p.ExpectedFullChecksum,
		HashState:            hashState,
		StorageBackend:       p.StorageBackend,
		StorageHandle:        p.StorageHandle,
		ArtifactKey:          p.ArtifactKey,
		ExpiresAt:            p.ExpiresAt,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.CreatedAt,
	}
	r.uploads[p.ID] = upload
	return upload, nil
}

func (r *fakeRepo) GetUpload(_ context.Context, id string) (*domain.Upload, error) {
	u, ok := r.uploads[id]
	if !ok {
		return nil, domain.WrapError(http.StatusNotFound, "UPLOAD_NOT_FOUND", "upload not found", false, nil)
	}
	copy := *u
	return &copy, nil
}

func (r *fakeRepo) LockUpload(_ context.Context, id string) (postgres.LockedUploadTxn, error) {
	if r.lockErr != nil {
		return nil, r.lockErr
	}
	u, ok := r.uploads[id]
	if !ok {
		return nil, domain.WrapError(http.StatusNotFound, "UPLOAD_NOT_FOUND", "upload not found", false, nil)
	}
	copy := *u
	lock := &fakeLock{upload: &copy}
	// After commit, write back to the map.
	lock.commitErr = nil
	// We mutate the map on PersistAppend; wire up a commit callback.
	_ = lock // The map update happens in PersistAppend; commit just succeeds.
	// Override PersistAppend to also update the map.
	return &repoBackedLock{fakeLock: lock, repo: r, id: id}, nil
}

// repoBackedLock wraps fakeLock so that Commit writes back to the fakeRepo.
type repoBackedLock struct {
	*fakeLock
	repo *fakeRepo
	id   string
}

func (l *repoBackedLock) Commit(_ context.Context) error {
	l.repo.uploads[l.id] = l.fakeLock.upload
	return l.fakeLock.commitErr
}

func (r *fakeRepo) ListUploadParts(_ context.Context, uploadID string) ([]domain.UploadPart, error) {
	return r.parts[uploadID], nil
}

func (r *fakeRepo) ReconcileUpload(_ context.Context, id string, remote domain.RemoteState, _ time.Time) (*domain.Upload, error) {
	u := r.uploads[id]
	u.CommittedOffset = remote.Offset
	return u, nil
}

func (r *fakeRepo) ListPendingFinalizations(_ context.Context, limit int, _ time.Time) ([]domain.Upload, error) {
	var result []domain.Upload
	for _, u := range r.uploads {
		if u.State == domain.StateFinalizing {
			result = append(result, *u)
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *fakeRepo) ListExpiredUploads(_ context.Context, limit int, now time.Time) ([]domain.Upload, error) {
	var result []domain.Upload
	for _, u := range r.uploads {
		if (u.State == domain.StateCreated || u.State == domain.StateUploading) && u.ExpiresAt.Before(now) {
			result = append(result, *u)
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *fakeRepo) MarkCompleted(_ context.Context, uploadID, artifactKey, _ string, _ time.Time) error {
	if u, ok := r.uploads[uploadID]; ok {
		u.State = domain.StateCompleted
		u.ArtifactKey = artifactKey
	}
	return nil
}

func (r *fakeRepo) MarkFailed(_ context.Context, uploadID, code, msg string, _, _ time.Time, terminal bool) error {
	r.markFailed[uploadID] = true
	if u, ok := r.uploads[uploadID]; ok {
		if terminal {
			u.State = domain.StateFailed
		}
		u.LastErrorCode = code
		u.LastErrorMessage = msg
	}
	return nil
}

func (r *fakeRepo) MarkExpired(_ context.Context, uploadID string, _ time.Time) error {
	r.markExpired[uploadID] = true
	if u, ok := r.uploads[uploadID]; ok {
		u.State = domain.StateExpired
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func testService(repo uploadRepository, adapter domain.BlobAdapter) *Service {
	return New(repo, adapter, config.Config{
		MaxUploadBytes: 100 * 1024 * 1024,
		MaxChunkBytes:  10 * 1024 * 1024,
		InactivityTTL:  24 * time.Hour,
		FinalizeLimit:  10,
		ExpireLimit:    10,
	}, slog.Default())
}

func errorCode(err error) string {
	var e *domain.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func errorStatus(err error) int {
	var e *domain.Error
	if errors.As(err, &e) {
		return e.StatusCode
	}
	return 0
}

// ---------------------------------------------------------------------------
// CreateUpload tests
// ---------------------------------------------------------------------------

func TestCreateUpload_Success(t *testing.T) {
	repo := newFakeRepo()
	svc := testService(repo, &fakeAdapter{})

	upload, err := svc.CreateUpload(context.Background(), CreateUploadInput{
		DeclaredLength: 1024,
		Metadata:       map[string]string{"filename": "test.txt"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if upload.ID == "" {
		t.Fatal("expected non-empty upload ID")
	}
	if upload.State != domain.StateCreated {
		t.Fatalf("expected state created, got %s", upload.State)
	}
	if upload.DeclaredLength != 1024 {
		t.Fatalf("expected declared length 1024, got %d", upload.DeclaredLength)
	}
}

func TestCreateUpload_ZeroLength(t *testing.T) {
	svc := testService(newFakeRepo(), &fakeAdapter{})
	_, err := svc.CreateUpload(context.Background(), CreateUploadInput{DeclaredLength: 0})
	if errorCode(err) != "INVALID_UPLOAD_LENGTH" {
		t.Fatalf("expected INVALID_UPLOAD_LENGTH, got %v", err)
	}
	if errorStatus(err) != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", errorStatus(err))
	}
}

func TestCreateUpload_ExceedsMax(t *testing.T) {
	svc := testService(newFakeRepo(), &fakeAdapter{})
	_, err := svc.CreateUpload(context.Background(), CreateUploadInput{
		DeclaredLength: 200 * 1024 * 1024, // larger than MaxUploadBytes=100MB
	})
	if errorCode(err) != "INVALID_UPLOAD_LENGTH" {
		t.Fatalf("expected INVALID_UPLOAD_LENGTH, got %v", err)
	}
	if errorStatus(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", errorStatus(err))
	}
}

func TestCreateUpload_BackendError(t *testing.T) {
	svc := testService(newFakeRepo(), &fakeAdapter{createErr: errors.New("storage down")})
	_, err := svc.CreateUpload(context.Background(), CreateUploadInput{DeclaredLength: 100})
	if errorCode(err) != "BACKEND_UNAVAILABLE" {
		t.Fatalf("expected BACKEND_UNAVAILABLE, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// AppendUpload tests
// ---------------------------------------------------------------------------

func uploadInRepo(repo *fakeRepo, length int64) *domain.Upload {
	hashState, _ := newHashState()
	upload := &domain.Upload{
		ID:             "upl_test",
		State:          domain.StateCreated,
		DeclaredLength: length,
		HashState:      hashState,
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
	repo.uploads[upload.ID] = upload
	return upload
}

func TestAppendUpload_Success(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 5)
	svc := testService(repo, &fakeAdapter{})

	payload := []byte("hello")
	upload, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID:       "upl_test",
		ExpectedOffset: 0,
		Payload:        payload,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if upload.CommittedOffset != 5 {
		t.Fatalf("expected committed offset 5, got %d", upload.CommittedOffset)
	}
	if upload.State != domain.StateFinalizing {
		t.Fatalf("expected state finalizing (final chunk), got %s", upload.State)
	}
}

func TestAppendUpload_OffsetMismatch(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 100)
	svc := testService(repo, &fakeAdapter{})

	_, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID:       "upl_test",
		ExpectedOffset: 50, // wrong: upload is at 0
		Payload:        []byte("hello"),
	})
	if errorCode(err) != "OFFSET_MISMATCH" {
		t.Fatalf("expected OFFSET_MISMATCH, got %v", err)
	}
	if errorStatus(err) != http.StatusConflict {
		t.Fatalf("expected 409, got %d", errorStatus(err))
	}
}

func TestAppendUpload_EmptyChunk(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 100)
	svc := testService(repo, &fakeAdapter{})

	_, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID: "upl_test",
		Payload:  []byte{},
	})
	if errorCode(err) != "EMPTY_CHUNK" {
		t.Fatalf("expected EMPTY_CHUNK, got %v", err)
	}
}

func TestAppendUpload_ChecksumMismatch(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 100)
	svc := testService(repo, &fakeAdapter{})

	_, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID:      "upl_test",
		Payload:       []byte("hello"),
		ChunkChecksum: "deadbeefdeadbeef",
	})
	if errorCode(err) != "CHECKSUM_MISMATCH" {
		t.Fatalf("expected CHECKSUM_MISMATCH, got %v", err)
	}
	if errorStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", errorStatus(err))
	}
}

func TestAppendUpload_ChunkExceedsDeclaredLength(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 3) // declared 3 bytes
	svc := testService(repo, &fakeAdapter{})

	_, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID: "upl_test",
		Payload:  []byte("hello"), // 5 bytes, exceeds declared 3
	})
	if errorCode(err) != "INVALID_UPLOAD_LENGTH" {
		t.Fatalf("expected INVALID_UPLOAD_LENGTH, got %v", err)
	}
}

func TestAppendUpload_IntermediateChunk_RemainsUploading(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 100) // 100 bytes total
	svc := testService(repo, &fakeAdapter{})

	upload, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID: "upl_test",
		Payload:  []byte("hello"), // 5 of 100 bytes
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if upload.State != domain.StateUploading {
		t.Fatalf("expected state uploading, got %s", upload.State)
	}
}

func TestAppendUpload_OnExpiredUpload(t *testing.T) {
	repo := newFakeRepo()
	hashState, _ := newHashState()
	repo.uploads["upl_test"] = &domain.Upload{
		ID:             "upl_test",
		State:          domain.StateCreated,
		DeclaredLength: 100,
		HashState:      hashState,
		ExpiresAt:      time.Now().Add(-1 * time.Hour), // already expired
	}
	svc := testService(repo, &fakeAdapter{})

	_, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID: "upl_test",
		Payload:  []byte("hello"),
	})
	if errorCode(err) != "UPLOAD_EXPIRED" {
		t.Fatalf("expected UPLOAD_EXPIRED, got %v", err)
	}
	if errorStatus(err) != http.StatusGone {
		t.Fatalf("expected 410, got %d", errorStatus(err))
	}
}

func TestAppendUpload_CorrectChecksumAccepted(t *testing.T) {
	repo := newFakeRepo()
	uploadInRepo(repo, 100)
	svc := testService(repo, &fakeAdapter{})

	payload := []byte("hello")
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])

	_, err := svc.AppendUpload(context.Background(), AppendUploadInput{
		UploadID:      "upl_test",
		Payload:       payload,
		ChunkChecksum: checksum,
	})
	if err != nil {
		t.Fatalf("unexpected error with correct checksum: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TerminateUpload tests
// ---------------------------------------------------------------------------

func TestTerminateUpload_Success(t *testing.T) {
	repo := newFakeRepo()
	hashState, _ := newHashState()
	repo.uploads["upl_test"] = &domain.Upload{
		ID:             "upl_test",
		State:          domain.StateUploading,
		DeclaredLength: 100,
		HashState:      hashState,
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
	svc := testService(repo, &fakeAdapter{})

	if err := svc.TerminateUpload(context.Background(), "upl_test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTerminateUpload_CompletedReturnsConflict(t *testing.T) {
	repo := newFakeRepo()
	hashState, _ := newHashState()
	repo.uploads["upl_test"] = &domain.Upload{
		ID:             "upl_test",
		State:          domain.StateCompleted,
		DeclaredLength: 100,
		HashState:      hashState,
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
	svc := testService(repo, &fakeAdapter{})

	err := svc.TerminateUpload(context.Background(), "upl_test")
	if errorCode(err) != "UPLOAD_COMPLETED" {
		t.Fatalf("expected UPLOAD_COMPLETED, got %v", err)
	}
	if errorStatus(err) != http.StatusConflict {
		t.Fatalf("expected 409, got %d", errorStatus(err))
	}
}

func TestTerminateUpload_AlreadyAbortedReturnsGone(t *testing.T) {
	repo := newFakeRepo()
	hashState, _ := newHashState()
	repo.uploads["upl_test"] = &domain.Upload{
		ID:             "upl_test",
		State:          domain.StateAborted,
		DeclaredLength: 100,
		HashState:      hashState,
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
	svc := testService(repo, &fakeAdapter{})

	// failIfExpired returns UPLOAD_ABORTED (410) before the terminal-state idempotency path.
	err := svc.TerminateUpload(context.Background(), "upl_test")
	if errorCode(err) != "UPLOAD_ABORTED" {
		t.Fatalf("expected UPLOAD_ABORTED, got %v", err)
	}
	if errorStatus(err) != http.StatusGone {
		t.Fatalf("expected 410, got %d", errorStatus(err))
	}
}

// ---------------------------------------------------------------------------
// buildArtifactKey tests
// ---------------------------------------------------------------------------

func TestBuildArtifactKey(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	uploadID := "upl_test123"

	cases := []struct {
		name     string
		hint     string
		wantHasPrefix string
		wantExact     string
	}{
		{
			name:      "no hint uses date path",
			hint:      "",
			wantHasPrefix: "uploads/2025/06/15/obj_upl_test123",
		},
		{
			name:      "simple hint used as-is",
			hint:      "my/folder/file.txt",
			wantExact: "my/folder/file.txt",
		},
		{
			name:      "traversal neutralized by path.Clean",
			hint:      "../../etc/passwd",
			wantExact: "etc/passwd",
		},
		{
			name:      "abs path stripped of leading slash",
			hint:      "/absolute/path.bin",
			wantExact: "absolute/path.bin",
		},
		{
			name:      "dotdot in middle resolved by path.Clean",
			hint:      "foo/../bar/file.txt",
			wantExact: "bar/file.txt",
		},
		{
			name:          "whitespace-only hint falls back to default",
			hint:          "   ",
			wantHasPrefix: "uploads/2025/06/15/obj_upl_test123",
		},
		{
			name:      "double-dot in filename preserved (not stripped)",
			hint:      "archive..tar.gz",
			wantExact: "archive..tar.gz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildArtifactKey(now, uploadID, tc.hint)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Fatalf("want %q, got %q", tc.wantExact, got)
			}
			if tc.wantHasPrefix != "" && !strings.HasPrefix(got, tc.wantHasPrefix) {
				t.Fatalf("want prefix %q, got %q", tc.wantHasPrefix, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// nextRetryAt exponential backoff test
// ---------------------------------------------------------------------------

func TestNextRetryAt_ExponentialBackoff(t *testing.T) {
	svc := testService(newFakeRepo(), &fakeAdapter{})

	cases := []struct {
		attempts int
		wantSec  float64
	}{
		{0, 1},
		{1, 2},
		{2, 4},
		{3, 8},
		{6, 64},
		{7, 64}, // capped at 2^6 = 64
	}
	for _, tc := range cases {
		before := svc.now()
		got := svc.nextRetryAt(tc.attempts)
		diff := got.Sub(before).Seconds()
		if diff < tc.wantSec-0.1 || diff > tc.wantSec+1.0 {
			t.Errorf("attempts=%d: expected ~%gs backoff, got %gs", tc.attempts, tc.wantSec, diff)
		}
	}
}

// ---------------------------------------------------------------------------
// Regression: frozen clock (Fix 1)
// ---------------------------------------------------------------------------

func TestFrozenClockBug_NowAdvances(t *testing.T) {
	svc := testService(newFakeRepo(), &fakeAdapter{})

	t1 := svc.now()
	time.Sleep(5 * time.Millisecond)
	t2 := svc.now()

	if !t2.After(t1) {
		t.Fatalf("clock is frozen: t1=%v t2=%v — time.Now().UTC method value bug", t1, t2)
	}
}

// ---------------------------------------------------------------------------
// Regression: MarkFailed state guard (Fix 2) — service-level smoke
// ---------------------------------------------------------------------------

func TestRunFinalizationPass_DoesNotFailCompletedUpload(t *testing.T) {
	repo := newFakeRepo()
	hashState, _ := newHashState()
	// Simulate: upload is in finalizing state but MarkCompleted races with MarkFailed.
	repo.uploads["upl_complete"] = &domain.Upload{
		ID:              "upl_complete",
		State:           domain.StateCompleted, // already completed
		DeclaredLength:  5,
		CommittedOffset: 5,
		HashState:       hashState,
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}
	svc := testService(repo, &fakeAdapter{})

	// RunFinalizationPass only picks up 'finalizing' uploads, so completed ones are ignored.
	if err := svc.RunFinalizationPass(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.markFailed["upl_complete"] {
		t.Fatal("MarkFailed was called on a completed upload")
	}
}

// ---------------------------------------------------------------------------
// Concurrency: now() is safe to call from multiple goroutines
// ---------------------------------------------------------------------------

func TestNowConcurrentSafe(t *testing.T) {
	svc := testService(newFakeRepo(), &fakeAdapter{})
	var count atomic.Int64
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					svc.now()
					count.Add(1)
				}
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	close(done)
}
