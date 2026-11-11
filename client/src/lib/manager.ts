import {
  createUploadSession,
  deleteUploadSession,
  getUploadOffset,
  getUploadStatus,
  patchUploadChunk,
} from './api'
import { sha256Base64 } from './checksum'
import { chooseChunkSize } from './chunking'
import { normalizeThrownError, UploadRequestError } from './errors'
import { buildFileFingerprint } from './fingerprint'
import { buildUploadMetadata } from './metadata'
import { LocalStorageResumeStore } from './resumeStore'
import { wait } from './time'
import type {
  EasyUploadManager,
  EasyUploadManagerConfig,
  ResumeRecord,
  ResumeStore,
  UploadMetadata,
  UploadStatusResponse,
  UploadTaskSnapshot,
  UploadTaskStatus,
} from './types'

type InternalTask = UploadTaskSnapshot & {
  fileType: string
  lastModified: number
  metadata: UploadMetadata
  file?: File
  running: boolean
  retryCount: number
  nextAttemptAt?: number
  manuallyPaused: boolean
  pauseRequested: boolean
  cancelRequested: boolean
  abortController?: AbortController
}

type MutableConfig = {
  baseUrl: string
  token: string
  concurrency: number
  pollIntervalMs: number
  retryBaseDelayMs: number
}

const defaultConfig = {
  baseUrl: 'http://localhost:8080',
  token: 'dev-token',
  concurrency: 3,
  pollIntervalMs: 1500,
  retryBaseDelayMs: 800,
} satisfies MutableConfig

// Maximum number of finalization poll attempts before giving up (~6 min at 1.5s interval).
const maxPollAttempts = 240

function resolveValue<T>(value: Promise<T> | T): Promise<T> {
  return Promise.resolve(value)
}

function clampConcurrency(value: number): number {
  return Math.max(1, Math.min(6, Math.floor(value)))
}

function createLocalId(): string {
  return typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `upl_local_${Date.now()}_${Math.random().toString(16).slice(2)}`
}

function isTerminalStatus(status: UploadTaskStatus): boolean {
  return ['completed', 'failed', 'aborted', 'expired', 'cancelled'].includes(status)
}

function normalizeBaseUrl(baseUrl: string): string {
  const trimmed = baseUrl.trim()
  return trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed
}

function statusFromServerState(state: string): UploadTaskStatus {
  switch (state) {
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    case 'aborted':
      return 'aborted'
    case 'expired':
      return 'expired'
    default:
      return 'finalizing'
  }
}

function snapshotTask(task: InternalTask): UploadTaskSnapshot {
  return {
    localId: task.localId,
    fileFingerprint: task.fileFingerprint,
    fileName: task.fileName,
    fileSize: task.fileSize,
    uploadedBytes: task.uploadedBytes,
    progressPct: task.progressPct,
    chunkSizeBytes: task.chunkSizeBytes,
    status: task.status,
    uploadUrl: task.uploadUrl,
    uploadId: task.uploadId,
    serverState: task.serverState,
    artifactKey: task.artifactKey,
    errorCode: task.errorCode,
    errorMessage: task.errorMessage,
    retryable: task.retryable,
    createdAt: task.createdAt,
    updatedAt: task.updatedAt,
  }
}

function compareTasks(left: UploadTaskSnapshot, right: UploadTaskSnapshot): number {
  return right.createdAt.localeCompare(left.createdAt)
}

export class BrowserEasyUploadManager implements EasyUploadManager {
  private readonly listeners = new Set<(tasks: UploadTaskSnapshot[]) => void>()
  private readonly tasks = new Map<string, InternalTask>()
  private readonly storage: ResumeStore
  private config: MutableConfig
  private readonly ready: Promise<void>
  private cachedSnapshot: UploadTaskSnapshot[] | null = null

  constructor(config: EasyUploadManagerConfig) {
    this.storage = config.storage ?? new LocalStorageResumeStore()
    this.config = this.mergeConfig(config)
    this.ready = this.restorePersistedTasks()
  }

  updateConfig(config: Partial<EasyUploadManagerConfig>): void {
    this.config = this.mergeConfig({ ...this.config, ...config })
    this.schedule()
  }

