import { formatTimestamp } from '../../lib/time'
import type { UploadTaskSnapshot } from '../../lib/types'

type UploadCardProps = {
  task: UploadTaskSnapshot
  onPause: (localId: string) => void
  onResume: (localId: string) => void
  onCancel: (localId: string) => void
  onRetry: (localId: string) => void
  onRemove: (localId: string) => void
}

function formatBytes(bytes: number): string {
  if (bytes === 0) {
    return '0 B'
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** exponent
  return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${units[exponent]}`
}

function statusLabel(task: UploadTaskSnapshot): string {
  switch (task.status) {
    case 'queued':
      return task.retryable ? 'Retry queued' : 'Queued'
    case 'creating':
      return 'Creating session'
    case 'reconciling':
      return 'Reconciling offset'
    case 'uploading':
      return 'Uploading bytes'
    case 'paused':
      return 'Paused'
    case 'finalizing':
      return 'Waiting for finalization'
    case 'completed':
      return 'Completed'
    case 'failed':
      return 'Failed'
    case 'aborted':
      return 'Aborted'
    case 'expired':
      return 'Expired'
    case 'cancelled':
      return 'Cancelled'
    default:
      return task.status
  }
}

function stateTone(status: UploadTaskSnapshot['status']): string {
  switch (status) {
    case 'completed':
      return 'is-success'
    case 'failed':
    case 'aborted':
    case 'expired':
    case 'cancelled':
      return 'is-danger'
    case 'finalizing':
    case 'reconciling':
    case 'paused':
      return 'is-warn'
    default:
      return 'is-neutral'
  }
}

export function UploadCard({ task, onPause, onResume, onCancel, onRetry, onRemove }: UploadCardProps) {
  const isTerminal = ['completed', 'failed', 'aborted', 'expired', 'cancelled'].includes(task.status)
  const canPause = ['queued', 'creating', 'reconciling', 'uploading', 'finalizing'].includes(task.status)
  const canResume = task.status === 'paused'
  const canRetry = ['failed', 'aborted', 'expired'].includes(task.status)

  return (
    <article className="upload-card">
      <div className="card-header">
        <div>
          <h3>{task.fileName}</h3>
          <p>{formatBytes(task.fileSize)}</p>
        </div>
        <span className={`state-chip ${stateTone(task.status)}`}>{statusLabel(task)}</span>
      </div>

      <div className="progress-row">
        <div className="progress-track">
          <span style={{ width: `${Math.min(100, task.progressPct)}%` }} />
        </div>
        <strong>{task.progressPct}%</strong>
      </div>

      <dl className="meta-grid">
        <div>
          <dt>Uploaded</dt>
          <dd>
            {formatBytes(task.uploadedBytes)} / {formatBytes(task.fileSize)}
          </dd>
        </div>
        <div>
          <dt>Chunk size</dt>
          <dd>{formatBytes(task.chunkSizeBytes)}</dd>
        </div>
        <div>
          <dt>Upload ID</dt>
          <dd>{task.uploadId || 'not created yet'}</dd>
        </div>
        <div>
          <dt>Updated</dt>
          <dd>{formatTimestamp(task.updatedAt)}</dd>
        </div>
        {task.artifactKey ? (
          <div className="full-width">
            <dt>Artifact key</dt>
            <dd>{task.artifactKey}</dd>
          </div>
        ) : null}
        {task.errorCode || task.errorMessage ? (
          <div className="full-width">
            <dt>Last error</dt>
            <dd>
              <strong>{task.errorCode || 'UPLOAD_ERROR'}</strong>
              <span>{task.errorMessage || 'Unknown error'}</span>
            </dd>
          </div>
        ) : null}
      </dl>

      <div className="card-actions">
        {canPause ? <button onClick={() => onPause(task.localId)}>Pause</button> : null}
        {canResume ? <button onClick={() => onResume(task.localId)}>Resume</button> : null}
        {!isTerminal ? <button onClick={() => onCancel(task.localId)}>Cancel</button> : null}
        {canRetry ? <button onClick={() => onRetry(task.localId)}>Retry</button> : null}
        {isTerminal ? (
          <button className="ghost-button" onClick={() => onRemove(task.localId)}>
            Remove
          </button>
        ) : null}
      </div>
    </article>
  )
}
