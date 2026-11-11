# Easy Upload v1 Technical Specification

## 1. Summary

Easy Upload is an upload-only resumable file upload service built around the `tus` 1.0 protocol. It accepts large uploads over unreliable networks, persists resumable state in PostgreSQL, and stores file bytes through pluggable storage adapters.

Version 1 supports:
- Local filesystem storage
- S3-compatible multipart object storage, including Cloudflare R2

The service:
- Implements `tus` create, resume, checksum, expiration, and termination semantics
- Enforces sequential uploads from clients
- Finalizes completed uploads asynchronously
- Exposes upload status through a JSON API
- Emits completion and failure records through an outbox table

The service does not provide file download APIs in v1.

For practical client-side usage examples and integration guidance, see [client_integration_guide.md](./client_integration_guide.md).

## 2. Goals

- Allow large uploads to resume from the last committed offset after connection loss
- Preserve integrity with per-chunk checksum validation
- Keep the external API stable across local and object-storage backends
- Make crash recovery explicit and deterministic
- Provide enough operational guidance to deploy the service safely

## 3. Non-Goals

- No end-user identity model
- No public file download API
- No out-of-order upload writes
- No `Upload-Defer-Length`
- No `Upload-Concat`
- No distributed global quota coordination
- No automatic retention cleanup for completed uploads in v1

## 4. Protocol Compliance and Explicit Deviations

The service targets `tus` 1.0 compatibility for:
- Creation
- HEAD-based resume discovery
- PATCH upload continuation
- Checksum extension
- Expiration extension
- Termination extension

Explicit deviations and omissions:
- `Creation With Upload` is not supported
- `Upload-Defer-Length` is not supported
- `Upload-Concat` is not supported
- Uploads are strictly sequential even if a storage backend could support out-of-order part ingestion

Compliance note:
- the `tus` protocol allows successful `HEAD` responses to use `200 OK` or `204 No Content`
- Easy Upload standardizes on `204 No Content` for successful `HEAD` responses

Protocol headers exposed by the service:
- `Tus-Resumable: 1.0.0`
- `Tus-Version: 1.0.0`
- `Tus-Extension: creation,expiration,checksum,termination`
- `Tus-Max-Size: 5497558138880`
- `Tus-Checksum-Algorithm: sha256`

## 5. Authentication

Authentication uses opaque service bearer tokens.

Request contract:
- `Authorization: Bearer <token>`

Validation model:
- Tokens are opaque to clients
- Local and development deployments may validate against a static configured token list
- Production deployments may validate with an introspection or external authorization service
- Token issuance, rotation, and lifecycle are outside core service scope
- Missing or invalid bearer tokens return HTTP `401 Unauthorized` with application error code `UNAUTHORIZED`

Successful authentication yields:
- `service_id`

The service may use authenticated policy inputs such as:
- `max_upload_size_override`
- `max_concurrent_uploads`
- `allowed_storage_backends`

These are authorization decisions, not public protocol fields.

## 6. HTTP API

### 6.1 `OPTIONS /v1/uploads`

Purpose:
- Discover protocol capabilities

Response:
- `204 No Content`

Headers:
- `Tus-Resumable: 1.0.0`
- `Tus-Version: 1.0.0`
- `Tus-Extension: creation,expiration,checksum,termination`
- `Tus-Max-Size: 5497558138880`
- `Tus-Checksum-Algorithm: sha256`

### 6.2 `POST /v1/uploads`

Purpose:
- Create an upload session

Required headers:
- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`
- `Upload-Length: <int64>`

Optional headers:
- `Upload-Metadata: filename <base64>,contentType <base64>,sha256 <base64>,callbackUrl <base64>,objectKeyHint <base64>`

Validation:
- `Upload-Length` must be present
- `Upload-Length` must be greater than `0`
- `Upload-Length` must be less than or equal to `Tus-Max-Size`
- metadata keys outside the allowed set are rejected
- malformed base64 metadata is rejected
- the declared length must be feasible under the configured maximum chunk size and the `10,000`-part limit for multipart backends

Behavior:
1. Authenticate request
2. Apply token policy limits if present
3. Create upload row in PostgreSQL with state `created`
4. Allocate storage handle through the configured adapter
5. Set inactivity expiration
6. Return upload URL

Response:
- `201 Created`

Headers:
- `Location: /v1/uploads/{upload_id}`
- `Tus-Resumable: 1.0.0`
- `Upload-Offset: 0`
- `Upload-Expires: <RFC3339 timestamp>`

### 6.3 `HEAD /v1/uploads/{upload_id}`

Purpose:
- Return the authoritative committed offset for resume

Required headers:
- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`

