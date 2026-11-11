import type { Dispatch, SetStateAction } from 'react'

type AppSettings = {
  baseUrl: string
  token: string
  concurrency: number
}

type ConnectionPanelProps = {
  settings: AppSettings
  onSettingsChange: Dispatch<SetStateAction<AppSettings>>
  onFilesSelected: (files: FileList) => void
}

export function ConnectionPanel({ settings, onSettingsChange, onFilesSelected }: ConnectionPanelProps) {
  return (
    <section className="console-panel">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">Control Surface</p>
          <h2>Connection and intake</h2>
        </div>
        <label className="file-picker">
          <span>Add files</span>
          <input
            type="file"
            multiple
            onChange={(event) => {
              if (event.target.files && event.target.files.length > 0) {
                onFilesSelected(event.target.files)
                event.target.value = ''
              }
            }}
          />
        </label>
      </div>

      <div className="settings-grid">
        <label>
          <span>API base URL</span>
          <input
            type="url"
            value={settings.baseUrl}
            onChange={(event) => onSettingsChange((current) => ({ ...current, baseUrl: event.target.value }))}
          />
        </label>

        <label>
          <span>Bearer token</span>
          <input
            type="text"
            value={settings.token}
            onChange={(event) => onSettingsChange((current) => ({ ...current, token: event.target.value }))}
          />
        </label>

        <label>
          <span>Queue concurrency</span>
          <input
            type="number"
            min={1}
            max={6}
            value={settings.concurrency}
            onChange={(event) =>
              onSettingsChange((current) => ({
                ...current,
                concurrency: Math.max(1, Math.min(6, Number(event.target.value) || 1)),
              }))
            }
          />
        </label>
      </div>
    </section>
  )
}
