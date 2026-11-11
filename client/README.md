# Easy Upload Client

Browser upload console and internal TypeScript upload core for the Easy Upload server.

## What It Includes

- React + Vite single-page upload console
- internal browser upload manager under `src/lib/`
- resumable session persistence with `localStorage`
- pause, resume, cancel, and retry controls for active uploads
- queue-level concurrency with sequential chunking per file
- Vitest coverage for chunking, resume storage, error normalization, and manager flows

## Local Prerequisites

Before starting the client, run the Go server with:

- PostgreSQL configured and migrated
- `EASY_UPLOAD_AUTH_TOKENS=dev-token`
- `EASY_UPLOAD_CORS_ALLOWED_ORIGINS=http://localhost:5173`

The default `.env.example` already includes the CORS origin needed for local browser development.

## Install

```bash
cd client
npm install
```

## Develop

```bash
npm run dev
```

The app serves on `http://localhost:5173` by default.

## Build

```bash
npm run build
```

## Test

```bash
npm run test
```

## Notes

- The top control panel lets you change the API base URL, bearer token, and queue concurrency without rebuilding.
- Active uploads can be paused and resumed without deleting their resumable session; cancel still terminates the server-side upload.
- The client targets the current server runtime shape, not every field described in the broader v1 docs.
- Upload state is recovered from `localStorage`; after a reload, the original file must be reattached before resume continues.