  addFiles(files: File[]): string[] {
    const ids: string[] = []

    for (const file of files) {
      const fingerprint = buildFileFingerprint(file)
      const existingTask = this.findTaskByFingerprint(fingerprint)
      if (existingTask) {
        this.attachFile(existingTask.localId, file)
        ids.push(existingTask.localId)
        continue
      }

      const task = this.createTaskFromFile(file)
      this.tasks.set(task.localId, task)
      ids.push(task.localId)
      this.emit()

      void this.resolveStoredRecord(task.localId, fingerprint, file)
    }

    return ids
  }

  attachFile(localId: string, file: File): void {
    const task = this.tasks.get(localId)
    if (!task) {
      return
    }

    const fingerprint = buildFileFingerprint(file)
    if (fingerprint !== task.fileFingerprint) {
      this.updateTask(task, {
        status: 'needs_file',
        errorCode: 'FILE_MISMATCH',
        errorMessage: 'Selected file does not match the saved upload fingerprint.',
        retryable: false,
      })
      return
    }

    task.file = file
    task.fileName = file.name
    task.fileType = file.type || 'application/octet-stream'
    task.lastModified = file.lastModified
    task.fileSize = file.size
    task.chunkSizeBytes = chooseChunkSize(file.size)
    task.status = task.manuallyPaused ? 'paused' : task.uploadUrl ? 'reconciling' : 'queued'
    task.errorCode = undefined
    task.errorMessage = undefined
    task.retryable = undefined
    task.pauseRequested = false
    task.cancelRequested = false
    task.updatedAt = new Date().toISOString()
    this.emit()
    this.schedule()
  }

  async pause(localId: string): Promise<void> {
    await this.ready

    const task = this.tasks.get(localId)
    if (!task || isTerminalStatus(task.status) || task.status === 'needs_file') {
      return
    }

    task.manuallyPaused = true
    task.pauseRequested = true
    task.cancelRequested = false
    task.nextAttemptAt = undefined
    task.retryable = false
    task.errorCode = undefined
    task.errorMessage = undefined
    task.status = task.file ? 'paused' : 'needs_file'
    task.updatedAt = new Date().toISOString()
    task.abortController?.abort()
    await this.persistTask(task)
    this.emit()
  }

  async cancel(localId: string): Promise<void> {
    await this.ready

    const task = this.tasks.get(localId)
    if (!task) {
      return
    }

    task.manuallyPaused = false
    task.pauseRequested = false
    task.cancelRequested = true
    task.abortController?.abort()

    if (task.uploadUrl && !isTerminalStatus(task.status)) {
      try {
        await deleteUploadSession({
          baseUrl: this.config.baseUrl,
          token: this.config.token,
          uploadUrl: task.uploadUrl,
        })
      } catch (error) {
        const normalized = normalizeThrownError(error)
        if (normalized.code !== 'REQUEST_ABORTED') {
          task.errorCode = normalized.code
          task.errorMessage = normalized.message
        }
      }
    }

    task.status = 'cancelled'
    task.running = false
    task.uploadUrl = undefined
    task.uploadId = undefined
    task.serverState = 'cancelled'
    task.abortController = undefined
    task.retryable = false
    task.updatedAt = new Date().toISOString()
    await resolveValue(this.storage.delete(task.fileFingerprint))
    this.emit()
  }

  resume(localId: string): void {
    const task = this.tasks.get(localId)
    if (!task) {
      return
    }

    task.manuallyPaused = false
    task.pauseRequested = false
    task.cancelRequested = false
    task.nextAttemptAt = undefined
    task.retryCount = 0
    task.errorCode = undefined
    task.errorMessage = undefined
    task.retryable = undefined
    task.status = task.file ? (task.uploadUrl ? 'reconciling' : 'queued') : 'needs_file'
    task.updatedAt = new Date().toISOString()
    void this.persistTask(task)
    this.emit()
    this.schedule()
  }

