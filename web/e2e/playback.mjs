// End-to-end test of the playback strip's zoom and export range. It needs its own empty
// server, and simulates the recordings so it doesn't need a camera:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/playback.mjs [screenshot-dir]
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

const day = new Date()
day.setHours(0, 0, 0, 0)
const at = (h, m = 0, s = 0) => new Date(day.getTime() + ((h * 60 + m) * 60 + s) * 1000)
// Two stretches with a gap between them.
const segments = [
  { start: at(0, 0).toISOString(), duration: 6 * 3600 },
  { start: at(7, 0).toISOString(), duration: 3600 },
]

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true }) }
const nav = page.getByRole('navigation', { name: 'Main' })

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  await page.evaluate(() => fetch('/api/cameras', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'Door', main_url: 'rtsp://127.0.0.1:1/x', record: false, retention_days: 1, enabled: true }) }))
  await page.route('**/api/cameras/*/recordings?**', (r) => r.fulfill({ json: segments }))
  await page.route('**/api/cameras/*/motion?**', (r) => r.fulfill({ json: [] }))
  await page.route('**/api/cameras/*/playback?**', (r) => r.abort())

  await page.reload()
  await nav.getByRole('button', { name: 'Playback' }).click()
  const strip = page.getByRole('slider', { name: 'Recording timeline' })
  await strip.locator('.span').first().waitFor()
  const box = await strip.boundingBox()
  const ticks = () => page.locator('.ticks span').allTextContents()
  const pos = async (fraction) => [box.x + box.width * fraction, box.y + box.height / 2]

  const pointer = () => strip.evaluate((el) => getComputedStyle(el).cursor)
  check('a pointing hand on the whole day', (await pointer()) === 'pointer', await pointer())
  check('whole day to start', (await ticks()).includes('03:00'), (await ticks()).join(' '))
  check('nothing to zoom out from', await page.getByRole('button', { name: 'Zoom out' }).isDisabled())

  // A click means the same time at any zoom.
  await page.mouse.click(...await pos(0.125))
  const started = () => page.locator('video').getAttribute('src').then((u) => new Date(new URL(u, base).searchParams.get('start')))
  await page.locator('video').waitFor({ state: 'attached' })
  const first = await started()
  check('a click seeks to that time', Math.abs(first - at(3)) < 120000, first.toISOString())

  // Fast speeds play keyframes only, from where playback has got to, and slower ones
  // go back to the whole video.
  const videoSrc = () => page.locator('video').getAttribute('src')
  check('normal speed plays the whole video', !(await videoSrc()).includes('keyframes'))
  await page.getByRole('button', { name: '2×' }).click()
  check('2× still plays the whole video', !(await videoSrc()).includes('keyframes'))
  await page.getByRole('button', { name: '8×' }).click()
  await page.waitForFunction(() => document.querySelector('video')?.getAttribute('src')?.includes('keyframes=1'))
  check('8× plays keyframes only', Math.abs((await started()) - first) < 5000)
  await page.getByRole('button', { name: '4×' }).click()
  check('4× keeps to keyframes', (await videoSrc()).includes('keyframes=1'))
  await page.getByRole('button', { name: '1×' }).click()
  await page.waitForFunction(() => !document.querySelector('video')?.getAttribute('src')?.includes('keyframes'))
  check('back to normal speed plays the whole video', true)

  await page.getByRole('button', { name: 'Zoom in' }).click()
  await page.getByRole('button', { name: 'Zoom in' }).click()
  await page.getByRole('button', { name: 'Zoom in' }).click()
  const zoomed = await ticks()
  check('labels get finer when zoomed', !zoomed.every((t) => t.endsWith(':00')) || zoomed.length > 4, zoomed.join(' '))
  await shot('playback-zoomed')

  await page.mouse.move(...await pos(0.5))
  for (let i = 0; i < 20; i++) await page.mouse.wheel(0, -100)
  await page.waitForTimeout(200)
  const close = await ticks()
  check('the wheel zooms to seconds', close.some((t) => /\d\d:\d\d:\d\d/.test(t)), close.join(' '))
  check('zoom stops at one minute', await page.getByRole('button', { name: 'Zoom in' }).isDisabled())

  check('an open hand when zoomed', (await pointer()) === 'grab', await pointer())
  await page.keyboard.down('Shift')
  check('a crosshair while Shift is held', (await pointer()) === 'crosshair', await pointer())
  await page.keyboard.up('Shift')

  // Dragging pans instead of seeking.
  const src = await page.locator('video').getAttribute('src')
  await page.mouse.move(...await pos(0.7))
  await page.mouse.down()
  check('a closed hand while dragging', (await pointer()) === 'grabbing', await pointer())
  await page.mouse.move(...await pos(0.3), { steps: 5 })
  await page.mouse.up()
  check('dragging moves the strip', (await ticks()).join() !== close.join())
  check('dragging doesn\'t seek', (await page.locator('video').getAttribute('src')) === src)

  await page.getByRole('button', { name: 'Show the whole day' }).click()
  check('Fit returns to the whole day', (await ticks()).includes('03:00'))

  // Export range across the gap.
  await page.keyboard.down('Shift')
  await page.mouse.move(...await pos(0.2))
  await page.mouse.down()
  await page.mouse.move(...await pos(0.3), { steps: 5 })
  await page.mouse.up()
  await page.keyboard.up('Shift')
  check('Shift+drag selects a range', await page.getByLabel('Export from').isVisible())
  check('a selection doesn\'t seek', (await page.locator('video').getAttribute('src')) === src)

  await page.getByLabel('Export from').fill('05:30:00')
  await page.getByLabel('Export to').fill('07:30:00')
  const href = await page.getByRole('link', { name: 'Export video' }).getAttribute('href')
  const url = new URL(href, base)
  check('the link covers the range', new Date(url.searchParams.get('start')).getTime() === at(5, 30).getTime() && url.searchParams.get('duration') === '7200.0', href)
  check('gaps are pointed out', (await page.locator('.export-bar').textContent()).includes('with gaps'))
  await shot('playback-export')

  await page.getByLabel('Export from').fill('06:10:00')
  await page.getByLabel('Export to').fill('06:50:00')
  check('a range with no video can\'t be exported', (await page.getByRole('button', { name: 'Export video' }).isDisabled()) && (await page.locator('.export-bar').textContent()).includes('no recording'))
  await page.getByLabel('Export to').fill('06:00:00')
  check('the end must follow the start', (await page.locator('.export-bar').textContent()).includes('after the start'))
  await page.getByLabel('Export from').fill('00:00:00')
  await page.getByLabel('Export to').fill('07:00:00')
  check('a very long range is refused', (await page.locator('.export-bar').textContent()).includes('up to 6 hours'))

  await page.getByRole('button', { name: 'Clear' }).click()
  check('a selection can be cleared', (await page.getByLabel('Export from').count()) === 0)

  // Marking a start from the whole day.
  await page.mouse.click(...await pos(0.125))
  await page.getByRole('button', { name: 'Mark start' }).click()
  const seconds = async (label) => { const [h, m, s] = (await page.getByLabel(label).inputValue()).split(':').map(Number); return h * 3600 + m * 60 + s }
  check('a marked section is 3 minutes', (await seconds('Export to')) - (await seconds('Export from')) === 180)
  check('it has a handle on each side', (await page.locator('.selection .handle').count()) === 2)
  const end = await page.locator('.handle.end').boundingBox()
  const begin = await page.locator('.handle.start').boundingBox()
  check('marking zooms in far enough to adjust it', end.x - (begin.x + begin.width) > 60, `${end.x - begin.x}px`)
  await shot('playback-section')

  const before = await page.locator('video').getAttribute('src')
  const from = await seconds('Export from')
  const to = await seconds('Export to')
  const grab = async (handle, by) => {
    await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2)
    check('a two-way arrow on a handle', (await page.locator('.handle.end').evaluate((el) => getComputedStyle(el).cursor)) === 'ew-resize')
    await page.mouse.down()
    await page.mouse.move(handle.x + handle.width / 2 + by, handle.y + handle.height / 2, { steps: 5 })
    check('the arrow stays while dragging a handle', (await pointer()) === 'ew-resize', await pointer())
    await page.mouse.up()
  }
  await grab(end, 80)
  check('the end handle lengthens the section', (await seconds('Export to')) > to && (await seconds('Export from')) === from)
  const later = await seconds('Export to')
  await grab(begin, -80)
  check('the start handle lengthens the section', (await seconds('Export from')) < from && (await seconds('Export to')) === later)
  check('moving a handle doesn\'t seek', (await page.locator('video').getAttribute('src')) === before)
  const left = await page.locator('.handle.start').boundingBox()
  await page.mouse.move(left.x + 6, left.y + 10)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width - 2, left.y + 10, { steps: 5 })
  await page.mouse.up()
  check('the handles can\'t cross', (await seconds('Export to')) > (await seconds('Export from')))
  await page.getByRole('button', { name: 'Clear' }).click()

  check('Play, Slow motion and Pause are there', await page.getByRole('button', { name: 'Slow motion' }).isEnabled() && await page.getByRole('button', { name: 'Play', exact: true }).isEnabled() && (await page.getByRole('button', { name: 'Pause' }).count()) === 1)
  const x = async (loc) => (await loc.boundingBox()).x
  const transport = page.getByRole('group', { name: 'Playback', exact: true })
  check('speeds on the left, play in the middle, marks on the right',
    (await x(page.getByRole('group', { name: 'Playback speed' }))) < (await x(transport)) && (await x(transport)) < (await x(page.getByRole('button', { name: 'Mark start' }))))
  const pb = await transport.boundingBox()
  const bar = await page.locator(".timeline").boundingBox()
  check('play controls are centred', Math.abs(pb.x + pb.width / 2 - (bar.x + bar.width / 2)) < 40, `${Math.round(pb.x + pb.width / 2)} vs ${Math.round(bar.x + bar.width / 2)}`)
  const clockSecs = async () => (await page.locator('.timecode').textContent()).split(':').reduce((a, n) => a * 60 + Number(n), 0)
  const was = await clockSecs()
  await page.getByRole('button', { name: 'Forward 10 seconds' }).click()
  check('forward 10 seconds', (await clockSecs()) - was === 10, `${was} then ${await clockSecs()}`)
  await page.getByRole('button', { name: 'Back 10 seconds' }).click()
  await page.getByRole('button', { name: 'Back 10 seconds' }).click()
  check('back 10 seconds', was - (await clockSecs()) === 10, `${was} then ${await clockSecs()}`)
  await shot('playback-controls')
  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
