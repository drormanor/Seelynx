// End-to-end test of the Live tile controls. It needs its own empty server, and no camera:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/live.mjs [screenshot-dir]
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
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const near = (a, b) => Math.abs(a - b) < 2

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  for (const name of ['Door', 'Yard']) {
    await page.evaluate((n) => fetch('/api/cameras', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: n, main_url: 'rtsp://127.0.0.1:1/x', record: false, retention_days: 1, enabled: true }) }), name)
  }
  await page.reload()
  await page.locator('.camlist button').first().waitFor()
  await page.locator('.camlist button').nth(0).click()
  await page.locator('.camlist button').nth(1).click()

  const tile = (name) => page.locator('.tile', { has: page.getByRole('button', { name: `Remove ${name} from grid` }) })
  const shape = (name) => page.getByLabel(`Picture shape for ${name}`)
  const picture = async (name) => {
    const v = tile(name).locator('video')
    const fit = await v.evaluate((el) => getComputedStyle(el).objectFit)
    return { fit, box: await v.boundingBox(), tile: await tile(name).boundingBox() }
  }

  let p = await picture('Door')
  check('Original shows the whole picture', p.fit === 'contain' && near(p.box.width, p.tile.width))
  check('tiles keep their size', p.tile.width > 300 && p.tile.height > 150, `${p.tile.width}x${p.tile.height}`)

  await shape('Door').selectOption({ label: 'Fill' })
  check('Fill crops to the tile', (await picture('Door')).fit === 'cover')
  check('other cameras are left alone', (await picture('Yard')).fit === 'contain')
  await shape('Door').selectOption({ label: 'Stretch' })
  p = await picture('Door')
  check('Stretch covers the tile', p.fit === 'fill' && near(p.box.width, p.tile.width) && near(p.box.height, p.tile.height))

  for (const [label, ratio] of [['16:9', 16 / 9], ['4:3', 4 / 3]]) {
    await shape('Door').selectOption({ label })
    p = await picture('Door')
    check(`${label} is that shape`, Math.abs(p.box.width / p.box.height - ratio) < 0.02, `${p.box.width}x${p.box.height}`)
    check(`${label} stays inside the tile, centred`, p.box.width <= p.tile.width + 1 && p.box.height <= p.tile.height + 1
      && near(p.box.x - p.tile.x, p.tile.x + p.tile.width - p.box.x - p.box.width)
      && near(p.box.y - p.tile.y, p.tile.y + p.tile.height - p.box.y - p.box.height))
  }
  await shot('live-tools')

  await page.reload()
  await shape('Door').waitFor()
  check('a shape is remembered', (await shape('Door').locator('option:checked').textContent()) === '4:3')

  await shape('Door').dblclick()
  check('double-clicking the controls doesn\'t open the camera', (await page.getByRole('button', { name: 'Back to grid' }).count()) === 0)
  await page.keyboard.press('Escape')

  await tile('Door').dblclick({ position: { x: 30, y: 120 } })
  await page.getByRole('button', { name: 'Back to grid' }).waitFor()
  check('a full-screen camera keeps its shape', (await page.locator('.tile video').getAttribute('class')).includes('shape-4x3'))
  await shape('Door').selectOption({ label: 'Fill' })
  check('the shape can change at full screen', (await page.locator('.tile video').evaluate((el) => getComputedStyle(el).objectFit)) === 'cover')
  await page.getByRole('button', { name: 'Open Door in Playback' }).click()
  await page.getByLabel('Camera').waitFor()
  check('full screen opens that camera in Playback', (await page.getByLabel('Camera').locator('option:checked').textContent()) === 'Door')

  await nav.getByRole('button', { name: 'Live' }).click()
  await page.getByRole('button', { name: 'Back to grid' }).or(page.getByRole('button', { name: 'Open Yard in Playback' })).first().waitFor()
  await page.getByRole('button', { name: 'Open Yard in Playback' }).click()
  await page.getByLabel('Camera').waitFor()
  check('a tile opens its camera in Playback', (await page.getByLabel('Camera').locator('option:checked').textContent()) === 'Yard')
  check('Playback is the open page', (await nav.getByRole('button', { name: 'Playback' }).getAttribute('aria-current')) === 'page')

  // Someone who can't play back video shouldn't be offered the button.
  await page.route('**/api/session', async (r) => {
    const res = await r.fetch()
    const body = await res.json()
    body.user.permissions = body.user.permissions.filter((x) => x !== 'playback')
    await r.fulfill({ response: res, json: body })
  })
  await page.reload()
  await shape('Yard').waitFor()
  check('no Playback button without permission', (await page.getByRole('button', { name: 'Open Yard in Playback' }).count()) === 0)
  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
