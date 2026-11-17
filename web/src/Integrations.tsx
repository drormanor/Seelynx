import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Guide } from './help'
import { api, errorText, type Camera, type ConnectionItem, type ConnectionType, type DriverData, type Item, type RuleData, type ScheduleData } from './api'

const triggers: { id: string; label: string; help: string }[] = [
  { id: 'motion', label: 'Motion', help: 'A camera reports motion.' },
  { id: 'analytics', label: 'Camera analytics', help: 'A camera reports an analytics or audio event, such as a line crossing.' },
  { id: 'offline', label: 'Camera offline', help: 'A recording camera stops sending video.' },
  { id: 'online', label: 'Camera back online', help: 'A camera reconnects.' },
  { id: 'disk', label: 'Storage problem', help: 'Recordings were deleted early to free space, or a storage location stopped working.' },
  { id: 'external', label: 'Outside system', help: 'Another system calls this rule\'s private web address.' },
]
const blankRule: RuleData = {
  enabled: true, trigger: 'motion', topic: '', camera_ids: [], schedule_id: 0, cooldown: 60,
  priority: 'medium', alarm: true, message: '{event} on {camera} at {time}', connections: [], snapshot: true, camera_id: 0,
}

export function Connections() {
  const [types, setTypes] = useState<ConnectionType[]>([])
  const [list, setList] = useState<ConnectionItem[]>([])
  const [draft, setDraft] = useState<ConnectionItem | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const load = useCallback(() => { api<ConnectionItem[]>('/connections').then(setList).catch((e) => setError(errorText(e))) }, [])
  useEffect(() => {
    load()
    api<ConnectionType[]>('/connection-types').then(setTypes).catch((e) => setError(errorText(e)))
  }, [load])

  const type = types.find((t) => t.id === draft?.type)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!draft) return
    setError(''); setNotice('')
    try {
      await api(draft.id ? `/connections/${draft.id}` : '/connections', draft.id ? 'PUT' : 'POST', draft)
      setDraft(null)
      load()
    } catch (err) { setError(errorText(err)) }
  }
  const test = async (c: ConnectionItem) => {
    setError(''); setNotice('')
    try { await api(`/connections/${c.id}/test`, 'POST'); setNotice(`Test message sent to ${c.name}.`) }
    catch (err) { setError(errorText(err)) }
  }
  const remove = async (id: number) => {
    setError(''); setNotice('')
    try { await api(`/connections/${id}`, 'DELETE'); load() }
    catch (err) { setError(errorText(err)) }
  }
  const label = (id: string) => types.find((t) => t.id === id)?.label ?? id
  return (
    <>
      <div className="section-head"><h2>Connections</h2><p className="muted">Links to the services you already use. Rules send messages through them. Passwords and tokens are stored encrypted and are never shown again.</p></div>
      <Guide topic="connections" />
      {error && <p className="error" role="alert">{error}</p>}
      {notice && <p className="notice" role="status">{notice}</p>}
      {!draft && <div className="row"><button className="primary" onClick={() => setDraft({ id: 0, name: '', type: 'webhook', config: {} })}>Add connection</button></div>}
      {draft && (
        <form className="card form" onSubmit={submit}>
          <h3>{draft.id ? `Edit ${draft.name}` : 'Add connection'}</h3>
          <div className="fields">
            <label>Service
              <select value={draft.type} disabled={!!draft.id} onChange={(e) => setDraft({ ...draft, type: e.target.value, config: {} })}>
                {types.map((t) => <option key={t.id} value={t.id}>{t.label}</option>)}
              </select>
            </label>
            <label>Connection name<input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} required /><span className="hint">Say where messages go, such as Security team email.</span></label>
            {type && <p className="hint wide">{type.help}</p>}
            {draft && <Guide key={draft.type} topic={`connection-${draft.type}`} className="wide" open />}
            {type?.fields.map((f) => (
              <label key={f.key} className="wide">{f.label}
                <input type={f.secret ? 'password' : 'text'} autoComplete="off" required={f.required} placeholder={f.placeholder}
                  value={draft.config[f.key] ?? ''} onChange={(e) => setDraft({ ...draft, config: { ...draft.config, [f.key]: e.target.value } })} />
              </label>
            ))}
          </div>
          <div className="row">
            <button className="primary">Save connection</button>
            <button type="button" className="ghost" onClick={() => setDraft(null)}>Cancel</button>
          </div>
        </form>
      )}
      <div className="table-wrap">
        <table>
          <tbody>
            {list.map((c) => (
              <tr key={c.id}>
                <td>{c.name}</td>
                <td className="muted">{label(c.type)}</td>
                <td className="actions">
                  <button className="ghost" aria-label={`Test ${c.name}`} onClick={() => test(c)}>Test</button>
                  <button className="ghost" aria-label={`Edit ${c.name}`} onClick={() => setDraft(c)}>Edit</button>
                  <button className="ghost" aria-label={`Remove ${c.name}`} onClick={() => remove(c.id)}>Remove</button>
                </td>
              </tr>
            ))}
            {list.length === 0 && <tr><td className="muted">No connections yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </>
  )
}

