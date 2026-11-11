package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"easy_upload/server/internal/domain"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Adapter struct {
	client *s3.Client
	bucket string
}

type s3Handle struct {
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	UploadID string `json:"upload_id"`
}

type S3Config struct {
	Region       string
	Endpoint     string
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
}

func NewS3Adapter(ctx context.Context, cfg S3Config) (*S3Adapter, error) {
	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	if cfg.Endpoint != "" {
		loadOptions = append(loadOptions, awsconfig.WithBaseEndpoint(cfg.Endpoint))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.UsePathStyle
	})
	return &S3Adapter{
		client: client,
		bucket: cfg.Bucket,
	}, nil
}

func (a *S3Adapter) Create(ctx context.Context, spec domain.CreateSpec) (domain.Handle, error) {
	input := &s3.CreateMultipartUploadInput{
		Bucket: &a.bucket,
		Key:    &spec.ArtifactKey,
	}
	if ct := spec.Metadata["contentType"]; ct != "" {
		input.ContentType = &ct
	}
	resp, err := a.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return domain.Handle{}, err
	}
	payload, err := json.Marshal(s3Handle{
		Bucket:   a.bucket,
		Key:      spec.ArtifactKey,
		UploadID: *resp.UploadId,
	})
	if err != nil {
		return domain.Handle{}, err
	}
	return domain.Handle{
		Backend: "s3",
		Opaque:  payload,
	}, nil
}

func (a *S3Adapter) Append(ctx context.Context, handle domain.Handle, seg domain.Segment, r io.Reader) (domain.AppendResult, error) {
	decoded, err := decodeS3Handle(handle)
	if err != nil {
		return domain.AppendResult{}, err
	}
	payload, err := io.ReadAll(r)
	if err != nil {
		return domain.AppendResult{}, err
	}
	partNumber := int32(seg.PartNumber)
	resp, err := a.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        &decoded.Bucket,
		Key:           &decoded.Key,
		UploadId:      &decoded.UploadID,
		PartNumber:    &partNumber,
		Body:          io.NopCloser(bytesReader(payload)),
		ContentLength: int64Ptr(int64(len(payload))),
	})
	if err != nil {
		return domain.AppendResult{}, err
	}
	etag := ""
	if resp.ETag != nil {
		etag = *resp.ETag
	}
	return domain.AppendResult{
		Size:        int64(len(payload)),
		BackendETag: etag,
	}, nil
}

func (a *S3Adapter) Inspect(ctx context.Context, handle domain.Handle) (domain.RemoteState, error) {
	decoded, err := decodeS3Handle(handle)
	if err != nil {
		return domain.RemoteState{}, err
	}
	var offset int64
	var parts []domain.RemotePart
	paginator := s3.NewListPartsPaginator(a.client, &s3.ListPartsInput{
		Bucket:   &decoded.Bucket,
		Key:      &decoded.Key,
		UploadId: &decoded.UploadID,
	})
	for paginator.HasMorePages() {
		resp, err := paginator.NextPage(ctx)
		if err != nil {
			return domain.RemoteState{}, err
		}
		for _, part := range resp.Parts {
			size := int64(0)
			if part.Size != nil {
				size = *part.Size
			}
			offset += size
			etag := ""
			if part.ETag != nil {
				etag = *part.ETag
			}
			partNumber := 0
			if part.PartNumber != nil {
				partNumber = int(*part.PartNumber)
			}
			parts = append(parts, domain.RemotePart{
				PartNumber:  partNumber,
				Size:        size,
				BackendETag: etag,
			})
		}
	}
	return domain.RemoteState{
		Offset: offset,
		Parts:  parts,
	}, nil
}

func (a *S3Adapter) Seal(ctx context.Context, handle domain.Handle, req domain.SealRequest) (domain.SealResult, error) {
	decoded, err := decodeS3Handle(handle)
	if err != nil {
		return domain.SealResult{}, err
	}
	parts := make([]types.CompletedPart, 0, len(req.Parts))
	for _, part := range req.Parts {
		partNumber := int32(part.PartNumber)
		etag := part.BackendETag
		parts = append(parts, types.CompletedPart{
			ETag:       &etag,
			PartNumber: &partNumber,
		})
	}
	if _, err := a.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   &decoded.Bucket,
		Key:      &decoded.Key,
		UploadId: &decoded.UploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: parts,
		},
	}); err != nil {
		return domain.SealResult{}, err
	}
	return domain.SealResult{
		ArtifactKey: req.ArtifactKey,
	}, nil
}

func (a *S3Adapter) Abort(ctx context.Context, handle domain.Handle) error {
	decoded, err := decodeS3Handle(handle)
	if err != nil {
		return err
	}
	_, err = a.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   &decoded.Bucket,
		Key:      &decoded.Key,
		UploadId: &decoded.UploadID,
	})
	return err
}

func (a *S3Adapter) DeleteExpired(ctx context.Context, handle domain.Handle) error {
	return a.Abort(ctx, handle)
}

func decodeS3Handle(handle domain.Handle) (s3Handle, error) {
	var decoded s3Handle
	if handle.Backend != "s3" {
		return decoded, fmt.Errorf("invalid s3 handle backend %q", handle.Backend)
	}
	if err := json.Unmarshal(handle.Opaque, &decoded); err != nil {
		return decoded, err
	}
	return decoded, nil
}

type readSeekCloser struct {
	*bytes.Reader
}

func (r readSeekCloser) Close() error { return nil }

func bytesReader(payload []byte) readSeekCloser {
	return readSeekCloser{Reader: bytes.NewReader(payload)}
}

func int64Ptr(v int64) *int64 { return &v }
