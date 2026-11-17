// Opens one camera's streams in headless Chrome and reports what the browser gets.
//   SL_PASSWORD=... node web/e2e/probe-camera.mjs <camera id>
import { chromium } from 'playwright-core'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8480'
const id = Number(process.argv[2])
const chrome = process.env.CHROME ?? 'C:/Program Files/Google/Chrome/Application/chrome.exe'
if (!id || !process.env.SL_PASSWORD) { console.error('usage: SL_PASSWORD=... node probe-camera.mjs <id>'); process.exit(2) }

const browser = await chromium.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage()
await page.goto(base)
const login = await page.evaluate(async ([u, p]) => (await fetch('/api/login', {
  method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: p }),
})).status, [process.env.SL_USER ?? 'admin', process.env.SL_PASSWORD])
console.log('sign in', login)

for (const stream of ['sub', 'main']) {
  const result = await page.evaluate(async ([cam, s]) => {
    const pc = new RTCPeerConnection()
    pc.addTransceiver('video', { direction: 'recvonly' })
    const v = document.createElement('video')
    v.muted = true; v.autoplay = true; v.playsInline = true
    document.body.append(v)
    pc.ontrack = (e) => { v.srcObject = e.streams[0] }
    await pc.setLocalDescription(await pc.createOffer())
    await new Promise((r) => setTimeout(r, 1200))
    const res = await fetch(`/api/cameras/${cam}/whep?stream=${s}`, {
      method: 'POST', headers: { 'Content-Type': 'application/sdp' }, body: pc.localDescription.sdp,
    })
    const body = await res.text()
    if (!res.ok) { pc.close(); v.remove(); return { status: res.status, body: body.slice(0, 200) } }
    await pc.setRemoteDescription({ type: 'answer', sdp: body })
    const codecs = [...new Set(body.split('\n').filter((l) => l.startsWith('a=rtpmap')).map((l) => l.trim().split(' ')[1]))].slice(0, 4)
    const decoded = async () => { let n = 0; (await pc.getStats()).forEach((s) => { if (s.type === "inbound-rtp" && s.kind === "video") n = s.framesDecoded ?? 0 }); return n }
    await new Promise((r) => setTimeout(r, 4000))
    const first = await decoded()
    await new Promise((r) => setTimeout(r, 4000))
    const framesPerSecond = (await decoded() - first) / 4
    const out = { status: res.status, state: pc.connectionState, width: v.videoWidth, height: v.videoHeight, framesPerSecond, codecs }
    pc.close(); v.remove()
    return out
  }, [id, stream])
  console.log(stream, JSON.stringify(result))
}
await browser.close()
