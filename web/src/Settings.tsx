import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Connections, Rules } from './Integrations'
import { AIServiceSettings, Drivers } from './Drivers'
import { Guide } from './help'
import { api, errorText, type BackupInfo, type GroupData, type Item, type Period, type ScheduleData, type TourData, type ViewData } from './api'

type Section = 'rules' | 'connections' | 'ai' | 'drivers' | 'groups' | 'schedules' | 'tours' | 'access' | 'backup' | 'logs'
const sections: { id: Section; label: string }[] = [
  { id: 'rules', label: 'Rules' },
  { id: 'connections', label: 'Connections' },
  { id: 'ai', label: 'AI service' },
  { id: 'drivers', label: 'Drivers' },
  { id: 'groups', label: 'Camera groups' },
  { id: 'schedules', label: 'Recording schedules' },
  { id: 'tours', label: 'Views and tours' },
  { id: 'access', label: 'Access list' },
  { id: 'backup', label: 'Backup and restore' },
  { id: 'logs', label: 'Logs' },
]
const dayNames = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

export function Settings() {
  const [section, setSection] = useState<Section>('rules')
  return (
    <div className="settings">
      <nav className="settings-nav" aria-label="Settings">
        {sections.map((s) => (
          <button key={s.id} className={s.id === section ? 'active' : ''} aria-current={s.id === section ? 'page' : undefined} onClick={() => setSection(s.id)}>{s.label}</button>
        ))}
      </nav>
      <div className="page">
        {section === 'rules' && <Rules />}
        {section === 'connections' && <Connections />}
        {section === 'ai' && <AIServiceSettings />}
        {section === 'drivers' && <Drivers />}
        {section === 'groups' && <Groups />}
        {section === 'schedules' && <Schedules />}
        {section === 'tours' && <Tours />}
        {section === 'access' && <AccessList />}
        {section === 'backup' && <Backup />}
        {section === 'logs' && <Logs />}
      </div>
    </div>
  )
}

function useItems<T>(kind: string) {
  const [items, setItems] = useState<Item<T>[]>([])
  const [error, setError] = useState('')
  const load = useCallback(() => {
    api<Item<T>[]>(`/config/${kind}`).then(setItems).catch((e) => setError(errorText(e)))
  }, [kind])
  useEffect(load, [load])
  const run = async (fn: () => Promise<unknown>) => {
    setError('')
    try {
      await fn()
      load()
      return true
    } catch (e) {
      setError(errorText(e))
      return false
    }
  }
  const add = (name: string, data: T) => run(() => api(`/config/${kind}`, 'POST', { name, data }))
  const remove = (id: number) => run(() => api(`/config/${kind}/${id}`, 'DELETE'))
  return { items, error, add, remove }
}

function List({ rows, empty }: { rows: { id: number; name: string; detail?: string; onRemove: () => void }[]; empty: string }) {
  return (
    <div className="table-wrap">
      <table>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id}>
              <td>{r.name}</td>
              <td className="muted">{r.detail}</td>
              <td className="actions"><button className="ghost" aria-label={`Remove ${r.name}`} onClick={r.onRemove}>Remove</button></td>
            </tr>
          ))}
          {rows.length === 0 && <tr><td className="muted">{empty}</td></tr>}
        </tbody>
      </table>
    </div>
  )
}

function Heading({ title, topic, children }: { title: string; topic: string; children: ReactNode }) {
  return <><div className="section-head"><h2>{title}</h2><p className="muted">{children}</p></div><Guide topic={topic} /></>
}

function Groups() {
  const { items, error, add, remove } = useItems<GroupData>('groups')
  const [name, setName] = useState('')
  const [parent, setParent] = useState(0)
  const nameOf = (id: number) => items.find((g) => g.id === id)?.name
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await add(name, { parent })) { setName(''); setParent(0) }
  }
  return (
    <>
      <Heading title="Camera groups" topic="groups">Organise cameras into folders, such as buildings or floors. Assign a camera to a group on the Cameras page.</Heading>
      {error && <p className="error" role="alert">{error}</p>}
      <form className="card form" onSubmit={submit}>
        <div className="fields">
          <label>Group name<input value={name} onChange={(e) => setName(e.target.value)} required /><span className="hint">Such as a building or floor.</span></label>
          <label>Inside
            <select value={parent} onChange={(e) => setParent(Number(e.target.value))}>
              <option value={0}>Top level</option>
              {items.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
            </select>
            <span className="hint">Choose a group to nest this one inside it.</span>
          </label>
        </div>
        <div className="row"><button className="primary">Add group</button></div>
      </form>
      <List empty="No groups yet." rows={items.map((g) => ({
        id: g.id, name: g.name, detail: g.data.parent ? `Inside ${nameOf(g.data.parent) ?? 'another group'}` : '', onRemove: () => remove(g.id),
      }))} />
    </>
  )
}

