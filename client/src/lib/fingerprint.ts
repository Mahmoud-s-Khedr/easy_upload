export function buildFileFingerprint(file: Pick<File, 'name' | 'size' | 'lastModified' | 'type'>): string {
  return [file.name, String(file.size), String(file.lastModified), file.type || 'application/octet-stream'].join(':')
}
