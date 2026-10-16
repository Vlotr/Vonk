import { describe, expect, test } from 'vitest'
import {
  countryName,
  displayFile,
  formatDateTime,
  formatTime,
  hiddenFramesSentence,
  issuesHeadline,
  majorMinor,
  newIssuesSentence,
  projectsHeadline,
  runtimeLabel,
  shortFunction,
  shortType,
  showingSentence,
  sourceLabel,
  timeAgo,
  urlPath,
} from './format'

describe('shortType', () => {
  test('keeps the part after the last backslash or dot', () => {
    expect(shortType('Illuminate\\Database\\QueryException')).toBe('QueryException')
    expect(shortType('django.db.utils.OperationalError')).toBe('OperationalError')
    expect(shortType('TypeError')).toBe('TypeError')
  })

  test('leaves names it cannot shorten alone', () => {
    expect(shortType('')).toBe('')
    expect(shortType('Weird\\')).toBe('Weird\\')
    expect(shortType('Checkout failed. Try again')).toBe('Checkout failed. Try again')
  })
})

describe('issuesHeadline', () => {
  test('counts open issues, singular and plural', () => {
    expect(issuesHeadline(14, 'Webshop')).toBe('14 open issues in Webshop')
    expect(issuesHeadline(1, 'Webshop')).toBe('1 open issue in Webshop')
    expect(issuesHeadline(0, 'Webshop')).toBe('No open issues in Webshop')
  })
})

describe('newIssuesSentence', () => {
  test('says how many of the open issues are new', () => {
    expect(newIssuesSentence(14, 6)).toBe('6 of them are new in the last 24 hours.')
    expect(newIssuesSentence(14, 1)).toBe('1 of them is new in the last 24 hours.')
    expect(newIssuesSentence(14, 0)).toBe('None of them are new in the last 24 hours.')
  })

  test('talks about one issue as "it"', () => {
    expect(newIssuesSentence(1, 1)).toBe('It is new in the last 24 hours.')
    expect(newIssuesSentence(1, 0)).toBe('It was first seen more than 24 hours ago.')
  })

  test('without open issues it says where new ones appear', () => {
    expect(newIssuesSentence(0, 0)).toBe('New errors show up here within a minute after your app sends them.')
  })
})

describe('showingSentence', () => {
  test('counts the rows shown of the total', () => {
    expect(showingSentence(6, 14)).toBe('Showing 6 of 14 open issues')
    expect(showingSentence(1, 1)).toBe('Showing 1 of 1 open issue')
  })
})

describe('projectsHeadline', () => {
  test('counts the projects with open issues', () => {
    expect(projectsHeadline([{ openIssues: 3 }, { openIssues: 1 }, { openIssues: 0 }])).toBe('2 projects have open issues')
    expect(projectsHeadline([{ openIssues: 3 }, { openIssues: 0 }])).toBe('1 project has open issues')
    expect(projectsHeadline([{ openIssues: 0 }])).toBe('No open issues in any project')
    expect(projectsHeadline([])).toBe('No projects yet')
  })
})

describe('timeAgo', () => {
  const now = new Date(2026, 9, 16, 12, 0, 0)
  const ago = (ms: number) => timeAgo(new Date(now.getTime() - ms), now)
  const minute = 60_000
  const hour = 60 * minute

  test('uses minutes, hours and days as in the design', () => {
    expect(ago(20_000)).toBe('just now')
    expect(ago(4 * minute)).toBe('4 min ago')
    expect(ago(59 * minute + 59_000)).toBe('59 min ago')
    expect(ago(hour)).toBe('1 h ago')
    expect(ago(23 * hour + 59 * minute)).toBe('23 h ago')
    expect(ago(24 * hour)).toBe('1 d ago')
    expect(ago(29 * 24 * hour)).toBe('29 d ago')
  })

  test('shows a date after a month', () => {
    expect(ago(40 * 24 * hour)).toBe('Sep 6')
    expect(timeAgo(new Date(2025, 0, 2, 9, 0), now)).toBe('Jan 2, 2025')
  })

  test('a time slightly ahead of the clock is "just now"', () => {
    expect(ago(-30_000)).toBe('just now')
  })
})

