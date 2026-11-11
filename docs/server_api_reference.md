# Server API Reference

This is the runtime API reference for the Easy Upload server. It documents the current implementation behavior exposed by the Go service today. For broader protocol and architecture context, see [technical_spec_v1.md](./technical_spec_v1.md). For end-to-end client-side upload flow guidance, see [client_integration_guide.md](./client_integration_guide.md).

## Base Conventions

- Base path: `/v1/uploads`
- Authentication: bearer token required on upload routes except `OPTIONS /v1/uploads` and unauthenticated browser preflight `OPTIONS` requests
- `Tus-Resumable: 1.0.0` is required on `POST`, `HEAD`, `PATCH`, and `DELETE`
- Successful `HEAD` returns `204 No Content`
- Upload completion is asynchronous after the final successful `PATCH`
- CORS is controlled by `EASY_UPLOAD_CORS_ALLOWED_ORIGINS`
- For allowed origins, the server exposes `Location`, `Upload-Offset`, `Upload-Length`, `Upload-Expires`, and tus discovery headers to browsers

Not implemented yet:

- `GET /health/live`
- `GET /health/ready`
- `GET /metrics`

Those routes are described elsewhere in the broader technical spec, but they are not part of the current runtime surface.

## Common Headers

| Header | Endpoints | Required | Format | Notes |
|---|---|---|---|---|
| `Authorization` | `POST`, `HEAD`, `PATCH`, `GET`, `DELETE` | Yes | `Bearer <token>` | Missing or invalid tokens return `401 UNAUTHORIZED` |
| `Tus-Resumable` | `POST`, `HEAD`, `PATCH`, `DELETE` | Yes | `1.0.0` | Wrong or missing value returns `412 INVALID_TUS_VERSION` |
| `Upload-Length` | `POST` | Yes | integer byte count | Must be a valid positive integer and within configured limits |
| `Upload-Metadata` | `POST`; returned by `HEAD` | Optional | `key <base64>,key <base64>` | Allowed keys: `filename`, `contentType`, `sha256`, `callbackUrl`, `objectKeyHint` |
| `Upload-Offset` | `PATCH`; returned by `HEAD` and `PATCH` | Yes on `PATCH` | integer byte count | Must match the current committed offset for writes |
| `Upload-Checksum` | `PATCH` | Optional | `sha256 <base64digest>` | Invalid formatting is rejected; checksum mismatches return `422 CHECKSUM_MISMATCH` |
| `Content-Type` | `PATCH` | Yes | `application/offset+octet-stream` | Any other value returns `415 UNSUPPORTED_MEDIA_TYPE` |

## Common Response Headers

| Header | Returned By | Meaning |
|---|---|---|
| `Location` | `POST` | Relative upload URL for the newly created session |
| `Upload-Offset` | `POST`, `HEAD`, `PATCH` | Current committed byte offset |
| `Upload-Length` | `HEAD` | Declared total upload size |
| `Upload-Expires` | `POST`, `HEAD`, `PATCH` | Current inactivity expiration timestamp |
| `Upload-Metadata` | `HEAD` | Server-encoded metadata for the upload |
| `Tus-Resumable` | discovery and tus endpoints | Protocol version marker |
| `Tus-Version` | discovery and tus endpoints | Supported tus version |
| `Tus-Extension` | discovery and tus endpoints | Enabled extensions: `creation,expiration,checksum,termination` |
| `Tus-Max-Size` | discovery and tus endpoints | Max configured upload size |
| `Tus-Checksum-Algorithm` | discovery and tus endpoints | Supported algorithm: `sha256` |
| `Cache-Control` | `HEAD` | Always `no-store` on successful `HEAD` |

## Error Response Envelope

Errors are returned as JSON in this shape:

```json
{
  "error": {
    "code": "STRING_CODE",
    "message": "human readable message",
    "retryable": false
  }
}
```

- `code`: stable application error code
- `message`: human-readable explanation
- `retryable`: server hint for whether retrying may succeed

Successful `204 No Content` responses do not return a body.

Example error body:

```json
{
  "error": {
    "code": "UNAUTHORIZED",
    "message": "missing bearer token",
    "retryable": false
  }
}
```

