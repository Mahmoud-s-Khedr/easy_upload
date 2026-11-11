package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"easy_upload/server/internal/domain"
	"easy_upload/server/internal/postgres"

	tc "github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// sharedRepo is the repository backed by the single test container.
// Populated by TestMain before any test runs.
var sharedRepo *postgres.Repository

func TestMain(m *testing.M) {
	// Honour the env var so CI / docker-compose.test.yml can inject a real URL.
	if url := os.Getenv("DATABASE_TEST_URL"); url != "" {
		pool, err := postgres.Connect(context.Background(), url)
		if err != nil {
			panic("connect to DATABASE_TEST_URL: " + err.Error())
		}
		defer pool.Close()
		sharedRepo = postgres.New(pool)
		os.Exit(m.Run())
	}

	ctx := context.Background()

	// Start a single container shared across all tests.
	// We apply the migration via pool.Exec to avoid init-script timing issues.
	// Use an explicit wait strategy (occurrence=2) to wait until postgres finishes
	// its cluster init and is truly ready to accept connections.
	ctr, err := pgcontainer.Run(ctx,
		"postgres:16-alpine",
		pgcontainer.WithDatabase("testdb"),
		pgcontainer.WithUsername("test"),
		pgcontainer.WithPassword("test"),
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		panic("start postgres container: " + err.Error())
	}
	defer func() { _ = ctr.Terminate(ctx) }()

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("connection string: " + err.Error())
	}

	pool, err := postgres.Connect(ctx, connStr)
	if err != nil {
		panic("connect: " + err.Error())
	}
	defer pool.Close()

	// Apply the migration.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "db", "migrations", "001_init.sql")
	migSQL, err := os.ReadFile(migrationPath)
	if err != nil {
		panic("read migration: " + err.Error())
	}
	if _, err := pool.Exec(ctx, string(migSQL)); err != nil {
		panic("apply migration: " + err.Error())
	}

	sharedRepo = postgres.New(pool)
	os.Exit(m.Run())
}

// testDB returns the shared repository.
// Each test uses unique upload IDs so they don't interfere with each other.
func testDB(t *testing.T) *postgres.Repository {
	t.Helper()
	if sharedRepo == nil {
		t.Fatal("sharedRepo is nil — TestMain did not initialise it")
	}
	return sharedRepo
}

