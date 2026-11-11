# Easy Upload Client Integration Guide

This guide is the practical companion to [technical_spec_v1.md](./technical_spec_v1.md). It explains how to integrate with Easy Upload from browser, mobile, or backend clients without requiring you to read the full technical specification first.

For exact request and response details for the current server runtime, see [server_api_reference.md](./server_api_reference.md).

The protocol steps are the same for every client type. The concrete code example in this guide uses browser JavaScript, but the upload lifecycle, error handling, resume strategy, and status polling all apply equally to mobile apps and server-side workers.

## What Your Client Must Implement

Before writing code, assume your client is responsible for:

- Supplying a valid bearer token
- Knowing the total upload size before creation
- Splitting the upload into sequential chunks
- Persisting the upload URL so resume is possible after restart
- Calling `HEAD` before resume or after any ambiguous network failure
- Computing a SHA-256 checksum per chunk
- Sending `PATCH` requests sequentially only
- Polling `GET /v1/uploads/{id}` after the last chunk until the upload reaches a terminal state

## Protocol at a Glance

The normal upload flow is:

1. Optionally call `OPTIONS /v1/uploads` to inspect server capabilities.
2. Call `POST /v1/uploads` to create an upload session.
3. Store the returned upload URL immediately.
4. Upload chunks with sequential `PATCH` requests.
5. If the result of a `PATCH` is uncertain, call `HEAD` and resume from the returned offset.
6. After the last chunk, poll `GET /v1/uploads/{id}` until the upload becomes `completed` or another terminal state.
7. Optionally call `DELETE /v1/uploads/{id}` to abort an incomplete upload.

Important rules:

- Successful `HEAD` returns `204 No Content`
- `HEAD` is the authoritative source of the committed offset
- Upload completion is asynchronous after the final `PATCH`

## Authentication

All upload endpoints require:

```http
Authorization: Bearer <token>
```

Auth is required on:

- `POST /v1/uploads`
- `HEAD /v1/uploads/{id}`
- `PATCH /v1/uploads/{id}`
- `GET /v1/uploads/{id}`
- `DELETE /v1/uploads/{id}`

If the token is missing or invalid, the server returns:

- HTTP `401 Unauthorized`
- application error code `UNAUTHORIZED`

Token acquisition is outside the scope of this guide.

## Runtime Notes

- The repository now ships a browser demo client in [`client/`](../client) that exercises the documented upload flow.
- Browser access requires configured CORS. Local development uses `EASY_UPLOAD_CORS_ALLOWED_ORIGINS=http://localhost:5173`.
- The current `GET /v1/uploads/{id}` runtime does not expose `checksum.verification_status`, even though older examples in this guide mention it.

## Endpoint-by-Endpoint Integration

### `POST /v1/uploads`

Use this to create a resumable upload session.

Required headers:

```http
Authorization: Bearer <token>
Tus-Resumable: 1.0.0
Upload-Length: <total-bytes>
```

Optional header:

```http
Upload-Metadata: filename <base64>,contentType <base64>,sha256 <base64>,callbackUrl <base64>,objectKeyHint <base64>
```

What the client should do:

- Send the total upload size in bytes
- Optionally include metadata
- Persist the `Location` header immediately

Important response headers:

- `Location`
- `Upload-Offset: 0`
- `Upload-Expires`

Common failure responses:

- `400 INVALID_UPLOAD_LENGTH`
- `400 UNSUPPORTED_METADATA`
- `401 UNAUTHORIZED`
- `413 INVALID_UPLOAD_LENGTH`
- `503 BACKEND_UNAVAILABLE`

Client reaction:

- Do not start uploading until `Location` has been stored
- If create fails, there is no resumable session yet; retry create only after fixing the cause

### `HEAD /v1/uploads/{id}`

Use this to discover the committed offset before resume or after uncertain failures.

Required headers:

```http
Authorization: Bearer <token>
Tus-Resumable: 1.0.0
```

Successful response:

- HTTP `204 No Content`

Important response headers:

- `Upload-Offset`
- `Upload-Length`
- `Upload-Expires`
- `Upload-Metadata`

Common failure responses:

- `401 UNAUTHORIZED`
- `410 UPLOAD_EXPIRED`
- `410 UPLOAD_ABORTED`
- `410 UPLOAD_FAILED`

Client reaction:

- Treat `Upload-Offset` as authoritative
- Ignore any locally cached offset if it differs
- Use `HEAD` before resuming after app restart, tab reload, timeout, or disconnection

