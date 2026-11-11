import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { UploadCard } from './UploadCard'
import type { UploadTaskSnapshot } from '../../lib/types'

function buildTask(status: UploadTaskSnapshot['status']): UploadTaskSnapshot {
  return {
    localId: 'task-1',
    fileFingerprint: 'file.bin:10:1:application/octet-stream',
    fileName: 'file.bin',
    fileSize: 10,
    uploadedBytes: status === 'paused' ? 5 : 3,
    progressPct: status === 'paused' ? 50 : 30,
    chunkSizeBytes: 8 * 1024 * 1024,
    status,
    uploadUrl: 'http://localhost:8080/v1/uploads/upl_1',
    uploadId: 'upl_1',
    createdAt: '2026-03-01T00:00:00.000Z',
    updatedAt: '2026-03-01T00:00:01.000Z',
  }
}

describe('UploadCard', () => {
  it('shows Pause and Cancel for active uploads', () => {
    const markup = renderToStaticMarkup(
      <UploadCard
        task={buildTask('uploading')}
        onPause={vi.fn()}
        onResume={vi.fn()}
        onCancel={vi.fn()}
        onRetry={vi.fn()}
        onRemove={vi.fn()}
      />,
    )

    expect(markup).toContain('Pause')
    expect(markup).toContain('Cancel')
    expect(markup).not.toContain('<button>Resume</button>')
    expect(markup).not.toContain('<button>Retry</button>')
  })

  it('shows Resume without Retry for paused uploads', () => {
    const markup = renderToStaticMarkup(
      <UploadCard
        task={buildTask('paused')}
        onPause={vi.fn()}
        onResume={vi.fn()}
        onCancel={vi.fn()}
        onRetry={vi.fn()}
        onRemove={vi.fn()}
      />,
    )

    expect(markup).toContain('Resume')
    expect(markup).toContain('Cancel')
    expect(markup).not.toContain('<button>Pause</button>')
    expect(markup).not.toContain('<button>Retry</button>')
  })
})
