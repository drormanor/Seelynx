import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Guide } from './help'
import { api, errorText, type Camera, type RoleItem, type User } from './api'

const permissionHelp: { id: string; label: string; help: string }[] = [
  { id: 'live', label: 'Live view', help: 'Watch live video' },
  { id: 'playback', label: 'Playback', help: 'Watch recordings and motion history' },
  { id: 'system', label: 'System status', help: 'See health, motion, events and alarms, and acknowledge alarms' },
  { id: 'cameras', label: 'Manage cameras', help: 'Add, change and remove cameras' },
  { id: 'users', label: 'Manage users', help: 'Add users and change roles. This can grant any access.' },
  { id: 'settings', label: 'Settings', help: 'Rules, connections, AI service, drivers, schedules, views, access list, backups and logs' },
]

export function Users({ currentId }: { currentId: number }) {
  const [tab, setTab] = useState<'users' | 'roles'>('users')
  return (
    <div className="page">
      <div className="toolbar">
        <h2>Users</h2>
        <div className="segmented" role="group" aria-label="Users section">
          <button className={tab === 'users' ? 'active' : ''} aria-pressed={tab === 'users'} onClick={() => setTab('users')}>Users</button>
          <button className={tab === 'roles' ? 'active' : ''} aria-pressed={tab === 'roles'} onClick={() => setTab('roles')}>Roles</button>
        </div>
      </div>
      <Guide key={tab} topic={tab} />
      {tab === 'users' ? <UserList currentId={currentId} /> : <RoleList />}
    </div>
  )
}

