export type UploadMetadata = {
  filename?: string
  contentType?: string
  sha256?: string
  callbackUrl?: string
  objectKeyHint?: string
}

export type UploadTaskStatus =
  | 'queued'
  | 'creating'
  | 'reconciling'
  | 'uploading'
  | 'paused'
  | 'finalizing'
  | 'completed'
  | 'failed'
  | 'aborted'
  | 'expired'
  | 'cancelled'
  | 'needs_file'

export type ResumeRecord = {
  localId: string
  fileFingerprint: string
  fileName: string
  fileSize: number
  fileType: string
  lastModified: number
  uploadUrl?: string
  uploadId?: string
  baseUrl: string
  chunkSizeBytes: number
  uploadedBytes: number
  status: UploadTaskStatus
  createdAt: string
  updatedAt: string
  metadata: UploadMetadata
}

export type UploadTaskSnapshot = {
  localId: string
  fileFingerprint: string
  fileName: string
  fileSize: number
  uploadedBytes: number
  progressPct: number
  chunkSizeBytes: number
  status: UploadTaskStatus
  uploadUrl?: string
  uploadId?: string
  serverState?: string
  artifactKey?: string
  errorCode?: string
  errorMessage?: string
  retryable?: boolean
  createdAt: string
  updatedAt: string
}

export type EasyUploadManagerConfig = {
  baseUrl: string
  token: string
  concurrency?: number
  pollIntervalMs?: number
  retryBaseDelayMs?: number
  storage?: ResumeStore
}

export interface ResumeStore {
  get(fingerprint: string): Promise<ResumeRecord | null> | ResumeRecord | null
  set(record: ResumeRecord): Promise<void> | void
  delete(fingerprint: string): Promise<void> | void
  list(): Promise<ResumeRecord[]> | ResumeRecord[]
}

export interface EasyUploadManager {
  addFiles(files: File[]): string[]
  attachFile(localId: string, file: File): void
  pause(localId: string): Promise<void>
  resume(localId: string): void
  cancel(localId: string): Promise<void>
  retry(localId: string): void
  remove(localId: string): void
  subscribe(listener: (tasks: UploadTaskSnapshot[]) => void): () => void
  getSnapshot(): UploadTaskSnapshot[]
}

export type UploadStatusResponse = {
  id: string
  state: string
  declared_length: number
  committed_offset: number
  expires_at: string
  metadata: UploadMetadata
  checksum: {
    algorithm: string
    full_file_digest: string | null
  }
  storage: {
    backend: string
    artifact_key: string | null
  }
  failure: {
    code: string
    message: string
  } | null
  created_at: string
  updated_at: string
  completed_at: string | null
}
