# Contributing

Easy Upload is an early-stage open-source project and a portfolio-quality backend system. Contributions are welcome, but the repository is still maturing, so changes should favor clarity and consistency over feature sprawl.

## Local Setup

1. Change into [server](./server).
2. Create a PostgreSQL database.
3. Apply [db/migrations/001_init.sql](./server/db/migrations/001_init.sql).
4. Export variables from [.env.example](./server/.env.example).
5. Run the service with `go run ./cmd/easy_upload`.

## Development Workflow

Run checks before opening a PR:

```bash
cd server
go test ./...
go vet ./...
```

## Expectations

- Keep the technical contract and implementation aligned.
- Update tests when behavior changes.
- Update docs when protocol behavior, setup, or limitations change.
- Do not introduce undocumented protocol deviations.
- Keep changes small and reviewable when possible.

## Coding Conventions

- Follow existing Go formatting and package structure.
- Prefer explicit, simple behavior over abstraction-heavy refactors.
- Keep documentation practical and accurate to the current implementation.
- If a change only updates the spec or docs, say so clearly.

## Pull Requests

A good PR should:

- explain what changed and why
- describe test coverage
- mention docs or spec impact
- call out behavior changes or known limitations
