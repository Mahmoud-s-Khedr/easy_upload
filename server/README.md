# easy_upload server

Go implementation of the resumable upload service defined in [technical_spec_v1.md](../docs/technical_spec_v1.md). Use [server_api_reference.md](../docs/server_api_reference.md) for the current runtime HTTP behavior, and use the technical spec for broader design and future-scope context.

If you are new to the repository, start with the [root README](../README.md) first.

For a code-oriented explanation of package responsibilities, request flow, worker flow, and current runtime caveats, use the [server code walkthrough](../docs/server_code_walkthrough.md).

## What is implemented

- `tus`-style create, head, patch, delete endpoints
- JSON status endpoint at `GET /v1/uploads/{id}`
- Bearer token auth
- PostgreSQL metadata store
- Local filesystem adapter
- S3/R2 multipart adapter
- Async finalization worker
- TTL sweeper for abandoned uploads
- Outbox event persistence

## Run locally

From the repository root:

```bash
cd server
```

Then:

1. Create a PostgreSQL database and apply [`db/migrations/001_init.sql`](./db/migrations/001_init.sql).
2. Export variables from [`.env.example`](./.env.example).
3. Start the server from the `server/` directory:

```bash
go run ./cmd/easy_upload
```

## Notes

- `POST`, `HEAD`, `PATCH`, and `DELETE` require `Tus-Resumable: 1.0.0`.
- Successful `HEAD` requests intentionally return `204 No Content`.
- `PATCH` currently buffers each chunk in memory before append so checksum validation happens before storage mutation.
- The S3 adapter finalizes multipart uploads but does not yet emit download URLs or webhook delivery.
- Size-related environment variables are raw integer byte counts; suffixes like `5GiB` are not supported in the current implementation.

## Development

Run checks from the `server/` directory:

```bash
go test ./...
go vet ./...
```

Configuration source:

- [`.env.example`](./.env.example)

Migration file:

- [`db/migrations/001_init.sql`](./db/migrations/001_init.sql)

## Current Gaps

- Chunk bodies are buffered in memory before write.
- The technical spec includes operational surfaces that are not fully implemented yet, such as health/readiness, metrics, and rate limiting.
- No public client SDK is shipped in this repository.

## Specification

- Concept note: [idea.md](../docs/idea.md)
- Runtime API reference: [server_api_reference.md](../docs/server_api_reference.md)
- Authoritative technical contract: [technical_spec_v1.md](../docs/technical_spec_v1.md)
- Client integration guide: [client_integration_guide.md](../docs/client_integration_guide.md)
- Roadmap and status: [roadmap.md](../docs/roadmap.md)