function Schedules() {
  const { items, error, add, remove } = useItems<ScheduleData>('schedules')
  const [name, setName] = useState('')
  const [days, setDays] = useState<number[]>([1, 2, 3, 4, 5])
  const [start, setStart] = useState('08:00')
  const [end, setEnd] = useState('18:00')
  const [periods, setPeriods] = useState<Period[]>([])
  const [localError, setLocalError] = useState('')

  const toggle = (d: number) => setDays((p) => (p.includes(d) ? p.filter((x) => x !== d) : [...p, d].sort()))
  const addPeriod = () => {
    if (days.length === 0) { setLocalError('Choose at least one day.'); return }
    setLocalError('')
    setPeriods((p) => [...p, { days, start, end }])
  }
  const describe = (p: Period) => `${p.days.map((d) => dayNames[d]).join(', ')} ${p.start}–${p.end}`
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const all = periods.length > 0 ? periods : [{ days, start, end }]
    if (await add(name, { periods: all })) { setName(''); setPeriods([]) }
  }
  return (
    <>
      <Heading title="Recording schedules" topic="schedules">Set the hours when cameras record one way. On each camera, choose how it records during them and the rest of the time. Times use the server's clock, and an end time earlier than the start runs overnight.</Heading>
      {(error || localError) && <p className="error" role="alert">{error || localError}</p>}
      <form className="card form" onSubmit={submit}>
        <div className="fields">
          <label className="wide">Schedule name<input value={name} onChange={(e) => setName(e.target.value)} required /></label>
          <div className="wide">
            <div className="daypicker" role="group" aria-label="Days">
              {dayNames.map((n, d) => <button type="button" key={n} className={days.includes(d) ? 'active' : ''} aria-pressed={days.includes(d)} onClick={() => toggle(d)}>{n}</button>)}
            </div>
          </div>
          <label>From<input type="time" value={start} onChange={(e) => setStart(e.target.value)} required /><span className="hint">Server time.</span></label>
          <label>To<input type="time" value={end} onChange={(e) => setEnd(e.target.value)} required /><span className="hint">Earlier than From runs overnight.</span></label>
        </div>
        {periods.length > 0 && <ul className="plain">{periods.map((p, i) => <li key={i}>{describe(p)}</li>)}</ul>}
        <div className="row">
          <button className="primary">Save schedule</button>
          <button type="button" className="ghost" onClick={addPeriod}>Add another period</button>
        </div>
      </form>
      <List empty="No schedules yet." rows={items.map((s) => ({ id: s.id, name: s.name, detail: s.data.periods.map(describe).join('; '), onRemove: () => remove(s.id) }))} />
    </>
  )
}

function Tours() {
  const views = useItems<ViewData>('views')
  const tours = useItems<TourData>('tours')
  const [name, setName] = useState('')
  const [dwell, setDwell] = useState(30)
  const [chosen, setChosen] = useState<number[]>([])
  const toggle = (id: number) => setChosen((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await tours.add(name, { dwell, views: chosen })) { setName(''); setChosen([]) }
  }
  const error = views.error || tours.error
  return (
    <>
      <Heading title="Views and tours" topic="tours">Save a view from the Live page. A tour rotates through saved views on a timer.</Heading>
      {error && <p className="error" role="alert">{error}</p>}
      <h3>Saved views</h3>
      <List empty="No saved views yet. Arrange a grid on the Live page and choose Save view." rows={views.items.map((v) => ({
        id: v.id, name: v.name, detail: `${v.data.layout} tiles`, onRemove: () => views.remove(v.id),
      }))} />
      <h3>Tours</h3>
      <form className="card form" onSubmit={submit}>
        <div className="fields">
          <label>Tour name<input value={name} onChange={(e) => setName(e.target.value)} required /></label>
          <label>Seconds per view<input type="number" min={5} max={3600} value={dwell} onChange={(e) => setDwell(Number(e.target.value))} required /><span className="hint">From 5 to 3600.</span></label>
        </div>
        <div className="checks" role="group" aria-label="Views in this tour">
          {views.items.map((v) => <label className="check" key={v.id}><input type="checkbox" checked={chosen.includes(v.id)} onChange={() => toggle(v.id)} /> {v.name}</label>)}
          {views.items.length === 0 && <p className="muted">Save a view first.</p>}
        </div>
        <div className="row"><button className="primary" disabled={chosen.length === 0}>Add tour</button></div>
      </form>
      <List empty="No tours yet." rows={tours.items.map((t) => ({
        id: t.id, name: t.name, detail: `${t.data.views.length} views, ${t.data.dwell}s each`, onRemove: () => tours.remove(t.id),
      }))} />
    </>
  )
}