Response for non-terminal or completed uploads:
- `204 No Content`

Easy Upload intentionally standardizes on `204 No Content` for successful `HEAD` responses. This is a deliberate `tus`-compliant choice, not a protocol deviation.

Headers:
- `Tus-Resumable: 1.0.0`
- `Upload-Offset: <committed_offset>`
- `Upload-Length: <declared_length>`
- `Upload-Expires: <RFC3339 timestamp>`
- `Upload-Metadata: ...`
- `Cache-Control: no-store`

State behavior:
- `created` and `uploading`: return current committed offset
- `finalizing`: return `Upload-Offset == Upload-Length`
- `completed`: return `Upload-Offset == Upload-Length`
- `aborted`, `expired`, `failed`: return `410 Gone`

### 6.4 `PATCH /v1/uploads/{upload_id}`

Purpose:
- Upload the next sequential chunk

Required headers:
- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`
- `Content-Type: application/offset+octet-stream`
- `Upload-Offset: <expected_current_offset>`

Optional headers:
- `Upload-Checksum: sha256 <base64digest>`

Rules:
- only the next contiguous bytes may be uploaded
- `Upload-Offset` must exactly match the committed offset
- writes are rejected for terminal uploads
- successful writes extend incomplete-upload TTL
- clients must not send concurrent `PATCH` requests for the same upload

Success response:
- `204 No Content`

Headers:
- `Tus-Resumable: 1.0.0`
- `Upload-Offset: <new_committed_offset>`
- `Upload-Expires: <RFC3339 timestamp>`

Failure responses:
- `409 Conflict` for offset mismatch
- `410 Gone` for `aborted`, `expired`, `failed`
- `413 Payload Too Large` for chunk-size violations
- `415 Unsupported Media Type` for invalid content type
- `422 Unprocessable Entity` for checksum mismatch
- `429 Too Many Requests` for rate limiting
- `503 Service Unavailable` for backend or dependency unavailability

Transition behavior:
- if the new offset is less than the declared length, state remains `uploading`
- if the new offset equals the declared length, state transitions to `finalizing`

### 6.5 `DELETE /v1/uploads/{upload_id}`

Purpose:
- Terminate an incomplete upload

Required headers:
- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`

Behavior:
- allowed for `created`, `uploading`, and `finalizing`
- marks upload `aborted`
- aborts adapter-backed staged storage
- writes `upload.aborted` outbox event

Response:
- `204 No Content`

### 6.6 `GET /v1/uploads/{upload_id}`

Purpose:
- Return JSON status for polling and operator inspection

Required headers:
- `Authorization: Bearer <token>`

Response:
- `200 OK`

Example:

```json
{
  "id": "upl_01J...",
  "state": "completed",
  "declared_length": 104857600,
  "committed_offset": 104857600,
  "expires_at": "2026-02-28T12:00:00Z",
  "metadata": {
    "filename": "video.mp4",
    "contentType": "video/mp4",
    "objectKeyHint": "candidate/path"
  },
  "checksum": {
    "algorithm": "sha256",
    "full_file_digest": "abc123",
    "verification_status": "verified"
  },
  "storage": {
    "backend": "local",
    "artifact_key": "uploads/2026/02/28/obj_01J..."
  },
  "failure": null,
  "owner": {
    "service_id": "svc_ingest"
  },
  "created_at": "2026-02-28T11:00:00Z",
  "updated_at": "2026-02-28T11:35:00Z",
  "completed_at": "2026-02-28T11:35:00Z"
}
```

### 6.7 `GET /health/live`

Purpose:
- Liveness endpoint for process health

Response:
- `200 OK` if the process is running

No dependency checks are performed.

### 6.8 `GET /health/ready`

Purpose:
- Readiness endpoint for traffic admission

Response:
- `200 OK` only when:
  - PostgreSQL is reachable
  - the configured storage backend passes a lightweight readiness check
  - worker loops are initialized
  - schema version is compatible