### `PATCH /v1/uploads/{id}`

Use this to upload the next sequential chunk.

Required headers:

```http
Authorization: Bearer <token>
Tus-Resumable: 1.0.0
Content-Type: application/offset+octet-stream
Upload-Offset: <current-committed-offset>
```

Optional header:

```http
Upload-Checksum: sha256 <base64digest>
```

Success response:

- HTTP `204 No Content`

Important response headers:

- `Upload-Offset`
- `Upload-Expires`

Common failure responses:

- `401 UNAUTHORIZED`
- `409 OFFSET_MISMATCH`
- `410 UPLOAD_EXPIRED`
- `410 UPLOAD_ABORTED`
- `410 UPLOAD_FAILED`
- `413 CHUNK_TOO_LARGE`
- `415 UNSUPPORTED_MEDIA_TYPE`
- `422 CHECKSUM_MISMATCH`
- `429 RATE_LIMITED`
- `503 BACKEND_UNAVAILABLE`

Client reaction:

- Send chunks strictly in order
- Never pipeline or parallelize `PATCH` for a single upload
- If a `PATCH` result is ambiguous, call `HEAD` before retrying

### `GET /v1/uploads/{id}`

Use this to poll upload status after the final chunk or to inspect terminal failures.

Required headers:

```http
Authorization: Bearer <token>
```

Success response:

- HTTP `200 OK`
- JSON body

Important response fields:

- `state`
- `committed_offset`
- `storage.artifact_key`
- `failure`

Client reaction:

- Use this endpoint for completion tracking
- Do not use it for offset recovery; use `HEAD` instead

### `DELETE /v1/uploads/{id}`

Use this to intentionally abandon an incomplete upload.

Required headers:

```http
Authorization: Bearer <token>
Tus-Resumable: 1.0.0
```

Success response:

- HTTP `204 No Content`

Client reaction:

- Remove the stored upload URL locally
- Treat the upload as terminal

## Chunk Sizing Guidance

For multipart-safe behavior across local storage and S3-compatible backends:

- Minimum multipart-safe chunk size: `5 MiB`
- The final chunk may be smaller than `5 MiB`
- Recommended adaptive formula:

```text
chunk_size = max(5 MiB, ceil(upload_length / 10000))
```

Round the result up to the nearest MiB for a stable default.

Clients may choose larger chunks, but they must still fit under the service’s configured maximum chunk size.

Recommended defaults:

| Upload Size | Suggested Chunk Size | Notes |
|---|---:|---|
| Small files up to `100 MiB` | `5 MiB` to `8 MiB` | Keeps implementation simple |
| Medium files `100 MiB` to `5 GiB` | `8 MiB` to `32 MiB` | Good balance between overhead and retry cost |
| Large multi-GB files | adaptive, rounded up | Needed to stay below `10,000` parts |

Practical note:

- The current server buffers each incoming chunk in memory before write, so conservative chunk sizes are safer even though the protocol allows larger chunks

## Resume Strategy

Resumability only works if the client persists the upload URL and treats `HEAD` as the source of truth.

Resume rules:

- Persist the upload URL returned by `POST`
- On restart, reload, reconnect, or retry ambiguity, call `HEAD`
- Resume from the returned `Upload-Offset`
- Do not assume a timed-out `PATCH` failed
- Do not assume a dropped connection means the server committed nothing

Platform-specific storage suggestions:

- Browser: `IndexedDB`, `localStorage`, or app state plus durable storage
- Mobile: key-value store or local database
- Backend: database row, queue payload, or durable job state

## Retry and Error Handling

Use the server error code and HTTP status together.

| Status / Code | Retry? | Call `HEAD`? | Restart Upload? | User Action? | Recommended Client Behavior |
|---|---|---|---|---|---|
| `401 UNAUTHORIZED` | No | No | Maybe | Yes | Refresh or reacquire token, then retry request |
| `409 OFFSET_MISMATCH` | Yes | Yes | No | No | Call `HEAD`, trust returned offset, resume from there |
| `410 UPLOAD_EXPIRED` | No | No | Yes | Maybe | Session is dead; create a new upload if needed |
| `410 UPLOAD_ABORTED` | No | No | Yes | Maybe | Treat as terminal; create a new upload if desired |
| `410 UPLOAD_FAILED` | No | No | Yes | Yes | Treat as terminal failure; inspect status response and restart |
| `413 CHUNK_TOO_LARGE` | No | No | No | No | Reduce chunk size and retry with a valid chunk size |
| `422 CHECKSUM_MISMATCH` | Yes | Usually no | No | No | Recompute checksum and resend the same chunk |
| `429 RATE_LIMITED` | Yes | No | No | No | Honor `Retry-After`, then retry |
| `503 BACKEND_UNAVAILABLE` | Yes | Sometimes | No | No | Use exponential backoff with jitter; use `HEAD` if prior `PATCH` outcome is unclear |

