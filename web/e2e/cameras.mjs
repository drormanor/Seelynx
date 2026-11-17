// End-to-end test of the default camera login. It needs its own empty server, and no camera:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/cameras.mjs [screenshot-dir]
import { chromium } from 'playwright-core'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
const secret = 'test-only-camera-login'

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
const pageErrors = []
const responses = []
page.on('pageerror', (e) => pageErrors.push(e.message))
page.on('response', async (r) => {
  if (r.url().includes('/api/') && r.request().method() !== 'OPTIONS') responses.push(await r.text().catch(() => ''))
})
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const tick = page.getByLabel('Use the default login')

const addCamera = async (name) => {
  await page.getByLabel('Name', { exact: false }).first().fill(name)
  await page.getByLabel('Main stream URL').fill('rtsp://127.0.0.1:1/x')
  await page.getByRole('button', { name: 'Add camera' }).last().click()
  await page.getByRole('cell', { name, exact: true }).waitFor()
}
const edit = (name) => page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: 'Edit', exact: true }).click()

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  await nav.getByRole('button', { name: 'Cameras' }).click()

  await page.getByRole('button', { name: 'Add camera' }).click()
  await page.getByLabel('Camera username').waitFor()
  check('no tick box until a default is set', (await tick.count()) === 0)
  await page.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Default login' }).click()
  await page.getByLabel('Default username').fill('installer')
  await page.getByLabel('Default password').fill(secret)
  await shot('camera-default-login')
  await page.getByRole('button', { name: 'Save default' }).click()
  await page.getByLabel('Default username').waitFor({ state: 'detached' })

  await page.getByRole('button', { name: 'Default login' }).click()
  check('the username is shown again', (await page.getByLabel('Default username').inputValue()) === 'installer')
  check('the password isn\'t', (await page.getByLabel('Default password').inputValue()) === '' && (await page.getByLabel('Default password').getAttribute('placeholder')) === 'Unchanged')
  await page.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Add camera' }).click()
  await tick.waitFor()
  check('new cameras start on the default', await tick.isChecked())
  check('its fields are hidden', (await page.getByLabel('Camera username').count()) === 0)
  await shot('camera-add-default')
  await addCamera('Uses default')

  await edit('Uses default')
  await page.getByLabel('Camera username').waitFor()
  check('the camera got the username', (await page.getByLabel('Camera username').inputValue()) === 'installer')
  check('the camera got the password', (await page.getByLabel('Camera password').getAttribute('placeholder')) === 'Unchanged')
  check('editing offers no tick box', (await tick.count()) === 0)
  await page.getByLabel('Camera username').fill('override')
  await page.getByLabel('Camera password').fill('another-test-login')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  await edit('Uses default')
  check('one camera can be overridden', (await page.getByLabel('Camera username').inputValue()) === 'override')
  await page.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Add camera' }).click()
  await tick.uncheck()
  await page.getByLabel('Camera username').fill('different')
  await addCamera('Own login')
  await edit('Own login')
  check('a new camera can use its own login', (await page.getByLabel('Camera username').inputValue()) === 'different')
  // The camera doesn't exist, so the form says there's no picture and offers to try again.
  await page.getByText("The camera didn't send a picture").waitFor()
  check('a camera with no picture says so', await page.getByRole('button', { name: 'Take a new picture' }).isEnabled())
  await page.getByRole('button', { name: 'Cancel' }).click()
  await page.getByRole('row', { name: /Own login/ }).getByText('No picture').waitFor()
  check('the list shows where a picture goes', true)
  await page.getByRole('button', { name: 'Edit Own login' }).click()
  check('choosing the picture opens the camera', (await page.getByLabel('Camera username').inputValue()) === 'different')
  await page.getByRole('button', { name: 'Cancel' }).click()

  // A schedule: all video during it, and only motion the rest of the time.
  const made = await page.request.post(base + '/api/config/schedules', { data: { name: 'Business hours', data: { periods: [{ days: [1, 2, 3, 4, 5], start: '08:00', end: '18:00' }] } } })
  check('a schedule can be made', made.ok(), String(made.status()))
  await edit('Own login')
  check('without a schedule there is one recording mode', (await page.getByLabel('Rest of the time').count()) === 0 && (await page.getByLabel('Recording mode').count()) === 1)
  await page.getByLabel('Recording schedule').selectOption({ label: 'Business hours' })
  check('a schedule asks how to record during it', (await page.getByLabel('During the schedule').inputValue()) === 'all')
  check('the rest of the time starts as not recording', (await page.getByLabel('Rest of the time').inputValue()) === '0')
  check('motion times only show with motion', (await page.getByLabel('Keep before motion (seconds)').count()) === 0)
  await page.getByLabel('Rest of the time').selectOption({ label: 'On motion only' })
  check('motion outside the schedule asks for motion times', await page.getByLabel('Keep before motion (seconds)').isVisible())
  check('the form sums it up', await page.getByText('During the schedule the camera keeps all video. The rest of the time it keeps only video near motion.').isVisible())
  await shot('camera-schedule')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  check('the list shows both', (await page.getByRole('row', { name: /Own login/ }).innerText()).includes('Scheduled: continuous, else on motion'))
  await edit('Own login')
  check('the choice is kept', (await page.getByLabel('Rest of the time').inputValue()) === '2')
  await page.getByLabel('During the schedule').selectOption({ label: 'On motion only' })
  await page.getByLabel('Rest of the time').selectOption({ label: "Don't record" })
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  check('motion during the schedule and off after', (await page.getByRole('row', { name: /Own login/ }).innerText()).includes('Scheduled: on motion, else off'))

  await page.getByRole('button', { name: 'Find ONVIF cameras' }).click()
  await tick.waitFor()
  check('ONVIF starts on the default', await tick.isChecked() && (await page.getByLabel('Camera username').count()) === 0)
  await tick.uncheck()
  check('ONVIF can use another login', (await page.getByLabel('Camera username').count()) === 1)
  await page.getByRole('button', { name: 'Cancel' }).click()

  await page.getByRole('button', { name: 'Default login' }).click()
  await page.getByRole('button', { name: 'Remove default' }).click()
  await page.getByLabel('Default username').waitFor({ state: 'detached' })
  await page.getByRole('button', { name: 'Add camera' }).click()
  await page.getByLabel('Camera username').waitFor()
  check('a removed default is no longer offered', (await tick.count()) === 0)
  await page.getByRole('button', { name: 'Cancel' }).click()
  check('cameras keep their login after that', (await page.getByRole('cell', { name: 'Own login', exact: true }).count()) === 1)

  check('the password never came back from the server', !responses.some((b) => b.includes(secret)), `${responses.length} responses`)
  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