- `503 Service Unavailable` otherwise

### 6.9 `GET /metrics`

Purpose:
- Prometheus metrics endpoint

Response:
- `200 OK`
- content in Prometheus exposition format

## 7. JSON Status Model

### 7.1 Status Fields

Core fields:
- `id`
- `state`
- `declared_length`
- `committed_offset`
- `expires_at`
- `metadata`
- `checksum`
- `storage`
- `failure`
- `owner`
- `created_at`
- `updated_at`
- `completed_at`

### 7.2 State Enum

Allowed values:
- `created`
- `uploading`
- `finalizing`
- `completed`
- `aborted`
- `expired`
- `failed`

### 7.3 Checksum Model

Fields:
- `algorithm`
- `full_file_digest`
- `verification_status`

Allowed `verification_status` values:
- `verified`
- `declared`
- `not_provided`

### 7.4 Storage Model

Fields:
- `backend`
- `artifact_key`

### 7.5 Failure Model

Fields:
- `code`
- `message`

### 7.6 Owner Model

Fields:
- `service_id`

## 8. Error Model

Non-`HEAD` errors return JSON:

```json
{
  "error": {
    "code": "OFFSET_MISMATCH",
    "message": "Upload offset does not match committed offset",
    "retryable": true
  }
}
```

Required error codes:
- `UNAUTHORIZED`
- `INVALID_TUS_VERSION`
- `INVALID_UPLOAD_LENGTH`
- `UNSUPPORTED_METADATA`
- `OFFSET_MISMATCH`
- `CHECKSUM_MISMATCH`
- `UPLOAD_EXPIRED`
- `UPLOAD_ABORTED`
- `UPLOAD_FAILED`
- `CHUNK_TOO_LARGE`
- `RATE_LIMITED`
- `BACKEND_UNAVAILABLE`
- `SCHEMA_INCOMPATIBLE`

HTTP status mapping:
- `UNAUTHORIZED` -> `401 Unauthorized`
- `INVALID_TUS_VERSION` -> `412 Precondition Failed`
- `INVALID_UPLOAD_LENGTH` -> `400 Bad Request` or `413 Payload Too Large`, depending on failure mode
- `UNSUPPORTED_METADATA` -> `400 Bad Request`
- `OFFSET_MISMATCH` -> `409 Conflict`
- `CHECKSUM_MISMATCH` -> `422 Unprocessable Entity`
- `UPLOAD_EXPIRED`, `UPLOAD_ABORTED`, `UPLOAD_FAILED` -> `410 Gone`
- `CHUNK_TOO_LARGE` -> `413 Payload Too Large`
- `RATE_LIMITED` -> `429 Too Many Requests`
- `BACKEND_UNAVAILABLE` -> `503 Service Unavailable`
- `SCHEMA_INCOMPATIBLE` -> `503 Service Unavailable`

Retry guidance:
- `OFFSET_MISMATCH`: client should issue `HEAD` and retry from the returned offset
- `CHECKSUM_MISMATCH`: client may retry the same chunk after recalculating checksum
- `RATE_LIMITED`: retry after `Retry-After`
- `BACKEND_UNAVAILABLE`: retry with backoff

## 9. State Machine

### 9.1 States

- `created`: session exists, no bytes committed
- `uploading`: at least one chunk committed, upload incomplete
- `finalizing`: all bytes received, final commit in progress
- `completed`: final artifact committed and immutable
- `aborted`: terminated by client
- `expired`: abandoned and expired by sweeper
- `failed`: unrecoverable integrity or finalization failure

### 9.2 Transitions

- `POST /v1/uploads` -> `created`
- first successful `PATCH` -> `uploading`
- subsequent successful `PATCH` before completion -> `uploading`
- successful final `PATCH` -> `finalizing`
- successful finalization -> `completed`
- `DELETE` from incomplete states -> `aborted`
- sweeper expiration from incomplete states -> `expired`
- unrecoverable finalization failure -> `failed`

### 9.3 Terminal State Rules

Terminal states:
- `completed`
- `aborted`
- `expired`
- `failed`

Rules:
- `PATCH` is rejected for terminal states
- `HEAD` is allowed for `completed`
- `HEAD` returns `410 Gone` for `aborted`, `expired`, `failed`
- `GET /v1/uploads/{id}` is allowed for all states

