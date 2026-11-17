// End-to-end test of the Storage page. It needs its own empty server:
//
//   bin\seelynx.exe -data <empty dir> -listen 127.0.0.1:8481 -port-offset 100 -webrtc-udp :8290 -mediamtx <path>
//   node web/e2e/storage.mjs [screenshot-dir]
//
// It adds a folder under the system's temporary folder as a second location.
import { chromium } from 'playwright-core'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8481'
const shots = process.argv[2]
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
const second = path.join(os.tmpdir(), `seelynx-e2e-${Date.now()}`)

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
const row = (name) => page.getByRole('row', { name: new RegExp(name) }).first()
const alert = page.getByRole('alert')
const folderBox = page.getByPlaceholder('D:\\Video')
const dialog = page.getByRole('dialog', { name: 'Choose a folder' })

try {
  await page.goto(base)
  await page.getByLabel('Username').fill('root')
  await page.getByLabel('Password').fill('admin-test-password')
  await page.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign out' }).waitFor()
  await nav.getByRole('button', { name: 'Storage' }).click()

  await row('Main storage').waitFor()
  check('the first location is set up from the start', (await row('Main storage').innerText()).includes('Recording'))

  await page.getByRole('button', { name: 'Add location' }).click()
  await page.getByLabel('Name').fill('Second disk')
  await folderBox.fill('relative\\folder')
  await page.getByRole('button', { name: 'Add location' }).last().click()
  await alert.waitFor()
  check('a folder that isn\'t a full path is refused', (await alert.innerText()).includes('full folder path'))

  // Choose the folder with the dialog: start from the top, go to the temporary folder,
  // make a new folder there and select it.
  // A network share asks for its login, and a folder on this server doesn't.
  check('a local folder has no login', (await page.getByLabel('Share username').count()) === 0)
  await folderBox.fill('\\\\nas-for-e2e\\video')
  check('a share asks for its login', await page.getByLabel('Share username').isVisible() && await page.getByLabel('Share password').isDisabled())
  await page.getByLabel('Share username').fill('seelynx')
  check('the password can be typed once there\'s a username', await page.getByLabel('Share password').isEnabled())
  await page.getByLabel('Share username').fill('')

  await folderBox.fill('')
  await page.getByRole('button', { name: 'Browse…' }).click()
  await dialog.waitFor()
  await dialog.getByRole('listitem').first().waitFor()
  check('the dialog starts with the server\'s drives or top folder', (await dialog.getByRole('listitem').count()) > 0)
  await dialog.getByLabel('Folder path').fill(os.tmpdir())
  await dialog.getByRole('button', { name: 'Go' }).click()
  await dialog.getByRole('button', { name: 'Up one folder' }).waitFor()
  await page.waitForFunction((p) => document.querySelector('dialog .hint.path')?.textContent === p, os.tmpdir())
  await dialog.getByRole('button', { name: 'New folder' }).click()
  await dialog.getByLabel('New folder name').fill(path.basename(second))
  await shot('storage-dialog')
  await dialog.getByRole('button', { name: 'Make folder' }).click()
  await page.waitForFunction((p) => document.querySelector('dialog .hint.path')?.textContent === p, second)
  check('New folder makes the folder and opens it', fs.existsSync(second))
  await dialog.getByRole('button', { name: 'Up one folder' }).click()
  await dialog.getByRole('listitem').filter({ hasText: path.basename(second) }).click()
  await page.waitForFunction((p) => document.querySelector('dialog .hint.path')?.textContent === p, second)
  await dialog.getByRole('button', { name: 'Select this folder' }).click()
  await dialog.waitFor({ state: 'detached' })
  check('the chosen folder fills in the path', (await folderBox.inputValue()) === second, await folderBox.inputValue())
  await page.getByRole('button', { name: 'Browse…' }).click()
  await page.waitForFunction((p) => document.querySelector('dialog .hint.path')?.textContent === p, second)
  check('the dialog opens at the folder already entered', true)
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await dialog.waitFor({ state: 'detached' })
  check('Cancel leaves the path alone', (await folderBox.inputValue()) === second)
  await page.getByLabel('Keep free (GB)').fill('5')
  await shot('storage-add')
  await page.getByRole('button', { name: 'Add location' }).last().click()
  await row('Second disk').waitFor()
  const added = await row('Second disk').innerText()
  check('a new location is checked and records', added.includes('Recording') && added.includes('5 GB'), added.replace(/\s+/g, ' '))
  check('its folder is marked', fs.existsSync(path.join(second, '.seelynx-storage')))
  await shot('storage-list')

  await row('Second disk').getByRole('button', { name: 'Edit' }).click()
  await page.getByLabel('Record new video here').uncheck()
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  check('a location can keep its video but stop recording', (await row('Second disk').innerText()).includes('Playback only'))

  await row('Second disk').getByRole('button', { name: 'Edit' }).click()
  await page.getByLabel(/Standby: record here/).check()
  check('standby hides the record choice', (await page.getByLabel('Record new video here').count()) === 0)
  await page.getByRole('button', { name: 'Save changes' }).click()
  await page.getByRole('button', { name: 'Save changes' }).waitFor({ state: 'detached' })
  await page.waitForFunction(() => [...document.querySelectorAll('tr')].some((r) => r.textContent.includes('Second disk') && r.textContent.includes('Standby')))
  check('a location can be on standby', (await row('Second disk').innerText()).includes('Records only while the other locations are unavailable'))
  await row('Main storage').getByRole('button', { name: 'Edit' }).click()
  await page.getByLabel(/Standby: record here/).check()
  await page.getByRole('button', { name: 'Save changes' }).click()
  await alert.waitFor()
  check('the last regular location can\'t go on standby', (await alert.innerText()).includes('isn\'t on standby'))
  await page.getByRole('button', { name: 'Cancel' }).click()

  await row('Main storage').getByRole('button', { name: 'Remove' }).click()
  await page.getByRole('button', { name: 'Remove location' }).click()
  await alert.waitFor()
  check('the last recording location can\'t be removed', (await alert.innerText()).includes('only location'))

  // Put a recording in the second location, and move it to the first before removing it.
  const mainPath = await row('Main storage').locator('.hint.path').innerText()
  const rec = path.join(second, 'cam_9_main', '2026-11-01_10-00-00-000000.mp4')
  fs.mkdirSync(path.dirname(rec), { recursive: true })
  fs.writeFileSync(rec, 'video')
  const hourAgo = new Date(Date.now() - 3600e3)
  fs.utimesSync(rec, hourAgo, hourAgo)
  await row('Second disk').getByRole('button', { name: 'Remove' }).click()
  check('removing explains what happens to the video', await page.getByText('The video stays on the disk').isVisible())
  await page.getByRole('radio', { name: /Move its video to/ }).check()
  check('the move offers the other location', (await page.getByLabel('Location to move the video to').innerText()).includes('Main storage'))
  await shot('storage-remove')
  await page.getByRole('button', { name: 'Move video and remove' }).click()
  await row('Second disk').waitFor({ state: 'detached', timeout: 60000 })
  const moved = path.join(mainPath, 'cam_9_main_at1', path.basename(rec))
  check('its video was moved before it was removed', fs.existsSync(moved) && !fs.existsSync(rec))
  check('a removed location\'s folder is left alone', fs.existsSync(second))

  await page.getByText('Help: Storage').click()
  check('the page has help', await page.getByText('How Seelynx shares cameras between locations').isVisible())
  const helpText = await page.locator('details.guide').first().innerText()
  check('paths in the help keep their backslashes', helpText.includes('such as D:\\Video or') && !helpText.includes('D:Video'))
  await page.getByText('Use a network share (NAS)').click()
  const shareText = await page.locator('details.guide').first().innerText()
  check('share paths in the help are shown right', shareText.includes('such as \\\\nas\\video.'), shareText.match(/such as [^,]*nas[^,.]*\./)?.[0])
  check('no browser errors', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
} catch (e) {
  check('test ran to completion', false, e.message.split('\n')[0])
  await shot('failure')
} finally {
  await browser.close()
  fs.rmSync(second, { recursive: true, force: true })
}
console.log(failures.length ? `\n${failures.length} FAILED` : '\nALL PASSED')
process.exit(failures.length ? 1 : 0)
