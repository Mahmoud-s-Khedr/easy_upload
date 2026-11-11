import { describe, expect, it } from 'vitest'
import { buildUploadMetadata, encodeUploadMetadata } from './metadata'

describe('metadata', () => {
  it('fills browser defaults from the file object', () => {
    const file = new File(['hello'], 'clip.mov', { type: 'video/quicktime', lastModified: 7 })

    expect(buildUploadMetadata(file)).toEqual({
      filename: 'clip.mov',
      contentType: 'video/quicktime',
      sha256: undefined,
      callbackUrl: undefined,
      objectKeyHint: undefined,
    })
  })

  it('encodes only supported metadata keys in server order', () => {
    const header = encodeUploadMetadata({
      filename: 'clip.mov',
      contentType: 'video/quicktime',
      objectKeyHint: 'portfolio/clip.mov',
      callbackUrl: 'https://example.com/hook',
    })

    expect(header).toBe(
      'filename Y2xpcC5tb3Y=,contentType dmlkZW8vcXVpY2t0aW1l,callbackUrl aHR0cHM6Ly9leGFtcGxlLmNvbS9ob29r,objectKeyHint cG9ydGZvbGlvL2NsaXAubW92',
    )
  })
})
