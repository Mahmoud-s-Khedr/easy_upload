package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"easy_upload/server/internal/domain"
)

type LocalAdapter struct {
	root string
}

type localHandle struct {
	StagingPath  string `json:"staging_path"`
	ManifestPath string `json:"manifest_path"`
}

type localManifest struct {
	Parts []domain.RemotePart `json:"parts"`
}

func NewLocalAdapter(root string) (*LocalAdapter, error) {
	if err := os.MkdirAll(filepath.Join(root, "staging"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o755); err != nil {
		return nil, err
	}
	return &LocalAdapter{root: root}, nil
}

func (a *LocalAdapter) Create(_ context.Context, spec domain.CreateSpec) (domain.Handle, error) {
	handle := localHandle{
		StagingPath:  filepath.Join(a.root, "staging", spec.UploadID+".part"),
		ManifestPath: filepath.Join(a.root, "staging", spec.UploadID+".json"),
	}
	file, err := os.OpenFile(handle.StagingPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		return domain.Handle{}, err
	}
	if err := file.Close(); err != nil {
		return domain.Handle{}, err
	}
	if err := writeManifest(handle.ManifestPath, localManifest{}); err != nil {
		return domain.Handle{}, err
	}
	payload, err := json.Marshal(handle)
	if err != nil {
		return domain.Handle{}, err
	}
	return domain.Handle{Backend: "local", Opaque: payload}, nil
}

func (a *LocalAdapter) Append(_ context.Context, handle domain.Handle, seg domain.Segment, r io.Reader) (domain.AppendResult, error) {
	decoded, err := decodeLocalHandle(handle)
	if err != nil {
		return domain.AppendResult{}, err
	}
	file, err := os.OpenFile(decoded.StagingPath, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return domain.AppendResult{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return domain.AppendResult{}, err
	}
	if info.Size() != seg.StartOffset {
		return domain.AppendResult{}, fmt.Errorf("staging offset mismatch: have %d want %d", info.Size(), seg.StartOffset)
	}

	written, err := io.Copy(file, r)
	if err != nil {
		return domain.AppendResult{}, err
	}
	if err := file.Sync(); err != nil {
		return domain.AppendResult{}, err
	}

	manifest, err := readManifest(decoded.ManifestPath)
	if err != nil {
		return domain.AppendResult{}, err
	}
	manifest.Parts = append(manifest.Parts, domain.RemotePart{
		PartNumber:     seg.PartNumber,
		Size:           written,
		ChecksumSHA256: seg.ChecksumSHA256,
	})
	if err := writeManifest(decoded.ManifestPath, manifest); err != nil {
		return domain.AppendResult{}, err
	}

	return domain.AppendResult{Size: written}, nil
}

func (a *LocalAdapter) Inspect(_ context.Context, handle domain.Handle) (domain.RemoteState, error) {
	decoded, err := decodeLocalHandle(handle)
	if err != nil {
		return domain.RemoteState{}, err
	}
	info, err := os.Stat(decoded.StagingPath)
	if err != nil {
		return domain.RemoteState{}, err
	}
	manifest, err := readManifest(decoded.ManifestPath)
	if err != nil {
		return domain.RemoteState{}, err
	}
	return domain.RemoteState{
		Offset: info.Size(),
		Parts:  manifest.Parts,
	}, nil
}

func (a *LocalAdapter) Seal(_ context.Context, handle domain.Handle, req domain.SealRequest) (domain.SealResult, error) {
	decoded, err := decodeLocalHandle(handle)
	if err != nil {
		return domain.SealResult{}, err
	}
	info, err := os.Stat(decoded.StagingPath)
	if err != nil {
		return domain.SealResult{}, err
	}
	if info.Size() != req.ExpectedSize {
		return domain.SealResult{}, fmt.Errorf("staging size mismatch: have %d want %d", info.Size(), req.ExpectedSize)
	}

	finalPath := filepath.Join(a.root, "objects", filepath.FromSlash(req.ArtifactKey))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return domain.SealResult{}, err
	}

	checksum, err := fileSHA256(decoded.StagingPath)
	if err != nil {
		return domain.SealResult{}, err
	}
	if req.ExpectedFullChecksum != "" && checksum != req.ExpectedFullChecksum {
		return domain.SealResult{}, fmt.Errorf("full checksum mismatch")
	}
	if err := os.Rename(decoded.StagingPath, finalPath); err != nil {
		return domain.SealResult{}, err
	}
	if err := os.Remove(decoded.ManifestPath); err != nil && !os.IsNotExist(err) {
		return domain.SealResult{}, err
	}
	return domain.SealResult{
		ArtifactKey:    req.ArtifactKey,
		ChecksumSHA256: checksum,
	}, nil
}

func (a *LocalAdapter) Abort(_ context.Context, handle domain.Handle) error {
	decoded, err := decodeLocalHandle(handle)
	if err != nil {
		return err
	}
	if err := os.Remove(decoded.StagingPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(decoded.ManifestPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (a *LocalAdapter) DeleteExpired(ctx context.Context, handle domain.Handle) error {
	return a.Abort(ctx, handle)
}

func decodeLocalHandle(handle domain.Handle) (localHandle, error) {
	var decoded localHandle
	if handle.Backend != "local" {
		return decoded, fmt.Errorf("invalid local handle backend %q", handle.Backend)
	}
	if err := json.Unmarshal(handle.Opaque, &decoded); err != nil {
		return decoded, err
	}
	return decoded, nil
}

func writeManifest(path string, manifest localManifest) error {
	tmpPath := path + ".tmp"
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readManifest(path string) (localManifest, error) {
	var manifest localManifest
	payload, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	err = json.Unmarshal(payload, &manifest)
	return manifest, err
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

