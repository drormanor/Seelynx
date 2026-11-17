// Browser test of users, roles and passwords. It needs its own empty server, so it
// never touches a live system:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/users.mjs [screenshot-dir]
import { chromium } from 'playwright-core'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
const ADMIN = 'admin-test-password'
const TEMP = 'temporary-password-1'
const NEXT = 'A-new-Passw0rd-here'

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const page = await context.newPage()
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const go = (name) => nav.getByRole('button', { name, exact: true }).click()
const signIn = async (user, pw) => {
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password', { exact: true }).fill(pw)
  await page.getByRole('button', { name: 'Sign in' }).click()
}
const signOut = async () => {
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByRole('button', { name: 'Sign in' }).waitFor()
}

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill(ADMIN)
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  check('first-run admin can sign in', true)

  // Two cameras, added through the API as the signed-in admin
  const ids = await page.evaluate(async () => {
    const add = async (name) => (await (await fetch('/api/cameras', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, main_url: 'rtsp://127.0.0.1:18554/main', record: false, retention_days: 1, enabled: true }),
    })).json()).id
    return [await add('Lobby'), await add('Vault')]
  })
  check('test cameras added', ids.every(Boolean))

  // Custom role limited to one camera
  await go('Users')
  await page.getByRole('button', { name: 'Roles', exact: true }).click()
  await page.getByLabel('Role name').fill('Reception')
  await page.getByRole('group', { name: 'Permissions' }).getByLabel(/Playback/).check()
  await page.getByLabel(/All cameras, including new ones/).uncheck()
  await page.getByRole('group', { name: 'Cameras' }).getByLabel('Lobby').check()
  await page.getByRole('button', { name: 'Add role' }).click()
  await page.getByRole('cell', { name: 'Reception', exact: true }).waitFor()
  check('custom role created', true)
  await shot('roles')

  // User with that role, forced to change password
  await page.getByRole('button', { name: 'Users', exact: true }).last().click()
  await page.getByLabel('Username').fill('pat')
  await page.getByLabel(/^Password/).fill(TEMP)
  await page.getByLabel('First name').fill('Pat')
  await page.getByLabel(/^Role/).selectOption({ label: 'Reception' })
  await page.getByLabel(/Require a complex password/).check()
  await page.getByRole('button', { name: 'Add user' }).click()
  await page.getByRole('cell', { name: 'pat', exact: true }).waitFor()
  check('user created with custom role', await page.getByRole('cell', { name: 'Must change password' }).count() === 1)
  await shot('users')

  // Duplicate keeps the role but not the username
  await page.getByRole('button', { name: 'Duplicate pat' }).click()
  check('duplicate copies the role', await page.getByLabel(/^Role/).inputValue() !== '2' && await page.getByLabel('Username').inputValue() === '')
  await page.getByRole('button', { name: 'Cancel' }).click()

  // Last admin protection
  await page.getByRole('button', { name: 'Edit root' }).click()
  await page.getByLabel(/^Role/).selectOption({ label: 'Viewer' })
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('alert').waitFor()
  check('last admin cannot be demoted', (await page.getByRole('alert').textContent()).includes('administrator'))
  await page.getByRole('button', { name: 'Cancel' }).click()
  await signOut()

  // Forced password change
  await signIn('pat', TEMP)
  await page.getByRole('heading', { name: 'Change password' }).waitFor()
  check('forced password screen appears', await nav.count() === 0)
  await shot('forced-password')
  await page.getByLabel('Current password').fill(TEMP)
  await page.getByLabel('New password', { exact: true }).fill('aaaaaaaaaaaa')
  await page.getByLabel('Repeat new password').fill('aaaaaaaaaaaa')
  await page.getByRole('button', { name: 'Change password' }).click()
  await page.getByRole('alert').waitFor()
  check('complexity rule is enforced', (await page.getByRole('alert').textContent()).includes('three'))
  await page.getByLabel('New password', { exact: true }).fill(NEXT)
  await page.getByLabel('Repeat new password').fill(NEXT)
  await page.getByRole('button', { name: 'Change password' }).click()
  await nav.waitFor()
  check('password change lets the user in', true)

  // Limited menu and cameras
  const tabs = (await nav.getByRole('button').allTextContents()).join(',')
  check('menu shows only permitted pages', tabs === 'Live,Playback', tabs)
  await page.locator('.camlist button').first().waitFor()
  const liveList = await page.locator('.camlist button').allTextContents()
  check('live list shows only the permitted camera', liveList.join() === 'Lobby', liveList.join())
  await go('Playback')
  const select = page.locator('.playback select').first()
  await select.locator('option').first().waitFor({ state: 'attached' })
  const options = await select.locator('option').allTextContents()
  check('playback list shows only the permitted camera', options.join() === 'Lobby', options.join())
  await shot('limited-user')
  const blocked = await page.evaluate(async (vault) => ({
    video: (await fetch(`/api/cameras/${vault}/motion?start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z`)).status,
    users: (await fetch('/api/users')).status,
  }), ids[1])
  check('server blocks the hidden camera and admin data', blocked.video === 404 && blocked.users === 403, JSON.stringify(blocked))

  // Changing your own password from the account button
  await page.getByRole('button', { name: 'Change password' }).click()
  await page.getByRole('heading', { name: 'Change password' }).waitFor()
  await page.getByRole('button', { name: 'Cancel' }).click()
  await nav.waitFor()
  check('change password can be cancelled', await page.getByRole('heading', { name: 'Change password' }).count() === 0)
  await signOut()

  await signIn('pat', TEMP)
  await page.getByRole('alert').waitFor()
  check('old password no longer works', true)
  await signIn('pat', NEXT)
  await nav.waitFor()
  check('new password works', true)

  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
