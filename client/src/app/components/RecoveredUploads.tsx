import type { UploadTaskSnapshot } from '../../lib/types'

type RecoveredUploadsProps = {
  tasks: UploadTaskSnapshot[]
  onAttach: (localId: string, file: File) => void
}

export function RecoveredUploads({ tasks, onAttach }: RecoveredUploadsProps) {
  if (tasks.length === 0) {
    return null
  }

  return (
    <section className="console-panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">Recovered State</p>
          <h2>Reattach files to resume</h2>
        </div>
      </div>

      <div className="recovered-grid">
        {tasks.map((task) => (
          <article key={task.localId} className="recovered-card">
            <div>
              <h3>{task.fileName}</h3>
              <p>{Math.round(task.fileSize / (1024 * 1024)) || 1} MiB saved locally</p>
            </div>
            <label className="ghost-button">
              <span>Select original file</span>
              <input
                type="file"
                onChange={(event) => {
                  const file = event.target.files?.[0]
                  if (file) {
                    onAttach(task.localId, file)
                    event.target.value = ''
                  }
                }}
              />
            </label>
          </article>
        ))}
      </div>
    </section>
  )
}
