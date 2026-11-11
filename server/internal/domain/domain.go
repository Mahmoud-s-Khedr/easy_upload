package domain

import (
	"context"
	"io"
	"time"
)

const (
	TusVersion            = "1.0.0"
	DefaultChecksumAlg    = "sha256"
	MaxMultipartPartCount = 10000
	EventUploadCompleted  = "upload.completed"
	EventUploadAborted    = "upload.aborted"
	EventUploadExpired    = "upload.expired"
	EventUploadFailed     = "upload.failed"
)

type UploadState string

const (
	StateCreated    UploadState = "created"
	StateUploading  UploadState = "uploading"
	StateFinalizing UploadState = "finalizing"
	StateCompleted  UploadState = "completed"
	StateAborted    UploadState = "aborted"
	StateExpired    UploadState = "expired"
	StateFailed     UploadState = "failed"
)

func (s UploadState) Terminal() bool {
	switch s {
	case StateCompleted, StateAborted, StateExpired, StateFailed:
		return true
	default:
		return false
	}
}

type Upload struct {
	ID                   string
	State                UploadState
	DeclaredLength       int64
	CommittedOffset      int64
	Metadata             map[string]string
	ChecksumAlgorithm    string
	ExpectedFullChecksum string
	HashState            []byte
	StorageBackend       string
	StorageHandle        []byte
	ArtifactKey          string
	ExpiresAt            time.Time
	LastErrorCode        string
	LastErrorMessage     string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	CompletedAt          *time.Time
	Version              int64
	PartCount            int
	FinalizationAttempts int
	NextFinalizationAt   time.Time
}

type UploadPart struct {
	UploadID           string
	PartNumber         int
	StartOffset        int64
	EndOffsetExclusive int64
	SizeBytes          int64
	ChecksumSHA256     string
	BackendETag        string
	CreatedAt          time.Time
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type OutboxEvent struct {
	EventID        string            `json:"event_id"`
	EventType      string            `json:"event_type"`
	UploadID       string            `json:"upload_id"`
	ArtifactKey    string            `json:"artifact_key,omitempty"`
	Backend        string            `json:"backend"`
	DeclaredLength int64             `json:"declared_length"`
	ChecksumSHA256 string            `json:"checksum_sha256,omitempty"`
	Metadata       map[string]string `json:"metadata"`
	OccurredAt     time.Time         `json:"occurred_at"`
}

type CreateSpec struct {
	UploadID       string
	DeclaredLength int64
	Metadata       map[string]string
	ArtifactKey    string
}

type Handle struct {
	Backend string `json:"backend"`
	Opaque  []byte `json:"opaque,omitempty"`
}

type Segment struct {
	PartNumber     int
	StartOffset    int64
	Size           int64
	ChecksumSHA256 string
}

type AppendResult struct {
	Size        int64
	BackendETag string
}

type RemotePart struct {
	PartNumber     int
	Size           int64
	ChecksumSHA256 string
	BackendETag    string
}

type RemoteState struct {
	Offset int64
	Parts  []RemotePart
}

type SealRequest struct {
	ArtifactKey          string
	ExpectedSize         int64
	ExpectedFullChecksum string
	Metadata             map[string]string
	Parts                []UploadPart
}

type SealResult struct {
	ArtifactKey    string
	ChecksumSHA256 string
}

type BlobAdapter interface {
	Create(ctx context.Context, spec CreateSpec) (Handle, error)
	Append(ctx context.Context, handle Handle, seg Segment, r io.Reader) (AppendResult, error)
	Inspect(ctx context.Context, handle Handle) (RemoteState, error)
	Seal(ctx context.Context, handle Handle, req SealRequest) (SealResult, error)
	Abort(ctx context.Context, handle Handle) error
	DeleteExpired(ctx context.Context, handle Handle) error
}

type StatusResponse struct {
	ID              string            `json:"id"`
	State           UploadState       `json:"state"`
	DeclaredLength  int64             `json:"declared_length"`
	CommittedOffset int64             `json:"committed_offset"`
	ExpiresAt       time.Time         `json:"expires_at"`
	Metadata        map[string]string `json:"metadata"`
	Checksum        StatusChecksum    `json:"checksum"`
	Storage         StatusStorage     `json:"storage"`
	Failure         *Failure          `json:"failure"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	CompletedAt     *time.Time        `json:"completed_at"`
}

type StatusChecksum struct {
	Algorithm      string  `json:"algorithm"`
	FullFileDigest *string `json:"full_file_digest"`
}

type StatusStorage struct {
	Backend     string  `json:"backend"`
	ArtifactKey *string `json:"artifact_key"`
}
