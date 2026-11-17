// End-to-end test of fisheye cameras: setting up the lens, and the dewarped views in Live
// and Playback, then turning a camera's picture. It needs its own empty server and a
// fisheye camera, such as a recording from one streamed by a MediaMTX of its own:
//
//   SL_FISHEYE=rtsp://127.0.0.1:19754 node web/e2e/fisheye.mjs [screenshot-dir]
//
// The camera's main and sub streams are $SL_FISHEYE/main and $SL_FISHEYE/sub.
import { chromium } from 'playwright-core'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const stream = process.env.SL_FISHEYE ?? 'rtsp://127.0.0.1:19754'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'

const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failures.push(name)
}

const browser = await chromium.launch({
  executablePath: chrome, headless: true,
  args: ['--autoplay-policy=no-user-gesture-required', '--disable-background-media-suspend', '--disable-renderer-backgrounding',
    '--disable-background-timer-throttling', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'],
})
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))
const shot = async (name, target = page) => { if (shots) await target.screenshot({ path: path.join(shots, name + '.png') }) }
const nav = page.getByRole('navigation', { name: 'Main' })
const api = (p, method = 'GET', body) => page.evaluate(async ([p, method, body]) => {
  const r = await fetch('/api' + p, { method, headers: body ? { 'Content-Type': 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined })
  return r.json()
}, [p, method, body])
// The share of a canvas's pixels that aren't black, from a screenshot of it.
const lit = async (loc) => {
  const png = await loc.screenshot()
  return page.evaluate(async (b64) => {
    const img = new Image()
    img.src = 'data:image/png;base64,' + b64
    await img.decode()
    const c = document.createElement('canvas')
    c.width = img.width; c.height = img.height
    const x = c.getContext('2d')
    x.drawImage(img, 0, 0)
    const d = x.getImageData(0, 0, c.width, c.height).data
    let n = 0
    for (let i = 0; i < d.length; i += 4) if (Math.max(d[i], d[i + 1], d[i + 2]) > 30) n++
    return n / (d.length / 4)
  }, png.toString('base64'))
}

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  const cam = await api('/cameras', 'POST', { name: 'Yard fisheye', main_url: `${stream}/main`, sub_url: `${stream}/sub`, record: true, retention_days: 1, enabled: true })
  const addedAt = Date.now()

  // Set up the lens.
  await nav.getByRole('button', { name: 'Cameras' }).click()
  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  check('a new camera has no lens until it has a picture', true)
  // A camera that has only just been added may not have connected yet.
  for (let i = 0; i < 6 && !(await page.locator('figure.camera-picture img').count()); i++) {
    await page.waitForTimeout(4000)
    await page.getByRole('button', { name: 'Take a new picture' }).click()
    await page.locator('figure.camera-picture img').waitFor({ timeout: 8000 }).catch(() => {})
  }
  const lensBox = page.getByLabel('This camera has a fisheye lens')
  check('the lens is off to begin with', !(await lensBox.isChecked()))
  await lensBox.check()
  await page.getByText('Found the circle').waitFor({ timeout: 5000 })
  const value = async (label) => Number(await page.getByLabel(label).inputValue())
  const [x, y, r] = [await value('Circle centre, across (%)'), await value('Circle centre, down (%)'), await value('Circle radius (% of height)')]
  check('the circle is found in the picture', Math.abs(x - 49.8) < 1 && Math.abs(y - 48.3) < 1 && Math.abs(r - 50.4) < 1, `centre ${x}%, ${y}%, radius ${r}%`)
  const preview = page.locator('.lens-result canvas.dewarp')
  await page.waitForTimeout(500)
  check('the preview shows the dewarped picture', (await lit(preview)) > 0.5)
  await page.getByLabel('Tilt down (degrees)').fill('10')
  await shot('fisheye-settings', page.locator('fieldset.lens'))
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  const saved = (await api('/cameras')).find((c) => c.id === cam.id)
  check('the lens is saved with the camera', saved?.fisheye?.mount === 'wall' && saved.fisheye.tilt === 10 && Math.abs(saved.fisheye.r * 100 - r) < 0.2, JSON.stringify(saved?.fisheye))

  // Live: a panorama to start with, then a virtual camera that's dragged around.
  await nav.getByRole('button', { name: 'Live' }).click()
  await page.locator('.sidebar').getByRole('button', { name: /Yard fisheye/ }).click()
  const tile = page.locator('.tile').filter({ hasText: 'Yard fisheye' })
  const canvas = tile.locator('canvas.dewarp')
  await canvas.waitFor()
  await page.waitForFunction(() => [...document.querySelectorAll('.tile video')].some((v) => v.readyState >= 2), null, { timeout: 30000 })
  await page.waitForTimeout(1000)
  check('a fisheye camera opens as a panorama', (await canvas.getAttribute('data-lens-view')) === 'panorama')
  const panoLit = await lit(canvas)
  check('the panorama shows the video', panoLit > 0.4, `${Math.round(panoLit * 100)}% lit`)
  await shot('fisheye-live-panorama', tile)
  const picker = page.getByLabel('Fisheye view for Yard fisheye')
  check('the picture shape is hidden for a dewarped view', (await tile.getByLabel('Picture shape for Yard fisheye').count()) === 0)
  await picker.selectOption('view')
  await page.waitForTimeout(500)
  check('the virtual camera fills its tile', (await lit(canvas)) > 0.9)
  await shot('fisheye-live-view', tile)
  const before = await canvas.screenshot()
  const box = await canvas.boundingBox()
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2 - 200, box.y + box.height / 2 + 60, { steps: 8 })
  await page.mouse.up()
  await page.mouse.wheel(0, 400)
  await page.waitForTimeout(400)
  const moved = JSON.parse(await page.evaluate(() => localStorage.getItem('live.lens')))
  const v = Object.values(moved)[0]
  check('dragging looks to the right and up, and scrolling widens the view', v.yaw > 5 && v.pitch > -10 && v.fov > 90, JSON.stringify(v))
  check('the picture follows the drag', !before.equals(await canvas.screenshot()))
  await shot('fisheye-live-dragged', tile)
  await picker.selectOption('circle')
  check('Circle shows the camera as it is', (await tile.locator('canvas.dewarp').count()) === 0 && (await tile.getByLabel('Picture shape for Yard fisheye').count()) === 1)
  await picker.selectOption('panorama')

  // Playback of what was just recorded.
  const wait = 25000 - (Date.now() - addedAt)
  if (wait > 0) await page.waitForTimeout(wait)
  await nav.getByRole('button', { name: 'Playback' }).click()
  await page.locator('.timeline .span').first().waitFor({ timeout: 30000 })
  const span = await page.locator('.timeline .span').last().boundingBox()
  await page.mouse.click(span.x + span.width * 0.3, span.y + span.height / 2)
  await page.waitForFunction(() => (document.querySelector('.player video')?.readyState ?? 0) >= 2, null, { timeout: 30000 })
  await page.waitForTimeout(800)
  const player = page.locator('.player canvas.dewarp')
  check('Playback shows the panorama too', (await player.getAttribute('data-lens-view')) === 'panorama' && (await lit(player)) > 0.3)
  await shot('fisheye-playback-panorama', page.locator('.player'))
  await page.getByLabel('Fisheye view for Yard fisheye').selectOption('view')
  await page.waitForTimeout(500)
  check('and the virtual camera', (await lit(player)) > 0.9)
  await shot('fisheye-playback-view', page.locator('.player'))
  // A paused video keeps following the drag.
  await page.getByRole('button', { name: 'Pause', exact: true }).click()
  const still = await player.screenshot()
  const pb = await player.boundingBox()
  await page.mouse.move(pb.x + pb.width / 2, pb.y + pb.height / 2)
  await page.mouse.down()
  await page.mouse.move(pb.x + pb.width / 2 + 250, pb.y + pb.height / 2, { steps: 6 })
  await page.mouse.up()
  await page.waitForTimeout(300)
  check('a paused video can still be looked around', !still.equals(await player.screenshot()))

  // Turning the lens off returns the camera to its circle everywhere.
  await api(`/cameras/${cam.id}/fisheye`, 'PUT', { lens: false })
  await nav.getByRole('button', { name: 'Live' }).click()
  await page.locator('.tile video').first().waitFor()
  await page.waitForTimeout(6000)
  check('without a lens there is no fisheye view', (await page.locator('canvas.dewarp').count()) === 0 && (await page.getByLabel('Fisheye view for Yard fisheye').count()) === 0)

  // Turning the picture, for a camera mounted on its side. A quarter-turned video fills
  // the same space as before, with its width and height swapped.
  const fits = (el) => el.evaluate((v) => {
    const r = v.getBoundingClientRect()
    const p = v.parentElement.getBoundingClientRect()
    return Math.abs(r.width - p.width) < 2 && Math.abs(r.height - p.height) < 2 && getComputedStyle(v).transform !== 'none'
  })
  await nav.getByRole('button', { name: 'Cameras' }).click()
  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  await page.getByLabel('Turn the picture').selectOption('90')
  check('the camera\'s picture turns as it is chosen', await page.locator('figure.camera-picture img.rot-90').waitFor({ timeout: 15000 }).then(() => true, () => false))
  await shot('rotate-settings', page.locator('figure.camera-picture'))
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  check('the turn is saved with the camera', (await api('/cameras')).find((c) => c.id === cam.id)?.rotation === 90)
  check('the list\'s picture is turned too', (await page.locator('.thumb.turn img.rot-90').count()) === 1)
  await nav.getByRole('button', { name: 'Live' }).click()
  const turned = page.locator('.tile video.rot-90')
  await turned.waitFor()
  await page.waitForFunction(() => (document.querySelector('.tile video.rot-90')?.readyState ?? 0) >= 2, null, { timeout: 30000 })
  check('Live shows the picture turned, filling its tile', await fits(turned))
  await shot('rotate-live', page.locator('.tile').first())
  await nav.getByRole('button', { name: 'Playback' }).click()
  await page.locator('.timeline .span').first().waitFor({ timeout: 30000 })
  const span2 = await page.locator('.timeline .span').last().boundingBox()
  await page.mouse.click(span2.x + span2.width * 0.3, span2.y + span2.height / 2)
  await page.waitForFunction(() => (document.querySelector('.player video')?.readyState ?? 0) >= 2, null, { timeout: 30000 })
  check('and so does Playback', await fits(page.locator('.player video.rot-90')))
  await shot('rotate-playback', page.locator('.player'))

  // A fisheye camera mounted upside down: the dewarped views are turned the right way up.
  await api(`/cameras/${cam.id}/fisheye`, 'PUT', { lens: true, ...saved.fisheye, tilt: 0 })
  await api(`/cameras/${cam.id}/rotation`, 'PUT', { rotation: 180 })
  await nav.getByRole('button', { name: 'Live' }).click()
  await page.locator('.tile canvas.dewarp').waitFor({ timeout: 15000 })
  await page.waitForTimeout(1500)
  await shot('rotate-fisheye-180', page.locator('.tile').first())
  check('a turned fisheye camera still dewarps', (await lit(page.locator('.tile canvas.dewarp'))) > 0.4)
  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('fisheye-failure')
} finally {
  await browser.close()
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