## 10. Storage Model

## 10.1 Control Plane

PostgreSQL stores:
- upload metadata
- authoritative committed offset
- part manifest
- expiration deadlines
- finalization scheduling
- service identity
- outbox events

## 10.2 Data Plane Contract

The storage adapter contract is part-oriented, not append-oriented.

```go
type BlobAdapter interface {
    Create(ctx context.Context, spec CreateSpec) (Handle, error)
    UploadPart(ctx context.Context, handle Handle, part Part, r io.Reader) (PartResult, error)
    Inspect(ctx context.Context, handle Handle) (RemoteState, error)
    Seal(ctx context.Context, handle Handle, req SealRequest) (SealResult, error)
    Abort(ctx context.Context, handle Handle) error
    DeleteExpired(ctx context.Context, handle Handle) error
}
```

Related types:

```go
type Part struct {
    PartNumber     int
    StartOffset    int64
    Size           int64
    ChecksumSHA256 string
}

type PartResult struct {
    Size        int64
    BackendETag string
}
```

External API behavior remains sequential even if an internal backend can support non-sequential parts.

## 10.3 Write Flow

For each successful `PATCH`:
1. Load and lock the upload row
2. Validate state, expiry, offset, length, rate limits, and checksum
3. Pass the request body to `UploadPart`
4. Persist part metadata and new committed offset
5. Extend incomplete-upload expiration
6. Commit transaction
7. Return new offset

## 10.4 Crash Recovery

Recovery rule:
- if adapter part upload succeeds but the PostgreSQL transaction fails, remote staged bytes may be ahead of the database row

Recovery mechanism:
- call `Inspect`
- reconcile remote state back into PostgreSQL
- treat remote staged part state as authoritative for durable bytes

## 10.5 Local Filesystem Adapter

Staging layout:
- `staging/{upload_id}.part`
- `objects/{yyyy}/{mm}/{dd}/{object_id}`

Behavior:
- `Create` creates an empty staging file
- `UploadPart` verifies file size equals expected offset and appends bytes sequentially
- `Seal` verifies size and checksum, then promotes the file to the final object path
- `Abort` deletes staged files
- `Inspect` uses file size and manifest to reconstruct durable progress

Filesystem requirements:
- staging and final object directories should be on the same filesystem for atomic rename
- if they are not, the implementation must fall back to copy, fsync, and delete
- operators must monitor disk capacity and alert at `>= 80%` utilization

## 10.6 S3 / R2 Adapter

Mechanism:
- multipart upload

Rules:
- minimum part size is `5 MiB`
- the final part may be smaller than `5 MiB`
- maximum part count is `10,000`

Adaptive SDK chunk sizing:

```text
adaptive_chunk_size = max(5 MiB, ceil(upload_length / 10000))
```

SDKs should round that result up to the nearest MiB.

Behavior:
- `Create` initiates multipart upload
- `UploadPart` uploads exactly one sequential part
- `Inspect` lists committed multipart parts
- `Seal` completes the multipart upload
- `Abort` aborts the multipart upload

Service-side validation:
- create requests must be rejected if the declared size cannot fit within `10,000` parts given the configured maximum chunk size

## 10.7 Artifact Key Rules

- the service generates the final object key unless `objectKeyHint` is accepted
- `objectKeyHint` is advisory, not guaranteed
- default format:
  - `uploads/{yyyy}/{mm}/{dd}/{object_id}`

## 11. Checksum Model by Backend

## 11.1 Shared Rules

- `sha256` is the only checksum algorithm in v1
- per-chunk checksum validation is supported on every `PATCH`
- part checksum and backend ETag are persisted in `upload_parts`

## 11.2 Local Filesystem

Behavior:
- validate per-chunk checksum before accepting the chunk
- maintain rolling full-file SHA-256 during upload
- if a full-file digest is declared at create time, validate it during finalization
- completed uploads report the final verified SHA-256 digest

Completed status:
- `checksum.verification_status = verified`

Completion event:
- `checksum_sha256` contains the verified digest
- `checksum_verification_status = verified`

## 11.3 S3 / R2 Multipart

Behavior:
- validate per-chunk checksum before `UploadPart`
- persist part checksums and ETags
- do not claim server-side verification of the fully assembled remote object against SHA-256 in v1

