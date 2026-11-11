import { describe, expect, it } from 'vitest'
import { buildFileFingerprint } from './fingerprint'

describe('fingerprint', () => {
  it('uses the stable browser fingerprint format', () => {
    const file = new File(['cv'], 'resume.pdf', { type: 'application/pdf', lastModified: 42 })

    expect(buildFileFingerprint(file)).toBe('resume.pdf:2:42:application/pdf')
  })
})
