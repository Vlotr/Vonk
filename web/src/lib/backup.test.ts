import { expect, test } from 'vitest'
import type { UserDataDeleted } from '../api/client'
import { backupKindLabel, confirmDeleteSentence, deletedSentences, restoreCommand, uploadProblem } from './backup'

test('automatic backups say why they were made', () => {
  expect(backupKindLabel('pre-migrate', 7)).toBe('Before a database update (version 7)')
  expect(backupKindLabel('pre-restore', 8)).toBe('Before a restore (version 8)')
})

test('the command restores a backup by its path on the server', () => {
  expect(restoreCommand('/data/backups', 'pre-migrate-0007-20261015T080000Z.db')).toBe(
    'monitor restore /data/backups/pre-migrate-0007-20261015T080000Z.db',
  )
  expect(restoreCommand('/data/backups/', 'a b.db')).toBe("monitor restore '/data/backups/a b.db'")
})

test('a file that is too large or empty is refused before it is sent', () => {
  expect(uploadProblem({ size: 2 * 2 ** 30 }, 2 ** 30)).toBe('The file is 2 GiB; at most 1 GiB fits now. Nothing was sent.')
  expect(uploadProblem({ size: 0 }, 2 ** 30)).toBe('The file is empty: choose a backup file.')
  expect(uploadProblem({ size: 4096 }, 2 ** 30)).toBe('')
})

const nothing: UserDataDeleted = {
  projects: 2,
  errorEvents: 0,
  pageviews: 0,
  pageleaves: 0,
  customEvents: 0,
  issuesDeleted: 0,
  issuesUpdated: 0,
  rows: 0,
}

test('the result of a deletion says what went', () => {
  expect(deletedSentences({ ...nothing })).toEqual(['Nothing was stored with that user id in the 2 projects.'])
  expect(
    deletedSentences({ ...nothing, projects: 1, errorEvents: 3, pageviews: 1, pageleaves: 1, customEvents: 2, issuesDeleted: 1, issuesUpdated: 2, rows: 8 }),
  ).toEqual([
    'Deleted 3 error events, 1 page view (with its 1 page leave) and 2 custom events of that user id in 1 project.',
    '1 issue had no other events and was removed; 2 issues were recounted.',
    'The page and event statistics drop them within a minute.',
  ])
  expect(deletedSentences({ ...nothing, pageviews: 2, rows: 2 })[0]).toBe('Deleted 2 page views of that user id in 2 projects.')
})

test('the confirmation names the user id and the projects', () => {
  expect(confirmDeleteSentence('jan@example.com', [])).toBe(
    'Delete everything stored with the user id “jan@example.com” in every project? This cannot be undone.',
  )
  expect(confirmDeleteSentence('42', ['Webshop', 'Portal'])).toBe(
    'Delete everything stored with the user id “42” in Webshop and Portal? This cannot be undone.',
  )
  expect(confirmDeleteSentence('42', ['A', 'B', 'C'])).toBe('Delete everything stored with the user id “42” in A, B and C? This cannot be undone.')
})