Recommended retry rules:

- Use exponential backoff with jitter for `503`
- Respect `Retry-After` for `429`
- Use `HEAD` after any network ambiguity
- Do not blindly retry a stale `PATCH` without offset reconciliation

## Completion Model

Easy Upload completes asynchronously after the final `PATCH`.

That means:

- the last `PATCH` only confirms that the final bytes were received
- it does not guarantee the upload is already `completed`
- the service may still be in `finalizing`

After your final chunk:

1. Stop sending `PATCH`
2. Poll `GET /v1/uploads/{id}`
3. Wait for a terminal state

States to handle:

- `completed`
- `failed`
- `expired`
- `aborted`

Important response fields:

- `state`
- `committed_offset`
- `storage.artifact_key`
- `failure`

Backend-specific checksum behavior:

- The current runtime returns `checksum.algorithm` and `checksum.full_file_digest`
- Verification status details are not yet exposed as a separate field by the server

## End-to-End Browser JavaScript Example

This example uses plain browser JavaScript and standard Web APIs only.

```js
const TUS_VERSION = "1.0.0";
const MIN_CHUNK_SIZE = 5 * 1024 * 1024;
const MAX_PARTS = 10000;

function roundUpToMiB(bytes) {
  const mib = 1024 * 1024;
  return Math.ceil(bytes / mib) * mib;
}

function chooseChunkSize(fileSize) {
  const adaptive = Math.ceil(fileSize / MAX_PARTS);
  return roundUpToMiB(Math.max(MIN_CHUNK_SIZE, adaptive));
}

function encodeMetadataValue(value) {
  return btoa(unescape(encodeURIComponent(value)));
}

function buildUploadMetadata(file) {
  const parts = [
    `filename ${encodeMetadataValue(file.name)}`,
    `contentType ${encodeMetadataValue(file.type || "application/octet-stream")}`,
  ];
  return parts.join(",");
}

async function sha256Base64(blob) {
  const buffer = await blob.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buffer);
  const bytes = new Uint8Array(digest);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

async function createUpload({ file, token, baseUrl }) {
  const response = await fetch(`${baseUrl}/v1/uploads`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Tus-Resumable": TUS_VERSION,
      "Upload-Length": String(file.size),
      "Upload-Metadata": buildUploadMetadata(file),
    },
  });

  if (!response.ok) {
    throw new Error(`Create upload failed: ${response.status}`);
  }

  const location = response.headers.get("Location");
  if (!location) {
    throw new Error("Missing Location header");
  }

  return new URL(location, baseUrl).toString();
}

async function getOffset({ uploadUrl, token }) {
  const response = await fetch(uploadUrl, {
    method: "HEAD",
    headers: {
      Authorization: `Bearer ${token}`,
      "Tus-Resumable": TUS_VERSION,
    },
  });

  if (response.status === 410) {
    throw new Error("Upload is no longer resumable");
  }

  if (response.status !== 204) {
    throw new Error(`HEAD failed: ${response.status}`);
  }

  return Number(response.headers.get("Upload-Offset") || "0");
}

async function patchChunk({ uploadUrl, token, offset, chunk }) {
  const checksum = await sha256Base64(chunk);

  const response = await fetch(uploadUrl, {
    method: "PATCH",
    headers: {
      Authorization: `Bearer ${token}`,
      "Tus-Resumable": TUS_VERSION,
      "Content-Type": "application/offset+octet-stream",
      "Upload-Offset": String(offset),
      "Upload-Checksum": `sha256 ${checksum}`,
    },
    body: chunk,
  });

  return response;
}

async function pollStatus({ uploadUrl, token, intervalMs = 1500 }) {
  while (true) {
    const response = await fetch(uploadUrl, {
      method: "GET",
      headers: {
        Authorization: `Bearer ${token}`,
      },
    });

    if (!response.ok) {
      throw new Error(`Status polling failed: ${response.status}`);
    }

    const status = await response.json();
    if (["completed", "failed", "expired", "aborted"].includes(status.state)) {
      return status;
    }

    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

async function uploadFile({ file, token, baseUrl, onProgress = () => {} }) {
  const storageKey = `easy_upload:${file.name}:${file.size}`;
  let uploadUrl = localStorage.getItem(storageKey);

  if (!uploadUrl) {
    uploadUrl = await createUpload({ file, token, baseUrl });
    localStorage.setItem(storageKey, uploadUrl);
  }

  let offset = await getOffset({ uploadUrl, token });
  const chunkSize = chooseChunkSize(file.size);

  while (offset < file.size) {
    const chunk = file.slice(offset, offset + chunkSize);

    let response;
    try {
      response = await patchChunk({ uploadUrl, token, offset, chunk });
    } catch (error) {
      // Network ambiguity: always reconcile with HEAD before retrying.
      offset = await getOffset({ uploadUrl, token });
      continue;
    }

    if (response.status === 204) {
      offset = Number(response.headers.get("Upload-Offset"));
      onProgress({
        uploadedBytes: offset,
        totalBytes: file.size,
        percent: Math.round((offset / file.size) * 100),
      });
      continue;
    }

    if (response.status === 409) {
      offset = await getOffset({ uploadUrl, token });
      continue;
    }

    if (response.status === 422) {
      throw new Error("Checksum mismatch. Rebuild the chunk checksum and retry.");
    }

    if (response.status === 429) {
      const retryAfter = Number(response.headers.get("Retry-After") || "1");
      await new Promise((resolve) => setTimeout(resolve, retryAfter * 1000));
      continue;
    }

    if (response.status === 410) {
      localStorage.removeItem(storageKey);
      throw new Error("Upload session is no longer resumable.");
    }

    if (response.status >= 500) {
      await new Promise((resolve) => setTimeout(resolve, 1000));
      offset = await getOffset({ uploadUrl, token });
      continue;
    }

    throw new Error(`Unexpected PATCH status: ${response.status}`);
  }

  const finalStatus = await pollStatus({ uploadUrl, token });

  if (finalStatus.state === "completed") {
    localStorage.removeItem(storageKey);
    return finalStatus;
  }

  throw new Error(`Upload ended in terminal state: ${finalStatus.state}`);
}
```

