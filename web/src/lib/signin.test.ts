import { expect, test } from 'vitest'
import { ApiError } from '../api/client'
import { problem, safeNext, tokenFromHash } from './signin'

test('safeNext keeps paths on this site and drops everything else', () => {
  expect(safeNext('/projects/1/issues?status=resolved')).toBe('/projects/1/issues?status=resolved')
  expect(safeNext('/')).toBe('/')
  for (const bad of [null, '', 'projects', '//evil.example.com', '/\\evil.example.com', 'https://evil.example.com/', 'javascript:alert(1)', '/\t/evil.example.com']) {
    expect(safeNext(bad), String(bad)).toBe('/')
  }
  // The sign-in pages themselves would loop.
  expect(safeNext('/login?next=/')).toBe('/')
})

test('tokenFromHash reads the token after #token=', () => {
  expect(tokenFromHash('#token=abc_DEF-123')).toBe('abc_DEF-123')
  expect(tokenFromHash('')).toBe('')
  expect(tokenFromHash('#')).toBe('')
  expect(tokenFromHash('#other=1')).toBe('')
})

test('problem shows the server message, or a sentence of its own', () => {
  expect(problem(new ApiError(401, 'The email or password is wrong.'))).toBe('The email or password is wrong.')
  expect(problem(new ApiError(400, 'the body must be one JSON object'))).toBe('That did not work. Check what you typed and try again.')
  expect(problem(new ApiError(500, 'internal error'))).toBe('Something went wrong on the server. Try again in a moment.')
  expect(problem(new TypeError('Failed to fetch'))).toBe('The server cannot be reached. Check your connection and try again.')
})
