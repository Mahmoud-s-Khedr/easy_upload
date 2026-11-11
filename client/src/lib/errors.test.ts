import { describe, expect, it } from 'vitest'
import { errorFromResponse, normalizeThrownError } from './errors'

describe('errors', () => {
  it('normalizes server retry and reconcile semantics', async () => {
    const response = new Response(
      JSON.stringify({
        error: {
          code: 'OFFSET_MISMATCH',
          message: 'offset does not match',
          retryable: true,
        },
      }),
      { status: 409, headers: { 'Content-Type': 'application/json' } },
    )

    const error = await errorFromResponse(response)

    expect(error.code).toBe('OFFSET_MISMATCH')
    expect(error.retryable).toBe(true)
    expect(error.shouldReconcile).toBe(true)
  })

  it('treats checksum mismatch as retryable for 422 and 460 responses', async () => {
    const response = new Response(
      JSON.stringify({
        error: {
          code: 'CHECKSUM_MISMATCH',
          message: 'digest mismatch',
        },
      }),
      { status: 460, headers: { 'Content-Type': 'application/json' } },
    )

    const error = await errorFromResponse(response)

    expect(error.code).toBe('CHECKSUM_MISMATCH')
    expect(error.retryable).toBe(true)
  })

  it('marks network failures as retryable ambiguous errors', () => {
    const error = normalizeThrownError(new Error('socket closed'))

    expect(error.code).toBe('NETWORK_ERROR')
    expect(error.retryable).toBe(true)
    expect(error.shouldReconcile).toBe(true)
  })
})
