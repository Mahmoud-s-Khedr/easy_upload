import type { UploadTaskSnapshot } from '../../lib/types'
import { UploadCard } from './UploadCard'

type UploadQueueProps = {
  tasks: UploadTaskSnapshot[]
  onPause: (localId: string) => void
  onResume: (localId: string) => void
  onCancel: (localId: string) => void
  onRetry: (localId: string) => void
  onRemove: (localId: string) => void
}

export function UploadQueue({ tasks, onPause, onResume, onCancel, onRetry, onRemove }: UploadQueueProps) {
  return (
    <section className="console-panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">Queue</p>
          <h2>Upload activity</h2>
        </div>
      </div>

      {tasks.length === 0 ? (
        <div className="empty-state">
          <strong>No uploads yet.</strong>
          <p>Add one or more files to start a resumable upload session.</p>
        </div>
      ) : (
        <div className="queue-grid">
          {tasks.map((task) => (
            <UploadCard
              key={task.localId}
              task={task}
              onPause={onPause}
              onResume={onResume}
              onCancel={onCancel}
              onRetry={onRetry}
              onRemove={onRemove}
            />
          ))}
        </div>
      )}
    </section>
  )
}