If the client provides a full-file digest:
- store it as declared metadata
- expose it in status and completion events
- mark it as declared, not verified

If the client does not provide a digest:
- `checksum.full_file_digest = null`
- `checksum.verification_status = not_provided`

Completed status:
- `checksum.verification_status = declared` when a client digest exists

Completion event:
- `checksum_sha256` contains the declared client digest if present
- `checksum_verification_status = declared` or `not_provided`

## 12. PostgreSQL Schema

## 12.1 `uploads`

Columns:
- `id` text or uuid primary key
- `state`
- `declared_length`
- `committed_offset`
- `metadata_json`
- `checksum_algorithm`
- `expected_full_checksum`
- `checksum_verification_status`
- `hash_state`
- `storage_backend`
- `storage_handle`
- `artifact_key`
- `expires_at`
- `last_error_code`
- `last_error_message`
- `created_at`
- `updated_at`
- `completed_at`
- `version`
- `part_count`
- `finalization_attempts`
- `next_finalization_at`
- `service_id`

Indexes:
- `(state, next_finalization_at)`
- `(state, expires_at)`
- optional `(service_id, created_at)`

## 12.2 `upload_parts`

Columns:
- `upload_id`
- `part_number`
- `start_offset`
- `end_offset_exclusive`
- `size_bytes`
- `checksum_sha256`
- `backend_etag`
- `created_at`

Constraints:
- primary key `(upload_id, part_number)`
- sequential ordering is enforced by service logic

## 12.3 `outbox_events`

Columns:
- `event_id`
- `event_type`
- `upload_id`
- `artifact_key`
- `backend`
- `declared_length`
- `checksum_sha256`
- `checksum_verification_status`
- `metadata_json`
- `occurred_at`
- `delivered_at`
- `payload_json`

## 13. Background Workers

### 13.1 Finalization Worker

Trigger:
- uploads in `finalizing`

Responsibilities:
1. claim work from PostgreSQL
2. verify upload completeness
3. validate checksum if backend supports full verification
4. call `Seal`
5. mark upload `completed` or `failed`
6. write outbox event

Concurrency guidance:
- worker scanning should use `FOR UPDATE SKIP LOCKED`

Retry policy:
- transient failures retry with exponential backoff
- maximum attempts: `10`

### 13.2 Expiration Sweeper

Runs periodically to:
- find expired incomplete uploads
- mark them `expired`
- clean up staged storage
- emit `upload.expired`

### 13.3 Outbox Delivery

The outbox table is part of v1.

Outbox delivery to webhooks, queues, or streams is outside the core HTTP scope but may be implemented by a separate worker.

## 14. Expiration and Retention

### 14.1 Incomplete Upload TTL

Applies only to:
- `created`
- `uploading`
- `finalizing`

Defaults:
- inactivity TTL `24h`
- every successful `PATCH` extends `expires_at`

### 14.2 Completed Upload Retention

Applies only to:
- `completed`

Default v1 policy:
- no automatic deletion

Any retention policy for completed uploads must be implemented separately from the incomplete-upload sweeper.

## 15. Rate Limiting

Required controls:
- per-token concurrent upload limit
- per-token request rate limit
- optional per-IP rate limit if source IP is trusted

Responses:
- `429 Too Many Requests`
- `Retry-After` header is required

Default implementation posture:
- token-scoped fixed-window or token-bucket limiting is sufficient in v1

## 16. Configuration