interface Draft {
  id?: number; username: string; password: string; role_id: number
  first_name: string; last_name: string; remarks: string
  complex_password: boolean; must_change: boolean; password_max_days: number; expires: string
}
const blank: Draft = {
  username: '', password: '', role_id: 2, first_name: '', last_name: '', remarks: '',
  complex_password: false, must_change: true, password_max_days: 0, expires: '',
}
const pad = (n: number) => String(n).padStart(2, '0')
const toDate = (unix: number) => {
  if (!unix) return ''
  const d = new Date(unix * 1000)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
// An account stays usable through the whole of its last day.
const toUnix = (date: string) => {
  if (!date) return 0
  const [y, m, d] = date.split('-').map(Number)
  return Math.floor(new Date(y, m - 1, d + 1).getTime() / 1000)
}
const fromUser = (u: User): Draft => ({
  id: u.id, username: u.username, password: '', role_id: u.role_id, first_name: u.first_name, last_name: u.last_name,
  remarks: u.remarks, complex_password: u.complex_password, must_change: u.must_change,
  password_max_days: u.password_max_days, expires: toDate(u.expires_at),
})

function UserList({ currentId }: { currentId: number }) {
  const [users, setUsers] = useState<User[]>([])
  const [roles, setRoles] = useState<RoleItem[]>([])
  const [draft, setDraft] = useState<Draft>({ ...blank })
  const [formKey, setFormKey] = useState(0)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState<number | null>(null)

  const load = useCallback(() => { api<User[]>('/users').then(setUsers).catch((e) => setError(errorText(e))) }, [])
  useEffect(() => {
    load()
    api<RoleItem[]>('/roles').then(setRoles).catch((e) => setError(errorText(e)))
  }, [load])

  const open = (d: Draft) => { setDraft(d); setFormKey((k) => k + 1); setError('') }
  const remove = async (id: number) => {
    setError('')
    try { await api(`/users/${id}`, 'DELETE'); setConfirming(null); load() }
    catch (e) { setError(errorText(e)) }
  }
  return (
    <>
      {error && <p className="error" role="alert">{error}</p>}
      <UserForm key={formKey} draft={draft} roles={roles} onCancel={() => open({ ...blank })} onSaved={() => { open({ ...blank }); load() }} />
      <div className="table-wrap">
        <table>
          <thead><tr><th>Username</th><th>Name</th><th>Role</th><th>Status</th><th /></tr></thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>{u.username}{u.id === currentId && <span className="muted"> (you)</span>}</td>
                <td>{[u.first_name, u.last_name].filter(Boolean).join(' ')}</td>
                <td>{u.role}</td>
                <td className="muted">{u.expires_at && u.expires_at * 1000 <= Date.now() ? 'Expired' : u.must_change ? 'Must change password' : 'Active'}</td>
                <td className="actions">
                  {confirming === u.id
                    ? <><button className="danger" onClick={() => remove(u.id)}>Remove user</button><button className="ghost" onClick={() => setConfirming(null)}>Keep</button></>
                    : <>
                        <button className="ghost" aria-label={`Edit ${u.username}`} onClick={() => open(fromUser(u))}>Edit</button>
                        <button className="ghost" aria-label={`Duplicate ${u.username}`} onClick={() => open({ ...fromUser(u), id: undefined, username: '', must_change: true })}>Duplicate</button>
                        {u.id !== currentId && <button className="ghost" aria-label={`Remove ${u.username}`} onClick={() => setConfirming(u.id)}>Remove</button>}
                      </>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}

function UserForm({ draft, roles, onCancel, onSaved }: { draft: Draft; roles: RoleItem[]; onCancel: () => void; onSaved: () => void }) {
  const [d, setD] = useState(draft)
  const [error, setError] = useState('')
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((p) => ({ ...p, [k]: v }))
  const editing = d.id !== undefined
  const role = roles.find((r) => r.id === d.role_id)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    const body = {
      username: d.username, role_id: d.role_id, first_name: d.first_name, last_name: d.last_name, remarks: d.remarks,
      complex_password: d.complex_password, must_change: d.must_change, password_max_days: d.password_max_days,
      expires_at: toUnix(d.expires), password: d.password || undefined,
    }
    try {
      await api(editing ? `/users/${d.id}` : '/users', editing ? 'PUT' : 'POST', body)
      onSaved()
    } catch (err) { setError(errorText(err)) }
  }
  return (
    <form className="card form" onSubmit={submit}>
      <h3>{editing ? `Edit ${d.username}` : 'Add user'}</h3>
      <div className="fields">
        <label>Username<input value={d.username} onChange={(e) => set('username', e.target.value)} autoComplete="off" required disabled={editing} /><span className="hint">{editing ? "Usernames can't be changed." : "Used to sign in. It can't be changed later."}</span></label>
        <label>{editing ? 'New password' : 'Password'}
          <input type="password" value={d.password} onChange={(e) => set('password', e.target.value)} autoComplete="new-password" minLength={10} required={!editing} placeholder={editing ? 'Unchanged' : ''} />
          <span className="hint">At least 10 characters.{editing ? ' Changing it signs this user out.' : ''}</span>
        </label>
        <label>Role
          <select value={d.role_id} onChange={(e) => set('role_id', Number(e.target.value))}>
            {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
          {role && <span className="hint">{role.all_cameras ? 'All cameras' : `${role.camera_ids.length} camera${role.camera_ids.length === 1 ? '' : 's'}`}. Set up roles on the Roles tab.</span>}
        </label>
        <label>First name<input value={d.first_name} onChange={(e) => set('first_name', e.target.value)} autoComplete="off" /></label>
        <label>Last name<input value={d.last_name} onChange={(e) => set('last_name', e.target.value)} autoComplete="off" /></label>
        <label>Remarks<input value={d.remarks} onChange={(e) => set('remarks', e.target.value)} autoComplete="off" /><span className="hint">Notes for administrators, such as a department.</span></label>
        <label>Change password every (days)
          <input type="number" min={0} max={3650} value={d.password_max_days} onChange={(e) => set('password_max_days', Number(e.target.value))} />
          <span className="hint">0 means never.</span>
        </label>
        <label>Account expires
          <input type="date" value={d.expires} onChange={(e) => set('expires', e.target.value)} />
          <span className="hint">Leave empty if it never expires.</span>
        </label>
        <label className="check"><input type="checkbox" checked={d.must_change} onChange={(e) => set('must_change', e.target.checked)} /> Must change password at next sign-in <span className="hint">So only this person knows it.</span></label>
        <label className="check"><input type="checkbox" checked={d.complex_password} onChange={(e) => set('complex_password', e.target.checked)} /> Require a complex password <span className="hint">Mixes letters, numbers and symbols.</span></label>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="row">
        <button className="primary">{editing ? 'Save changes' : 'Add user'}</button>
        <button type="button" className="ghost" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  )
}

interface RoleDraft { id?: number; name: string; permissions: string[]; all_cameras: boolean; camera_ids: number[] }
const blankRole: RoleDraft = { name: '', permissions: ['live'], all_cameras: true, camera_ids: [] }

function RoleList() {
  const [roles, setRoles] = useState<RoleItem[]>([])
  const [cameras, setCameras] = useState<Camera[]>([])
  const [d, setD] = useState<RoleDraft>({ ...blankRole })
  const [error, setError] = useState('')

  const load = useCallback(() => { api<RoleItem[]>('/roles').then(setRoles).catch((e) => setError(errorText(e))) }, [])
  useEffect(() => {
    load()
    api<Camera[]>('/cameras').then(setCameras).catch((e) => setError(errorText(e)))
  }, [load])

  const toggle = <T,>(list: T[], item: T) => (list.includes(item) ? list.filter((x) => x !== item) : [...list, item])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api(d.id ? `/roles/${d.id}` : '/roles', d.id ? 'PUT' : 'POST', d)
      setD({ ...blankRole })
      load()
    } catch (err) { setError(errorText(err)) }
  }
  const remove = async (id: number) => {
    setError('')
    try { await api(`/roles/${id}`, 'DELETE'); load() }
    catch (err) { setError(errorText(err)) }
  }
  const labels = (r: RoleItem) => r.permissions.map((p) => permissionHelp.find((x) => x.id === p)?.label ?? p).join(', ')
  return (
    <>
      {error && <p className="error" role="alert">{error}</p>}
      <form className="card form" onSubmit={submit}>
        <h3>{d.id ? `Edit ${d.name}` : 'Add role'}</h3>
        <div className="fields"><label className="wide">Role name<input value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} required /><span className="hint">Name it after the job, such as Reception.</span></label></div>
        <div className="checks" role="group" aria-label="Permissions">
          {permissionHelp.map((p) => (
            <label className="check" key={p.id}>
              <input type="checkbox" checked={d.permissions.includes(p.id)} onChange={() => setD({ ...d, permissions: toggle(d.permissions, p.id) })} />
              {p.label} <span className="hint">{p.help}</span>
            </label>
          ))}
        </div>
        <div className="checks" role="group" aria-label="Cameras">
          <label className="check"><input type="checkbox" checked={d.all_cameras} onChange={(e) => setD({ ...d, all_cameras: e.target.checked })} /> All cameras, including new ones</label>
          {!d.all_cameras && cameras.map((c) => (
            <label className="check" key={c.id}>
              <input type="checkbox" checked={d.camera_ids.includes(c.id)} onChange={() => setD({ ...d, camera_ids: toggle(d.camera_ids, c.id) })} /> {c.name}
            </label>
          ))}
        </div>
        <p className="hint">Changes apply straight away, including for people already signed in.</p>
        <div className="row">
          <button className="primary">{d.id ? 'Save changes' : 'Add role'}</button>
          {d.id && <button type="button" className="ghost" onClick={() => setD({ ...blankRole })}>Cancel</button>}
        </div>
      </form>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Role</th><th>Permissions</th><th>Cameras</th><th /></tr></thead>
          <tbody>
            {roles.map((r) => (
              <tr key={r.id}>
                <td>{r.name}{r.builtin && <span className="muted"> (built-in)</span>}</td>
                <td className="muted">{labels(r)}</td>
                <td className="muted nowrap">{r.all_cameras ? 'All' : r.camera_ids.length}</td>
                <td className="actions">{!r.builtin && <>
                  <button className="ghost" aria-label={`Edit ${r.name}`} onClick={() => setD({ id: r.id, name: r.name, permissions: r.permissions, all_cameras: r.all_cameras, camera_ids: r.camera_ids })}>Edit</button>
                  <button className="ghost" aria-label={`Remove ${r.name}`} onClick={() => remove(r.id)}>Remove</button>
                </>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}
