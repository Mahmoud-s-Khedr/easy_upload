import type { UploadMetadata } from './types'

const allowedMetadataKeys = ['filename', 'contentType', 'sha256', 'callbackUrl', 'objectKeyHint'] as const

function encodeMetadataValue(value: string): string {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }
  return btoa(binary)
}

export function buildUploadMetadata(file: File, metadata: UploadMetadata = {}): UploadMetadata {
  return {
    filename: metadata.filename ?? file.name,
    contentType: metadata.contentType ?? (file.type || 'application/octet-stream'),
    sha256: metadata.sha256,
    callbackUrl: metadata.callbackUrl,
    objectKeyHint: metadata.objectKeyHint,
  }
}

export function encodeUploadMetadata(metadata: UploadMetadata): string | undefined {
  const entries: string[] = []
  for (const key of allowedMetadataKeys) {
    const value = metadata[key]
    if (!value) {
      continue
    }
    entries.push(`${key} ${encodeMetadataValue(value)}`)
  }
  return entries.length > 0 ? entries.join(',') : undefined
}