What this example demonstrates:

- resumable URL persistence
- `HEAD`-first offset recovery
- per-chunk SHA-256 checksums
- sequential `PATCH`
- async completion polling

## Platform-Neutral Pseudocode

```text
if no stored upload URL exists:
    create upload with POST
    persist returned upload URL

offset = HEAD(upload URL).Upload-Offset

while offset < total_size:
    chunk = read next chunk starting at offset
    checksum = sha256(chunk)

    try PATCH(upload URL, offset, chunk, checksum)
    if success:
        offset = response Upload-Offset
        persist offset if desired
    else if 409:
        offset = HEAD(upload URL).Upload-Offset
    else if ambiguous network failure:
        offset = HEAD(upload URL).Upload-Offset
    else if 422:
        recompute checksum and retry same chunk
    else if 429:
        wait Retry-After and retry
    else if 410:
        fail upload and create new session if needed
    else:
        apply retry or fail logic based on status

status = poll GET(upload URL) until terminal
if status == completed:
    clear persisted upload URL
else:
    handle terminal failure
```

## Integration Checklist

- Token is available before the upload begins
- File size is known at create time
- Chunk size is chosen deliberately
- Upload URL is persisted after `POST`
- Per-chunk checksum calculation is implemented
- `HEAD` recovery is implemented
- Final status polling is implemented
- Handling for `409`, `422`, `429`, `503`, and `410` exists
- Uploads are sent sequentially only

## Scenarios You Should Explicitly Test

Happy path:

- create upload
- send sequential chunks
- poll until `completed`

Resume scenarios:

- reload or restart before completion
- network timeout after sending a chunk
- stale offset conflict resolved with `HEAD`

Failure scenarios:

- invalid token
- chunk checksum mismatch
- chunk too large
- rate limiting
- backend unavailable
- expired upload session

Backend-specific completion:

- local backend with `checksum.full_file_digest` after completion
- S3/R2 backend with the same current status surface, without a separate verification field yet

## Related Documents

- Concept note: [idea.md](./idea.md)
- Technical contract: [technical_spec_v1.md](./technical_spec_v1.md)
