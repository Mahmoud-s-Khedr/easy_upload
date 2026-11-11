import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { buildFileFingerprint } from './fingerprint'
import { BrowserEasyUploadManager } from './manager'

vi.mock('./checksum', () => ({
  sha256Base64: vi.fn(async () => 'checksum'),
}))

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init.headers || {}),
    },
  })
}

function noContentResponse(headers: Record<string, string> = {}): Response {
  return new Response(null, { status: 204, headers })
}

async function waitForTask(
  manager: BrowserEasyUploadManager,
  predicate: (status: ReturnType<typeof manager.getSnapshot>) => boolean,
  timeout = 1000,
) {
  await vi.waitFor(() => {
    expect(predicate(manager.getSnapshot())).toBe(true)
  }, { timeout })
}

describe('BrowserEasyUploadManager', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('creates a new upload and persists the returned location before completion', async () => {
    const file = new File(['resume body'], 'resume.txt', { type: 'text/plain', lastModified: 10 })
    const patchGate = createDeferred<Response>()

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    fetchMock
      .mockResolvedValueOnce(
        new Response(null, {
          status: 201,
          headers: {
            Location: '/v1/uploads/upl_1',
            'Upload-Offset': '0',
          },
        }),
      )
      .mockImplementationOnce(() => patchGate.promise)
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_1',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_1' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })
    manager.addFiles([file])

    await vi.waitFor(() => {
      const stored = localStorage.getItem(`easy-upload:resume:${buildFileFingerprint(file)}`)
      expect(stored).not.toBeNull()
    })

    patchGate.resolve(noContentResponse({ 'Upload-Offset': String(file.size) }))

    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'completed'))
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('POST')
  })

  it('restores saved uploads as needs_file and resumes them with HEAD once reattached', async () => {
    const file = new File(['abcdef'], 'resume.pdf', { type: 'application/pdf', lastModified: 22 })
    localStorage.setItem(
      `easy-upload:resume:${buildFileFingerprint(file)}`,
      JSON.stringify({
        localId: 'saved-1',
        fileFingerprint: buildFileFingerprint(file),
        fileName: file.name,
        fileSize: file.size,
        fileType: file.type,
        lastModified: file.lastModified,
        uploadUrl: 'http://localhost:8080/v1/uploads/upl_saved',
        uploadId: 'upl_saved',
        baseUrl: 'http://localhost:8080',
        chunkSizeBytes: 8 * 1024 * 1024,
        uploadedBytes: 2,
        status: 'uploading',
        createdAt: '2026-02-28T00:00:00.000Z',
        updatedAt: '2026-02-28T00:00:01.000Z',
        metadata: { filename: file.name, contentType: file.type },
      }),
    )

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    fetchMock
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': '2' }))
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_saved',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_saved' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })

    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'needs_file'))

    manager.attachFile('saved-1', file)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'completed'))
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('HEAD')
  })

  it('reconciles with HEAD after an ambiguous PATCH failure', async () => {
    const file = new File(['abcdef'], 'clip.bin', { type: 'application/octet-stream', lastModified: 30 })

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    fetchMock
      .mockResolvedValueOnce(
        new Response(null, {
          status: 201,
          headers: {
            Location: '/v1/uploads/upl_reconcile',
            'Upload-Offset': '0',
          },
        }),
      )
      .mockRejectedValueOnce(new Error('socket closed'))
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_reconcile',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_reconcile' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token', retryBaseDelayMs: 1 })
    manager.addFiles([file])

    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'completed'))
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'HEAD')).toBe(true)
  })

  it('limits global concurrency to three uploads and keeps each upload sequential', async () => {
    const files = [
      new File(['aaaaaaaaaaaaaaaa'], 'a.bin', { lastModified: 1 }),
      new File(['bbbbbbbbbbbbbbbb'], 'b.bin', { lastModified: 2 }),
      new File(['cccccccccccccccc'], 'c.bin', { lastModified: 3 }),
      new File(['dddddddddddddddd'], 'd.bin', { lastModified: 4 }),
    ]

    let activePatches = 0
    let maxActivePatches = 0
    const patchesByUpload = new Map<string, number>()

    let postCount = 0
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
      const method = init?.method || 'GET'
      const url = String(input)

      if (method === 'POST') {
        postCount += 1
        const id = `upl_${postCount}`
        return new Response(null, {
          status: 201,
          headers: {
            Location: `/v1/uploads/${id}`,
            'Upload-Offset': '0',
          },
        })
      }

      if (method === 'PATCH') {
        const uploadId = url.split('/').pop() || 'unknown'
        const activeForUpload = patchesByUpload.get(uploadId) || 0
        patchesByUpload.set(uploadId, activeForUpload + 1)
        activePatches += 1
        maxActivePatches = Math.max(maxActivePatches, activePatches)
        expect(patchesByUpload.get(uploadId)).toBe(1)
        await new Promise((resolve) => setTimeout(resolve, 5))
        activePatches -= 1
        patchesByUpload.set(uploadId, 0)
        return noContentResponse({ 'Upload-Offset': '16' })
      }

      return jsonResponse({
        id: url.split('/').pop(),
        state: 'completed',
        declared_length: 16,
        committed_offset: 16,
        expires_at: '2026-02-28T00:00:00Z',
        metadata: {},
        checksum: { algorithm: 'sha256', full_file_digest: null },
        storage: { backend: 'local', artifact_key: url },
        failure: null,
        created_at: '2026-02-28T00:00:00Z',
        updated_at: '2026-02-28T00:00:01Z',
        completed_at: '2026-02-28T00:00:02Z',
      })
    })
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token', concurrency: 3, pollIntervalMs: 1 })
    manager.addFiles(files)

    await waitForTask(manager, (tasks) => tasks.filter((task) => task.status === 'completed').length === 4, 5000)
    expect(maxActivePatches).toBeLessThanOrEqual(3)
  })

  it('clears saved state and marks uploads terminal on 410 responses', async () => {
    const file = new File(['hello'], 'expired.txt', { type: 'text/plain', lastModified: 50 })
    localStorage.setItem(
      `easy-upload:resume:${buildFileFingerprint(file)}`,
      JSON.stringify({
        localId: 'saved-expired',
        fileFingerprint: buildFileFingerprint(file),
        fileName: file.name,
        fileSize: file.size,
        fileType: file.type,
        lastModified: file.lastModified,
        uploadUrl: 'http://localhost:8080/v1/uploads/upl_expired',
        uploadId: 'upl_expired',
        baseUrl: 'http://localhost:8080',
        chunkSizeBytes: 8 * 1024 * 1024,
        uploadedBytes: 2,
        status: 'uploading',
        createdAt: '2026-02-28T00:00:00.000Z',
        updatedAt: '2026-02-28T00:00:01.000Z',
        metadata: { filename: file.name, contentType: file.type },
      }),
    )

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse(
          { error: { code: 'UPLOAD_EXPIRED', message: 'upload has expired', retryable: false } },
          { status: 410 },
        ),
      ),
    )

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })
    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'needs_file'))

    manager.attachFile('saved-expired', file)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'expired'))
    expect(localStorage.getItem(`easy-upload:resume:${buildFileFingerprint(file)}`)).toBeNull()
  })

  it('reconcile: HEAD returns advanced offset and upload resumes from that position', async () => {
    // File is 12 bytes; server has already received the first 6 bytes.
    const file = new File(['abcdefghijkl'], 'part.bin', { type: 'application/octet-stream', lastModified: 70 })

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    fetchMock
      // POST → upload created at offset 0
      .mockResolvedValueOnce(
        new Response(null, {
          status: 201,
          headers: { Location: '/v1/uploads/upl_reconcile2', 'Upload-Offset': '0' },
        }),
      )
      // PATCH → network failure (triggers reconcile)
      .mockRejectedValueOnce(new Error('socket closed'))
      // HEAD → server reports offset = 6 (first 6 bytes received)
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': '6' }))
      // PATCH for bytes 6-12 → succeeds
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': '12' }))
      // GET status → completed
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_reconcile2',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_reconcile2' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({
      baseUrl: 'http://localhost:8080',
      token: 'dev-token',
      retryBaseDelayMs: 1,
    })
    manager.addFiles([file])

    await waitForTask(manager, (tasks) => tasks.some((task) => task.status === 'completed'), 2000)

    // Verify the second PATCH started at offset 6 (not 0 or 12).
    const patches = fetchMock.mock.calls.filter(([, init]) => init?.method === 'PATCH')
    expect(patches.length).toBe(2)
    const secondPatchOffset = patches[1]?.[1]?.headers
    // Upload-Offset header on the second PATCH must be '6'
    expect(
      secondPatchOffset instanceof Headers
        ? secondPatchOffset.get('Upload-Offset')
        : (secondPatchOffset as Record<string, string>)?.['Upload-Offset'],
    ).toBe('6')
  })

  it('polling timeout: task transitions to failed with FINALIZATION_TIMEOUT after maxPollAttempts', async () => {
    const file = new File(['small'], 'tiny.bin', { type: 'application/octet-stream', lastModified: 80 })

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    // POST → created
    fetchMock.mockResolvedValueOnce(
      new Response(null, {
        status: 201,
        headers: { Location: '/v1/uploads/upl_timeout', 'Upload-Offset': '0' },
      }),
    )
    // PATCH → uploaded everything
    fetchMock.mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
    // All status polls return 'finalizing' indefinitely
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        jsonResponse({
          id: 'upl_timeout',
          state: 'finalizing',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: null },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: null,
        }),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    // pollIntervalMs=0 so the loop spins fast enough to reach 240 attempts.
    const manager = new BrowserEasyUploadManager({
      baseUrl: 'http://localhost:8080',
      token: 'dev-token',
      pollIntervalMs: 0,
    })
    manager.addFiles([file])

    await waitForTask(
      manager,
      (tasks) => tasks.some((task) => task.status === 'failed' && task.errorCode === 'FINALIZATION_TIMEOUT'),
      10000,
    )

    const task = manager.getSnapshot().find((t) => t.status === 'failed')!
    expect(task.errorCode).toBe('FINALIZATION_TIMEOUT')
    expect(task.retryable).toBe(false)
  })

  it('concurrent cancel: abort fires while upload is in progress and task ends as cancelled', async () => {
    const file = new File(['concurrent cancel test'], 'cc.bin', { type: 'application/octet-stream', lastModified: 95 })
    const patchGate = createDeferred<Response>()
    let abortFired = false

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_, init) => {
      if (init?.method === 'POST') {
        return Promise.resolve(
          new Response(null, {
            status: 201,
            headers: { Location: '/v1/uploads/upl_cc', 'Upload-Offset': '0' },
          }),
        )
      }
      if (init?.method === 'PATCH') {
        init.signal?.addEventListener('abort', () => {
          abortFired = true
          patchGate.reject(new DOMException('Aborted', 'AbortError'))
        })
        return patchGate.promise
      }
      if (init?.method === 'DELETE') {
        return Promise.resolve(noContentResponse())
      }
      return Promise.resolve(noContentResponse({ 'Upload-Offset': '0' }))
    })
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })
    const [localId] = manager.addFiles([file])

    // Wait until PATCH is in-flight (task is uploading).
    await waitForTask(manager, (tasks) => tasks.some((t) => t.localId === localId && t.status === 'uploading'))

    await manager.cancel(localId)

    await waitForTask(
      manager,
      (tasks) => tasks.some((t) => t.localId === localId && t.status === 'cancelled'),
    )

    expect(abortFired).toBe(true)
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(true)
  })

  it('issues DELETE on cancel and removes local resume state', async () => {
    const file = new File(['cancel me'], 'cancel.txt', { type: 'text/plain', lastModified: 90 })
    const patchResolvers = createDeferred<Response>()

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_, init) => {
      if (init?.method === 'POST') {
        return Promise.resolve(
          new Response(null, {
            status: 201,
            headers: {
              Location: '/v1/uploads/upl_cancel',
              'Upload-Offset': '0',
            },
          }),
        )
      }
      if (init?.method === 'PATCH') {
        const signal = init.signal
        signal?.addEventListener('abort', () => {
          patchResolvers.reject(new DOMException('Aborted', 'AbortError'))
        })
        return patchResolvers.promise
      }
      if (init?.method === 'DELETE') {
        return Promise.resolve(noContentResponse())
      }
      return Promise.resolve(noContentResponse({ 'Upload-Offset': '0' }))
    })
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })
    const [localId] = manager.addFiles([file])

    await vi.waitFor(() => {
      expect(localStorage.getItem(`easy-upload:resume:${buildFileFingerprint(file)}`)).not.toBeNull()
    })

    await manager.cancel(localId)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'cancelled'))
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(true)
    expect(localStorage.getItem(`easy-upload:resume:${buildFileFingerprint(file)}`)).toBeNull()
  })

  it('pause during an in-flight PATCH aborts without DELETE and preserves resume state', async () => {
    const file = new File(['pause me'], 'pause.bin', { type: 'application/octet-stream', lastModified: 120 })
    const patchGate = createDeferred<Response>()
    let abortFired = false

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_, init) => {
      if (init?.method === 'POST') {
        return Promise.resolve(
          new Response(null, {
            status: 201,
            headers: {
              Location: '/v1/uploads/upl_pause',
              'Upload-Offset': '0',
            },
          }),
        )
      }
      if (init?.method === 'PATCH') {
        init.signal?.addEventListener('abort', () => {
          abortFired = true
          patchGate.reject(new DOMException('Aborted', 'AbortError'))
        })
        return patchGate.promise
      }
      throw new Error(`Unexpected ${init?.method || 'GET'} request`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })
    const [localId] = manager.addFiles([file])

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'uploading'))

    await manager.pause(localId)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'paused'))

    const raw = localStorage.getItem(`easy-upload:resume:${buildFileFingerprint(file)}`)
    expect(abortFired).toBe(true)
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    expect(raw).not.toBeNull()
    expect(JSON.parse(raw || '{}')).toMatchObject({
      status: 'paused',
      uploadUrl: 'http://localhost:8080/v1/uploads/upl_pause',
      uploadId: 'upl_pause',
    })
  })

  it('resume from paused performs HEAD before continuing', async () => {
    const file = new File(['abcdef'], 'paused.pdf', { type: 'application/pdf', lastModified: 121 })
    localStorage.setItem(
      `easy-upload:resume:${buildFileFingerprint(file)}`,
      JSON.stringify({
        localId: 'saved-paused',
        fileFingerprint: buildFileFingerprint(file),
        fileName: file.name,
        fileSize: file.size,
        fileType: file.type,
        lastModified: file.lastModified,
        uploadUrl: 'http://localhost:8080/v1/uploads/upl_paused',
        uploadId: 'upl_paused',
        baseUrl: 'http://localhost:8080',
        chunkSizeBytes: 8 * 1024 * 1024,
        uploadedBytes: 2,
        status: 'paused',
        createdAt: '2026-02-28T00:00:00.000Z',
        updatedAt: '2026-02-28T00:00:01.000Z',
        metadata: { filename: file.name, contentType: file.type },
      }),
    )

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    fetchMock
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': '2' }))
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_paused',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_paused' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === 'saved-paused' && task.status === 'needs_file'))

    manager.attachFile('saved-paused', file)
    await Promise.resolve()

    expect(manager.getSnapshot().find((task) => task.localId === 'saved-paused')?.status).toBe('paused')
    expect(fetchMock).not.toHaveBeenCalled()

    manager.resume('saved-paused')

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === 'saved-paused' && task.status === 'completed'))
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('HEAD')
  })

  it('paused tasks are skipped by the scheduler while other uploads continue', async () => {
    const pausedFile = new File(['abcdef'], 'paused-skip.bin', { type: 'application/octet-stream', lastModified: 122 })
    const freshFile = new File(['fresh upload body'], 'fresh.bin', { type: 'application/octet-stream', lastModified: 123 })

    localStorage.setItem(
      `easy-upload:resume:${buildFileFingerprint(pausedFile)}`,
      JSON.stringify({
        localId: 'saved-skip',
        fileFingerprint: buildFileFingerprint(pausedFile),
        fileName: pausedFile.name,
        fileSize: pausedFile.size,
        fileType: pausedFile.type,
        lastModified: pausedFile.lastModified,
        uploadUrl: 'http://localhost:8080/v1/uploads/upl_skip',
        uploadId: 'upl_skip',
        baseUrl: 'http://localhost:8080',
        chunkSizeBytes: 8 * 1024 * 1024,
        uploadedBytes: 2,
        status: 'paused',
        createdAt: '2026-02-28T00:00:00.000Z',
        updatedAt: '2026-02-28T00:00:01.000Z',
        metadata: { filename: pausedFile.name, contentType: pausedFile.type },
      }),
    )

    let postCount = 0
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
      const method = init?.method || 'GET'
      const url = String(input)

      expect(url.includes('upl_skip')).toBe(false)

      if (method === 'POST') {
        postCount += 1
        return new Response(null, {
          status: 201,
          headers: {
            Location: `/v1/uploads/upl_fresh_${postCount}`,
            'Upload-Offset': '0',
          },
        })
      }

      if (method === 'PATCH') {
        return noContentResponse({ 'Upload-Offset': String(freshFile.size) })
      }

      return jsonResponse({
        id: 'upl_fresh_1',
        state: 'completed',
        declared_length: freshFile.size,
        committed_offset: freshFile.size,
        expires_at: '2026-02-28T00:00:00Z',
        metadata: {},
        checksum: { algorithm: 'sha256', full_file_digest: null },
        storage: { backend: 'local', artifact_key: 'uploads/upl_fresh_1' },
        failure: null,
        created_at: '2026-02-28T00:00:00Z',
        updated_at: '2026-02-28T00:00:01Z',
        completed_at: '2026-02-28T00:00:02Z',
      })
    })
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token', concurrency: 2, pollIntervalMs: 1 })

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === 'saved-skip' && task.status === 'needs_file'))
    manager.attachFile('saved-skip', pausedFile)
    expect(manager.getSnapshot().find((task) => task.localId === 'saved-skip')?.status).toBe('paused')

    manager.addFiles([freshFile])

    await waitForTask(manager, (tasks) => tasks.some((task) => task.fileName === 'fresh.bin' && task.status === 'completed'))
    expect(fetchMock.mock.calls.length).toBeGreaterThan(0)
  })

  it('pause before session creation keeps the task resumable until resume starts POST', async () => {
    const file = new File(['queued first'], 'precreate.bin', { type: 'application/octet-stream', lastModified: 124 })
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({ baseUrl: 'http://localhost:8080', token: 'dev-token' })
    const [localId] = manager.addFiles([file])

    await manager.pause(localId)
    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'paused'))

    const pausedRecord = JSON.parse(localStorage.getItem(`easy-upload:resume:${buildFileFingerprint(file)}`) || '{}')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(pausedRecord.status).toBe('paused')
    expect(pausedRecord.uploadUrl).toBeUndefined()
    expect(pausedRecord.uploadId).toBeUndefined()

    fetchMock
      .mockResolvedValueOnce(
        new Response(null, {
          status: 201,
          headers: {
            Location: '/v1/uploads/upl_precreate',
            'Upload-Offset': '0',
          },
        }),
      )
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_precreate',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_precreate' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )

    manager.resume(localId)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'completed'))
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('POST')
  })

  it('pause during finalization stops polling and resume reconciles before completion', async () => {
    const file = new File(['finalize'], 'finalize.bin', { type: 'application/octet-stream', lastModified: 125 })

    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
    fetchMock
      .mockResolvedValueOnce(
        new Response(null, {
          status: 201,
          headers: {
            Location: '/v1/uploads/upl_finalize_pause',
            'Upload-Offset': '0',
          },
        }),
      )
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_finalize_pause',
          state: 'finalizing',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: null },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: null,
        }),
      )
      .mockResolvedValueOnce(noContentResponse({ 'Upload-Offset': String(file.size) }))
      .mockResolvedValueOnce(
        jsonResponse({
          id: 'upl_finalize_pause',
          state: 'completed',
          declared_length: file.size,
          committed_offset: file.size,
          expires_at: '2026-02-28T00:00:00Z',
          metadata: {},
          checksum: { algorithm: 'sha256', full_file_digest: null },
          storage: { backend: 'local', artifact_key: 'uploads/upl_finalize_pause' },
          failure: null,
          created_at: '2026-02-28T00:00:00Z',
          updated_at: '2026-02-28T00:00:01Z',
          completed_at: '2026-02-28T00:00:02Z',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const manager = new BrowserEasyUploadManager({
      baseUrl: 'http://localhost:8080',
      token: 'dev-token',
      pollIntervalMs: 10_000,
    })
    const [localId] = manager.addFiles([file])

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'finalizing'))
    expect(fetchMock).toHaveBeenCalledTimes(3)

    await manager.pause(localId)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'paused'))
    expect(fetchMock).toHaveBeenCalledTimes(3)

    manager.resume(localId)

    await waitForTask(manager, (tasks) => tasks.some((task) => task.localId === localId && task.status === 'completed'))
    expect(fetchMock.mock.calls[3]?.[1]?.method).toBe('HEAD')
  })
})
