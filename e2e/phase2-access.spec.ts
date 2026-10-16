// Accounts and access: the admin (made on the setup page by global-setup.ts)
// creates a project in Settings and invites a member who may see one of
// the two example projects. The member accepts the invite, signs in, and
// sees only that project; the other one is "not found", in the dashboard
// and in the API.

import { expect, test } from '@playwright/test'
import { join } from 'node:path'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

test('an invited member sees only the project they were given', async ({ browser }) => {
  const monitor = env('MONITOR_URL')

  // The admin creates a project in Settings and gets its keys and setup steps.
  const admin = await (await browser.newContext({ storageState: env('ADMIN_STATE') })).newPage()
  await admin.goto(`${monitor}/settings/projects`)
  await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Manage projects')
  await admin.getByRole('button', { name: 'Add project' }).click()
  await admin.getByLabel('Project name').fill('Internal tools')
  await admin.getByLabel('Keep data for').selectOption({ label: 'Indefinitely' })
  await admin.getByLabel('Errors from the browser').uncheck()
  await expect(admin.getByText('After you create the project, you get the keys and the setup steps.')).toBeVisible()
  await admin.getByRole('button', { name: 'Create project' }).click()

  await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Set up Internal tools')
  await expect(admin.locator('pre[aria-label="DSN"]')).toHaveText(/^http:\/\/[0-9a-f]{32}@127\.0\.0\.1:\d+\/\d+$/)
  await expect(admin.getByText('composer require sentry/sentry-laravel')).toBeVisible()
  await expect(admin.getByText(/^SENTRY_LARAVEL_DSN=http:\/\//)).toBeVisible()
  await expect(admin.getByRole('heading', { name: 'Errors from the browser' })).toHaveCount(0)

  // Your projects lists all three, with what they track and keep.
  await admin.goto(`${monitor}/settings/projects`)
  const internal = admin.locator('tbody tr').filter({ hasText: 'Internal tools' })
  await expect(internal).toContainText('Laravel errors')
  await expect(internal).toContainText('Indefinitely')
  await expect(internal.getByRole('button', { name: 'Add page views' })).toBeVisible()
  await expect(admin.locator('tbody tr')).toHaveCount(3)
  await admin.getByRole('button', { name: 'Add project' }).click()
  await admin.getByLabel('Project name').blur()
  await admin.screenshot({ path: join(screenshots, 'phase2-settings.png'), fullPage: true })

  // An invite for a member who may see Webshop, not Customer portal.
  await admin.goto(`${monitor}/settings/users`)
  await admin.getByRole('button', { name: 'Invite user' }).click()
  await admin.getByLabel('Email').fill('member@example.com')
  await admin.getByLabel('Role').selectOption('member')
  await admin.getByLabel('Webshop').check()
  await admin.getByRole('button', { name: 'Create invite' }).click()
  await expect(admin.getByRole('heading', { name: 'Invite for member@example.com' })).toBeVisible()
  const link = await admin.locator('pre[aria-label="Link"]').innerText()
  expect(link).toMatch(new RegExp(`^${monitor}/invite#token=[A-Za-z0-9_-]{43}$`))
  await admin.getByRole('button', { name: 'Done' }).click()
  const pending = admin.getByRole('region', { name: 'Pending invites' })
  await expect(pending).toContainText('member@example.com')
  await expect(pending).toContainText('Webshop')

  // The member opens the link in a browser of their own and chooses a password.
  const memberContext = await browser.newContext()
  const member = await memberContext.newPage()
  await member.goto(link)
  await expect(member.getByRole('heading', { level: 1 })).toHaveText('Join [Tool name]')
  await expect(member.getByLabel('Email')).toHaveValue('member@example.com')
  await member.getByLabel('Password').fill('member password 1')
  await member.getByRole('button', { name: 'Create account' }).click()
  await member.waitForURL(`${monitor}/`)

  // Signed out and in again, through the login page.
  await member.goto(`${monitor}/settings`)
  await member.waitForURL(`${monitor}/settings/account`)
  await expect(member.getByText('You are signed in as member@example.com, member.')).toBeVisible()
  await member.getByRole('button', { name: 'Sign out' }).click()
  await member.waitForURL(`${monitor}/login`)
  await member.goto(`${monitor}/projects/${env('PORTAL_PROJECT_ID')}/issues`)
  await member.waitForURL(/\/login\?next=/)
  await member.getByLabel('Email').fill('member@example.com')
  await member.getByLabel('Password').fill('wrong password')
  await member.getByRole('button', { name: 'Sign in' }).click()
  await expect(member.getByRole('alert')).toHaveText('The email or password is wrong.')
  await member.getByLabel('Password').fill('member password 1')
  await member.getByRole('button', { name: 'Sign in' }).click()

  // The login page sent the member on to the URL they asked for: Customer
  // portal, which they may not see.
  await member.waitForURL(`${monitor}/projects/${env('PORTAL_PROJECT_ID')}/issues`)
  await expect(member.getByRole('heading', { level: 1 })).toHaveText('Project not found')

  // All projects shows Webshop only.
  await member.goto(monitor)
  const rows = member.getByRole('region', { name: 'Projects' }).locator('tbody tr')
  await expect(rows).toHaveCount(1)
  await expect(rows.first()).toContainText('Webshop')
  await expect(member.getByText('Customer portal')).toHaveCount(0)
  await expect(member.getByText('Internal tools')).toHaveCount(0)

  // Webshop's issues are there.
  await member.goto(`${monitor}/projects/${env('PROJECT_ID')}/issues`)
  await expect(member.getByRole('heading', { level: 1 })).toContainText('in Webshop')

  // Settings shows the member their account only.
  await member.goto(`${monitor}/settings/users`)
  await expect(member.getByRole('heading', { level: 1 })).toHaveText('Page not found')
  await member.goto(`${monitor}/settings/account`)
  await expect(member.getByRole('navigation', { name: 'Settings pages' }).getByRole('link')).toHaveCount(0)

  // The API says the same: the other project does not exist for this member.
  const api = memberContext.request
  expect((await api.get(`${monitor}/ui/api/projects/${env('PORTAL_PROJECT_ID')}/issues`)).status()).toBe(404)
  expect((await api.get(`${monitor}/ui/api/projects/${env('PROJECT_ID')}/issues`)).status()).toBe(200)
  expect((await api.get(`${monitor}/ui/api/settings/projects`)).status()).toBe(404)
  const projects = (await (await api.get(`${monitor}/ui/api/projects`)).json()) as { name: string }[]
  expect(projects.map((p) => p.name)).toEqual(['Webshop'])

  // The admin sees the member in the users list, with their one project.
  await admin.goto(`${monitor}/settings/users`)
  const memberRow = admin.getByRole('region', { name: 'Users' }).locator('tbody tr').filter({ hasText: 'member@example.com' })
  await expect(memberRow).toContainText('Member')
  await expect(memberRow).toContainText('Webshop')

  // Delete the new project again, after typing its name (the phase 2 issue
  // test expects the two example projects only).
  await admin.goto(`${monitor}/settings/projects`)
  await admin.locator('tbody tr').filter({ hasText: 'Internal tools' }).getByRole('button', { name: 'Edit' }).click()
  await expect(admin.getByRole('heading', { name: 'Edit Internal tools' })).toBeVisible()
  const remove = admin.getByRole('button', { name: 'Delete project' })
  await expect(remove).toBeDisabled()
  await admin.getByLabel('Type Internal tools to confirm').fill('Internal tools')
  await remove.click()
  await expect(admin.locator('tbody tr').filter({ hasText: 'Internal tools' })).toHaveCount(0)
  await expect(admin.locator('tbody tr')).toHaveCount(2)

  await memberContext.close()
})