## Endpoint Reference

### 6.1 `OPTIONS /v1/uploads`

Purpose:

- Capability discovery and browser preflight support

Request:

- No authentication required

Required headers:

- None

Optional headers:

- Browser preflight headers such as `Origin` and `Access-Control-Request-Method`

Success response:

- `204 No Content`

Important response headers:

- `Tus-Resumable: 1.0.0`
- `Tus-Version: 1.0.0`
- `Tus-Extension: creation,expiration,checksum,termination`
- `Tus-Max-Size`
- `Tus-Checksum-Algorithm: sha256`
- CORS headers for allowed origins

Common failure responses:

- None for the implemented runtime path; unsupported origins receive `204` without CORS allow headers

Behavior notes:

- This endpoint is also used to satisfy browser preflight for allowed origins
- `OPTIONS /v1/uploads/{id}` is also handled by the CORS middleware for upload-by-id preflight

Example:

```bash
curl -i -X OPTIONS http://localhost:8080/v1/uploads \
  -H 'Origin: http://localhost:5173' \
  -H 'Access-Control-Request-Method: POST'
```

### 6.2 `POST /v1/uploads`

Purpose:

- Create a resumable upload session

Request:

- `POST /v1/uploads`

Required headers:

- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`
- `Upload-Length: <total-bytes>`

Optional headers:

- `Upload-Metadata: filename <base64>,contentType <base64>,sha256 <base64>,callbackUrl <base64>,objectKeyHint <base64>`

Allowed metadata keys:

- `filename`
- `contentType`
- `sha256`
- `callbackUrl`
- `objectKeyHint`

Success response:

- `201 Created`

Important response headers:

- `Location: /v1/uploads/{id}`
- `Upload-Offset: 0`
- `Upload-Expires: <RFC3339 timestamp>`

Common failure responses:

- `400 INVALID_UPLOAD_LENGTH`
- `400 UNSUPPORTED_METADATA`
- `401 UNAUTHORIZED`
- `412 INVALID_TUS_VERSION`
- `413 INVALID_UPLOAD_LENGTH`
- `503 BACKEND_UNAVAILABLE`

Behavior notes:

- `Location` is returned as a relative path and should be resolved against the server base URL
- There is no upload session to resume until the `Location` header has been returned successfully

Example:

```bash
curl -i -X POST http://localhost:8080/v1/uploads \
  -H 'Authorization: Bearer dev-token' \
  -H 'Tus-Resumable: 1.0.0' \
  -H 'Upload-Length: 1048576' \
  -H 'Upload-Metadata: filename ZmlsZS50eHQ=,contentType dGV4dC9wbGFpbg=='
```

### 6.3 `HEAD /v1/uploads/{id}`

Purpose:

- Return the authoritative committed offset for resume

Request:

- `HEAD /v1/uploads/{id}`

Required headers:

- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`

Optional headers:

- None

Success response:

- `204 No Content`

Important response headers:

- `Upload-Offset`
- `Upload-Length`
- `Upload-Expires`
- optional `Upload-Metadata`
- `Cache-Control: no-store`

Common failure responses:

- `401 UNAUTHORIZED`
- `404 UPLOAD_NOT_FOUND`
- `412 INVALID_TUS_VERSION`
- `410 UPLOAD_ABORTED`
- `410 UPLOAD_EXPIRED`
- `410 UPLOAD_FAILED`

Behavior notes:

- Completed uploads still return `204 No Content` with `Upload-Offset == Upload-Length`
- `HEAD` is the source of truth for committed offset recovery

Example:

```bash
curl -i -X HEAD http://localhost:8080/v1/uploads/upl_01EXAMPLE \
  -H 'Authorization: Bearer dev-token' \
  -H 'Tus-Resumable: 1.0.0'
```

### 6.4 `PATCH /v1/uploads/{id}`

Purpose:

- Append the next sequential chunk

Request:

- `PATCH /v1/uploads/{id}`

Required headers:

- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`
- `Content-Type: application/offset+octet-stream`
- `Upload-Offset: <current-committed-offset>`

Optional headers:

- `Upload-Checksum: sha256 <base64digest>`

Success response:

- `204 No Content`

Important response headers:

- `Upload-Offset`
- `Upload-Expires`

Common failure responses:

- `400 OFFSET_MISMATCH` for malformed `Upload-Offset`
- `400 INVALID_BODY`
- `400 EMPTY_CHUNK`
- `400 CHECKSUM_MISMATCH` for malformed checksum header format or invalid base64 digest
- `400 INVALID_UPLOAD_LENGTH` when a chunk exceeds the declared total upload size
- `401 UNAUTHORIZED`
- `409 OFFSET_MISMATCH` when the supplied offset does not match the committed offset
- `410 UPLOAD_ABORTED`
- `410 UPLOAD_EXPIRED`
- `410 UPLOAD_FAILED`
- `412 INVALID_TUS_VERSION`
- `413 CHUNK_TOO_LARGE`
- `415 UNSUPPORTED_MEDIA_TYPE`
- `422 CHECKSUM_MISMATCH` when the checksum does not match the chunk payload
- `503 BACKEND_UNAVAILABLE`

Behavior notes:

- Chunk uploads must be strictly sequential for a single upload
- The server currently buffers the request body in memory before append so checksum validation can happen before storage mutation
- A successful final `PATCH` only means the last bytes were accepted; completion still happens asynchronously

Example:

```bash
curl -i -X PATCH http://localhost:8080/v1/uploads/upl_01EXAMPLE \
  -H 'Authorization: Bearer dev-token' \
  -H 'Tus-Resumable: 1.0.0' \
  -H 'Content-Type: application/offset+octet-stream' \
  -H 'Upload-Offset: 0' \
  -H 'Upload-Checksum: sha256 Om6weQ85rIfJTzhWn33+K/MRB1VDtKlb7qvSLgt3xys=' \
  --data-binary @chunk.bin
```

### 6.5 `GET /v1/uploads/{id}`

Purpose:

- Return JSON upload status for polling and inspection

Request:

- `GET /v1/uploads/{id}`

Required headers:

- `Authorization: Bearer <token>`

Optional headers:

- None

Success response:

- `200 OK`

Response body shape:

```json
{
  "id": "upl_01EXAMPLE",
  "state": "completed",
  "declared_length": 1048576,
  "committed_offset": 1048576,
  "expires_at": "2026-02-28T12:00:00Z",
  "metadata": {
    "filename": "file.txt",
    "contentType": "text/plain"
  },
  "checksum": {
    "algorithm": "sha256",
    "full_file_digest": "3a6eb0790f39ac87c94f38569f7dfe2bf311075543b4a95beeabd22e0b77c72b"
  },
  "storage": {
    "backend": "local",
    "artifact_key": "uploads/2026/02/28/obj_upl_01EXAMPLE"
  },
  "failure": null,
  "created_at": "2026-02-28T11:50:00Z",
  "updated_at": "2026-02-28T11:51:00Z",
  "completed_at": "2026-02-28T11:51:05Z"
}
```

Documented fields:

- `id`: upload identifier
- `state`: upload lifecycle state
- `declared_length`: total expected size in bytes
- `committed_offset`: currently committed bytes
- `expires_at`: inactivity expiration timestamp
- `metadata`: upload metadata map
- `checksum.algorithm`: checksum algorithm
- `checksum.full_file_digest`: full-file digest when available
- `storage.backend`: configured storage backend
- `storage.artifact_key`: finalized artifact key when completed
- `failure`: terminal failure code/message when present
- `created_at`: upload creation timestamp
- `updated_at`: last mutation timestamp
- `completed_at`: completion timestamp when finished

Common failure responses:

- `401 UNAUTHORIZED`
- `404 UPLOAD_NOT_FOUND`

Behavior notes:

- The current runtime does not expose a separate `checksum.verification_status` field
- Use `HEAD`, not `GET`, for offset reconciliation and resume

Example:

```bash
curl -i http://localhost:8080/v1/uploads/upl_01EXAMPLE \
  -H 'Authorization: Bearer dev-token'
