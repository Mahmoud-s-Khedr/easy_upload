import { useDeferredValue, useEffect, useState, useSyncExternalStore } from 'react'
import { BrowserEasyUploadManager } from '../lib/manager'
import { LocalStorageResumeStore } from '../lib/resumeStore'
import { ConnectionPanel } from './components/ConnectionPanel'
import { RecoveredUploads } from './components/RecoveredUploads'
import { UploadQueue } from './components/UploadQueue'

type AppSettings = {
  baseUrl: string
  token: string
  concurrency: number
}

const settingsStorageKey = 'easy-upload:console:settings'

const defaultSettings: AppSettings = {
  baseUrl: 'http://localhost:8080',
  token: 'dev-token',
  concurrency: 3,
}

function loadSettings(): AppSettings {
  const raw = localStorage.getItem(settingsStorageKey)
  if (!raw) {
    return defaultSettings
  }
  try {
    const parsed = JSON.parse(raw) as Partial<AppSettings>
    return {
      baseUrl: parsed.baseUrl || defaultSettings.baseUrl,
      token: parsed.token || defaultSettings.token,
      concurrency: Number(parsed.concurrency) || defaultSettings.concurrency,
    }
  } catch {
    return defaultSettings
  }
}

export function App() {
  const [settings, setSettings] = useState<AppSettings>(loadSettings)
  const [manager] = useState(
    () =>
      new BrowserEasyUploadManager({
        ...settings,
        storage: new LocalStorageResumeStore(),
      }),
  )
  const tasks = useSyncExternalStore(manager.subscribe.bind(manager), manager.getSnapshot.bind(manager))
  const deferredTasks = useDeferredValue(tasks)

  useEffect(() => {
    manager.updateConfig(settings)
    localStorage.setItem(settingsStorageKey, JSON.stringify(settings))
  }, [manager, settings])

  const recoveredTasks = deferredTasks.filter((task) => task.status === 'needs_file')
  const queueTasks = deferredTasks.filter((task) => task.status !== 'needs_file')
  const activeCount = queueTasks.filter((task) =>
    ['creating', 'reconciling', 'uploading', 'finalizing', 'queued'].includes(task.status),
  ).length
  const completedCount = queueTasks.filter((task) => task.status === 'completed').length

  return (
    <main className="page-shell">
      <section className="hero-panel">
        <div>
          <p className="eyebrow">Easy Upload</p>
          <h1>Resumable Upload Console</h1>
        </div>
        <div className="hero-stats">
          <div>
            <span>Queue</span>
            <strong>{queueTasks.length}</strong>
          </div>
          <div>
            <span>Active</span>
            <strong>{activeCount}</strong>
          </div>
          <div>
            <span>Completed</span>
            <strong>{completedCount}</strong>
          </div>
        </div>
      </section>

      <ConnectionPanel
        settings={settings}
        onSettingsChange={setSettings}
        onFilesSelected={(files) => manager.addFiles(Array.from(files))}
      />

      <RecoveredUploads tasks={recoveredTasks} onAttach={(localId, file) => manager.attachFile(localId, file)} />
      <UploadQueue
        tasks={queueTasks}
        onPause={(localId) => void manager.pause(localId)}
        onResume={(localId) => manager.resume(localId)}
        onCancel={(localId) => void manager.cancel(localId)}
        onRetry={(localId) => manager.retry(localId)}
        onRemove={(localId) => manager.remove(localId)}
      />
    </main>
  )
}
