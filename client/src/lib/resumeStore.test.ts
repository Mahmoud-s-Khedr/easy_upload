import { beforeEach, describe, expect, it } from 'vitest'
import { LocalStorageResumeStore } from './resumeStore'

describe('resume store', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('persists, loads, lists, and deletes records', () => {
    const store = new LocalStorageResumeStore()
    const record = {
      localId: 'local-1',
      fileFingerprint: 'resume.pdf:4:9:application/pdf',
      fileName: 'resume.pdf',
      fileSize: 4,
      fileType: 'application/pdf',
      lastModified: 9,
      uploadUrl: 'http://localhost:8080/v1/uploads/upl_1',
      uploadId: 'upl_1',
      baseUrl: 'http://localhost:8080',
      chunkSizeBytes: 8 * 1024 * 1024,
      uploadedBytes: 2,
      status: 'uploading' as const,
      createdAt: '2026-02-28T00:00:00.000Z',
      updatedAt: '2026-02-28T00:00:01.000Z',
      metadata: { filename: 'resume.pdf', contentType: 'application/pdf' },
    }

    store.set(record)

    expect(store.get(record.fileFingerprint)).toEqual(record)
    expect(store.list()).toEqual([record])

    store.delete(record.fileFingerprint)
    expect(store.get(record.fileFingerprint)).toBeNull()
  })

  it('cleans malformed entries during list', () => {
    localStorage.setItem('easy-upload:resume:broken', '{oops')
    const store = new LocalStorageResumeStore()

    expect(store.list()).toEqual([])
    expect(localStorage.getItem('easy-upload:resume:broken')).toBeNull()
  })
})