```

### 6.6 `DELETE /v1/uploads/{id}`

Purpose:

- Abort an incomplete upload

Request:

- `DELETE /v1/uploads/{id}`

Required headers:

- `Authorization: Bearer <token>`
- `Tus-Resumable: 1.0.0`

Optional headers:

- None

Success response:

- `204 No Content`

Common failure responses:

- `401 UNAUTHORIZED`
- `404 UPLOAD_NOT_FOUND`
- `409 UPLOAD_COMPLETED`
- `412 INVALID_TUS_VERSION`
- `503 BACKEND_UNAVAILABLE`

Behavior notes:

- Incomplete uploads can be aborted
- Completed uploads cannot be terminated
- Already-terminal non-completed uploads may effectively no-op

Example:

```bash
curl -i -X DELETE http://localhost:8080/v1/uploads/upl_01EXAMPLE \
  -H 'Authorization: Bearer dev-token' \
  -H 'Tus-Resumable: 1.0.0'
```

## Common Error Codes

| HTTP status | Application code | Meaning | Retry guidance |
|---|---|---|---|
| `401` | `UNAUTHORIZED` | Missing or invalid bearer token | Do not retry until token issue is fixed |
| `412` | `INVALID_TUS_VERSION` | Missing or wrong `Tus-Resumable` version | Fix request and retry |
| `400` or `413` | `INVALID_UPLOAD_LENGTH` | Invalid create length or chunk exceeds declared size | Fix request before retry |
| `400` | `UNSUPPORTED_METADATA` | Invalid metadata format, key, or base64 value | Fix metadata before retry |
| `404` | `UPLOAD_NOT_FOUND` | Unknown upload ID or invalid upload path | Check ID/path before retry |
| `400` or `409` | `OFFSET_MISMATCH` | Malformed offset header or stale write offset | For `409`, call `HEAD` and resume from returned offset |
| `400` | `INVALID_BODY` | Request body could not be read | Retry only after fixing request/body conditions |
| `400` | `EMPTY_CHUNK` | Chunk payload was empty | Send a non-empty chunk |
| `413` | `CHUNK_TOO_LARGE` | Chunk exceeds configured maximum chunk size | Reduce chunk size |
| `415` | `UNSUPPORTED_MEDIA_TYPE` | Wrong `Content-Type` on `PATCH` | Fix request and retry |
| `400` or `422` | `CHECKSUM_MISMATCH` | Invalid checksum header or payload digest mismatch | Fix checksum or resend chunk |
| `410` | `UPLOAD_ABORTED` | Upload was terminated | Session is dead; create a new upload if needed |
| `410` | `UPLOAD_EXPIRED` | Upload expired due to inactivity | Session is dead; create a new upload if needed |
| `410` | `UPLOAD_FAILED` | Upload reached terminal failed state | Inspect status and create a new upload if needed |
| `409` | `UPLOAD_COMPLETED` | Completed uploads cannot be deleted | No retry; treat as terminal |
| `503` | `BACKEND_UNAVAILABLE` | Storage backend allocation or append failed | Retry with backoff; reconcile with `HEAD` if needed |
| `405` | `METHOD_NOT_ALLOWED` | Unsupported HTTP method on route | Use a supported method |
| `500` | `INTERNAL_ERROR` | Unhandled server error | Retry only if appropriate for your workflow |

## Runtime and Operations Notes

Key runtime configuration comes from [`server/.env.example`](../server/.env.example):

- `EASY_UPLOAD_ADDR`
  Default: `:8080`
- `EASY_UPLOAD_AUTH_TOKENS`
  Default example: `dev-token`
- `EASY_UPLOAD_CORS_ALLOWED_ORIGINS`
  Default example: `http://localhost:5173`
- `EASY_UPLOAD_MAX_UPLOAD_BYTES`
  Default: `5497558138880`
- `EASY_UPLOAD_MAX_CHUNK_BYTES`
  Default: `134217728`
- `EASY_UPLOAD_INACTIVITY_TTL`
  Default: `24h`

Current limitations:

- `GET /health/live`, `GET /health/ready`, and `GET /metrics` are not implemented yet
- Chunk bodies are buffered in memory before append
- The runtime provides no public download API
- The broader technical spec may describe surfaces that are not yet shipped

## Related Docs

- [technical_spec_v1.md](./technical_spec_v1.md)
- [client_integration_guide.md](./client_integration_guide.md)
- [../server/README.md](../server/README.md)
