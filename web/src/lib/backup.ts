// Text for Settings → Backup and Settings → Privacy. Pure functions, tested in backup.test.ts.

import type { BackupKind, UserDataDeleted } from '../api/client'
import { plural } from './format'
import { formatBytes } from './status'

/** PG_DUMP is the backup command the pages offer for a Postgres database. */
export const PG_DUMP = 'pg_dump --format=custom --file=monitor.dump "$DATABASE_URL"'

/** PG_RESTORE puts a PG_DUMP file back. */
export const PG_RESTORE = 'pg_restore --clean --dbname="$DATABASE_URL" monitor.dump'

/** backupKindLabel says why an automatic backup was made: "Before a database update (version 7)". */
export function backupKindLabel(kind: BackupKind, version: number): string {
  return `${kind === 'pre-restore' ? 'Before a restore' : 'Before a database update'} (version ${version})`
}

/** shellWord quotes a path for a shell when it needs it. */
function shellWord(s: string): string {
  return /^[\w./:@%+=,-]+$/.test(s) ? s : `'${s.replaceAll("'", "'\\''")}'`
}

/** restoreCommand restores one of the automatic backups from the server's command line. */
export function restoreCommand(backupsDir: string, name: string): string {
  return `monitor restore ${shellWord(`${backupsDir.replace(/\/+$/, '')}/${name}`)}`
}

/** uploadProblem says why a file cannot be sent as a backup, or "" when it can. */
export function uploadProblem(file: { size: number }, maxUploadBytes: number): string {
  if (file.size === 0) return 'The file is empty: choose a backup file.'
  if (file.size > maxUploadBytes) return `The file is ${formatBytes(file.size)}; at most ${formatBytes(maxUploadBytes)} fits now. Nothing was sent.`
  return ''
}

/**
 * downloadProblem says why the server refused a download, by the status of
 * its answer to the check before it (a HEAD answer has no text); 0 means
 * no answer at all.
 */
export function downloadProblem(status: number): string {
  if (status === 0) return 'The tool did not answer. Try again.'
  if (status === 409) return 'A backup is being made already. Try again when it is done.'
  if (status === 507) return 'There is not enough free disk space for the copy a download makes. Free up disk space first.'
  return `The backup could not be made (error ${status}). The server log says why.`
}

/** listing joins words as people do: "A", "A and B", "A, B and C". */
function listing(words: string[]): string {
  if (words.length <= 1) return words.join('')
  return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
}

/** deletedSentences tell what deleting a user's data removed. */
export function deletedSentences(d: UserDataDeleted): string[] {
  const where = d.projects === 1 ? '1 project' : `${d.projects} projects`
  if (d.rows === 0) return [`Nothing was stored with that user id in ${d.projects === 1 ? 'that project' : `the ${where}`}.`]

  const parts: string[] = []
  if (d.errorEvents > 0) parts.push(plural(d.errorEvents, 'error event'))
  if (d.pageviews > 0) {
    const leaves = d.pageleaves > 0 ? ` (with ${d.pageviews === 1 ? 'its' : 'their'} ${plural(d.pageleaves, 'page leave')})` : ''
    parts.push(plural(d.pageviews, 'page view') + leaves)
  }
  if (d.customEvents > 0) parts.push(plural(d.customEvents, 'custom event'))
  const sentences = [`Deleted ${listing(parts)} of that user id in ${where}.`]

  const issues: string[] = []
  if (d.issuesDeleted > 0) issues.push(`${plural(d.issuesDeleted, 'issue')} had no other events and ${d.issuesDeleted === 1 ? 'was' : 'were'} removed`)
  if (d.issuesUpdated > 0) issues.push(`${plural(d.issuesUpdated, 'issue')} ${d.issuesUpdated === 1 ? 'was' : 'were'} recounted`)
  if (issues.length > 0) sentences.push(`${issues.join('; ')}.`)
  if (d.errorEvents + d.pageviews + d.customEvents > 0) sentences.push('The page and event statistics drop them within a minute.')
  return sentences
}

/** confirmDeleteSentence asks before a user's data is deleted; no project names means every project. */
export function confirmDeleteSentence(userId: string, projectNames: string[]): string {
  const where = projectNames.length === 0 ? 'every project' : listing(projectNames)
  return `Delete everything stored with the user id “${userId}” in ${where}? This cannot be undone.`
}
