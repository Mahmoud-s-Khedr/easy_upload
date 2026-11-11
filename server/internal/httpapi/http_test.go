package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easy_upload/server/internal/config"
	"easy_upload/server/internal/domain"
	"easy_upload/server/internal/service"
)

func TestParseUploadMetadata(t *testing.T) {
	metadata, err := parseUploadMetadata("filename ZmlsZS50eHQ=,contentType dGV4dC9wbGFpbg==,objectKeyHint Zm9sZGVyL2ZpbGUudHh0")
	if err != nil {
		t.Fatalf("parseUploadMetadata returned error: %v", err)
	}
	if metadata["filename"] != "file.txt" {
		t.Fatalf("unexpected filename %q", metadata["filename"])
	}
	if metadata["contentType"] != "text/plain" {
		t.Fatalf("unexpected content type %q", metadata["contentType"])
	}
	if metadata["objectKeyHint"] != "folder/file.txt" {
		t.Fatalf("unexpected object key hint %q", metadata["objectKeyHint"])
	}
}

func TestParseChecksumHeader(t *testing.T) {
	checksum, err := parseChecksumHeader("sha256 Om6weQ85rIfJTzhWn33+K/MRB1VDtKlb7qvSLgt3xys=")
	if err != nil {
		t.Fatalf("parseChecksumHeader returned error: %v", err)
	}
	const want = "3a6eb0790f39ac87c94f38569f7dfe2bf311075543b4a95beeabd22e0b77c72b"
	if checksum != want {
		t.Fatalf("unexpected checksum %q", checksum)
	}
}

type fakeUploadService struct {
	upload *domain.Upload
	err    error
}

func (f fakeUploadService) CreateUpload(_ context.Context, _ service.CreateUploadInput) (*domain.Upload, error) {
	return f.upload, f.err
}

func (f fakeUploadService) GetUpload(_ context.Context, _ string, _ bool) (*domain.Upload, error) {
	return f.upload, f.err
}

func (f fakeUploadService) AppendUpload(_ context.Context, _ service.AppendUploadInput) (*domain.Upload, error) {
	return f.upload, f.err
}

func (f fakeUploadService) TerminateUpload(_ context.Context, _ string) error {
	return f.err
}

func (f fakeUploadService) BuildStatus(upload *domain.Upload) (*domain.StatusResponse, error) {
	return &domain.StatusResponse{ID: upload.ID, State: upload.State}, f.err
}

func TestAuthMissingTokenReturnsUnauthorized(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test", nil)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestAuthMalformedTokenReturnsUnauthorized(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Basic abc123")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestAuthInvalidTokenReturnsUnauthorized(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestHeadUploadReturnsNoContentForActiveUpload(t *testing.T) {
	upload := &domain.Upload{
		ID:              "upl_test",
		State:           domain.StateUploading,
		DeclaredLength:  100,
		CommittedOffset: 40,
		ExpiresAt:       time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
		Metadata:        map[string]string{"filename": "video.mp4"},
	}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodHead, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := rec.Header().Get("Upload-Offset"); got != "40" {
		t.Fatalf("unexpected Upload-Offset %q", got)
	}
	if got := rec.Header().Get("Upload-Length"); got != "100" {
		t.Fatalf("unexpected Upload-Length %q", got)
	}
}

func TestHeadUploadReturnsNoContentForCompletedUpload(t *testing.T) {
	upload := &domain.Upload{
		ID:              "upl_test",
		State:           domain.StateCompleted,
		DeclaredLength:  100,
		CommittedOffset: 100,
		ExpiresAt:       time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
	}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodHead, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := rec.Header().Get("Upload-Offset"); got != "100" {
		t.Fatalf("unexpected Upload-Offset %q", got)
	}
}

func TestHeadUploadReturnsGoneForTerminalUpload(t *testing.T) {
	upload := &domain.Upload{
		ID:    "upl_test",
		State: domain.StateFailed,
	}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodHead, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("unexpected status %d", rec.Code)
	}
}

func testConfig() config.Config {
	return config.Config{
		AuthTokens:         []string{"dev-token"},
		CORSAllowedOrigins: []string{"http://localhost:5173"},
		MaxUploadBytes:     5497558138880,
		MaxChunkBytes:      134217728,
	}
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if payload.Error.Code != code {
		t.Fatalf("unexpected error code %q", payload.Error.Code)
	}
}

