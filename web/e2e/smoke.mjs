// Browser smoke test: drives a headless Chrome with a throwaway profile against
// a running server that has at least one online, recording camera.
//
//   SL_PASSWORD=... node web/e2e/smoke.mjs [screenshot-dir]
//
// Env: SL_URL (default http://127.0.0.1:8480), SL_USER (default admin),
//      SL_PASSWORD (required), CHROME (path to chrome.exe), SL_TILES (default 1)
import { chromium } from 'playwright-core'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8480'
const user = process.env.SL_USER ?? 'admin'
const password = process.env.SL_PASSWORD
const tiles = Number(process.env.SL_TILES ?? 1)
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
if (!password) { console.error('SL_PASSWORD is required'); process.exit(2) }

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 860 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
page.on('console', (m) => { if (m.type() === 'error') pageErrors.push(m.text()) })
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png') }) }

/** Reports whether each playing video on the page is really advancing. */
const videoProgress = () => page.evaluate(async () => {
  const vids = [...document.querySelectorAll('video')]
  const before = vids.map((v) => v.currentTime)
  await new Promise((r) => setTimeout(r, 2500))
  return vids.map((v, i) => ({ width: v.videoWidth, height: v.videoHeight, advanced: v.currentTime - before[i] }))
})

try {
  await page.goto(base)
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password').fill('wrong-password-here')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('alert').waitFor()
  check('wrong password is refused', (await page.getByRole('alert').textContent()).includes('Incorrect'))

  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  check('sign in works', true)

  // Live
  const grid = tiles > 9 ? '16' : tiles > 4 ? '9' : '4'
  await page.getByRole('group', { name: 'Grid size' }).getByRole('button', { name: grid, exact: true }).click()
  const list = page.locator('.camlist button')
  await list.first().waitFor()
  const total = await list.count()
  for (let i = 0; i < Math.min(tiles, total); i++) await list.nth(i).click()
  await page.waitForFunction((n) => {
    const v = [...document.querySelectorAll('video')]
    return v.length >= n && v.every((x) => x.videoWidth > 0)
  }, Math.min(tiles, total), { timeout: 30000 })
  const live = await videoProgress()
  check(`live video plays in ${live.length} tile(s)`, live.length > 0 && live.every((v) => v.width > 0 && v.advanced > 1),
    JSON.stringify(live[0]))
  check('grid uses the sub stream', live[0].width === 640, `${live[0].width}x${live[0].height}`)
  await shot('live')

  // A forced shape changes real video, and keeps playing.
  await page.locator('.tile select.shape').first().selectOption({ label: '4:3' })
  const forced = await page.locator('.tile video').first().boundingBox()
  check('a forced shape applies to real video', Math.abs(forced.width / forced.height - 4 / 3) < 0.02, `${forced.width}x${forced.height}`)
  await shot('live-shape')
  const still = await videoProgress()
  check('video keeps playing after a shape change', still[0].advanced > 1, JSON.stringify(still[0]))
  await page.locator('.tile select.shape').first().selectOption({ label: 'Original' })

  await page.locator('.tile').first().dblclick()
  await page.waitForFunction(() => document.querySelector('video')?.videoWidth === 1920, null, { timeout: 20000 })
  check('double-click switches to the main stream', true)
  await shot('live-focused')
  await page.getByRole('button', { name: 'Back to grid' }).click()

  // Playback
  await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: 'Playback' }).click()
  await page.locator('.timeline .span').first().waitFor({ timeout: 15000 })
  check('timeline shows recordings', true)
  const span = await page.locator('.timeline .span').first().boundingBox()
  await page.mouse.click(span.x + span.width / 2, span.y + span.height / 2)
  await page.waitForFunction(() => {
    const v = document.querySelector('.player video')
    return v && v.videoWidth > 0 && v.currentTime > 0
  }, null, { timeout: 20000 })
  const played = await videoProgress()
  check('recorded video plays', played[0].advanced > 1, JSON.stringify(played[0]))
  const code = await page.locator('.timecode').textContent()
  check('position is shown', /\d\d:\d\d:\d\d/.test(code), code)
  await shot('playback')

  // Remaining pages render
  await page.getByRole('button', { name: 'Cameras' }).click()
  await page.getByRole('cell', { name: 'Online' }).first().waitFor({ timeout: 10000 })
  check('cameras page shows an online camera', true)
  await shot('cameras')
  await page.getByRole('button', { name: 'System' }).click()
  await page.getByText('Incoming video').last().waitFor()
  check('system page renders', true)
  await page.waitForTimeout(500)
  await shot('system')
  await page.getByRole('button', { name: 'Users' }).click()
  await page.getByRole('heading', { name: 'Add user' }).waitFor()
  check('users page renders', true)

  // The signed-in-wrong-password 401 is the one expected console error.
  const unexpected = pageErrors.filter((e) => !e.includes('401'))
  check('no unexpected browser errors', unexpected.length === 0, unexpected.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
  console.log(pageErrors.slice(0, 5).join('\n'))
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
