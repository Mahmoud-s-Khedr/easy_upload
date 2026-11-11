# Server Code Walkthrough

## 1. Purpose and reading guide

This document explains how the Go server works today at the code level. It complements the runtime API reference in [server_api_reference.md](./server_api_reference.md) and the broader design contract in [technical_spec_v1.md](./technical_spec_v1.md).

Use this walkthrough when you need to answer questions like:

- Which package owns a piece of behavior?
- What happens between an HTTP request and a database write?
- How do local and S3 storage backends fit behind the same service layer?
- Which parts of the runtime are implemented now, and which parts are still rough?

Compact runtime map:

```text
client
  |
  v
internal/httpapi.Handler
  |
  v
internal/service.Service
  |            |
  |            +--> internal/storage.BlobAdapter
  |                    |- LocalAdapter
  |                    `- S3Adapter
  |
  `--> internal/postgres.Repository
           |
           +--> uploads
           +--> upload_parts
           `--> outbox_events

background goroutines
  `--> internal/worker.Worker
         |- RunFinalizer
         `- RunSweeper
```

The walkthrough is implementation-first. When the current runtime differs from the broader specification, this document describes the runtime and calls out the gap explicitly.

## 2. Startup path

The server boots from [`server/cmd/easy_upload/main.go`](../server/cmd/easy_upload/main.go).

Startup sequence:

1. `main()` builds a structured logger with `slog.New`.
2. `config.Load()` reads environment variables into `config.Config`.
3. `signal.NotifyContext` creates a root context that is canceled on `SIGINT` or `SIGTERM`.
4. `app.New(ctx, cfg, logger)` wires together the runtime.
5. A shutdown goroutine calls `application.Server.Shutdown(...)` when the root context is canceled.
6. `application.Server.ListenAndServe()` starts the HTTP server.

`app.New` in [`server/internal/app/app.go`](../server/internal/app/app.go) is the composition root:

- `postgres.Connect` creates and pings the `pgxpool` connection pool.
- `storage.NewAdapter` selects either the local adapter or the S3 adapter based on `cfg.StorageBackend`.
- `postgres.New(pool)` creates the repository wrapper.
- `service.New(repo, adapter, cfg, logger)` builds the core business logic.
- `httpapi.NewHandler(svc, cfg)` builds the HTTP layer.
- `worker.New(svc, logger)` builds the background worker wrapper.
- Two goroutines start immediately:
  - `workers.RunFinalizer(ctx, cfg.FinalizeInterval)`
  - `workers.RunSweeper(ctx, cfg.SweepInterval)`
- `http.Server` is created with `Addr: cfg.Addr` and `Handler: handler.Routes()`.

Configuration lives in [`server/internal/config/config.go`](../server/internal/config/config.go). Important runtime knobs include:

- database URL and auth tokens
- storage backend selection
- local filesystem root or S3 connection settings
- upload size and chunk size limits
- inactivity TTL
- finalization and expiration worker intervals

## 3. Package map

### `server/cmd/easy_upload`

Process entrypoint only. It loads config, creates the app, manages shutdown, and starts the server.

### `server/internal/app`

Dependency wiring. This is where repository, storage adapter, service, HTTP handler, and worker instances are assembled.

### `server/internal/config`

Environment parsing and validation. `Load()` produces the concrete runtime configuration used everywhere else.

### `server/internal/httpapi`

HTTP boundary. This package:

- registers routes
- enforces bearer-token auth
- validates tus headers and request shapes
- parses metadata and checksum headers
- reads `PATCH` request bodies
- converts service errors into the JSON error envelope

The main file is [`server/internal/httpapi/http.go`](../server/internal/httpapi/http.go).

### `server/internal/service`

Core upload logic. This layer owns:

- upload creation
- append validation
- rolling hash updates
- expiration checks
- upload termination
- status projection
- background finalization passes
- background expiration passes

This is the main control-flow package for the server.

### `server/internal/postgres`

Persistence layer on top of `pgx`. It owns:

- connection pool setup
- row locking for append and terminate paths
- upload row reads and writes
- upload part persistence
- reconciliation writes
- outbox event insertion

### `server/internal/storage`

Blob backend implementations behind the `domain.BlobAdapter` interface.

- [`local.go`](../server/internal/storage/local.go): staging file plus JSON manifest on the local filesystem
- [`s3.go`](../server/internal/storage/s3.go): S3-compatible multipart upload flow
- [`factory.go`](../server/internal/storage/factory.go): backend selection

### `server/internal/worker`

Thin ticker-based wrapper around service passes. It does not contain business logic itself; it just schedules `RunFinalizationPass` and `RunExpirationPass`.

### `server/internal/domain`

Shared types and constants:

- upload states
- blob adapter contract
- upload and part models
- status response model
- error wrapper
- ULID-based ID generation

## 4. Request lifecycle by endpoint

All routes are built in `Handler.Routes()` in [`server/internal/httpapi/http.go`](../server/internal/httpapi/http.go).

Route registration:

- `OPTIONS /v1/uploads` -> `handleOptionsUploads`
- `/v1/uploads` -> authenticated `handleUploads`
- `/v1/uploads/{id}` -> authenticated `handleUploadByID`

The `withCORS` middleware wraps the whole mux, so unauthenticated browser `OPTIONS` requests can still succeed for allowed origins.

### `OPTIONS /v1/uploads`

Code path:

- `Handler.Routes`
- `handleOptionsUploads`
- `writeTusDiscoveryHeaders`

Behavior:

- returns `204 No Content`
- adds tus discovery headers
- relies on `withCORS` to add browser CORS headers for allowed origins

No service or database call happens on this path.

### `POST /v1/uploads`

Code path:

- `handleUploads`
- `handleCreateUpload`
- `requireTusVersion`
- `parseUploadMetadata`
- `service.CreateUpload`

Inside `service.CreateUpload` in [`server/internal/service/service.go`](../server/internal/service/service.go):

1. Validate `DeclaredLength` against zero and `cfg.MaxUploadBytes`.
2. Generate a ULID-based upload ID via `domain.NewID("upl", now)`.
3. Build the final artifact key with `buildArtifactKey(...)`.
4. Initialize rolling SHA-256 state with `newHashState()`.
5. Ask the active storage adapter to allocate a backend handle with `adapter.Create(...)`.
6. Insert the initial upload row through `repo.CreateUpload(...)`.
7. If the database insert fails after backend allocation, abort the backend handle.

Result:

- storage is allocated first
- PostgreSQL becomes the source of truth only after `CreateUpload` commits
- the response returns `Location`, `Upload-Offset: 0`, and `Upload-Expires`

### `HEAD /v1/uploads/{id}`

Code path:

- `handleUploadByID`
- `handleHeadUpload`
- `service.GetUpload(..., reconcile=true)`

Inside `service.GetUpload`:

- `repo.GetUpload` loads the upload row.
- If `reconcile=true` and the upload is not terminal, `refreshFromRemote` calls `adapter.Inspect`.
- If the backend reports a higher offset than PostgreSQL, `repo.ReconcileUpload` updates `uploads.committed_offset` and rewrites `upload_parts`.

If the upload is `aborted`, `expired`, or `failed`, `terminalHeadError` converts it into `410 Gone`.

For active, finalizing, or completed uploads, the handler returns:

- `204 No Content`
- `Upload-Offset`
- `Upload-Length`
- `Upload-Expires`
- optional `Upload-Metadata`

### `PATCH /v1/uploads/{id}`

This is the hottest path in the server.

Code path:

- `handleUploadByID`
- `handlePatchUpload`
- `service.AppendUpload`

Detailed flow:

1. `handlePatchUpload` enforces `Tus-Resumable: 1.0.0`.
2. It requires `Content-Type: application/offset+octet-stream`.
3. It parses `Upload-Offset`.
4. It parses and decodes `Upload-Checksum` when present.
5. It wraps the request body with `http.MaxBytesReader`.
6. It reads the entire chunk into memory with `io.ReadAll`.
7. It calls `service.AppendUpload`.
8. `service.AppendUpload` locks the row with `repo.LockUpload(... FOR UPDATE)`.
9. The service checks expiration, terminal state, offset match, non-empty payload, chunk-size limit, and declared-length overflow.
10. The service computes the chunk SHA-256 and compares it with the optional header value.
11. The service restores the rolling file hash from `upload.HashState`, writes the new bytes into the hash, and marshals the updated hash state.
12. The service appends the bytes to the active storage backend with `adapter.Append(...)`.
13. The service persists both:
    - one `upload_parts` row
    - one `uploads` row update via `lock.PersistAppend(...)`
14. If the new offset equals `DeclaredLength`, the upload state becomes `finalizing`; otherwise it remains `uploading`.
15. The transaction commits and the handler returns the new offset and expiration time.

Important implementation note:

- The chunk is fully buffered in memory before any storage write happens. That keeps checksum validation ahead of mutation, but it also means large chunks consume memory per request.

### `DELETE /v1/uploads/{id}`

Code path:

- `handleUploadByID`
- `handleDeleteUpload`
- `service.TerminateUpload`

Inside `service.TerminateUpload`:

1. Lock the upload row with `repo.LockUpload`.
2. Check expiration and terminal-state rules.
3. Reject termination of already completed uploads.
4. Abort staged backend state with `adapter.Abort`.
5. Mark the row aborted with `lock.MarkAborted`.
6. Insert an `upload.aborted` outbox record in the same transaction.
7. Commit.

### `GET /v1/uploads/{id}`

Code path:

- `handleUploadByID`
- `handleGetUpload`
- `service.GetUpload(..., reconcile=false)`
- `service.BuildStatus`

This endpoint returns the JSON polling view. It does not reconcile from remote storage. `BuildStatus` projects the database model into the public status response and includes:

- state
- declared length
- committed offset
- metadata
- checksum algorithm and optional full digest
- backend and optional artifact key
- failure details when present

## 5. Upload state machine

States are defined in [`server/internal/domain/domain.go`](../server/internal/domain/domain.go).

| State | Meaning | Entered by | Terminal |
|---|---|---|---|
| `created` | Upload row exists but no bytes are committed yet | `service.CreateUpload` | No |
| `uploading` | At least one chunk is committed and upload is incomplete | `service.AppendUpload` or `repo.ReconcileUpload` | No |
| `finalizing` | All bytes are committed and the worker must seal backend state | `service.AppendUpload` or `repo.ReconcileUpload` | No |
| `completed` | Final artifact is sealed and completion event is written | `repo.MarkCompleted` from worker flow | Yes |
| `aborted` | Upload was explicitly terminated | `lock.MarkAborted` | Yes |
| `expired` | Inactive upload passed TTL and was cleaned up | `service.failIfExpired` or `service.RunExpirationPass` | Yes |
| `failed` | Finalization hit a terminal failure | `repo.MarkFailed(..., terminal=true)` | Yes |

Transition summary:

- request-driven:
  - `created` -> `uploading`
  - `uploading` -> `uploading`
  - `uploading` -> `finalizing`
  - `created|uploading|finalizing` -> `aborted`
  - `created|uploading` -> `expired`
- worker-driven:
  - `finalizing` -> `completed`
  - `finalizing` -> `finalizing` with retry metadata updated
  - `finalizing` -> `failed`
  - `created|uploading` -> `expired`

The code treats `completed`, `aborted`, `expired`, and `failed` as terminal via `UploadState.Terminal()`.

## 6. Persistence model

Schema lives in [`server/db/migrations/001_init.sql`](../server/db/migrations/001_init.sql).

### `uploads`

This is the main control-plane table. It stores:

- upload identity and state
- declared length and committed offset
- client metadata
- rolling hash state
- backend name and opaque backend handle
- artifact key
- TTL and timestamps
- finalization retry bookkeeping
- last error details

`uploads.committed_offset` is the server's authoritative progress value for resume.

### `upload_parts`

This table records every committed segment:

- part number
- byte range
- part size
- chunk checksum
- backend ETag when available

Why keep `upload_parts` separately when `uploads.committed_offset` already exists:

- finalization needs ordered part metadata, especially for multipart backends
- reconciliation can rebuild server-visible progress from backend-reported parts
- the service can preserve chunk-level integrity metadata independently of the aggregate offset

### `outbox_events`

This table is written as part of the same transaction that records terminal outcomes:

- `upload.completed`
- `upload.aborted`
- `upload.expired`
- `upload.failed`

The current server persists outbox rows, but no delivery worker is implemented yet in this repository.

## 7. Storage adapter model

The shared contract is `domain.BlobAdapter` in [`server/internal/domain/domain.go`](../server/internal/domain/domain.go):

- `Create`
- `Append`
- `Inspect`
- `Seal`
- `Abort`
- `DeleteExpired`

The service layer depends on this interface, so request handling does not need backend-specific branching after startup.

### Local adapter

Implementation: [`server/internal/storage/local.go`](../server/internal/storage/local.go)

Behavior:

- `Create` allocates:
  - a staging file under `root/staging/{upload_id}.part`
  - a JSON manifest under `root/staging/{upload_id}.json`
- `Append` appends bytes to the staging file, verifies the local file offset, and records part metadata in the manifest
- `Inspect` reads the file size and manifest to report remote state
- `Seal` verifies final size, computes file SHA-256, optionally checks the expected full checksum, then renames the staging file into `root/objects/{artifactKey}`
- `Abort` and `DeleteExpired` delete staging artifacts

This backend is simple and makes recovery easy to reason about because the staging file is directly inspectable on disk.

### S3 adapter

Implementation: [`server/internal/storage/s3.go`](../server/internal/storage/s3.go)

Behavior:

- `Create` starts a multipart upload and stores bucket, key, and multipart upload ID in the opaque handle
- `Append` uploads one multipart part using `UploadPart`
- `Inspect` uses `ListParts` to recover committed remote parts and total offset
- `Seal` calls `CompleteMultipartUpload` with the ordered part list from PostgreSQL
- `Abort` and `DeleteExpired` call `AbortMultipartUpload`

Important difference from local storage:

- the S3 adapter does not calculate a final SHA-256 itself during `Seal`
- the service relies on its rolling hash state to provide the final digest when the backend does not return one

## 8. Background workers

Worker scheduling lives in [`server/internal/worker/worker.go`](../server/internal/worker/worker.go). The worker package is intentionally thin; most real behavior stays in the service layer.

### Finalization worker

`RunFinalizer` wakes on `cfg.FinalizeInterval` and calls `service.RunFinalizationPass`.

`RunFinalizationPass`:

1. Queries `repo.ListPendingFinalizations`.
2. Loops through uploads whose state is `finalizing` and whose `next_finalization_at` is due.
3. Calls `finalizeUpload` for each candidate.

`finalizeUpload`:

- checks that committed bytes equal declared length
- loads ordered parts from `repo.ListUploadParts`
- reconstructs the file digest from `upload.HashState`
- validates the optional expected full checksum
- calls `adapter.Seal`
- marks completion with `repo.MarkCompleted`, or
- records failure with `repo.MarkFailed`

Retry behavior:

- non-terminal backend failures stay in `finalizing`
- `finalization_attempts` increments on each failure
- `next_finalization_at` is moved forward with exponential backoff from `nextRetryAt`
- after `maxFinalizationAttempts` the service records a terminal `failed` state

### Expiration worker

`RunSweeper` wakes on `cfg.SweepInterval` and calls `service.RunExpirationPass`.

`RunExpirationPass`:

1. Queries `repo.ListExpiredUploads` for `created` or `uploading` rows older than `expires_at`.
2. Calls `adapter.DeleteExpired` for backend cleanup.
3. Marks the row terminal with `repo.MarkExpired`.

Expiration can also happen inline on request paths through `service.failIfExpired`, which means a stale client can discover expiration without waiting for the sweeper loop.

## 9. Checksums, reconciliation, and failure handling

### Chunk checksums

The `PATCH` handler accepts `Upload-Checksum: sha256 <base64digest>`.

Flow:

- `parseChecksumHeader` decodes the base64 digest into a lowercase hex string
- `service.AppendUpload` hashes the payload with SHA-256
- if the header was provided and does not match, the service returns `422 CHECKSUM_MISMATCH`

### Rolling file hash

The service persists incremental SHA-256 state in `uploads.hash_state`.

Helpers in [`server/internal/service/hash.go`](../server/internal/service/hash.go):

- `newHashState`
- `hashFromState`
- `marshalHashState`
- `digestFromState`

This lets the service build a full-file digest without re-reading every committed byte during normal finalization.

### Reconciliation on `HEAD`

The resume path can reconcile PostgreSQL with backend state:

- `service.GetUpload(..., reconcile=true)`
- `refreshFromRemote`
- `adapter.Inspect`
- `repo.ReconcileUpload`

This is meant to recover from cases where storage advanced farther than PostgreSQL believes.

Current caveat:

- `repo.ReconcileUpload` rebuilds `committed_offset` and `upload_parts`, but it does not rebuild `hash_state`. Finalization still derives the full digest from `hash_state`, so recovered uploads can end up with stale checksum state.

### Error shaping

Most application errors are created with `domain.WrapError(...)` in [`server/internal/domain/errors.go`](../server/internal/domain/errors.go). The HTTP layer turns them into the standard JSON envelope through `writeError`.

This gives the service layer one place to define:

- HTTP status code
- stable error code
- human-readable message
- retryability hint

## 10. Known limitations and review findings

This section documents current runtime risks, not intended design.

### 1. Completion path likely conflicts with the schema

`repo.MarkCompleted` sets `last_error_code = NULL` and `last_error_message = NULL`, but the migration declares both columns as `NOT NULL`.

Relevant files:

- [`server/internal/postgres/repository.go`](../server/internal/postgres/repository.go)
- [`server/db/migrations/001_init.sql`](../server/db/migrations/001_init.sql)

Practical effect:

- the completion path is not fully trustworthy until the schema/runtime mismatch is fixed

### 2. Reconciliation does not rebuild rolling hash state

`HEAD` reconciliation can make PostgreSQL's offset catch up to the backend without updating `uploads.hash_state`.

Relevant files:

- [`server/internal/service/service.go`](../server/internal/service/service.go)
- [`server/internal/postgres/repository.go`](../server/internal/postgres/repository.go)

Practical effect:

- recovered uploads can finalize with a stale digest or fail full-checksum validation incorrectly

### 3. S3 multipart feasibility is not enforced up front

The domain layer defines `MaxMultipartPartCount = 10000`, but `service.CreateUpload` only checks `MaxUploadBytes`. It does not reject uploads that would require too many parts for the configured chunk size.

Relevant files:

- [`server/internal/domain/domain.go`](../server/internal/domain/domain.go)
- [`server/internal/service/service.go`](../server/internal/service/service.go)

Practical effect:

- a configuration can accept an upload size that is not actually sealable with the S3 backend

### 4. Finalization failure updates are weakly guarded

`repo.MarkFailed` updates by upload ID without a state guard such as `WHERE state = 'finalizing'`.

Relevant files:

- [`server/internal/postgres/repository.go`](../server/internal/postgres/repository.go)
- [`server/internal/service/service.go`](../server/internal/service/service.go)

Practical effect:

- a worker operating on a stale list of pending finalizations can overwrite a more recent terminal state during races

### Other important runtime limitations

- `PATCH` buffers the full chunk in memory before the append reaches storage
- health, readiness, metrics, and rate limiting are described in broader docs but not implemented in the current runtime
- outbox rows are persisted, but no outbox delivery worker ships in this repository yet

## 11. Where to read next

- Runtime HTTP behavior: [server_api_reference.md](./server_api_reference.md)
- Protocol and architecture spec: [technical_spec_v1.md](./technical_spec_v1.md)
- Server quickstart and runtime notes: [../server/README.md](../server/README.md)
- Repository overview: [../README.md](../README.md)
