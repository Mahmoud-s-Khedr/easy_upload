# Roadmap and Status

## Current Implementation Scope

Implemented now:

- `tus`-style create, head, patch, delete endpoints
- JSON status endpoint for upload polling
- bearer token auth using configured opaque tokens
- PostgreSQL-backed upload metadata and part manifests
- local filesystem adapter
- S3 / R2 multipart adapter
- async finalization worker
- expiration sweeper
- outbox event persistence
- technical spec and client integration guide

## Known Gaps Between Spec and Implementation

Specified but not fully implemented yet:

- health and readiness endpoints
- metrics endpoint
- rate limiting
- readiness failure on schema incompatibility
- richer observability surface
- full production-grade checksum and recovery guarantees across every backend path

## Known Limitations

- Chunk bodies are buffered in memory before write so checksum validation can happen before storage mutation.
- The local and S3 adapters share the same external API surface, but operational hardening is still incomplete.
- No public client SDK is shipped yet.
- The repository is documentation-heavy because the implementation is still early-stage relative to the v1 specification.

## Likely Next Milestones

- Add health, readiness, and metrics endpoints
- Add rate limiting and retry-oriented headers
- Improve S3 checksum semantics and verification reporting
- Add an outbox delivery worker
- Replace in-memory chunk buffering with a more streaming-oriented data path
- Tighten implementation parity with the technical spec