// makeParams returns a minimal valid CreateUploadParams for a given ID and size.
func makeParams(id string, declaredLength int64, now time.Time) postgres.CreateUploadParams {
	handle, _ := json.Marshal(map[string]string{"key": id})
	return postgres.CreateUploadParams{
		ID:                id,
		DeclaredLength:    declaredLength,
		Metadata:          map[string]string{"filename": id + ".bin"},
		ChecksumAlgorithm: "sha256",
		HashState:         []byte{},
		StorageBackend:    "local",
		StorageHandle:     handle,
		ArtifactKey:       "",
		ExpiresAt:         now.Add(24 * time.Hour),
		CreatedAt:         now,
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Basic CRUD
// ──────────────────────────────────────────────────────────────────────────────

func TestCreateAndGetUpload(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	created, err := repo.CreateUpload(ctx, makeParams("upl_cag01", 1024, now))
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if created.ID != "upl_cag01" {
		t.Errorf("ID: want upl_cag01, got %s", created.ID)
	}
	if created.State != domain.StateCreated {
		t.Errorf("State: want created, got %s", created.State)
	}
	if created.CommittedOffset != 0 {
		t.Errorf("CommittedOffset: want 0, got %d", created.CommittedOffset)
	}

	got, err := repo.GetUpload(ctx, "upl_cag01")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetUpload ID mismatch: %s vs %s", got.ID, created.ID)
	}
	if got.DeclaredLength != 1024 {
		t.Errorf("DeclaredLength: want 1024, got %d", got.DeclaredLength)
	}
}

func TestGetUpload_NotFound(t *testing.T) {
	repo := testDB(t)
	_, err := repo.GetUpload(context.Background(), "upl_nonexistent")
	if err == nil {
		t.Fatal("expected error for missing upload")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Pessimistic locking
// ──────────────────────────────────────────────────────────────────────────────

func TestLockUpload_PessimisticLock(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := repo.CreateUpload(ctx, makeParams("upl_lock01", 512, now)); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	lock1, err := repo.LockUpload(ctx, "upl_lock01")
	if err != nil {
		t.Fatalf("LockUpload: %v", err)
	}

	// Second goroutine tries to acquire the same lock — should block until lock1 commits.
	var (
		wg      sync.WaitGroup
		t2start time.Time
		t2end   time.Time
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		t2start = time.Now()
		lock2, err := repo.LockUpload(ctx, "upl_lock01")
		t2end = time.Now()
		if err != nil {
			t.Errorf("goroutine LockUpload: %v", err)
			return
		}
		_ = lock2.Rollback(ctx)
	}()

	// Hold the first lock for a measurable duration.
	time.Sleep(50 * time.Millisecond)
	if err := lock1.Commit(ctx); err != nil {
		t.Fatalf("Commit lock1: %v", err)
	}

	wg.Wait()

	held := t2end.Sub(t2start)
	if held < 40*time.Millisecond {
		t.Errorf("second goroutine did not block: held duration = %v (want ≥ 40ms)", held)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// PersistAppend
// ──────────────────────────────────────────────────────────────────────────────

func TestPersistAppend_UpdatesOffsetAndParts(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := repo.CreateUpload(ctx, makeParams("upl_pa01", 1000, now)); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	lock, err := repo.LockUpload(ctx, "upl_pa01")
	if err != nil {
		t.Fatalf("LockUpload: %v", err)
	}

	mutation := postgres.AppendMutation{
		Part: domain.UploadPart{
			UploadID:           "upl_pa01",
			PartNumber:         1,
			StartOffset:        0,
			EndOffsetExclusive: 500,
			SizeBytes:          500,
			ChecksumSHA256:     "abc",
			BackendETag:        "etag1",
			CreatedAt:          now,
		},
		NewOffset:    500,
		NewState:     domain.StateUploading,
		NewExpiresAt: now.Add(24 * time.Hour),
		NewHashState: []byte("state1"),
		UpdatedAt:    now,
	}
	if err := lock.PersistAppend(ctx, mutation); err != nil {
		t.Fatalf("PersistAppend: %v", err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := repo.GetUpload(ctx, "upl_pa01")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.CommittedOffset != 500 {
		t.Errorf("CommittedOffset: want 500, got %d", got.CommittedOffset)
	}
	if got.State != domain.StateUploading {
		t.Errorf("State: want uploading, got %s", got.State)
	}
	if got.PartCount != 1 {
		t.Errorf("PartCount: want 1, got %d", got.PartCount)
	}

	parts, err := repo.ListUploadParts(ctx, "upl_pa01")
	if err != nil {
		t.Fatalf("ListUploadParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts count: want 1, got %d", len(parts))
	}
	if parts[0].SizeBytes != 500 {
		t.Errorf("part SizeBytes: want 500, got %d", parts[0].SizeBytes)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// MarkCompleted
// ──────────────────────────────────────────────────────────────────────────────

func createFinalizingUpload(t *testing.T, repo *postgres.Repository, id string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	params := makeParams(id, 100, now)
	if _, err := repo.CreateUpload(ctx, params); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	lock, err := repo.LockUpload(ctx, id)
	if err != nil {
		t.Fatalf("LockUpload: %v", err)
	}
	mutation := postgres.AppendMutation{
		Part: domain.UploadPart{
			UploadID:           id,
			PartNumber:         1,
			StartOffset:        0,
			EndOffsetExclusive: 100,
			SizeBytes:          100,
			ChecksumSHA256:     "",
			BackendETag:        "etag",
			CreatedAt:          now,
		},
		NewOffset:    100,
		NewState:     domain.StateFinalizing,
		NewExpiresAt: now.Add(24 * time.Hour),
		NewHashState: []byte{},
		UpdatedAt:    now,
	}
	if err := lock.PersistAppend(ctx, mutation); err != nil {
		_ = lock.Rollback(ctx)
		t.Fatalf("PersistAppend: %v", err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestMarkCompleted_EmitsOutboxEvent(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	createFinalizingUpload(t, repo, "upl_mc01", now)

	if err := repo.MarkCompleted(ctx, "upl_mc01", "artifacts/file.bin", "sha256:abc", now); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	got, err := repo.GetUpload(ctx, "upl_mc01")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.State != domain.StateCompleted {
		t.Errorf("State: want completed, got %s", got.State)
	}
	if got.ArtifactKey != "artifacts/file.bin" {
		t.Errorf("ArtifactKey: want artifacts/file.bin, got %s", got.ArtifactKey)
	}

	// Verify outbox event was inserted.
	var count int
	row := repo.Pool().QueryRow(ctx,
		"SELECT COUNT(*) FROM outbox_events WHERE upload_id = $1 AND event_type = $2",
		"upl_mc01", domain.EventUploadCompleted)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if count != 1 {
		t.Errorf("outbox event count: want 1, got %d", count)
	}
}

func TestMarkCompleted_IdempotentIfNotFinalizing(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := repo.CreateUpload(ctx, makeParams("upl_mc02", 100, now)); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// Upload is in 'created' state — MarkCompleted should be a no-op.
	if err := repo.MarkCompleted(ctx, "upl_mc02", "artifacts/file.bin", "abc", now); err != nil {
		t.Errorf("MarkCompleted on non-finalizing upload should not error: %v", err)
	}

	got, err := repo.GetUpload(ctx, "upl_mc02")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.State != domain.StateCreated {
		t.Errorf("State should remain created, got %s", got.State)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// MarkFailed — regression for Fix 2: state guard
// ──────────────────────────────────────────────────────────────────────────────

func TestMarkFailed_DoesNotOverwriteCompleted(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	createFinalizingUpload(t, repo, "upl_mf01", now)

	// Complete it first.
	if err := repo.MarkCompleted(ctx, "upl_mf01", "artifacts/f.bin", "sha256:abc", now); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	// Now attempt to mark it failed — must be silent (no error, no state change).
	if err := repo.MarkFailed(ctx, "upl_mf01", "SOME_ERR", "some error", now, now.Add(time.Minute), false); err != nil {
		t.Errorf("MarkFailed on completed upload should not error: %v", err)
	}

	got, err := repo.GetUpload(ctx, "upl_mf01")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.State != domain.StateCompleted {
		t.Errorf("State should remain completed after MarkFailed, got %s", got.State)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// MarkExpired
// ──────────────────────────────────────────────────────────────────────────────

func TestMarkExpired_SilentIfAlreadyTerminal(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	createFinalizingUpload(t, repo, "upl_me01", now)
	if err := repo.MarkCompleted(ctx, "upl_me01", "artifacts/f.bin", "sha256:abc", now); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	// MarkExpired on already-completed upload must be a no-op.
	if err := repo.MarkExpired(ctx, "upl_me01", now); err != nil {
		t.Errorf("MarkExpired on terminal upload should not error: %v", err)
	}

	got, err := repo.GetUpload(ctx, "upl_me01")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if got.State != domain.StateCompleted {
		t.Errorf("State should remain completed, got %s", got.State)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ReconcileUpload
// ──────────────────────────────────────────────────────────────────────────────

func TestReconcileUpload_AdvancesOffset(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := repo.CreateUpload(ctx, makeParams("upl_rec01", 1000, now)); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	remote := domain.RemoteState{
		Offset: 600,
		Parts: []domain.RemotePart{
			{PartNumber: 1, Size: 300, ChecksumSHA256: "", BackendETag: "e1"},
			{PartNumber: 2, Size: 300, ChecksumSHA256: "", BackendETag: "e2"},
		},
	}
	reconciled, err := repo.ReconcileUpload(ctx, "upl_rec01", remote, now)
	if err != nil {
		t.Fatalf("ReconcileUpload: %v", err)
	}
	if reconciled.CommittedOffset != 600 {
		t.Errorf("CommittedOffset: want 600, got %d", reconciled.CommittedOffset)
	}
	if reconciled.State != domain.StateUploading {
		t.Errorf("State: want uploading, got %s", reconciled.State)
	}
	if reconciled.PartCount != 2 {
		t.Errorf("PartCount: want 2, got %d", reconciled.PartCount)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ListPendingFinalizations
// ──────────────────────────────────────────────────────────────────────────────

func TestListPendingFinalizations_RespectsDueTime(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create two uploads and advance both to 'finalizing'.
	for _, id := range []string{"upl_lpf01", "upl_lpf02"} {
		createFinalizingUpload(t, repo, id, now)
	}

	// Due in 1 hour — should NOT appear.
	createFinalizingUpload(t, repo, "upl_lpf03", now)
	// Manually push next_finalization_at into the future for upl_lpf03.
	if _, err := repo.Pool().Exec(ctx,
		"UPDATE uploads SET next_finalization_at = $1 WHERE id = 'upl_lpf03'",
		now.Add(time.Hour)); err != nil {
		t.Fatalf("update next_finalization_at: %v", err)
	}

	due, err := repo.ListPendingFinalizations(ctx, 10, now)
	if err != nil {
		t.Fatalf("ListPendingFinalizations: %v", err)
	}
	for _, u := range due {
		if u.ID == "upl_lpf03" {
			t.Errorf("upl_lpf03 should not be listed (due in future)")
		}
	}
	found01, found02 := false, false
	for _, u := range due {
		if u.ID == "upl_lpf01" {
			found01 = true
		}
		if u.ID == "upl_lpf02" {
			found02 = true
		}
	}
	if !found01 || !found02 {
		t.Errorf("expected upl_lpf01 and upl_lpf02 in results; got %v", uploadIDs(due))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ListExpiredUploads
// ──────────────────────────────────────────────────────────────────────────────

func TestListExpiredUploads(t *testing.T) {
	repo := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create upload that expires in the past.
	params := makeParams("upl_leu01", 100, now)
	params.ExpiresAt = now.Add(-time.Minute) // already expired
	if _, err := repo.CreateUpload(ctx, params); err != nil {
		t.Fatalf("CreateUpload expired: %v", err)
	}

	// Create upload that expires in the future — should not appear.
	if _, err := repo.CreateUpload(ctx, makeParams("upl_leu02", 100, now)); err != nil {
		t.Fatalf("CreateUpload active: %v", err)
	}

	expired, err := repo.ListExpiredUploads(ctx, 10, now)
	if err != nil {
		t.Fatalf("ListExpiredUploads: %v", err)
	}

	found := false
	for _, u := range expired {
		if u.ID == "upl_leu01" {
			found = true
		}
		if u.ID == "upl_leu02" {
			t.Errorf("upl_leu02 should not be expired")
		}
	}
	if !found {
		t.Errorf("upl_leu01 should appear as expired; got %v", uploadIDs(expired))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func uploadIDs(us []domain.Upload) []string {
	ids := make([]string, len(us))
	for i, u := range us {
		ids[i] = u.ID
	}
	return ids
}