describe('formatDateTime', () => {
  const now = new Date(2026, 9, 16, 12, 0, 0)

  test('shows month, day and 24-hour time', () => {
    expect(formatDateTime(new Date(2026, 9, 15, 21, 6), now)).toBe('Oct 15, 21:06')
    expect(formatDateTime(new Date(2026, 0, 3, 7, 5), now)).toBe('Jan 3, 07:05')
  })

  test('adds the year when it is not this year', () => {
    // The same style as dates without a time: "Jan 2, 2025".
    expect(formatDateTime(new Date(2025, 11, 31, 23, 59), now)).toBe('Dec 31, 2025, 23:59')
  })
})

describe('formatTime', () => {
  test('shows 24-hour time', () => {
    expect(formatTime(new Date(2026, 9, 16, 10, 42))).toBe('10:42')
    expect(formatTime(new Date(2026, 9, 16, 7, 5))).toBe('07:05')
  })
})

describe('sourceLabel', () => {
  test('names the sources as the design does', () => {
    expect(sourceLabel('laravel')).toBe('Laravel')
    expect(sourceLabel('browser')).toBe('Browser')
    expect(sourceLabel('php')).toBe('PHP')
    expect(sourceLabel('node')).toBe('Node.js')
    expect(sourceLabel('python')).toBe('Python')
    expect(sourceLabel('other')).toBe('Other')
  })
})

describe('majorMinor', () => {
  test('keeps major and minor version, as the design shows "PHP 8.3"', () => {
    expect(majorMinor('8.4.1')).toBe('8.4')
    expect(majorMinor('8.3')).toBe('8.3')
    expect(majorMinor('8')).toBe('8')
    expect(majorMinor('8.4.0-dev')).toBe('8.4')
    expect(majorMinor('v20.11.1')).toBe('v20.11')
  })

  test('leaves versions without numbers alone', () => {
    expect(majorMinor('unknown')).toBe('unknown')
    expect(majorMinor('')).toBe('')
  })
})

describe('runtimeLabel', () => {
  test('names runtimes', () => {
    expect(runtimeLabel('php')).toBe('PHP')
    expect(runtimeLabel('node')).toBe('Node.js')
    expect(runtimeLabel('CPython')).toBe('CPython')
  })
})

describe('displayFile', () => {
  test('shows project paths without the leading slash', () => {
    expect(displayFile('/app/Http/Controllers/CheckoutController.php')).toBe('app/Http/Controllers/CheckoutController.php')
  })

  test('shows only the path of a URL', () => {
    expect(displayFile('http://127.0.0.1:5173/src/main.js')).toBe('src/main.js')
    expect(displayFile('https://shop.example.com/assets/index-D8xk2Lp1.js?v=2')).toBe('assets/index-D8xk2Lp1.js')
  })

  test('leaves other names alone', () => {
    expect(displayFile('app.js')).toBe('app.js')
    expect(displayFile('')).toBe('')
  })
})

describe('shortFunction', () => {
  test('keeps the method of a PHP class', () => {
    expect(shortFunction('App\\Http\\Controllers\\CheckoutController::store')).toBe('store')
    expect(shortFunction('App\\Models\\CartItem::{closure}')).toBe('{closure}')
  })

  test('leaves other names alone', () => {
    expect(shortFunction('HTMLButtonElement.onCheckoutClick')).toBe('HTMLButtonElement.onCheckoutClick')
    expect(shortFunction('')).toBe('')
  })
})

describe('countryName', () => {
  test('names a country code in English', () => {
    expect(countryName('NL')).toBe('Netherlands')
  })
})

describe('hiddenFramesSentence', () => {
  test('counts the hidden framework frames', () => {
    expect(hiddenFramesSentence(14)).toBe('14 framework lines hidden. Show them.')
    expect(hiddenFramesSentence(1)).toBe('1 framework line hidden. Show it.')
  })
})

describe('urlPath', () => {
  test('shows path and query of a URL, as the Details card does', () => {
    expect(urlPath('http://127.0.0.1:8000/checkout')).toBe('/checkout')
    expect(urlPath('https://shop.example.com/cart?step=2')).toBe('/cart?step=2')
  })

  test('leaves what is not a URL alone', () => {
    expect(urlPath('/checkout')).toBe('/checkout')
    expect(urlPath('not a url')).toBe('not a url')
  })
})