func TestOptionsUploadsAllowsConfiguredOriginWithoutAuth(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodOptions, "/v1/uploads", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("unexpected Access-Control-Allow-Origin %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPatch) {
		t.Fatalf("unexpected Access-Control-Allow-Methods %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Upload-Checksum") {
		t.Fatalf("unexpected Access-Control-Allow-Headers %q", got)
	}
	if got := rec.Header().Get("Tus-Resumable"); got != domain.TusVersion {
		t.Fatalf("unexpected Tus-Resumable %q", got)
	}
}

func TestOptionsUploadByIDAllowsConfiguredOriginWithoutAuth(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodOptions, "/v1/uploads/upl_test", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("unexpected Access-Control-Allow-Origin %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Upload-Offset") {
		t.Fatalf("unexpected Access-Control-Expose-Headers %q", got)
	}
}

func TestGetUploadStillRequiresAuth(t *testing.T) {
	upload := &domain.Upload{ID: "upl_test", State: domain.StateUploading}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestPatchChecksumMismatchReturnsUnprocessableEntity(t *testing.T) {
	handler := NewHandler(fakeUploadService{
		err: domain.WrapError(http.StatusUnprocessableEntity, "CHECKSUM_MISMATCH", "chunk checksum does not match payload", false, nil),
	}, testConfig())

	req := httptest.NewRequest(http.MethodPatch, "/v1/uploads/upl_test", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("Upload-Checksum", "sha256 Om6weQ85rIfJTzhWn33+K/MRB1VDtKlb7qvSLgt3xys=")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusUnprocessableEntity, "CHECKSUM_MISMATCH")
}

// ---------------------------------------------------------------------------
// POST /v1/uploads
// ---------------------------------------------------------------------------

func TestCreateUpload_Success(t *testing.T) {
	upload := &domain.Upload{
		ID:        "upl_new",
		State:     domain.StateCreated,
		ExpiresAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Upload-Length", "1024")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/v1/uploads/upl_new" {
		t.Fatalf("unexpected Location %q", got)
	}
	if got := rec.Header().Get("Upload-Offset"); got != "0" {
		t.Fatalf("unexpected Upload-Offset %q", got)
	}
}

func TestCreateUpload_MissingTusVersion(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Upload-Length", "1024")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusPreconditionFailed, "INVALID_TUS_VERSION")
}

func TestCreateUpload_MissingUploadLength(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "INVALID_UPLOAD_LENGTH")
}

func TestCreateUpload_InvalidMetadataKey(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Upload-Length", "1024")
	req.Header.Set("Upload-Metadata", "badkey dW5rbm93bg==")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "UNSUPPORTED_METADATA")
}

// ---------------------------------------------------------------------------
// PATCH /v1/uploads/{id}
// ---------------------------------------------------------------------------

func TestPatchUpload_Success(t *testing.T) {
	upload := &domain.Upload{
		ID:              "upl_test",
		State:           domain.StateUploading,
		DeclaredLength:  100,
		CommittedOffset: 5,
		ExpiresAt:       time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodPatch, "/v1/uploads/upl_test", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := rec.Header().Get("Upload-Offset"); got != "5" {
		t.Fatalf("unexpected Upload-Offset %q (want committed offset from service)", got)
	}
}

func TestPatchUpload_WrongContentType(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodPatch, "/v1/uploads/upl_test", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Upload-Offset", "0")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE")
}

func TestPatchUpload_MissingOffset(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodPatch, "/v1/uploads/upl_test", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	// Upload-Offset header intentionally omitted
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "OFFSET_MISMATCH")
}

func TestPatchUpload_InvalidChecksumFormat(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodPatch, "/v1/uploads/upl_test", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("Upload-Checksum", "md5 abc123") // unsupported algorithm
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "CHECKSUM_MISMATCH")
}

// ---------------------------------------------------------------------------
// DELETE /v1/uploads/{id}
// ---------------------------------------------------------------------------

func TestDeleteUpload_Success(t *testing.T) {
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodDelete, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Tus-Resumable", domain.TusVersion)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /v1/uploads/{id}
// ---------------------------------------------------------------------------

func TestGetUpload_ReturnsJSON(t *testing.T) {
	upload := &domain.Upload{
		ID:    "upl_test",
		State: domain.StateUploading,
	}
	handler := NewHandler(fakeUploadService{upload: upload}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("unexpected Content-Type %q", ct)
	}
}

// ---------------------------------------------------------------------------
// Path-traversal guard
// ---------------------------------------------------------------------------

func TestUploadIDWithSlash_Returns404(t *testing.T) {
	// The handler rejects upload IDs containing a slash via the slash-guard in handleUploadByID.
	// Note: Go's ServeMux redirects (301) paths with ".." before they reach the handler,
	// so we test with a literal slash instead to exercise the guard directly.
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test/extra", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusNotFound, "UPLOAD_NOT_FOUND")
}

func TestDirtyPathIsRedirected(t *testing.T) {
	// Go's ServeMux cleans dirty paths with a 301 redirect before reaching any handler.
	// This is a security property: path traversal attempts are neutralised at the mux layer.
	handler := NewHandler(fakeUploadService{}, testConfig())

	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/upl_test/../../etc/passwd", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301 redirect for dirty path, got %d", rec.Code)
	}
}
