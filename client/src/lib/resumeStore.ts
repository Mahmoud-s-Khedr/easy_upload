import type { ResumeRecord, ResumeStore } from './types'

const STORAGE_PREFIX = 'easy-upload:resume:'

function isResumeRecord(value: unknown): value is ResumeRecord {
  if (!value || typeof value !== 'object') {
    return false
  }
  const record = value as Partial<ResumeRecord>
  return Boolean(
    typeof record.localId === 'string' &&
      typeof record.fileFingerprint === 'string' &&
      typeof record.fileName === 'string' &&
      typeof record.fileSize === 'number' &&
      (typeof record.uploadUrl === 'string' || typeof record.uploadUrl === 'undefined') &&
      (typeof record.uploadId === 'string' || typeof record.uploadId === 'undefined'),
  )
}

export class LocalStorageResumeStore implements ResumeStore {
  list(): ResumeRecord[] {
    const records: ResumeRecord[] = []
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index)
      if (!key || !key.startsWith(STORAGE_PREFIX)) {
        continue
      }
      const raw = localStorage.getItem(key)
      if (!raw) {
        continue
      }
      try {
        const parsed = JSON.parse(raw) as unknown
        if (isResumeRecord(parsed)) {
          records.push(parsed)
          continue
        }
      } catch {
        // Malformed entries are deleted so future loads remain stable.
      }
      localStorage.removeItem(key)
    }
    return records.sort((left, right) => left.createdAt.localeCompare(right.createdAt))
  }

  get(fingerprint: string): ResumeRecord | null {
    const raw = localStorage.getItem(this.key(fingerprint))
    if (!raw) {
      return null
    }
    try {
      const parsed = JSON.parse(raw) as unknown
      if (isResumeRecord(parsed)) {
        return parsed
      }
    } catch {
      // Invalid JSON is treated as missing and cleaned up.
    }
    localStorage.removeItem(this.key(fingerprint))
    return null
  }

  set(record: ResumeRecord): void {
    localStorage.setItem(this.key(record.fileFingerprint), JSON.stringify(record))
  }

  delete(fingerprint: string): void {
    localStorage.removeItem(this.key(fingerprint))
  }

  private key(fingerprint: string): string {
    return `${STORAGE_PREFIX}${fingerprint}`
  }
}
