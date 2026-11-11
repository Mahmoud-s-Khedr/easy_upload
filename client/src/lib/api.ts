import { errorFromResponse } from './errors'
import { encodeUploadMetadata } from './metadata'
import type { UploadMetadata, UploadStatusResponse } from './types'

const TUS_VERSION = '1.0.0'

type RequestConfig = {
  baseUrl: string
  token: string
}

type CreateUploadParams = RequestConfig & {
  fileSize: number
  metadata: UploadMetadata
  signal?: AbortSignal
}

type PatchUploadParams = RequestConfig & {
  uploadUrl: string
  offset: number
  chunk: Blob
  checksum: string
  signal?: AbortSignal
}

type RequestHeaders = Record<string, string>

function buildHeaders(token: string, headers: RequestHeaders): RequestHeaders {
  return {
    Authorization: `Bearer ${token}`,
    ...headers,
  }
}

function resolveUploadUrl(baseUrl: string, uploadUrl: string): string {
  return new URL(uploadUrl, baseUrl).toString()
}

function getUploadId(uploadUrl: string): string {
  const pathname = new URL(uploadUrl).pathname
  return pathname.split('/').pop() || ''
}

export async function createUploadSession(params: CreateUploadParams): Promise<{
  uploadUrl: string
  uploadId: string
  offset: number
}> {
  const response = await fetch(new URL('/v1/uploads', params.baseUrl), {
    method: 'POST',
    headers: buildHeaders(params.token, {
      'Tus-Resumable': TUS_VERSION,
      'Upload-Length': String(params.fileSize),
      ...(encodeUploadMetadata(params.metadata) ? { 'Upload-Metadata': encodeUploadMetadata(params.metadata)! } : {}),
    }),
    signal: params.signal,
  })

  if (!response.ok) {
    throw await errorFromResponse(response)
  }

  const location = response.headers.get('Location')
  if (!location) {
    throw new Error('Upload create response did not include Location header')
  }

  const uploadUrl = resolveUploadUrl(params.baseUrl, location)
  return {
    uploadUrl,
    uploadId: getUploadId(uploadUrl),
    offset: Number(response.headers.get('Upload-Offset') || '0'),
  }
}

export async function getUploadOffset(params: RequestConfig & { uploadUrl: string; signal?: AbortSignal }): Promise<number> {
  const response = await fetch(params.uploadUrl, {
    method: 'HEAD',
    headers: buildHeaders(params.token, {
      'Tus-Resumable': TUS_VERSION,
    }),
    signal: params.signal,
  })

  if (response.status !== 204) {
    throw await errorFromResponse(response)
  }

  return Number(response.headers.get('Upload-Offset') || '0')
}

export async function patchUploadChunk(params: PatchUploadParams): Promise<number> {
  const response = await fetch(params.uploadUrl, {
    method: 'PATCH',
    headers: buildHeaders(params.token, {
      'Tus-Resumable': TUS_VERSION,
      'Content-Type': 'application/offset+octet-stream',
      'Upload-Offset': String(params.offset),
      'Upload-Checksum': `sha256 ${params.checksum}`,
    }),
    body: params.chunk,
    signal: params.signal,
  })

  if (response.status !== 204) {
    throw await errorFromResponse(response)
  }

  return Number(response.headers.get('Upload-Offset') || String(params.offset + params.chunk.size))
}

export async function getUploadStatus(params: RequestConfig & { uploadUrl: string; signal?: AbortSignal }): Promise<UploadStatusResponse> {
  const response = await fetch(params.uploadUrl, {
    method: 'GET',
    headers: buildHeaders(params.token, {}),
    signal: params.signal,
  })

  if (!response.ok) {
    throw await errorFromResponse(response)
  }

  return (await response.json()) as UploadStatusResponse
}

export async function deleteUploadSession(params: RequestConfig & { uploadUrl: string }): Promise<void> {
  const response = await fetch(params.uploadUrl, {
    method: 'DELETE',
    headers: buildHeaders(params.token, {
      'Tus-Resumable': TUS_VERSION,
    }),
  })

  if (response.status !== 204) {
    throw await errorFromResponse(response)
  }
}