export function Rules() {
  const [rules, setRules] = useState<Item<RuleData>[]>([])
  const [cameras, setCameras] = useState<Camera[]>([])
  const [schedules, setSchedules] = useState<Item<ScheduleData>[]>([])
  const [connections, setConnections] = useState<ConnectionItem[]>([])
  const [drivers, setDrivers] = useState<Item<DriverData>[]>([])
  const [name, setName] = useState('')
  const [id, setId] = useState(0)
  const [d, setD] = useState<RuleData | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(() => { api<Item<RuleData>[]>('/config/rules').then(setRules).catch((e) => setError(errorText(e))) }, [])
  useEffect(() => {
    load()
    api<Camera[]>('/cameras').then(setCameras).catch((e) => setError(errorText(e)))
    api<Item<ScheduleData>[]>('/config/schedules').then(setSchedules).catch((e) => setError(errorText(e)))
    api<ConnectionItem[]>('/connections').then(setConnections).catch((e) => setError(errorText(e)))
    api<Item<DriverData>[]>('/config/drivers').then(setDrivers).catch((e) => setError(errorText(e)))
  }, [load])

  const toggle = (list: number[], v: number) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v])
  const open = (rule?: Item<RuleData>) => {
    setError('')
    setId(rule?.id ?? 0)
    setName(rule?.name ?? '')
    setD(rule ? { ...blankRule, ...rule.data, camera_ids: rule.data.camera_ids ?? [], connections: rule.data.connections ?? [], snapshot: rule.data.snapshot ?? false, camera_id: rule.data.camera_id ?? 0 } : { ...blankRule })
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api(id ? `/config/rules/${id}` : '/config/rules', id ? 'PUT' : 'POST', { name, data: d })
      setD(null)
      load()
    } catch (err) { setError(errorText(err)) }
  }
  const remove = async (ruleId: number) => {
    setError('')
    try { await api(`/config/rules/${ruleId}`, 'DELETE'); load() }
    catch (err) { setError(errorText(err)) }
  }
  const usesCameras = d && !['disk', 'external'].includes(d.trigger)
  const canSnapshot = d && (['motion', 'analytics', 'online'].includes(d.trigger) || (d.trigger === 'external' && d.camera_id > 0))
  return (
    <>
      <div className="section-head"><h2>Rules</h2><p className="muted">When something happens, raise an alarm and tell your other systems.</p></div>
      <Guide topic="rules" />
      {error && <p className="error" role="alert">{error}</p>}
      {!d && <div className="row"><button className="primary" onClick={() => open()}>Add rule</button></div>}
      {d && (
        <form className="card form" onSubmit={submit}>
          <h3>{id ? `Edit ${name}` : 'Add rule'}</h3>
          <div className="fields">
            <label>Rule name<input value={name} onChange={(e) => setName(e.target.value)} required /><span className="hint">Shown on each alarm.</span></label>
            <label>When
              <select value={d.trigger} onChange={(e) => setD({ ...d, trigger: e.target.value })}>
                {triggers.map((t) => <option key={t.id} value={t.id}>{t.label}</option>)}
              </select>
              <span className="hint">{triggers.find((t) => t.id === d.trigger)?.help}</span>
            </label>
            {d.trigger === 'analytics' && drivers.length > 0 && (
              <label className="wide">Which analytic
                <select value={d.topic} onChange={(e) => setD({ ...d, topic: e.target.value })}>
                  <option value="">Any analytic</option>
                  {drivers.flatMap((v) => v.data.analytics.map((a) => <option key={`${v.id}-${a.topic}`} value={a.topic}>{a.name} ({v.name})</option>))}
                </select>
                <span className="hint">From your drivers. The camera must have this analytic turned on.</span>
              </label>
            )}
            {d.trigger === 'analytics' && (
              <label className="wide">Only events containing (optional)
                <input value={d.topic} onChange={(e) => setD({ ...d, topic: e.target.value })} placeholder="LineDetector" />
                <span className="hint">Leave empty for every analytics event.</span>
              </label>
            )}
            {d.trigger === 'external' && (
              <label className="wide">Camera for this event (optional)
                <select value={d.camera_id} onChange={(e) => setD({ ...d, camera_id: Number(e.target.value) })}>
                  <option value={0}>None</option>
                  {cameras.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
                <span className="hint">Ties the alarm to this camera, and lets it include a snapshot.</span>
              </label>
            )}
            <label>Only during
              <select value={d.schedule_id} onChange={(e) => setD({ ...d, schedule_id: Number(e.target.value) })}>
                <option value={0}>Always</option>
                {schedules.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
              <span className="hint">Add schedules under Recording schedules.</span>
            </label>
            <label>Wait between alerts (seconds)
              <input type="number" min={0} max={86400} value={d.cooldown} onChange={(e) => setD({ ...d, cooldown: Number(e.target.value) })} />
              <span className="hint">Per camera. Stops one event flooding your phone.</span>
            </label>
            <label>Priority
              <select value={d.priority} onChange={(e) => setD({ ...d, priority: e.target.value })}>
                <option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option>
              </select>
              <span className="hint">Sets the alarm's color, and urgency in services that support it.</span>
            </label>
            <label className="wide">Message
              <input value={d.message} onChange={(e) => setD({ ...d, message: e.target.value })} />
              <span className="hint">You can use {'{camera}'}, {'{event}'}, {'{topic}'} and {'{time}'}.</span>
            </label>
          </div>
          {usesCameras && (
            <div className="checks" role="group" aria-label="Rule cameras">
              <p className="hint">Cameras (none ticked means all cameras)</p>
              {cameras.map((c) => <label className="check" key={c.id}><input type="checkbox" checked={d.camera_ids.includes(c.id)} onChange={() => setD({ ...d, camera_ids: toggle(d.camera_ids, c.id) })} /> {c.name}</label>)}
            </div>
          )}
          <div className="checks" role="group" aria-label="Rule actions">
            <p className="hint">Then</p>
            <label className="check"><input type="checkbox" checked={d.alarm} onChange={(e) => setD({ ...d, alarm: e.target.checked })} /> Raise an alarm on screen</label>
            {connections.map((c) => <label className="check" key={c.id}><input type="checkbox" checked={d.connections.includes(c.id)} onChange={() => setD({ ...d, connections: toggle(d.connections, c.id) })} /> Send to {c.name}</label>)}
            {connections.length === 0 && <p className="hint">Add a connection to send messages to other services.</p>}
            {canSnapshot && <label className="check"><input type="checkbox" checked={d.snapshot} onChange={(e) => setD({ ...d, snapshot: e.target.checked })} /> Include a snapshot <span className="hint">A picture from the camera, shown on the alarm and sent to the services above that accept pictures. Online services receive it outside your network.</span></label>}
          </div>
          <label className="check"><input type="checkbox" checked={d.enabled} onChange={(e) => setD({ ...d, enabled: e.target.checked })} /> Rule is on <span className="hint">Untick to pause it without deleting it.</span></label>
          <div className="row">
            <button className="primary">Save rule</button>
            <button type="button" className="ghost" onClick={() => setD(null)}>Cancel</button>
          </div>
        </form>
      )}
      <div className="table-wrap">
        <table>
          <tbody>
            {rules.map((r) => (
              <tr key={r.id}>
                <td>{r.name}{!r.data.enabled && <span className="muted"> (off)</span>}</td>
                <td className="muted">{triggers.find((t) => t.id === r.data.trigger)?.label}
                  {r.data.token && <><br /><code className="hook">{`${location.origin}/api/hooks/${r.data.token}`}</code></>}
                </td>
                <td className="actions">
                  <button className="ghost" aria-label={`Edit ${r.name}`} onClick={() => open(r)}>Edit</button>
                  <button className="ghost" aria-label={`Remove ${r.name}`} onClick={() => remove(r.id)}>Remove</button>
                </td>
              </tr>
            ))}
            {rules.length === 0 && <tr><td className="muted">No rules yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </>
  )
}
