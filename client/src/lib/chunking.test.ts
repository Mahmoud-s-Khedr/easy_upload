import { describe, expect, it } from 'vitest'
import { chooseChunkSize, MAX_CHUNK_SIZE, MEBIBYTE, roundUpToMiB } from './chunking'

describe('chunking', () => {
  it('rounds sizes up to the next mebibyte', () => {
    expect(roundUpToMiB(MEBIBYTE + 1)).toBe(2 * MEBIBYTE)
  })

  it('uses an 8 MiB minimum chunk size', () => {
    expect(chooseChunkSize(3 * MEBIBYTE)).toBe(8 * MEBIBYTE)
  })

  it('caps chunk size at 64 MiB', () => {
    expect(chooseChunkSize(900_000_000_000)).toBe(MAX_CHUNK_SIZE)
  })
})
