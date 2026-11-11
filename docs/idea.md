# Easy Upload Project Idea

This file remains the short project concept note.

The authoritative implementation contract now lives in [technical_spec_v1.md](./technical_spec_v1.md).

## Project Overview

Easy Upload is a resumable upload service designed for large files and unstable networks. It uses `tus` semantics, persistent server-side offset tracking, and pluggable storage backends so uploads can resume without restarting from byte `0`.

## Why It Exists

Standard single-request uploads fail poorly for large files. A disconnect or timeout often forces the client to start over, wasting bandwidth and time. Easy Upload exists to make uploads durable, resumable, and storage-agnostic.

## Core Value

- Demonstrates practical HTTP protocol design
- Exercises durable state handling and crash recovery
- Showcases streaming and storage integration across local and object backends

## Current Direction

- Backend: Go
- Protocol: `tus` 1.0 compatible
- Storage: local filesystem plus S3-compatible multipart storage
- Control plane: PostgreSQL
- Detailed API, state machine, storage, and operational rules: see [technical_spec_v1.md](./technical_spec_v1.md)