| Variable | Default | Description |
|---|---:|---|
| `EASY_UPLOAD_ADDR` | `:8080` | HTTP listen address |
| `EASY_UPLOAD_DATABASE_URL` | none | PostgreSQL connection string |
| `EASY_UPLOAD_AUTH_TOKENS` | none | Static opaque tokens for local/dev |
| `EASY_UPLOAD_STORAGE_BACKEND` | `local` | Active storage backend |
| `EASY_UPLOAD_LOCAL_ROOT` | `./data` | Local storage root |
| `EASY_UPLOAD_S3_BUCKET` | none | S3 or R2 bucket name |
| `EASY_UPLOAD_S3_REGION` | `auto` | Object storage region |
| `EASY_UPLOAD_S3_ENDPOINT` | none | Optional S3-compatible endpoint |
| `EASY_UPLOAD_S3_ACCESS_KEY` | none | Object storage access key |
| `EASY_UPLOAD_S3_SECRET_KEY` | none | Object storage secret key |
| `EASY_UPLOAD_S3_USE_PATH_STYLE` | `false` | Path-style S3 access |
| `EASY_UPLOAD_MAX_UPLOAD_BYTES` | `5497558138880` | Maximum upload size, `5 TiB` |
| `EASY_UPLOAD_MAX_CHUNK_BYTES` | `134217728` | Maximum request chunk size, `128 MiB` |
| `EASY_UPLOAD_INACTIVITY_TTL` | `24h` | TTL for incomplete uploads |
| `EASY_UPLOAD_SWEEP_INTERVAL` | `15m` | Expiration sweep interval |
| `EASY_UPLOAD_FINALIZE_INTERVAL` | `5s` | Finalization polling interval |
| `EASY_UPLOAD_FINALIZE_LIMIT` | `100` | Finalization batch size |
| `EASY_UPLOAD_EXPIRE_LIMIT` | `100` | Expiration batch size |
| `EASY_UPLOAD_HTTP_SHUTDOWN_TIMEOUT` | `30s` | Graceful HTTP shutdown timeout |
| `EASY_UPLOAD_DB_MAX_CONNS` | `25` | PostgreSQL max connections |
| `EASY_UPLOAD_DB_MIN_CONNS` | `5` | PostgreSQL min connections |
| `EASY_UPLOAD_RATE_LIMIT_RPS` | `20` | Per-token requests per second |
| `EASY_UPLOAD_RATE_LIMIT_BURST` | `40` | Per-token burst limit |
| `EASY_UPLOAD_MAX_CONCURRENT_UPLOADS_PER_TOKEN` | `10` | Maximum incomplete uploads per token |

Configuration format notes:
- All environment variables use the `EASY_UPLOAD_` prefix
- Size-related environment variables are raw integer byte counts in v1
- Human-readable byte suffixes such as `5GiB`, `128MiB`, and `5GB` are not supported
- Duration values use Go duration syntax such as `24h`, `15m`, and `30s`
- Boolean values use `true` or `false`

## 17. Health, Readiness, and Metrics

### 17.1 Liveness

`GET /health/live` reports process liveness only.

### 17.2 Readiness

`GET /health/ready` must fail when:
- PostgreSQL is unavailable
- storage adapter readiness fails
- worker loops are not initialized
- schema version is incompatible
- shutdown mode has begun

### 17.3 Metrics

`GET /metrics` exposes Prometheus metrics.

Minimum metrics:
- `easy_upload_requests_total`
- `easy_upload_request_duration_seconds`
- `easy_upload_uploads_created_total`
- `easy_upload_uploads_completed_total`
- `easy_upload_uploads_failed_total`
- `easy_upload_uploads_expired_total`
- `easy_upload_bytes_uploaded_total`
- `easy_upload_finalization_duration_seconds`
- `easy_upload_storage_errors_total`
- `easy_upload_rate_limit_rejections_total`

## 18. Observability

### 18.1 Logging

Logs must be structured and include:
- `request_id`
- `upload_id`
- `service_id`
- `method`
- `path`
- `status_code`
- `duration_ms`
- `storage_backend`
- `error_code`

### 18.2 Tracing

Tracing should be OpenTelemetry-compatible.

Minimum tracing contract:
- request ID propagation
- trace correlation in logs
- tracing is optional in v1 implementation, but correlation IDs are mandatory

## 19. Graceful Shutdown

On `SIGTERM` or equivalent:
1. mark the server unready
2. stop accepting new `POST`, `PATCH`, and `DELETE` requests
3. allow in-flight requests to complete up to the configured timeout
4. stop scheduling new worker iterations
5. allow active worker iterations to finish up to timeout
6. force shutdown after timeout

Defaults:
- HTTP shutdown timeout: `30s`
- recommended orchestrator termination grace period: `60s`

Kubernetes guidance:
- `terminationGracePeriodSeconds: 60`

## 20. Migration Policy

Migration tool:
- `golang-migrate`

Rules:
- migrations are forward-only in v1
- migrations run as an external deployment step
- the service must fail readiness if schema version is incompatible