  retry(localId: string): void {
    const task = this.tasks.get(localId)
    if (!task) {
      return
    }

    task.manuallyPaused = false
    task.pauseRequested = false
    task.cancelRequested = false
    task.retryCount = 0
    task.nextAttemptAt = undefined
    task.errorCode = undefined
    task.errorMessage = undefined
    task.retryable = undefined
    task.artifactKey = undefined
    task.serverState = undefined

    if (isTerminalStatus(task.status)) {
      task.uploadUrl = undefined
      task.uploadId = undefined
      task.uploadedBytes = 0
      task.progressPct = 0
    }

    task.status = task.file ? (task.uploadUrl ? 'reconciling' : 'queued') : 'needs_file'
    task.updatedAt = new Date().toISOString()
    void this.persistTask(task)
    this.emit()
    this.schedule()
  }

  remove(localId: string): void {
    const task = this.tasks.get(localId)
    if (!task || task.running) {
      return
    }

    this.tasks.delete(localId)
    void resolveValue(this.storage.delete(task.fileFingerprint))
    this.emit()
  }

  subscribe(listener: (tasks: UploadTaskSnapshot[]) => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot(): UploadTaskSnapshot[] {
    if (this.cachedSnapshot === null) {
      this.cachedSnapshot = Array.from(this.tasks.values()).map(snapshotTask).sort(compareTasks)
    }
    return this.cachedSnapshot
  }

  private mergeConfig(config: Partial<EasyUploadManagerConfig>): MutableConfig {
    return {
      baseUrl: normalizeBaseUrl(config.baseUrl ?? defaultConfig.baseUrl),
      token: config.token ?? defaultConfig.token,
      concurrency: clampConcurrency(config.concurrency ?? defaultConfig.concurrency),
      pollIntervalMs: config.pollIntervalMs ?? defaultConfig.pollIntervalMs,
      retryBaseDelayMs: config.retryBaseDelayMs ?? defaultConfig.retryBaseDelayMs,
    }
  }

  private async restorePersistedTasks(): Promise<void> {
    const records = await resolveValue(this.storage.list())
    for (const record of records) {
      if (this.tasks.has(record.localId)) {
        continue
      }
      this.tasks.set(record.localId, this.createTaskFromRecord(record))
    }
    this.emit()
  }

  private async resolveStoredRecord(localId: string, fingerprint: string, file: File): Promise<void> {
    await this.ready

    const task = this.tasks.get(localId)
    if (!task) {
      return
    }

    const record = await resolveValue(this.storage.get(fingerprint))
    if (!record) {
      task.status = task.manuallyPaused ? 'paused' : 'queued'
      task.updatedAt = new Date().toISOString()
      this.emit()
      this.schedule()
      return
    }

    this.applyRecord(task, record)
    task.file = file
    task.status = task.manuallyPaused ? 'paused' : 'reconciling'
    task.errorCode = undefined
    task.errorMessage = undefined
    task.retryable = undefined
    task.updatedAt = new Date().toISOString()
    this.emit()
    this.schedule()
  }

  private createTaskFromFile(file: File): InternalTask {
    const now = new Date().toISOString()
    return {
      localId: createLocalId(),
      fileFingerprint: buildFileFingerprint(file),
      fileName: file.name,
      fileSize: file.size,
      uploadedBytes: 0,
      progressPct: 0,
      chunkSizeBytes: chooseChunkSize(file.size),
      status: 'queued',
      createdAt: now,
      updatedAt: now,
      fileType: file.type || 'application/octet-stream',
      lastModified: file.lastModified,
      metadata: buildUploadMetadata(file),
      file,
      running: false,
      retryCount: 0,
      manuallyPaused: false,
      pauseRequested: false,
      cancelRequested: false,
    }
  }

  private createTaskFromRecord(record: ResumeRecord): InternalTask {
    return {
      localId: record.localId,
      fileFingerprint: record.fileFingerprint,
      fileName: record.fileName,
      fileSize: record.fileSize,
      uploadedBytes: record.uploadedBytes,
      progressPct: record.fileSize > 0 ? Math.round((record.uploadedBytes / record.fileSize) * 100) : 0,
      chunkSizeBytes: record.chunkSizeBytes,
      status: 'needs_file',
      uploadUrl: record.uploadUrl,
      uploadId: record.uploadId,
      createdAt: record.createdAt,
      updatedAt: record.updatedAt,
      fileType: record.fileType,
      lastModified: record.lastModified,
      metadata: record.metadata,
      running: false,
      retryCount: 0,
      manuallyPaused: record.status === 'paused',
      pauseRequested: false,
      cancelRequested: false,
    }
  }

  private applyRecord(task: InternalTask, record: ResumeRecord): void {
    task.uploadUrl = record.uploadUrl
    task.uploadId = record.uploadId
    task.uploadedBytes = record.uploadedBytes
    task.chunkSizeBytes = record.chunkSizeBytes
    task.createdAt = record.createdAt
    task.updatedAt = record.updatedAt
    task.metadata = record.metadata
    task.manuallyPaused = record.status === 'paused'
    task.progressPct = task.fileSize > 0 ? Math.round((task.uploadedBytes / task.fileSize) * 100) : 0
  }

  private findTaskByFingerprint(fingerprint: string): InternalTask | undefined {
    return Array.from(this.tasks.values()).find((task) => task.fileFingerprint === fingerprint)
  }

  private emit(): void {
    this.cachedSnapshot = null
    const snapshot = this.getSnapshot()
    for (const listener of this.listeners) {
      listener(snapshot)
    }
  }

  private schedule(): void {
    const runningCount = Array.from(this.tasks.values()).filter((task) => task.running).length
    if (runningCount >= this.config.concurrency) {
      return
    }

    const now = Date.now()
    const availableSlots = this.config.concurrency - runningCount
    const pendingTasks = Array.from(this.tasks.values())
      .filter((task) => this.isRunnable(task, now))
      .sort((left, right) => left.createdAt.localeCompare(right.createdAt))
      .slice(0, availableSlots)

    for (const task of pendingTasks) {
      void this.runTask(task.localId)
    }
  }

  private isRunnable(task: InternalTask, now: number): boolean {
    if (task.running || !task.file || task.cancelRequested || task.manuallyPaused || task.status === 'paused') {
      return false
    }
    if (task.nextAttemptAt && task.nextAttemptAt > now) {
      window.setTimeout(() => this.schedule(), task.nextAttemptAt - now)
      return false
    }
    return ['queued', 'creating', 'reconciling', 'uploading', 'finalizing'].includes(task.status)
  }

  private async runTask(localId: string): Promise<void> {
    const task = this.tasks.get(localId)
    if (!task || task.running || !task.file || task.manuallyPaused) {
      return
    }

    task.running = true
    task.pauseRequested = false
    task.cancelRequested = false
    this.emit()

    try {
      await this.ready
      if (!this.tasks.has(localId) || !task.file || task.manuallyPaused) {
        return
      }

      await this.ensureUploadSession(task)
      if (task.cancelRequested || task.pauseRequested || task.manuallyPaused || !task.file) {
        return
      }

      if (task.uploadedBytes < task.file.size) {
        await this.uploadChunks(task)
      }

      if (!task.cancelRequested && !task.pauseRequested && !task.manuallyPaused && task.file && task.uploadedBytes >= task.file.size) {
        await this.pollUntilTerminal(task)
      }
    } catch (error) {
      await this.handleTaskError(task, normalizeThrownError(error))
    } finally {
      task.running = false
      task.abortController = undefined
      this.emit()
      this.schedule()
    }
  }

  private async ensureUploadSession(task: InternalTask): Promise<void> {
    if (!task.file) {
      return
    }

    if (!task.uploadUrl) {
      this.updateTask(task, { status: 'creating' })
      const controller = this.createAbortController(task)
      const created = await createUploadSession({
        baseUrl: this.config.baseUrl,
        token: this.config.token,
        fileSize: task.file.size,
        metadata: task.metadata,
        signal: controller.signal,
      })
      task.uploadUrl = created.uploadUrl
      task.uploadId = created.uploadId
      task.uploadedBytes = created.offset
      task.progressPct = task.fileSize > 0 ? Math.round((task.uploadedBytes / task.fileSize) * 100) : 0
      task.status = 'uploading'
      task.updatedAt = new Date().toISOString()
      await this.persistTask(task)
      this.emit()
      return
    }

    this.updateTask(task, { status: 'reconciling' })
    const controller = this.createAbortController(task)
    const offset = await getUploadOffset({
      baseUrl: this.config.baseUrl,
      token: this.config.token,
      uploadUrl: task.uploadUrl,
      signal: controller.signal,
    })
    task.uploadedBytes = offset
    task.progressPct = task.fileSize > 0 ? Math.round((offset / task.fileSize) * 100) : 0
    task.status = task.file.size > offset ? 'uploading' : 'finalizing'
    task.updatedAt = new Date().toISOString()
    await this.persistTask(task)
    this.emit()
  }

  private async uploadChunks(task: InternalTask): Promise<void> {
    if (!task.file || !task.uploadUrl) {
      return
    }

    while (!task.cancelRequested && !task.pauseRequested && !task.manuallyPaused && task.file && task.uploadedBytes < task.file.size) {
      const chunk = task.file.slice(task.uploadedBytes, task.uploadedBytes + task.chunkSizeBytes)
      task.status = 'uploading'
      const controller = this.createAbortController(task)
      task.errorCode = undefined
      task.errorMessage = undefined
      task.retryable = undefined
      task.updatedAt = new Date().toISOString()
      this.emit()

      try {
        const checksum = await sha256Base64(chunk)
        const nextOffset = await patchUploadChunk({
          baseUrl: this.config.baseUrl,
          token: this.config.token,
          uploadUrl: task.uploadUrl,
          offset: task.uploadedBytes,
          chunk,
          checksum,
          signal: controller.signal,
        })
        task.uploadedBytes = nextOffset
        task.progressPct = Math.round((nextOffset / task.file.size) * 100)
        task.retryCount = 0
        task.updatedAt = new Date().toISOString()
        await this.persistTask(task)
        this.emit()
      } catch (error) {
        const normalized = normalizeThrownError(error)
        if ((task.cancelRequested || task.pauseRequested || task.manuallyPaused) && normalized.code === 'REQUEST_ABORTED') {
          await this.handleTaskError(task, normalized)
          return
        }
        if (normalized.shouldReconcile) {
          task.status = 'reconciling'
          task.updatedAt = new Date().toISOString()
          this.emit()
          try {
            const reconcileController = this.createAbortController(task)
            const offset = await getUploadOffset({
              baseUrl: this.config.baseUrl,
              token: this.config.token,
              uploadUrl: task.uploadUrl,
              signal: reconcileController.signal,
            })
            task.uploadedBytes = offset
            task.progressPct = Math.round((offset / task.file.size) * 100)
            task.updatedAt = new Date().toISOString()
            await this.persistTask(task)
            this.emit()
            continue
          } catch (reconcileError) {
            await this.handleTaskError(task, normalizeThrownError(reconcileError))
            return
          }
        }
        await this.handleTaskError(task, normalized)
        return
      }
    }

    if ((task.pauseRequested || task.manuallyPaused) && task.file) {
      task.status = 'paused'
      task.updatedAt = new Date().toISOString()
      await this.persistTask(task)
      this.emit()
    }
  }

  private async pollUntilTerminal(task: InternalTask): Promise<void> {
    if (!task.uploadUrl) {
      return
    }

    task.status = 'finalizing'
    task.updatedAt = new Date().toISOString()
    this.emit()

    let pollAttempts = 0
    while (!task.cancelRequested && !task.pauseRequested && !task.manuallyPaused && task.uploadUrl) {
      if (pollAttempts >= maxPollAttempts) {
        task.status = 'failed'
        task.errorCode = 'FINALIZATION_TIMEOUT'
        task.errorMessage = 'Server finalization timed out. The upload may still complete — retry to check.'
        task.retryable = false
        task.updatedAt = new Date().toISOString()
        await resolveValue(this.storage.delete(task.fileFingerprint))
        this.emit()
        return
      }

      let status: UploadStatusResponse
      try {
        const controller = this.createAbortController(task)
        status = await getUploadStatus({
          baseUrl: this.config.baseUrl,
          token: this.config.token,
          uploadUrl: task.uploadUrl,
          signal: controller.signal,
        })

        task.serverState = status.state
        task.uploadedBytes = status.committed_offset
        task.progressPct = task.fileSize > 0 ? Math.round((status.committed_offset / task.fileSize) * 100) : 0
        task.updatedAt = new Date().toISOString()

        const nextStatus = statusFromServerState(status.state)
        if (nextStatus === 'finalizing') {
          pollAttempts++
          await this.persistTask(task)
          this.emit()
          await wait(this.config.pollIntervalMs, controller.signal)
          continue
        }

        task.status = nextStatus
        task.artifactKey = status.storage.artifact_key ?? undefined
        task.errorCode = status.failure?.code || undefined
        task.errorMessage = status.failure?.message || undefined
        task.retryable = nextStatus === 'failed'
        await this.persistTask(task)
        await resolveValue(this.storage.delete(task.fileFingerprint))
        this.emit()
        return
      } catch (error) {
        await this.handleTaskError(task, normalizeThrownError(error))
        return
      }
    }

    if ((task.pauseRequested || task.manuallyPaused) && task.file) {
      task.status = 'paused'
      task.updatedAt = new Date().toISOString()
      await this.persistTask(task)
      this.emit()
    }
  }

  private async handleTaskError(task: InternalTask, error: UploadRequestError): Promise<void> {
    if (task.pauseRequested || task.manuallyPaused) {
      task.status = task.file ? 'paused' : 'needs_file'
      task.pauseRequested = false
      task.retryable = false
      task.errorCode = undefined
      task.errorMessage = undefined
      task.updatedAt = new Date().toISOString()
      await this.persistTask(task)
      this.emit()
      return
    }

    if (task.cancelRequested || error.code === 'REQUEST_ABORTED') {
      task.status = 'cancelled'
      task.retryable = false
      task.updatedAt = new Date().toISOString()
      await resolveValue(this.storage.delete(task.fileFingerprint))
      this.emit()
      return
    }

    if (error.shouldClearResume) {
      task.uploadUrl = undefined
      task.uploadId = undefined
      task.uploadedBytes = 0
      task.progressPct = 0
      task.status = error.terminalStatus ?? 'failed'
      task.errorCode = error.code
      task.errorMessage = error.message
      task.retryable = false
      task.updatedAt = new Date().toISOString()
      await resolveValue(this.storage.delete(task.fileFingerprint))
      this.emit()
      return
    }

    if (error.retryable) {
      task.status = task.file ? 'queued' : 'needs_file'
      task.errorCode = error.code
      task.errorMessage = error.message
      task.retryable = true
      task.retryCount += 1
      task.nextAttemptAt = Date.now() + this.nextBackoffMs(task.retryCount)
      task.updatedAt = new Date().toISOString()
      await this.persistTask(task)
      this.emit()
      window.setTimeout(() => this.schedule(), this.nextBackoffMs(task.retryCount))
      return
    }

    task.status = error.terminalStatus ?? 'failed'
    task.errorCode = error.code
    task.errorMessage = error.message
    task.retryable = false
    task.updatedAt = new Date().toISOString()
    await this.persistTask(task)
    this.emit()
  }

  private async persistTask(task: InternalTask): Promise<void> {
    if (isTerminalStatus(task.status) || task.status === 'cancelled') {
      await resolveValue(this.storage.delete(task.fileFingerprint))
      return
    }

    if ((!task.uploadUrl || !task.uploadId) && !task.manuallyPaused) {
      await resolveValue(this.storage.delete(task.fileFingerprint))
      return
    }

    const record: ResumeRecord = {
      localId: task.localId,
      fileFingerprint: task.fileFingerprint,
      fileName: task.fileName,
      fileSize: task.fileSize,
      fileType: task.fileType,
      lastModified: task.lastModified,
      uploadUrl: task.uploadUrl,
      uploadId: task.uploadId,
      baseUrl: this.config.baseUrl,
      chunkSizeBytes: task.chunkSizeBytes,
      uploadedBytes: task.uploadedBytes,
      status: task.manuallyPaused ? 'paused' : task.status,
      createdAt: task.createdAt,
      updatedAt: task.updatedAt,
      metadata: task.metadata,
    }
    await resolveValue(this.storage.set(record))
  }

  private nextBackoffMs(retryCount: number): number {
    const boundedCount = Math.min(retryCount, 6)
    return this.config.retryBaseDelayMs * 2 ** Math.max(0, boundedCount - 1)
  }

  private createAbortController(task: InternalTask): AbortController {
    const controller = new AbortController()
    task.abortController = controller
    return controller
  }

  private updateTask(task: InternalTask, updates: Partial<InternalTask>): void {
    Object.assign(task, updates, { updatedAt: new Date().toISOString() })
    this.emit()
  }
}