function AccessList() {
  const { items, error, add, remove } = useItems<Record<string, never>>('access')
  const [address, setAddress] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await add(address, {})) setAddress('')
  }
  return (
    <>
      <Heading title="Access list" topic="access">When this list is empty, any computer can reach the sign-in page. Add an entry and only those addresses can connect. The server's own machine is always allowed.</Heading>
      {error && <p className="error" role="alert">{error}</p>}
      <form className="card form" onSubmit={submit}>
        <div className="fields"><label className="wide">IP address or network<input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="192.168.1.0/24" required /><span className="hint">One address, such as 192.168.1.25, or a network, such as 192.168.1.0/24.</span></label></div>
        <div className="row"><button className="primary">Add address</button></div>
      </form>
      <List empty="No entries. All computers are allowed." rows={items.map((a) => ({ id: a.id, name: a.name, onRemove: () => remove(a.id) }))} />
    </>
  )
}

function Backup() {
  const [list, setList] = useState<BackupInfo[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirming, setConfirming] = useState('')
  const [busy, setBusy] = useState(false)
  const load = useCallback(() => { api<BackupInfo[]>('/backups').then(setList).catch((e) => setError(errorText(e))) }, [])
  useEffect(load, [load])

  const backupNow = async () => {
    setBusy(true); setError(''); setNotice('')
    try { await api('/backups', 'POST'); load() } catch (e) { setError(errorText(e)) }
    setBusy(false)
  }
  const restore = async (name: string) => {
    setError('')
    try {
      await api(`/restore?name=${encodeURIComponent(name)}`, 'POST')
      setConfirming('')
      setNotice('Restore is ready. Restart the Seelynx server to finish. The current configuration will be kept as a copy.')
    } catch (e) { setError(errorText(e)) }
  }
  return (
    <>
      <Heading title="Backup and restore" topic="backup">Backups hold cameras, users and settings, but not video. A backup runs automatically every day, and the latest 14 are kept. Camera passwords in backups are encrypted with data\secret.key, so keep a copy of that file somewhere safe. Without it, restored cameras need their passwords entered again.</Heading>
      {error && <p className="error" role="alert">{error}</p>}
      {notice && <p className="notice" role="status">{notice}</p>}
      <div className="row"><button className="primary" disabled={busy} onClick={backupNow}>Back up now</button></div>
      <div className="table-wrap">
        <table>
          <tbody>
            {list.map((b) => (
              <tr key={b.name}>
                <td className="nowrap">{new Date(b.at).toLocaleString()}</td>
                <td className="muted">{(b.size / 1024).toFixed(0)} KB</td>
                <td className="actions">
                  {confirming === b.name
                    ? <><button className="danger" onClick={() => restore(b.name)}>Restore this backup</button><button className="ghost" onClick={() => setConfirming('')}>Cancel</button></>
                    : <><a className="ghost button" href={`/api/backups/${b.name}`}>Download</a><button className="ghost" onClick={() => setConfirming(b.name)}>Restore</button></>}
                </td>
              </tr>
            ))}
            {list.length === 0 && <tr><td className="muted">No backups yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </>
  )
}

function Logs() {
  return (
    <>
      <Heading title="Logs" topic="logs">Download the server and video engine logs as a zip file for troubleshooting. Camera passwords in stream addresses are removed.</Heading>
      <div className="row"><a className="primary button" href="/api/logs">Download logs</a></div>
    </>
  )
}
