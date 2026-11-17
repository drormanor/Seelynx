// End-to-end test of the AI service and driver builder. It needs its own empty server, so
// it never touches a live system:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/drivers.mjs [screenshot-dir]
//
// A local server plays the AI service, and the camera's answer is simulated in the browser.
import { chromium } from 'playwright-core'
import http from 'node:http'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
const KEY = 'sk-e2e-not-a-real-key'
const TOPIC = 'tns1:RuleEngine/LineDetector/Crossed'
const LOITER = 'tns1:VideoAnalytics/tnsht:LoiteringDetector/ObjectIsLoitering'
const report = {
  manufacturer: 'Vicon', model: 'V2002D-W28IR', firmware: '1.0', snapshot: true,
  profiles: [{ name: 'Main', codec: 'H264', width: 1920, height: 1080 }],
  topics: [{ topic: TOPIC, fields: ['Value', 'token'] }, { topic: LOITER, fields: ['Value', 'token'] }],
}

const calls = []
const service = http.createServer((req, res) => {
  let body = ''
  req.on('data', (d) => { body += d })
  req.on('end', () => {
    calls.push({ url: req.url, key: req.headers['x-api-key'], body })
    const text = body.includes('LineDetector')
      ? JSON.stringify({ analytics: [
        { topic: TOPIC, name: 'Line Cross', description: 'Someone crosses a line.' },
        { topic: 'tns1:Made/Up', name: 'Invented', description: 'Not from the camera.' },
      ], notes: 'Mount it high.' })
      : '{"ok":true}'
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ stop_reason: 'end_turn', content: [{ type: 'text', text }] }))
  })
})
await new Promise((r) => service.listen(9912, '127.0.0.1', r))

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const section = (name) => page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name, exact: true }).click()

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  await page.evaluate(() => fetch('/api/cameras', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'Door', main_url: 'rtsp://127.0.0.1:1/x', record: false, retention_days: 1, enabled: true }) }))

  // Without a service
  await nav.getByRole('button', { name: 'Settings' }).click()
  await section('Drivers')
  await page.getByRole('status').waitFor()
  check('drivers ask for an AI service first', (await page.getByRole('status').textContent()).includes('Connect an AI service'))
  check('no build form without a service', (await page.getByRole('button', { name: 'Read camera' }).count()) === 0)

  // Connect one. The address isn't offered for Claude, so use the compatible form's API.
  await section('AI service')
  await page.getByLabel('API key').fill(KEY)
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await page.getByRole('status').waitFor()
  check('key isn\'t shown again', !(await page.content()).includes(KEY))
  await page.evaluate((k) => fetch('/api/ai', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider: 'anthropic', api_key: k, base_url: 'http://127.0.0.1:9912' }) }), KEY)
  await page.reload()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  await nav.getByRole('button', { name: 'Settings' }).click()
  await section('AI service')
  await page.getByRole('button', { name: 'Test', exact: true }).click()
  await page.getByText('It works').waitFor()
  check('test reaches the service with the key', calls.at(-1)?.key === KEY && calls.at(-1)?.url === '/v1/messages')
  await shot('ai-service')

  // Build
  await page.route('**/api/drivers/probe', (route) => route.fulfill({ json: report }))
  await section('Drivers')
  await page.getByLabel('Camera', { exact: true }).selectOption({ label: 'Door' })
  await page.getByRole('button', { name: 'Read camera' }).click()
  await page.getByText('Exactly what will be sent').waitFor()
  check('what will be sent is shown first', (await page.locator('details.guide pre').textContent()).includes('LineDetector'))
  await shot('driver-report')
  const before = calls.length
  check('nothing is sent until asked', calls.length === before)
  await page.getByRole('button', { name: 'Build driver' }).click()
  await page.getByRole('heading', { name: /Review Vicon/ }).waitFor()
  const sent = JSON.parse(calls.at(-1).body)
  check('the AI receives the report', sent.messages[0].content.includes('V2002D-W28IR'))
  check('the key isn\'t in the prompt', !calls.at(-1).body.includes(KEY))
  check('the AI\'s name is used', (await page.getByLabel(`Name for ${TOPIC}`).inputValue()) === 'Line Cross')
  check('invented events are dropped', (await page.getByLabel(/^Name for /).count()) === 2 && !(await page.content()).includes('Made/Up'))
  check('a missed event gets a plain name', (await page.getByLabel(`Name for ${LOITER}`).inputValue()) === 'ObjectIsLoitering')
  await page.getByLabel(`Name for ${LOITER}`).fill('Loitering')
  await shot('driver-review')
  await page.getByRole('button', { name: 'Save driver' }).click()
  await page.getByRole('cell', { name: 'Vicon V2002D-W28IR', exact: true }).waitFor()
  check('driver saved with edits', (await page.getByRole('cell', { name: '2 analytics' }).count()) === 1)

  // Rules use it
  await section('Rules')
  await page.getByRole('button', { name: 'Add rule' }).click()
  await page.getByLabel('When').selectOption({ label: 'Camera analytics' })
  const pick = page.getByLabel('Which analytic')
  check('rules offer the driver\'s names', (await pick.locator('option').allTextContents()).some((t) => t.includes('Line Cross')))
  await pick.selectOption({ label: 'Line Cross (Vicon V2002D-W28IR)' })
  check('choosing one fills in the event', (await page.getByLabel(/Only events containing/).inputValue()) === TOPIC)
  await shot('rule-analytic')
  await page.getByRole('button', { name: 'Cancel' }).click()

  // Motion recording
  await nav.getByRole('button', { name: 'Cameras' }).click()
  await page.getByRole('button', { name: 'Edit' }).first().click()
  await page.getByRole('checkbox', { name: /^Record/ }).check()
  await page.getByLabel(/Recording mode/).selectOption({ label: 'On motion only' })
  check('motion recording offers before and after', (await page.getByLabel(/Keep before motion/).inputValue()) === '10' && (await page.getByLabel(/Keep after motion/).inputValue()) === '30')
  await page.getByLabel(/Keep before motion/).fill('5')
  await shot('motion-recording')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('cell', { name: 'On motion' }).waitFor()
  await page.getByRole('button', { name: 'Edit' }).first().click()
  check('the settings are saved', (await page.getByLabel(/Keep before motion/).inputValue()) === '5')
  await page.getByRole('button', { name: 'Cancel' }).click()

  // Help
  await nav.getByRole('button', { name: 'Settings' }).click()
  await section('Drivers')
  check('drivers have help', (await page.locator('details.guide > summary', { hasText: 'Help: Drivers' }).count()) === 1)
  await page.getByRole('button', { name: 'Remove Vicon V2002D-W28IR' }).click()
  await page.getByText('No drivers yet.').waitFor()
  check('a driver can be removed', true)

  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
  service.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
