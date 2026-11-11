package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"easy_upload/server/internal/config"
	"easy_upload/server/internal/domain"
	"easy_upload/server/internal/service"
)

var allowedMetadataKeys = map[string]struct{}{
	"filename":      {},
	"contentType":   {},
	"sha256":        {},
	"callbackUrl":   {},
	"objectKeyHint": {},
}

var corsAllowedMethods = []string{
	http.MethodOptions,
	http.MethodPost,
	http.MethodHead,
	http.MethodPatch,
	http.MethodGet,
	http.MethodDelete,
}

var corsAllowedHeaders = []string{
	"Authorization",
	"Tus-Resumable",
	"Upload-Length",
	"Upload-Metadata",
	"Upload-Offset",
	"Upload-Checksum",
	"Content-Type",
}

var corsExposedHeaders = []string{
	"Location",
	"Upload-Offset",
	"Upload-Length",
	"Upload-Expires",
	"Tus-Resumable",
	"Tus-Version",
	"Tus-Extension",
	"Tus-Max-Size",
	"Tus-Checksum-Algorithm",
	"Retry-After",
}

type Handler struct {
	service   uploadService
	cfg       config.Config
	authToken map[string]struct{}
}

type uploadService interface {
	CreateUpload(ctx context.Context, input service.CreateUploadInput) (*domain.Upload, error)
	GetUpload(ctx context.Context, uploadID string, reconcile bool) (*domain.Upload, error)
	AppendUpload(ctx context.Context, input service.AppendUploadInput) (*domain.Upload, error)
	TerminateUpload(ctx context.Context, uploadID string) error
	BuildStatus(upload *domain.Upload) (*domain.StatusResponse, error)
}

func NewHandler(svc uploadService, cfg config.Config) *Handler {
	auth := make(map[string]struct{}, len(cfg.AuthTokens))
	for _, token := range cfg.AuthTokens {
		auth[token] = struct{}{}
	}
	return &Handler{
		service:   svc,
		cfg:       cfg,
		authToken: auth,
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("OPTIONS /v1/uploads", h.handleOptionsUploads)
	mux.Handle("/v1/uploads", h.auth(http.HandlerFunc(h.handleUploads)))
	mux.Handle("/v1/uploads/", h.auth(http.HandlerFunc(h.handleUploadByID)))
	return h.withCORS(mux)
}

func (h *Handler) handleOptionsUploads(w http.ResponseWriter, _ *http.Request) {
	writeTusDiscoveryHeaders(w.Header(), h.cfg)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleUploads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleCreateUpload(w, r)
	default:
		writeError(w, domain.WrapError(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, nil))
	}
}

func (h *Handler) handleUploadByID(w http.ResponseWriter, r *http.Request) {
	uploadID := strings.TrimPrefix(r.URL.Path, "/v1/uploads/")
	if uploadID == "" || strings.Contains(uploadID, "/") {
		writeError(w, domain.WrapError(http.StatusNotFound, "UPLOAD_NOT_FOUND", "upload not found", false, nil))
		return
	}
	switch r.Method {
	case http.MethodHead:
		h.handleHeadUpload(w, r, uploadID)
	case http.MethodPatch:
		h.handlePatchUpload(w, r, uploadID)
	case http.MethodDelete:
		h.handleDeleteUpload(w, r, uploadID)
	case http.MethodGet:
		h.handleGetUpload(w, r, uploadID)
	default:
		writeError(w, domain.WrapError(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, nil))
	}
}