The service may start only when the schema it depends on is present and compatible.

## 21. Locking and Throughput Guidance

The service intentionally serializes mutating operations per upload.

Rules:
- one mutating request per upload is guarded by row-level locking
- `HEAD` and `GET` are read-oriented and do not need row locks in the normal path
- worker scanners should use `FOR UPDATE SKIP LOCKED`
- hot-upload contention is expected only when multiple clients race the same upload URL

Operational guidance:
- size DB pools explicitly
- monitor lock wait time and worker lag
- treat throughput limits as an upload-session contention problem, not table-lock escalation

## 22. Testing and Acceptance Criteria

### 22.1 Protocol

- create upload returns `201` and `Location`
- `HEAD` immediately after create returns offset `0`
- valid sequential `PATCH` requests advance offset
- invalid checksum returns `422`
- stale offset returns `409`
- `HEAD` on completed upload returns `204`
- `HEAD` on expired, aborted, or failed upload returns `410`

### 22.2 Storage

- local backend verifies and reports final SHA-256
- S3/R2 backend accepts final multipart chunk smaller than `5 MiB`
- create is rejected if declared size cannot fit under `10,000` parts and configured chunk limits
- part checksums and ETags persist in manifest records

### 22.3 Lifecycle

- final chunk transitions upload to `finalizing`
- finalization produces `completed` and outbox event
- incomplete uploads expire after TTL
- completed uploads are not deleted by the TTL sweeper

### 22.4 Health and Operations

- liveness returns `200` during runtime
- readiness returns `503` when DB is unavailable
- readiness flips to `503` before graceful shutdown begins refusing new work
- metrics endpoint is available

### 22.5 Rate Limits and Auth

- missing or invalid token returns `401`
- per-token concurrent upload caps are enforced
- rate-limited requests return `429` and `Retry-After`

### 22.6 Worker and Recovery

- recovery reconciles remote staged state after DB commit loss
- worker scanners use `SKIP LOCKED`
- transient finalization failures retry up to `10` times
- local backend emits `verified` checksum status
- S3/R2 backend emits `declared` or `not_provided` checksum status

## 23. Assumptions and Defaults

- Opaque bearer tokens are the v1 auth contract
- Full-file checksum verification is guaranteed only for the local filesystem backend in v1
- S3 and R2 full-file digests are treated as declared metadata unless a future offline verifier is added
- `/metrics` is the canonical metrics endpoint
- Completed uploads do not expire automatically in v1
- `golang-migrate` is the default migration tool
- This document is the authoritative v1 technical contract for implementation

## 24. Review Clarifications

- Easy Upload intentionally uses `204 No Content` for successful `HEAD` responses
- Application error codes are distinct from HTTP status codes
- Size-related configuration values are integer byte counts only in v1

## 25. Known Constraints

### Rolling hash state is not recoverable after a partial crash

The server maintains a streaming SHA-256 hash state (`hash_state`) in the database, updated on every successful chunk append within the same database transaction that records the new offset. This guarantees consistency between `committed_offset` and `hash_state` under normal operation.

**Edge case:** If a server crash occurs *after* a storage backend write (`adapter.Append`) completes but *before* the database transaction commits (recording the new offset and updated hash state), the following state can arise on restart:

- The storage backend holds more bytes than the database knows about.
- A subsequent `HEAD` request triggers the reconcile path, which advances `committed_offset` to match the remote storage offset.
- However, `hash_state` is **not** updated during reconcile — it is impossible to reconstruct the streaming hash from opaque storage bytes without re-reading all stored data.

**Consequence:** If the client provided an `Upload-Metadata: sha256 <digest>` full-file checksum, finalization will fail with `CHECKSUM_MISMATCH` because the recomputed digest (from the stale `hash_state`) will not match. The upload will be marked `failed`.

**Mitigation:**
- Clients that supply a full-file checksum should treat a `CHECKSUM_MISMATCH` failure as a signal to retry the upload from scratch (create a new session).
- Operators can reduce exposure by setting `EASY_UPLOAD_INACTIVITY_TTL` to a short value, causing stale sessions to expire before they accumulate.
- Uploads without a full-file checksum (`sha256` metadata omitted) are unaffected by this scenario; finalization proceeds and the outbox event will contain the checksum derived from whatever bytes were successfully hashed.
