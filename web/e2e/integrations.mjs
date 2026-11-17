// End-to-end test of connections, rules and alarms. It needs its own empty server, so it
// never touches a live system:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/integrations.mjs [screenshot-dir]
//
// An outside system calls the rule's hook, and a local server plays the service that
// should hear about it.
import { chromium } from 'playwright-core'
import http from 'node:http'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
const SECRET_PATH = '/in/very-secret-token'

const received = []
const receiver = http.createServer((req, res) => {
  let body = ''
  req.on('data', (d) => { body += d })
  req.on('end', () => { received.push({ url: req.url, auth: req.headers.authorization, body }); res.end('ok') })
})
await new Promise((r) => receiver.listen(9911, '127.0.0.1', r))

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}
const until = async (fn, ms = 8000) => {
  const end = Date.now() + ms
  while (Date.now() < end) { if (await fn()) return true; await new Promise((r) => setTimeout(r, 100)) }
  return false
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const go = (name) => nav.getByRole('button', { name }).click()
const section = (name) => page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name }).click()

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()

  // Connection
  await go('Settings')
  await section('Connections')
  await page.getByRole('button', { name: 'Add connection' }).click()
  check('every service is offered', (await page.getByLabel('Service').locator('option').count()) === 13)
  await page.getByLabel('Service').selectOption({ label: 'Microsoft 365 email' })
  check('Microsoft asks for its app details', await page.getByLabel('Directory (tenant) ID').isVisible())
  check('Microsoft shows both permission options', (await page.getByText(/^Option [AB]:/).count()) === 2)
  await page.getByText('Option A: one mailbox only').click()
  check('the mailbox commands are shown', await page.getByText('New-ManagementScope').isVisible())
  await shot('microsoft-guide')
  await page.getByLabel('Service').selectOption({ label: 'Gmail or Google Workspace' })
  check('Gmail asks for an app password', (await page.getByLabel('App password').getAttribute('type')) === 'password')
  check('Gmail shows how to get one', (await page.getByRole('link', { name: 'App passwords' }).getAttribute('href')) === 'https://myaccount.google.com/apppasswords')
  await shot('gmail-guide')
  await page.getByLabel('Service').selectOption({ label: 'Webhook' })
  await page.getByLabel('Connection name').fill('Local service')
  await page.getByLabel('Web address').fill('http://127.0.0.1:9911' + SECRET_PATH)
  await page.getByLabel(/Extra header/).fill('Authorization: Bearer abc123')
  await page.getByRole('button', { name: 'Save connection' }).click()
  await page.getByRole('cell', { name: 'Local service', exact: true }).waitFor()
  check('connection saved', true)
  const html = await page.content()
  check('the secret address isn\'t in the page', !html.includes('very-secret-token'))

  await page.getByRole('button', { name: 'Test Local service' }).click()
  await page.getByRole('status').waitFor()
  check('test message arrives', await until(() => received.length === 1), received[0]?.url)
  check('extra header is sent', received[0]?.auth === 'Bearer abc123')
  const sample = Buffer.from(JSON.parse(received[0]?.body ?? '{}').snapshot ?? '', 'base64')
  check('the test includes a sample picture', sample[0] === 0xFF && sample[1] === 0xD8 && sample.length > 1000, `${sample.length} bytes`)
  await shot('connections')

  // Rule with a private hook address
  await section('Rules')
  await page.getByRole('button', { name: 'Add rule' }).click()
  await page.getByLabel('Rule name').fill('Door forced')
  const snapshotBox = page.getByRole('group', { name: 'Rule actions' }).getByLabel(/Include a snapshot/)
  check('a motion rule offers a snapshot, ticked', await snapshotBox.isChecked())
  await page.getByLabel('When').selectOption({ label: 'Camera offline' })
  check('an offline rule offers none', (await snapshotBox.count()) === 0)
  await page.getByLabel('When').selectOption({ label: 'Outside system' })
  check('an outside rule can name a camera', await page.getByLabel(/Camera for this event/).isVisible())
  check('no snapshot without a camera', (await snapshotBox.count()) === 0)
  await page.getByLabel('Priority').selectOption({ label: 'High' })
  await page.getByLabel('Message').fill('Access control says: {event}')
  await page.getByRole('group', { name: 'Rule actions' }).getByLabel('Send to Local service').check()
  await shot('rule-form')
  await page.getByRole('button', { name: 'Save rule' }).click()
  await page.getByRole('cell', { name: 'Door forced', exact: true }).waitFor()
  const hook = await page.locator('code.hook').textContent()
  check('rule gets a private address', /\/api\/hooks\/[0-9a-f]{48}$/.test(hook))

  // An outside system fires it
  const wrong = await fetch(base + '/api/hooks/wrong', { method: 'POST', body: '{}' })
  check('a wrong address is refused', wrong.status === 404)
  const res = await fetch(hook, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ message: 'door 3 forced' }) })
  check('the hook accepts the call', res.status === 202)
  check('the rule reaches the outside service', await until(() => received.length === 2))
  const sent = JSON.parse(received[1]?.body ?? '{}')
  check('the message is filled in', sent.text === 'Access control says: door 3 forced' && sent.priority === 'high', received[1]?.body?.slice(0, 120))

  // Alarm appears live, then gets handled
  check('menu shows the open alarm', await until(async () => (await nav.getByRole('button', { name: /Alarms/ }).textContent()).includes('1'), 9000))
  await go('Alarms')
  await page.getByText('Access control says: door 3 forced').waitFor()
  await shot('alarms')
  await page.getByLabel(/Note for alarm/).fill('Guard checked it')
  await page.getByRole('button', { name: 'Acknowledge' }).click()
  await page.getByRole('heading', { name: 'Acknowledged' }).waitFor()
  check('alarm can be acknowledged', (await page.getByRole('cell', { name: /Guard checked it/ }).count()) === 1)

  // Protection
  await go('Settings')
  await section('Connections')
  await page.getByRole('button', { name: 'Remove Local service' }).click()
  await page.getByRole('alert').waitFor()
  check('a connection in use is protected', (await page.getByRole('alert').textContent()).includes('rule'))

  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
  receiver.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