func (h *Handler) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	if err := requireTusVersion(r); err != nil {
		writeError(w, err)
		return
	}
	lengthHeader := strings.TrimSpace(r.Header.Get("Upload-Length"))
	if lengthHeader == "" {
		writeError(w, domain.WrapError(http.StatusBadRequest, "INVALID_UPLOAD_LENGTH", "Upload-Length header is required", false, nil))
		return
	}
	length, err := strconv.ParseInt(lengthHeader, 10, 64)
	if err != nil {
		writeError(w, domain.WrapError(http.StatusBadRequest, "INVALID_UPLOAD_LENGTH", "Upload-Length must be a valid integer", false, err))
		return
	}
	metadata, err := parseUploadMetadata(r.Header.Get("Upload-Metadata"))
	if err != nil {
		writeError(w, err)
		return
	}
	upload, err := h.service.CreateUpload(r.Context(), service.CreateUploadInput{
		DeclaredLength: length,
		Metadata:       metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeTusDiscoveryHeaders(w.Header(), h.cfg)
	w.Header().Set("Location", "/v1/uploads/"+upload.ID)
	w.Header().Set("Upload-Offset", "0")
	w.Header().Set("Upload-Expires", upload.ExpiresAt.Format(time.RFC3339))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) handleHeadUpload(w http.ResponseWriter, r *http.Request, uploadID string) {
	if err := requireTusVersion(r); err != nil {
		writeError(w, err)
		return
	}
	upload, err := h.service.GetUpload(r.Context(), uploadID, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := terminalHeadError(upload); err != nil {
		writeError(w, err)
		return
	}
	writeTusDiscoveryHeaders(w.Header(), h.cfg)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Upload-Offset", strconv.FormatInt(upload.CommittedOffset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(upload.DeclaredLength, 10))
	w.Header().Set("Upload-Expires", upload.ExpiresAt.Format(time.RFC3339))
	if metadata := encodeUploadMetadata(upload.Metadata); metadata != "" {
		w.Header().Set("Upload-Metadata", metadata)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handlePatchUpload(w http.ResponseWriter, r *http.Request, uploadID string) {
	if err := requireTusVersion(r); err != nil {
		writeError(w, err)
		return
	}
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "application/offset+octet-stream" {
		writeError(w, domain.WrapError(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/offset+octet-stream", false, nil))
		return
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Upload-Offset")), 10, 64)
	if err != nil {
		writeError(w, domain.WrapError(http.StatusBadRequest, "OFFSET_MISMATCH", "Upload-Offset must be a valid integer", false, err))
		return
	}
	checksum, err := parseChecksumHeader(r.Header.Get("Upload-Checksum"))
	if err != nil {
		writeError(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, h.cfg.MaxChunkBytes)
	defer body.Close()
	payload, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, domain.WrapError(http.StatusRequestEntityTooLarge, "CHUNK_TOO_LARGE", "chunk exceeds maximum allowed size", false, err))
			return
		}
		writeError(w, domain.WrapError(http.StatusBadRequest, "INVALID_BODY", "failed to read request body", false, err))
		return
	}
	upload, err := h.service.AppendUpload(r.Context(), service.AppendUploadInput{
		UploadID:       uploadID,
		ExpectedOffset: offset,
		Payload:        payload,
		ChunkChecksum:  checksum,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeTusDiscoveryHeaders(w.Header(), h.cfg)
	w.Header().Set("Upload-Offset", strconv.FormatInt(upload.CommittedOffset, 10))
	w.Header().Set("Upload-Expires", upload.ExpiresAt.Format(time.RFC3339))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleDeleteUpload(w http.ResponseWriter, r *http.Request, uploadID string) {
	if err := requireTusVersion(r); err != nil {
		writeError(w, err)
		return
	}
	if err := h.service.TerminateUpload(r.Context(), uploadID); err != nil {
		writeError(w, err)
		return
	}
	writeTusDiscoveryHeaders(w.Header(), h.cfg)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleGetUpload(w http.ResponseWriter, r *http.Request, uploadID string) {
	upload, err := h.service.GetUpload(r.Context(), uploadID, false)
	if err != nil {
		writeError(w, err)
		return
	}
	status, err := h.service.BuildStatus(upload)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerToken(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			writeError(w, err)
			return
		}
		if _, ok := h.authToken[token]; !ok {
			writeError(w, domain.WrapError(http.StatusUnauthorized, "UNAUTHORIZED", "invalid bearer token", false, nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) withCORS(next http.Handler) http.Handler {
	allowedOrigins := make(map[string]struct{}, len(h.cfg.CORSAllowedOrigins))
	for _, origin := range h.cfg.CORSAllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}

	allowMethods := strings.Join(corsAllowedMethods, ", ")
	allowHeaders := strings.Join(corsAllowedHeaders, ", ")
	exposeHeaders := strings.Join(corsExposedHeaders, ", ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		_, originAllowed := allowedOrigins[origin]

		if originAllowed {
			header := w.Header()
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Expose-Headers", exposeHeaders)
			header.Add("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			if !originAllowed {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			header := w.Header()
			header.Set("Access-Control-Allow-Methods", allowMethods)
			header.Set("Access-Control-Allow-Headers", allowHeaders)
			header.Add("Vary", "Access-Control-Request-Method")
			header.Add("Vary", "Access-Control-Request-Headers")
			writeTusDiscoveryHeaders(header, h.cfg)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func bearerToken(_ context.Context, header string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(header))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", domain.WrapError(http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token", false, nil)
	}
	return parts[1], nil
}

func requireTusVersion(r *http.Request) error {
	if got := strings.TrimSpace(r.Header.Get("Tus-Resumable")); got != domain.TusVersion {
		return domain.WrapError(http.StatusPreconditionFailed, "INVALID_TUS_VERSION", "Tus-Resumable must be 1.0.0", false, nil)
	}
	return nil
}

func parseUploadMetadata(header string) (map[string]string, error) {
	if strings.TrimSpace(header) == "" {
		return map[string]string{}, nil
	}
	result := make(map[string]string)
	items := strings.Split(header, ",")
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, " ", 2)
		if len(parts) != 2 {
			return nil, domain.WrapError(http.StatusBadRequest, "UNSUPPORTED_METADATA", "invalid Upload-Metadata format", false, nil)
		}
		key := parts[0]
		if _, ok := allowedMetadataKeys[key]; !ok {
			return nil, domain.WrapError(http.StatusBadRequest, "UNSUPPORTED_METADATA", fmt.Sprintf("unsupported metadata key %q", key), false, nil)
		}
		decoded, err := decodeBase64(parts[1])
		if err != nil {
			return nil, domain.WrapError(http.StatusBadRequest, "UNSUPPORTED_METADATA", fmt.Sprintf("invalid base64 value for %q", key), false, err)
		}
		result[key] = decoded
	}
	return result, nil
}

func encodeUploadMetadata(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}
	keys := []string{"filename", "contentType", "sha256", "callbackUrl", "objectKeyHint"}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		value := metadata[key]
		if value == "" {
			continue
		}
		values = append(values, key+" "+base64.StdEncoding.EncodeToString([]byte(value)))
	}
	return strings.Join(values, ",")
}

func parseChecksumHeader(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", nil
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") {
		return "", domain.WrapError(http.StatusBadRequest, "CHECKSUM_MISMATCH", "Upload-Checksum must be 'sha256 <base64digest>'", false, nil)
	}
	raw, err := decodeBase64Bytes(parts[1])
	if err != nil {
		return "", domain.WrapError(http.StatusBadRequest, "CHECKSUM_MISMATCH", "Upload-Checksum must contain valid base64 digest bytes", false, err)
	}
	return fmt.Sprintf("%x", raw), nil
}

func decodeBase64(value string) (string, error) {
	raw, err := decodeBase64Bytes(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeBase64Bytes(value string) ([]byte, error) {
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func terminalHeadError(upload *domain.Upload) error {
	switch upload.State {
	case domain.StateAborted:
		return domain.WrapError(http.StatusGone, "UPLOAD_ABORTED", "upload has been aborted", false, nil)
	case domain.StateExpired:
		return domain.WrapError(http.StatusGone, "UPLOAD_EXPIRED", "upload has expired", false, nil)
	case domain.StateFailed:
		return domain.WrapError(http.StatusGone, "UPLOAD_FAILED", "upload has failed", false, nil)
	default:
		return nil
	}
}

func writeTusDiscoveryHeaders(header http.Header, cfg config.Config) {
	header.Set("Tus-Resumable", domain.TusVersion)
	header.Set("Tus-Version", domain.TusVersion)
	header.Set("Tus-Extension", "creation,expiration,checksum,termination")
	header.Set("Tus-Max-Size", strconv.FormatInt(cfg.MaxUploadBytes, 10))
	header.Set("Tus-Checksum-Algorithm", domain.DefaultChecksumAlg)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	apiErr := domain.ErrorOrDefault(err)
	if apiErr.StatusCode != http.StatusNoContent {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(apiErr.StatusCode)
	if apiErr.StatusCode == http.StatusNoContent {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":      apiErr.Code,
			"message":   apiErr.Message,
			"retryable": apiErr.Retryable,
		},
	})
}
