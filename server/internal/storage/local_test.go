package storage

import (
	"bytes"
	"context"
	"testing"

	"easy_upload/server/internal/domain"
)

func TestLocalAdapterLifecycle(t *testing.T) {
	adapter, err := NewLocalAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalAdapter returned error: %v", err)
	}

	handle, err := adapter.Create(context.Background(), domain.CreateSpec{
		UploadID:       "upl_test",
		DeclaredLength: 11,
		ArtifactKey:    "uploads/2026/02/28/object_upl_test",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if _, err := adapter.Append(context.Background(), handle, domain.Segment{
		PartNumber:     1,
		StartOffset:    0,
		Size:           5,
		ChecksumSHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
	}, bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Append part 1 returned error: %v", err)
	}

	if _, err := adapter.Append(context.Background(), handle, domain.Segment{
		PartNumber:     2,
		StartOffset:    5,
		Size:           6,
		ChecksumSHA256: "711e9609339e92b03358e108741e9819f7ba721a1e2d85f280899b1f2a7d3d1c",
	}, bytes.NewReader([]byte(" world"))); err != nil {
		t.Fatalf("Append part 2 returned error: %v", err)
	}

	state, err := adapter.Inspect(context.Background(), handle)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if state.Offset != 11 {
		t.Fatalf("unexpected offset %d", state.Offset)
	}
	if len(state.Parts) != 2 {
		t.Fatalf("unexpected part count %d", len(state.Parts))
	}

	result, err := adapter.Seal(context.Background(), handle, domain.SealRequest{
		ArtifactKey:          "uploads/2026/02/28/object_upl_test",
		ExpectedSize:         11,
		ExpectedFullChecksum: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
	})
	if err != nil {
		t.Fatalf("Seal returned error: %v", err)
	}
	if result.ArtifactKey != "uploads/2026/02/28/object_upl_test" {
		t.Fatalf("unexpected artifact key %q", result.ArtifactKey)
	}
}
