// Browser test of the Settings area. Needs a running server with one camera. It
// creates and removes data, so run it on a separate copy, not a live system.
// Everything it creates is removed again, so it can be re-run.
//
//   SL_PASSWORD=... node web/e2e/settings.mjs [screenshot-dir]
import { chromium } from 'playwright-core'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8480'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
if (!process.env.SL_PASSWORD) { console.error('SL_PASSWORD is required'); process.exit(2) }

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const context = await browser.newContext({ viewport: { width: 1440, height: 860 }, acceptDownloads: true })
const page = await context.newPage()
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png') }) }
const nav = (name) => page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name, exact: true }).click()
const section = (name) => page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name }).click()
const removeRow = async (name) => {
  await page.getByRole('button', { name: `Remove ${name}` }).click()
  await page.getByRole('cell', { name, exact: true }).waitFor({ state: 'detached' })
}

try {
  await page.goto(base)
  await page.getByLabel('Username').fill(process.env.SL_USER ?? 'admin')
  await page.getByLabel('Password').fill(process.env.SL_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()

  // Group
  await nav('Settings')
  await section('Camera groups')
  await page.getByLabel('Group name').fill('E2E Lobby')
  await page.getByRole('button', { name: 'Add group' }).click()
  await page.getByRole('cell', { name: 'E2E Lobby', exact: true }).waitFor()
  check('group can be created', true)

  // Schedule, with two periods
  await section('Recording schedules')
  await page.getByLabel('Schedule name').fill('E2E Hours')
  await page.getByRole('button', { name: 'Add another period' }).click()
  await page.getByRole('button', { name: 'Sat' }).click()
  await page.getByRole('button', { name: 'Save schedule' }).click()
  await page.getByRole('cell', { name: 'E2E Hours', exact: true }).waitFor()
  check('schedule can be created', true)
  await shot('schedules')

  // Assign both to the camera
  await nav('Cameras')
  await page.getByRole('button', { name: 'Edit' }).first().click()
  await page.getByLabel('Group').selectOption({ label: 'E2E Lobby' })
  await page.getByLabel('Numeric ID').fill('101')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  check('camera accepts group and numeric ID', true)

  // Grouped sidebar, saved view, tour
  await nav('Live')
  await page.getByRole('heading', { name: 'E2E Lobby' }).waitFor()
  check('live sidebar shows the group', (await page.locator('.camlist').first().textContent()).includes('101'))
  await page.locator('.camlist button').first().click()
  await page.getByRole('button', { name: 'Save view' }).click()
  await page.getByLabel('View name').fill('E2E View')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await page.getByLabel('Open a saved view').waitFor()
  check('view can be saved', true)
  await shot('live-views')

  await page.getByRole('button', { name: /^Remove / }).first().click()
  await page.getByLabel('Open a saved view').selectOption({ label: 'E2E View' })
  await page.locator('.tile video').first().waitFor()
  check('saved view reopens with its camera', true)

  await nav('Settings')
  await section('Views and tours')
  await page.getByLabel('Tour name').fill('E2E Tour')
  await page.getByLabel('Seconds per view').fill('5')
  await page.getByRole('group', { name: 'Views in this tour' }).getByLabel('E2E View').check()
  await page.getByRole('button', { name: 'Add tour' }).click()
  await page.getByRole('cell', { name: 'E2E Tour', exact: true }).waitFor()
  check('tour can be created', true)

  // A view used by a tour is protected
  await removeRow('E2E Tour').catch(() => {})
  await removeRow('E2E View')
  check('view removed after its tour', true)

  // Access list: a loopback client stays allowed, so this can't lock the test out
  await section('Access list')
  await page.getByLabel('IP address or network').fill('not-an-address')
  await page.getByRole('button', { name: 'Add address' }).click()
  await page.getByRole('alert').waitFor()
  check('bad address is refused', true)
  await page.getByLabel('IP address or network').fill('10.99.0.0/16')
  await page.getByRole('button', { name: 'Add address' }).click()
  await page.getByRole('cell', { name: '10.99.0.0/16', exact: true }).waitFor()
  await page.reload()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  check('this machine stays allowed with an access list set', true)
  await nav('Settings')
  await section('Access list')
  await removeRow('10.99.0.0/16')

  // Backup and logs
  await section('Backup and restore')
  const before = await page.locator('table tr').count()
  await page.getByRole('button', { name: 'Back up now' }).click()
  await page.getByRole('link', { name: 'Download' }).first().waitFor()
  check('backup appears in the list', true, `rows before: ${before}`)
  await shot('backup')
  const dl = page.waitForEvent('download')
  await page.getByRole('link', { name: 'Download' }).first().click()
  check('backup downloads', (await (await dl).suggestedFilename()).endsWith('.db'))

  await section('Logs')
  const logs = page.waitForEvent('download')
  await page.getByRole('link', { name: 'Download logs' }).click()
  check('logs download as a zip', (await (await logs).suggestedFilename()).endsWith('.zip'))

  // Clean up: camera back to how it was, then the group and schedule
  await nav('Cameras')
  await page.getByRole('button', { name: 'Edit' }).first().click()
  await page.getByLabel('Group').selectOption({ label: 'None' })
  await page.getByLabel('Numeric ID').fill('0')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  await nav('Settings')
  await section('Camera groups')
  await removeRow('E2E Lobby')
  await section('Recording schedules')
  await removeRow('E2E Hours')
  check('cleanup finished', true)

  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
