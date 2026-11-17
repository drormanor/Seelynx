// Load test: adds simulated cameras to a running server, then samples health.
// Requires scripts/sim-cameras.ps1 to be running. Windows only (uses typeperf-free
// process sampling through PowerShell).
//
//   SL_PASSWORD=... node scripts/load-test.mjs [cameras=100] [seconds=180]
//   SL_PASSWORD=... node scripts/load-test.mjs --remove     removes "Load NNN" cameras
import { execFileSync } from 'node:child_process'

const base = process.env.SL_URL ?? 'http://127.0.0.1:8480'
const remove = process.argv[2] === '--remove'
const target = Number(process.argv[2] ?? 100)
const seconds = Number(process.argv[3] ?? 180)

const login = await fetch(base + '/api/login', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: process.env.SL_USER ?? 'admin', password: process.env.SL_PASSWORD }),
})
if (!login.ok) throw new Error('sign-in failed: ' + login.status)
const cookie = login.headers.get('set-cookie').split(';')[0]
const call = async (path, method = 'GET', body) => {
  const res = await fetch(base + '/api' + path, {
    method,
    headers: { cookie, 'Content-Type': 'application/json' },
    body: body && JSON.stringify(body),
  })
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`)
  return res.status === 204 ? null : res.json()
}

const cameras = await call('/cameras')
if (remove) {
  const load = cameras.filter((c) => c.name.startsWith('Load '))
  for (const c of load) await call(`/cameras/${c.id}`, 'DELETE')
  console.log(`removed ${load.length} cameras`)
  process.exit(0)
}

const started = Date.now()
for (let i = cameras.length + 1; i <= target; i++) {
  await call('/cameras', 'POST', {
    name: `Load ${String(i).padStart(3, '0')}`,
    main_url: 'rtsp://127.0.0.1:18554/main',
    sub_url: 'rtsp://127.0.0.1:18554/sub',
    record: true, retention_days: 1, enabled: true,
  })
}
console.log(`added ${Math.max(target - cameras.length, 0)} cameras in ${((Date.now() - started) / 1000).toFixed(1)}s`)

const ps = `$r=@{}; Get-Process seelynx,mediamtx -ErrorAction SilentlyContinue | Where-Object { $_.Path -like '*\\NVR\\*' } | ForEach-Object { [pscustomobject]@{ id=$_.Id; name=$_.ProcessName; cmd=(Get-CimInstance Win32_Process -Filter "ProcessId=$($_.Id)").CommandLine; cpu=$_.TotalProcessorTime.TotalSeconds; mb=[math]::Round($_.WorkingSet64/1MB) } } | ConvertTo-Json -Compress`
const sample = () => {
  const out = execFileSync('powershell', ['-NoProfile', '-Command', ps], { encoding: 'utf8' })
  return [JSON.parse(out)].flat().map((p) => ({ ...p, role: p.name === 'seelynx' ? 'seelynx' : p.cmd.includes('sim') ? 'simulator' : 'engine' }))
}

const cores = 16
let prev = sample()
let prevAt = Date.now()
const rows = []
const end = Date.now() + seconds * 1000
while (Date.now() < end) {
  await new Promise((r) => setTimeout(r, 15000))
  const now = sample()
  const at = Date.now()
  const sys = await call('/system')
  const line = { t: Math.round((at - started) / 1000), online: sys.online, mbps: Math.round(sys.ingest_mbps) }
  for (const p of now) {
    const before = prev.find((x) => x.id === p.id)
    if (before) {
      line[p.role + '_cpu%'] = Number((((p.cpu - before.cpu) / ((at - prevAt) / 1000)) / cores * 100).toFixed(1))
      line[p.role + '_mb'] = p.mb
    }
  }
  console.log(JSON.stringify(line))
  rows.push(line)
  prev = now
  prevAt = at
}
const steady = rows.slice(Math.floor(rows.length / 2))
const avg = (k) => (steady.reduce((s, r) => s + (r[k] ?? 0), 0) / steady.length).toFixed(1)
console.log(`\nsteady state: online=${avg('online')} ingest=${avg('mbps')} Mbit/s engine_cpu=${avg('engine_cpu%')}% engine_mem=${avg('engine_mb')} MB seelynx_cpu=${avg('seelynx_cpu%')}% seelynx_mem=${avg('seelynx_mb')} MB (percent of all ${cores} threads)`)
