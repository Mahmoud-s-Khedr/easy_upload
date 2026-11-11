const MEBIBYTE = 1024 * 1024
const MIN_CHUNK_SIZE = 8 * MEBIBYTE
const MAX_CHUNK_SIZE = 64 * MEBIBYTE
const MAX_PARTS = 10_000

export function roundUpToMiB(bytes: number): number {
  return Math.ceil(bytes / MEBIBYTE) * MEBIBYTE
}

export function chooseChunkSize(fileSize: number): number {
  const adaptiveSize = roundUpToMiB(Math.ceil(fileSize / MAX_PARTS))
  return Math.min(MAX_CHUNK_SIZE, Math.max(MIN_CHUNK_SIZE, adaptiveSize))
}

export { MAX_CHUNK_SIZE, MIN_CHUNK_SIZE, MEBIBYTE }
