import type { UploadTaskStatus } from './types'

export type NormalizedUploadError = {
  status: number | null
  code: string
  message: string
  retryable: boolean
  shouldReconcile: boolean
  shouldClearResume: boolean
  terminalStatus?: Extract<UploadTaskStatus, 'failed' | 'aborted' | 'expired'>
}

type ErrorPayload = {
  error?: {
    code?: string
    message?: string
    retryable?: boolean
  }
}

export class UploadRequestError extends Error {
  status: number | null
  code: string
  retryable: boolean
  shouldReconcile: boolean
  shouldClearResume: boolean
  terminalStatus?: Extract<UploadTaskStatus, 'failed' | 'aborted' | 'expired'>

  constructor(details: NormalizedUploadError) {
    super(details.message)
    this.name = 'UploadRequestError'
    this.status = details.status
    this.code = details.code
    this.retryable = details.retryable
    this.shouldReconcile = details.shouldReconcile
    this.shouldClearResume = details.shouldClearResume
    this.terminalStatus = details.terminalStatus
  }
}

function mapTerminalStatus(code: string): Extract<UploadTaskStatus, 'failed' | 'aborted' | 'expired'> | undefined {
  switch (code) {
    case 'UPLOAD_ABORTED':
      return 'aborted'
    case 'UPLOAD_EXPIRED':
      return 'expired'
    case 'UPLOAD_FAILED':
      return 'failed'
    default:
      return undefined
  }
}

export async function errorFromResponse(response: Response): Promise<UploadRequestError> {
  let payload: ErrorPayload = {}
  const contentType = response.headers.get('Content-Type') || ''

  if (contentType.includes('application/json')) {
    payload = (await response.json()) as ErrorPayload
  }

  const code = payload.error?.code || `HTTP_${response.status}`
  const message = payload.error?.message || response.statusText || 'Upload request failed'

  const details: NormalizedUploadError = {
    status: response.status,
    code,
    message,
    retryable: payload.error?.retryable ?? response.status >= 500,
    shouldReconcile: false,
    shouldClearResume: false,
  }

  switch (response.status) {
    case 401:
      details.retryable = false
      break
    case 409:
      details.retryable = true
      details.shouldReconcile = true
      break
    case 410:
      details.retryable = false
      details.shouldClearResume = true
      details.terminalStatus = mapTerminalStatus(code)
      break
    case 413:
      details.retryable = false
      break
    case 422:
    case 460:
      if (code === 'CHECKSUM_MISMATCH') {
        details.retryable = true
      }
      break
    case 503:
      details.retryable = true
      break
    default:
      if (code === 'CHECKSUM_MISMATCH') {
        details.retryable = true
      }
      break
  }

  return new UploadRequestError(details)
}

export function normalizeThrownError(error: unknown): UploadRequestError {
  if (error instanceof UploadRequestError) {
    return error
  }
  if (error instanceof DOMException && error.name === 'AbortError') {
    return new UploadRequestError({
      status: null,
      code: 'REQUEST_ABORTED',
      message: 'Upload request was aborted',
      retryable: false,
      shouldReconcile: false,
      shouldClearResume: false,
    })
  }
  if (error instanceof Error) {
    return new UploadRequestError({
      status: null,
      code: 'NETWORK_ERROR',
      message: error.message || 'Network request failed',
      retryable: true,
      shouldReconcile: true,
      shouldClearResume: false,
    })
  }
  return new UploadRequestError({
    status: null,
    code: 'UNKNOWN_ERROR',
    message: 'Unknown upload error',
    retryable: false,
    shouldReconcile: false,
    shouldClearResume: false,
  })
}
