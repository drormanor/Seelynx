// Checks that every page, Settings section and connection type has its help. It needs its
// own empty server, so it never touches a live system:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/help.mjs [screenshot-dir]
import { chromium } from 'playwright-core'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png') }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const section = (name) => page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name }).click()
const hasHelp = async (title) => (await page.locator('details.guide > summary', { hasText: title }).count()) === 1

try {
  await page.goto(base)
  check('sign-in page is reachable', true)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  const version = (await page.locator('header .version').innerText()).trim()
  check('the version is shown next to Help', /^Version ([0-9a-f]{7}\+?|dev)$/.test(version), version)

  // Live has its empty state until a camera exists, so Live and Playback get checked on Help.
  for (const [name, title] of [['Alarms', 'Help: Alarms'], ['Cameras', 'Help: Cameras'], ['Users', 'Help: Users'], ['System', 'Help: System']]) {
    await nav.getByRole('button', { name }).click()
    await page.locator('details.guide > summary').first().waitFor()
    check(`${name} has help`, await hasHelp(title))
  }
  await nav.getByRole('button', { name: 'Users' }).click()
  await page.getByRole('button', { name: 'Roles', exact: true }).click()
  check('Roles has help', await hasHelp('Help: Roles'))

  await nav.getByRole('button', { name: 'Settings' }).click()
  for (const [name, title] of [['Rules', 'Help: Rules'], ['Connections', 'Help: Connections'], ['AI service', 'Help: AI service'], ['Drivers', 'Help: Drivers'], ['Camera groups', 'Help: Camera groups'],
    ['Recording schedules', 'Help: Recording schedules'], ['Views and tours', 'Help: Views and tours'], ['Access list', 'Help: Access list'],
    ['Backup and restore', 'Help: Backup and restore'], ['Logs', 'Help: Logs']]) {
    await section(name)
    await page.getByRole('heading', { name, exact: true }).waitFor()
    check(`${name} has help`, await hasHelp(title))
  }

  // Every service offered must have its steps, and the steps must change with the service.
  await section('Connections')
  await page.getByRole('button', { name: 'Add connection' }).click()
  const service = page.getByLabel('Service')
  const labels = await service.locator('option').allTextContents()
  check('all services are listed', labels.length === 13, String(labels.length))
  const seen = new Set()
  for (const label of labels) {
    await service.selectOption({ label })
    const guide = page.locator('.fields details.guide')
    const open = (await guide.count()) === 1 && (await guide.getAttribute('open')) !== null
    const title = open ? await guide.locator('> summary').textContent() : ''
    const steps = open ? await guide.locator('> ol > li').count() : 0
    check(`${label} has setup steps`, open && steps >= 3 && !seen.has(title), `${steps} steps`)
    seen.add(title)
    if (shots) await shot('service-' + label.split(' ')[0].toLowerCase())
  }

  // Help page
  await page.getByRole('button', { name: 'Help', exact: true }).click()
  await page.getByRole('heading', { name: 'Help', exact: true }).waitFor()
  check('Help opens with getting started', await page.locator('details.guide[open] > summary', { hasText: 'Getting started' }).isVisible())
  for (const title of ['Help: Live view', 'Help: Playback', 'Help: Your password']) check(`Help lists ${title}`, await hasHelp(title))
  check('Help lists every service', (await page.locator('details.guide > summary', { hasText: /^How to / }).count()) === 13)
  await page.locator('details.guide > summary', { hasText: 'Help: Rules' }).click()
  check('a topic opens when clicked', await page.getByText('Letting another system trigger a rule').isVisible())
  await shot('help')

  // A restricted user must not be shown administrator topics.
  await nav.getByRole('button', { name: 'Users' }).click()
  await page.getByLabel('Username').fill('viewer1')
  await page.locator('input[type=password]').first().fill('viewer-test-password')
  await page.getByLabel('Role').selectOption({ label: 'Viewer' })
  await page.getByLabel(/Must change password/).uncheck()
  await page.getByRole('button', { name: 'Add user' }).click()
  await page.getByRole('cell', { name: 'viewer1', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.getByRole('button', { name: 'Sign in' }).waitFor()
  await page.getByLabel('Username').fill('viewer1')
  await page.getByLabel('Password').fill('viewer-test-password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  await page.getByRole('button', { name: 'Help', exact: true }).click()
  await page.getByRole('heading', { name: 'Help', exact: true }).waitFor()
  check('a viewer sees their topics', await hasHelp('Help: Live view'))
  check('a viewer sees no admin topics', !(await hasHelp('Help: Rules')) && !(await hasHelp('Help: Users')))
  check('a viewer sees no service setup', (await page.locator('details.guide > summary', { hasText: /^How to / }).count()) === 0)

  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  if (shots) await page.screenshot({ path: path.join(shots, 'failure.png'), fullPage: true })
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
