# Easy Upload

Resumable file upload service built around `tus` 1.0 semantics, PostgreSQL-backed upload state, and pluggable local or S3-compatible storage.

## What It Is

Easy Upload is an upload-only microservice for large files and unstable networks. It lets clients pause, resume, recover from dropped connections, terminate abandoned uploads, and poll for asynchronous completion without restarting from byte `0`.

The project is structured as a portfolio-quality backend system, not just a toy API:

- resumable upload state is persisted in PostgreSQL
- storage is abstracted behind local filesystem and S3/R2 adapters
- finalization is asynchronous
- client recovery is explicitly documented
- protocol behavior is specified before it is implemented

## Why It Exists

Large uploads fail badly over unreliable networks. A timeout or disconnect often forces the client to start over, wasting time and bandwidth. Easy Upload exists to solve that exact problem with a protocol-driven, storage-agnostic service that is small enough to reason about but deep enough to demonstrate real backend engineering tradeoffs.

## Highlights

- `tus`-style create, resume, checksum, expiration, and termination flow
- PostgreSQL-backed upload session state and part manifests
- Local filesystem adapter and S3/R2 multipart adapter
- Async finalization after the final chunk arrives
- Browser console with pause, resume, cancel, and recovery controls
- Client integration guide with resume, retry, and polling flows
- Explicit technical spec, not just implicit code behavior

## Architecture

At a high level, Easy Upload separates the control plane from the data plane:

- Control plane: PostgreSQL stores upload metadata, offsets, part manifests, expiration state, and outbox events
- Data plane: storage adapters persist staged bytes and finalize completed artifacts
- API layer: HTTP endpoints implement resumable upload behavior and status polling
- Worker layer: background loops finalize completed uploads and expire abandoned ones

This project is useful as a portfolio piece because it touches:

- HTTP protocol design
- resumable state machines
- object storage semantics
- crash recovery
- streaming and buffering tradeoffs
- client/server contract design

## Repository Layout

- [`docs/`](./docs) project docs, spec, roadmap, and client guide
- [`server/`](./server) Go implementation
- [`client/`](./client) browser upload console and internal TypeScript upload core

## Quickstart

From the repository root:

```bash
cd server
```

Apply the PostgreSQL schema:

```bash
psql "postgres://postgres:postgres@localhost:5432/easy_upload?sslmode=disable" -f db/migrations/001_init.sql
```

Export environment variables from [`server/.env.example`](./server/.env.example) and start the service:

```bash
set -a
source .env.example
set +a
go run ./cmd/easy_upload
```

Run the Go checks:

```bash
go test ./...
go vet ./...
```

Run the browser client:

```bash
cd ../client
npm install
npm run dev
```

Client defaults:

- browser app runs on `http://localhost:5173`
- server default CORS allowlist includes `http://localhost:5173`
- demo token defaults to `dev-token`, but can be edited in the UI

## Documentation

- Concept note: [docs/idea.md](./docs/idea.md)
- Technical spec: [docs/technical_spec_v1.md](./docs/technical_spec_v1.md)
- Server API reference: [docs/server_api_reference.md](./docs/server_api_reference.md)
- Client guide: [docs/client_integration_guide.md](./docs/client_integration_guide.md)
- Docs index: [docs/README.md](./docs/README.md)
- Server implementation notes: [server/README.md](./server/README.md)
- Roadmap and status: [docs/roadmap.md](./docs/roadmap.md)

## Current Status

Implemented now:

- core upload endpoints
- local filesystem adapter
- S3-compatible multipart adapter
- PostgreSQL-backed upload metadata and part manifests
- outbox event persistence
- browser upload console with resumable queue and localStorage-backed recovery
- technical specification and client integration guide

Specified but not fully implemented yet:

- health and readiness endpoints
- metrics endpoint
- rate limiting
- schema compatibility readiness gate
- richer observability surface
- full production-grade recovery and checksum hardening across every backend path

## Known Limitations

- The server currently buffers chunk bodies in memory before write so checksum verification can happen before storage mutation.
- Local and S3-compatible backends share the same external API shape, but operational hardening is still early-stage.
- The client upload core is internal to the browser demo in this phase; it is not published as a standalone SDK package.
- The repository is documentation-heavy by design because the implementation is still catching up to the full v1 specification.

## Roadmap

Near-term polish and hardening work:

- add health, readiness, and metrics endpoints to the runtime
- implement rate limiting
- improve S3 checksum and verification story
- add an outbox delivery worker
- replace full in-memory chunk buffering with a more streaming-oriented path
- close the remaining gap between the technical spec and the current runtime
