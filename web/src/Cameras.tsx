import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Guide } from './help'
import { api, errorText, type Camera, type DefaultLogin, type Device, type Fisheye, type GroupData, type Item, type Profile, type Rotation, type ScheduleData } from './api'
import { LensSettings } from './Fisheye'

interface Draft {
  id?: number; name: string; main_url: string; sub_url: string; username: string
  password: string; passwordSet: boolean; record: boolean; retention_days: number; enabled: boolean
  group_id: number; numeric_id: number; schedule_id: number
  motion_only: boolean; pre_seconds: number; post_seconds: number; outside_mode: number
  useDefault: boolean
  fisheye: Fisheye | null
  rotation: Rotation
}
const blank: Draft = {
  name: '', main_url: '', sub_url: '', username: '', password: '', passwordSet: false, record: true, retention_days: 30, enabled: true,
  group_id: 0, numeric_id: 0, schedule_id: 0, motion_only: false, pre_seconds: 10, post_seconds: 30, outside_mode: 0, useDefault: false, fisheye: null, rotation: 0,
}

export function Cameras() {
  const [cameras, setCameras] = useState<Camera[]>([])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [scanning, setScanning] = useState(false)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState<number | null>(null)
  const [login, setLogin] = useState<DefaultLogin>({ username: '', has_password: false })
  const [editingLogin, setEditingLogin] = useState(false)
  // Pictures are taken again every five minutes while the page is open.
  const [round, setRound] = useState(() => Math.floor(Date.now() / 300e3))
  useEffect(() => {
    const t = setInterval(() => setRound(Math.floor(Date.now() / 300e3)), 30e3)
    return () => clearInterval(t)
  }, [])

  useEffect(() => { api<DefaultLogin>('/camera-login').then(setLogin).catch((e) => setError(errorText(e))) }, [])

  const load = useCallback(() => api<Camera[]>('/cameras').then(setCameras).catch((e) => setError(errorText(e))), [])
  useEffect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  const remove = async (id: number) => {
    try {
      await api(`/cameras/${id}`, 'DELETE')
      setConfirming(null)
      load()
    } catch (e) { setError(errorText(e)) }
  }

  const edit = (c: Camera) => { setScanning(false); setEditingLogin(false); setDraft({ ...c, password: '', passwordSet: c.has_password, useDefault: false }) }

  return (
    <div className="page">
      <div className="toolbar">
        <h2>Cameras <span className="muted">{cameras.length}</span></h2>
        <span className="spacer" />
        <button className="ghost" onClick={() => { setEditingLogin(true); setScanning(false); setDraft(null) }}>Default login</button>
        <button className="ghost" onClick={() => { setScanning(true); setDraft(null); setEditingLogin(false) }}>Find ONVIF cameras</button>
        <button className="primary" onClick={() => { setDraft({ ...blank, useDefault: !!login.username }); setScanning(false); setEditingLogin(false) }}>Add camera</button>
      </div>
      <Guide topic="cameras" />
      {error && <p className="error" role="alert">{error}</p>}
      {editingLogin && <DefaultLoginForm login={login} onClose={() => setEditingLogin(false)} onSaved={(l) => { setLogin(l); setEditingLogin(false) }} />}
      {scanning && <Discovery login={login} onClose={() => setScanning(false)} onPick={(d) => { setDraft(d); setScanning(false) }} />}
      {draft && <CameraForm key={draft.id ?? 'new'} draft={draft} login={login} onClose={() => setDraft(null)} onSaved={() => { setDraft(null); load() }} />}
      <div className="table-wrap">
        <table>
          <thead><tr><th aria-label="Picture" /><th>Name</th><th>Status</th><th>Recording</th><th>Keep for</th><th className="num">Mbit/s</th><th /></tr></thead>
          <tbody>
            {cameras.map((c) => (
              <tr key={c.id}>
                <td className="thumb-cell">
                  <button className="thumb-button" title={`Edit ${c.name}`} aria-label={`Edit ${c.name}`} onClick={() => edit(c)}>
                    <Thumbnail key={round} id={c.id} name={c.name} round={round} rotation={c.rotation} />
                  </button>
                </td>
                <td>{c.name}</td>
                <td>{!c.enabled ? <span className="muted">Disabled</span> : <><span className={c.online ? 'dot on' : c.idle ? 'dot idle' : 'dot'} /> {c.online ? 'Online' : c.idle ? 'Standby' : 'Offline'}</>}</td>
                <td>{recordingLabel(c)}</td>
                <td>{c.retention_days === 0 ? 'Until disk is full' : `${c.retention_days} days`}</td>
                <td className="num">{c.mbps.toFixed(1)}</td>
                <td className="actions">
                  {confirming === c.id
                    ? <><button className="danger" onClick={() => remove(c.id)}>Remove camera</button><button className="ghost" onClick={() => setConfirming(null)}>Keep</button></>
                    : <><button className="ghost" onClick={() => edit(c)}>Edit</button><button className="ghost" onClick={() => setConfirming(c.id)}>Remove</button></>}
                </td>
              </tr>
            ))}
            {cameras.length === 0 && <tr><td colSpan={7} className="muted">No cameras yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// Thumbnail is a camera's picture in the list. It loads only once it scrolls into view.
function Thumbnail({ id, name, round, rotation }: { id: number; name: string; round: number; rotation: Rotation }) {
  const [failed, setFailed] = useState(false)
  if (failed) return <span className="thumb none">No picture</span>
  return (
    <span className="thumb turn">
      <img className={`rot-${rotation}`} src={`/api/cameras/${id}/picture?r=${round}`} alt={`Picture from ${name}`} loading="lazy" onError={() => setFailed(true)} />
    </span>
  )
}

/** The ways a camera's picture can be turned, for a camera mounted on its side or upside down. */
const rotations: { value: Rotation; label: string }[] = [
  { value: 0, label: 'As the camera sends it' },
  { value: 90, label: 'A quarter turn clockwise' },
  { value: 180, label: 'Upside down' },
  { value: 270, label: 'A quarter turn anticlockwise' },
]

function ago(taken: string) {
  const s = Math.max(0, Math.round((Date.now() - new Date(taken).getTime()) / 1000))
  if (s < 90) return 'just now'
  if (s < 5400) return `${Math.round(s / 60)} minutes ago`
  if (s < 129600) return `${Math.round(s / 3600)} hours ago`
  return `on ${new Date(taken).toLocaleDateString()}`
}

// CameraPicture shows what a camera sees, to help tell where it is.
function CameraPicture({ id, name, rotation, onPicture }: { id: number; name: string; rotation: Rotation; onPicture?: (url: string | null) => void }) {
  const [pic, setPic] = useState<{ url: string; taken: string } | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'none'>('loading')

  const load = useCallback(async (fresh: boolean) => {
    setState('loading')
    try {
      const res = await fetch(`/api/cameras/${id}/picture${fresh ? '?fresh=1' : ''}`)
      if (!res.ok) throw new Error()
      const url = URL.createObjectURL(await res.blob())
      setPic({ url, taken: res.headers.get('X-Picture-Taken') ?? '' })
      setState('ready')
    } catch { setState('none') }
  }, [id])
  useEffect(() => { load(false) }, [load])
  // Each picture's memory is freed once it's replaced or the form closes.
  useEffect(() => { if (pic) { const u = pic.url; return () => URL.revokeObjectURL(u) } }, [pic])
  useEffect(() => { onPicture?.(pic?.url ?? null) }, [pic, onPicture])

  return (
    <figure className="camera-picture">
      {pic ? <div className="turn"><img className={`rot-${rotation}`} src={pic.url} alt={`Picture from ${name}`} /></div> : <div className="thumb none">{state === 'loading' ? 'Getting a picture…' : 'No picture'}</div>}
      <figcaption>
        <span className="muted">{state === 'loading' && pic ? 'Getting a new picture…'
          : state === 'none' && !pic ? "The camera didn't send a picture. Check it's online and its login is right."
          : pic?.taken ? `Taken ${ago(pic.taken)}` : ''}</span>
        <button type="button" className="ghost" disabled={state === 'loading'} onClick={() => load(true)}>Take a new picture</button>
      </figcaption>
    </figure>
  )
}

function DefaultLoginForm({ login, onClose, onSaved }: { login: DefaultLogin; onClose: () => void; onSaved: (l: DefaultLogin) => void }) {
  const [username, setUsername] = useState(login.username)
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const save = async (body: { username: string; password?: string }) => {
    setBusy(true)
    setError('')
    try {
      onSaved(await api<DefaultLogin>('/camera-login', 'PUT', body))
    } catch (err) {
      setError(errorText(err))
      setBusy(false)
    }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    // An untouched password box means "keep the stored password".
    save({ username, password: password || (login.has_password ? undefined : '') })
  }
  return (
    <form className="card form" onSubmit={submit}>
      <h3>Default camera login</h3>
      <p className="muted">New cameras start with this login, and you can change it for any camera. Cameras you've already added aren't affected.</p>
      <div className="fields">
        <label>Default username<input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" required autoFocus /></label>
        <label>Default password
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" placeholder={login.has_password ? 'Unchanged' : ''} />
          <span className="hint">Stored encrypted, and never shown again.</span>
        </label>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="row">
        <button className="primary" disabled={busy}>Save default</button>
        {login.username && <button type="button" className="ghost" disabled={busy} onClick={() => save({ username: '', password: '' })}>Remove default</button>}
        <button type="button" className="ghost" onClick={onClose}>Cancel</button>
      </div>
    </form>
  )
}

const motionHint = 'On motion only needs ONVIF and a login, with motion detection on in the camera. Until the camera reports motion, all video is kept.'
const modeName = (motion: boolean) => motion ? 'keeps only video near motion' : 'keeps all video'
const usesMotion = (d: { motion_only: boolean; schedule_id: number; outside_mode: number }) => d.motion_only || (d.schedule_id !== 0 && d.outside_mode === 2)

function scheduleSummary(d: Draft) {
  const outside = d.outside_mode === 0 ? "doesn't record" : modeName(d.outside_mode === 2)
  return `During the schedule the camera ${modeName(d.motion_only)}. The rest of the time it ${outside}.`
}

// recordingLabel describes a camera's recording in the list.
function recordingLabel(c: Camera) {
  if (!c.record) return 'Off'
  const mode = (motion: boolean) => motion ? 'On motion' : 'Continuous'
  if (!c.schedule_id) return mode(c.motion_only)
  const rest = c.outside_mode === 0 ? 'off' : mode(c.outside_mode === 2).toLowerCase()
  return `Scheduled: ${mode(c.motion_only).toLowerCase()}, else ${rest}`
}

function CameraForm({ draft, login, onClose, onSaved }: { draft: Draft; login: DefaultLogin; onClose: () => void; onSaved: () => void }) {
  const [d, setD] = useState(draft)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [groups, setGroups] = useState<Item<GroupData>[]>([])
  const [schedules, setSchedules] = useState<Item<ScheduleData>[]>([])
  const [picture, setPicture] = useState<string | null>(null)
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((p) => ({ ...p, [k]: v }))

  useEffect(() => {
    api<Item<GroupData>[]>('/config/groups').then(setGroups).catch((e) => setError(errorText(e)))
    api<Item<ScheduleData>[]>('/config/schedules').then(setSchedules).catch((e) => setError(errorText(e)))
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    // An untouched password box means "keep the stored password".
    const body = {
      name: d.name, main_url: d.main_url, sub_url: d.sub_url, username: d.useDefault ? '' : d.username,
      use_default_login: d.useDefault,
      record: d.record, retention_days: d.retention_days, enabled: d.enabled,
      group_id: d.group_id, numeric_id: d.numeric_id, schedule_id: d.schedule_id,
      motion_only: d.motion_only, pre_seconds: d.pre_seconds, post_seconds: d.post_seconds, outside_mode: d.schedule_id ? d.outside_mode : 0,
      password: d.password || (d.passwordSet ? undefined : ''),
    }
    try {
      const saved = await api<Camera>(d.id ? `/cameras/${d.id}` : '/cameras', d.id ? 'PUT' : 'POST', body)
      if (d.rotation !== (d.id ? draft.rotation : 0)) await api(`/cameras/${saved.id}/rotation`, 'PUT', { rotation: d.rotation })
      if (d.id && JSON.stringify(d.fisheye) !== JSON.stringify(draft.fisheye)) {
        await api(`/cameras/${d.id}/fisheye`, 'PUT', d.fisheye ? { lens: true, ...d.fisheye } : { lens: false })
      }
      onSaved()
    } catch (err) {
      setError(errorText(err))
      setBusy(false)
    }
  }
  return (
    <form className="card form" onSubmit={submit}>
      <h3>{d.id ? `Edit ${draft.name}` : 'Add camera'}</h3>
      {d.id && <CameraPicture id={d.id} name={draft.name} rotation={d.rotation} onPicture={setPicture} />}
      <div className="fields">
        <label>Name<input value={d.name} onChange={(e) => set('name', e.target.value)} required autoFocus /><span className="hint">Say where it is, such as Front door.</span></label>
        <label>Keep recordings for (days)
          <input type="number" min={0} max={3650} value={d.retention_days} onChange={(e) => set('retention_days', Number(e.target.value))} required />
          <span className="hint">0 keeps them until disk space runs low.</span>
        </label>
        <label className="wide">Main stream URL
          <input value={d.main_url} onChange={(e) => set('main_url', e.target.value)} placeholder="rtsp://192.168.1.20:554/stream1" required />
          <span className="hint">Recorded, and used for full-quality viewing. Find it in the camera's manual, or use Find ONVIF cameras.</span>
        </label>
        <label className="wide">Sub stream URL (optional)
          <input value={d.sub_url} onChange={(e) => set('sub_url', e.target.value)} placeholder="rtsp://192.168.1.20:554/stream2" />
          <span className="hint">Lower resolution, used in grids. Strongly recommended for larger systems.</span>
        </label>
        {!d.id && login.username && (
          <label className="check wide"><input type="checkbox" checked={d.useDefault} onChange={(e) => set('useDefault', e.target.checked)} /> Use the default login <span className="hint">Signs in as {login.username}. Untick to enter a different one.</span></label>
        )}
        {!d.useDefault && <>
        <label>Camera username<input value={d.username} onChange={(e) => set('username', e.target.value)} autoComplete="off" /><span className="hint">The login set on the camera. Motion events need this.</span></label>
        <label>Camera password
          <input type="password" value={d.password} onChange={(e) => set('password', e.target.value)} autoComplete="new-password" placeholder={d.passwordSet ? 'Unchanged' : ''} />
          <span className="hint">Stored encrypted.</span>
        </label>
        </>}
        <label>Group
          <select value={d.group_id} onChange={(e) => set('group_id', Number(e.target.value))}>
            <option value={0}>None</option>
            {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </select>
          <span className="hint">Organizes the list on the Live page. Add groups in Settings.</span>
        </label>
        <label>Recording schedule
          <select value={d.schedule_id} onChange={(e) => set('schedule_id', Number(e.target.value))}>
            <option value={0}>Always</option>
            {schedules.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
          <span className="hint">{d.schedule_id ? 'Then choose below how it records in and out of these hours.' : 'Records the same way at all times. Add schedules in Settings.'}</span>
        </label>
        {d.record && (
          <label>{d.schedule_id ? 'During the schedule' : 'Recording mode'}
            <select value={d.motion_only ? 'motion' : 'all'} onChange={(e) => setD((p) => ({
              ...p, motion_only: e.target.value === 'motion',
              pre_seconds: p.pre_seconds || 10, post_seconds: p.post_seconds || 30,
            }))}>
              <option value="all">Keep all video</option>
              <option value="motion">On motion only</option>
            </select>
            {!d.schedule_id && <span className="hint">{d.motion_only ? motionHint : 'Keeps everything for as long as you set above.'}</span>}
          </label>
        )}
        {d.record && d.schedule_id !== 0 && (
          <label>Rest of the time
            <select value={d.outside_mode} onChange={(e) => setD((p) => ({
              ...p, outside_mode: Number(e.target.value),
              pre_seconds: p.pre_seconds || 10, post_seconds: p.post_seconds || 30,
            }))}>
              <option value={0}>Don't record</option>
              <option value={1}>Keep all video</option>
              <option value={2}>On motion only</option>
            </select>
          </label>
        )}
        {d.record && d.schedule_id !== 0 && <p className="hint wide">{scheduleSummary(d)}{usesMotion(d) ? ' ' + motionHint : ''}</p>}
        {d.record && usesMotion(d) && <>
          <label>Keep before motion (seconds)
            <input type="number" min={0} max={300} value={d.pre_seconds} onChange={(e) => set('pre_seconds', Number(e.target.value))} required />
            <span className="hint">At least 5. Cameras report motion a moment late.</span>
          </label>
          <label>Keep after motion (seconds)
            <input type="number" min={0} max={300} value={d.post_seconds} onChange={(e) => set('post_seconds', Number(e.target.value))} required />
            <span className="hint">Counted from when motion stops.</span>
          </label>
        </>}
        <label>Numeric ID
          <input type="number" min={0} max={999999} value={d.numeric_id} onChange={(e) => set('numeric_id', Number(e.target.value))} />
          <span className="hint">Optional short number. 0 means none.</span>
        </label>
        <label>Turn the picture
          <select value={d.rotation} onChange={(e) => set('rotation', Number(e.target.value) as Rotation)}>
            {rotations.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
          </select>
          <span className="hint">For a camera mounted on its side or upside down. Live and Playback show it turned; the recording is kept as the camera sends it.</span>
        </label>
        <label className="check"><input type="checkbox" checked={d.record} onChange={(e) => set('record', e.target.checked)} /> Record <span className="hint">Untick to view only. The camera then connects when someone watches it.</span></label>
        <label className="check"><input type="checkbox" checked={d.enabled} onChange={(e) => set('enabled', e.target.checked)} /> Enabled <span className="hint">Untick to stop using this camera but keep its settings.</span></label>
        {d.id
          ? <LensSettings value={d.fisheye} onChange={(f) => set('fisheye', f)} picture={picture} />
          : <p className="hint wide">A fisheye lens can be set up once the camera is added and has sent a picture.</p>}
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="row">
        <button className="primary" disabled={busy}>{d.id ? 'Save changes' : 'Add camera'}</button>
        <button type="button" className="ghost" onClick={onClose}>Cancel</button>
      </div>
    </form>
  )
}

function Discovery({ login, onClose, onPick }: { login: DefaultLogin; onClose: () => void; onPick: (d: Draft) => void }) {
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [xaddr, setXaddr] = useState('')
  const [name, setName] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [useDefault, setUseDefault] = useState(!!login.username)
  const [error, setError] = useState('')

  const addedCount = devices?.filter((d) => d.cameras).length ?? 0
  const picked = devices?.find((d) => d.xaddr === xaddr)
  const scan = async () => {
    setBusy(true)
    setError('')
    try { setDevices(await api<Device[]>('/onvif/discover', 'POST')) }
    catch (e) { setError(errorText(e)) }
    setBusy(false)
  }
  const read = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const ps = await api<Profile[]>('/onvif/profiles', 'POST', useDefault ? { xaddr, use_default_login: true } : { xaddr, username, password })
      if (ps.length === 0) throw new Error('This camera reported no video profiles.')
      onPick({
        ...blank, name: name || new URL(xaddr).hostname, useDefault,
        username: useDefault ? '' : username, password: useDefault ? '' : password,
        main_url: ps[0].uri, sub_url: ps.length > 1 ? ps[ps.length - 1].uri : '',
      })
    } catch (err) {
      setError(errorText(err))
      setBusy(false)
    }
  }
  return (
    <div className="card form">
      <h3>Find ONVIF cameras</h3>
      <div className="row">
        <button className="ghost" disabled={busy} onClick={scan}>{busy ? 'Working…' : 'Scan local network'}</button>
        {devices && <span className="muted">{devices.length === 0 ? 'No cameras answered. Enter an address below instead.'
          : `${devices.length} found${addedCount ? `, ${addedCount} already added` : ''}`}</span>}
      </div>
      {devices && devices.length > 0 && (
        <ul className="picklist">
          {devices.map((d) => (
            <li key={d.xaddr}><button className={d.xaddr === xaddr ? 'active' : ''} onClick={() => { setXaddr(d.xaddr); setName(d.name) }}>{d.name || 'Unnamed camera'} <span className="muted">{d.host}</span>
              {d.cameras && <span className="tag">Added as {d.cameras.join(', ')}</span>}</button></li>
          ))}
        </ul>
      )}
      <form onSubmit={read}>
        <div className="fields">
          <label className="wide">Device address<input value={xaddr} onChange={(e) => setXaddr(e.target.value)} placeholder="http://192.168.1.20/onvif/device_service" required /><span className="hint">Filled in when you pick a camera above. Otherwise use the address shown, with your camera's IP address.</span></label>
          {picked?.cameras && <p className="warn wide" role="status">This camera is already added as {picked.cameras.join(', ')}. Add it again only for another of its streams or lenses.</p>}
          {login.username && (
            <label className="check wide"><input type="checkbox" checked={useDefault} onChange={(e) => setUseDefault(e.target.checked)} /> Use the default login <span className="hint">Signs in as {login.username}. Untick to enter a different one.</span></label>
          )}
          {!useDefault && <>
            <label>Camera username<input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" /></label>
            <label>Camera password<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" /></label>
          </>}
        </div>
        {error && <p className="error" role="alert">{error}</p>}
        <div className="row">
          <button className="primary" disabled={busy}>Read streams</button>
          <button type="button" className="ghost" onClick={onClose}>Cancel</button>
        </div>
      </form>
    </div>
  )
}
