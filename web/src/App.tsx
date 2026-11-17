import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, errorText, setSignedOutHandler, type Permission, type User } from './api'
import { Live } from './Live'
import { Playback } from './Playback'
import { Cameras } from './Cameras'
import { Users } from './Users'
import { System } from './System'
import { Storage } from './Storage'
import { Settings } from './Settings'
import { Alarms, useOpenAlarms } from './Alarms'
import { HelpPage } from './help'

type Page = 'live' | 'playback' | 'cameras' | 'users' | 'system' | 'storage' | 'settings' | 'alarms' | 'account' | 'help'
const pages: { id: Page; label: string; perm: Permission }[] = [
  { id: 'live', label: 'Live', perm: 'live' },
  { id: 'playback', label: 'Playback', perm: 'playback' },
  { id: 'alarms', label: 'Alarms', perm: 'system' },
  { id: 'cameras', label: 'Cameras', perm: 'cameras' },
  { id: 'users', label: 'Users', perm: 'users' },
  { id: 'system', label: 'System', perm: 'system' },
  { id: 'storage', label: 'Storage', perm: 'settings' },
  { id: 'settings', label: 'Settings', perm: 'settings' },
]

interface Session { needs_setup: boolean; user: User | null; must_change_password: boolean; build?: { version: string; built?: string } }

export function App() {
  const [session, setSession] = useState<Session | null>(null)
  const [loadError, setLoadError] = useState('')
  const [page, setPage] = useState<Page>('live')
  const [playbackCamera, setPlaybackCamera] = useState<number | null>(null)
  const canSeeAlarms = !!session?.user?.permissions.includes('system') && !session.must_change_password
  const alarms = useOpenAlarms(canSeeAlarms)

  const refresh = useCallback(() => {
    api<Session>('/session').then(setSession).catch((e) => setLoadError(errorText(e)))
  }, [])
  useEffect(() => {
    setSignedOutHandler(refresh)
    refresh()
  }, [refresh])

  if (loadError) return <div className="center"><p role="alert">Can't reach the server. {loadError}</p></div>
  if (!session) return <div className="center"><p className="muted">Loading…</p></div>
  if (!session.user) return <SignIn setup={session.needs_setup} onDone={refresh} />

  const user = session.user
  const logout = () => api('/logout', 'POST').finally(refresh)
  if (session.must_change_password) {
    return (
      <div className="center">
        <ChangePassword forced complex={user.complex_password} onDone={refresh} onCancel={logout} />
      </div>
    )
  }
  const allowed = pages.filter((p) => user.permissions.includes(p.perm))
  const current = page === 'account' || page === 'help' || allowed.some((p) => p.id === page) ? page : allowed[0]?.id ?? 'account'
  return (
    <div className="shell">
      <header className="topbar">
        <img className="brand" src="/logo-nav.png" alt="Seelynx" />
        <nav aria-label="Main">
          {allowed.map((p) => (
            <button key={p.id} className={p.id === current ? 'tab active' : 'tab'} aria-current={p.id === current ? 'page' : undefined} onClick={() => setPage(p.id)}>
              {p.label}{p.id === 'alarms' && alarms.count > 0 && <span className="count">{alarms.count}</span>}
            </button>
          ))}
        </nav>
        <span className="spacer" />
        {session.build && (
          <span className="version" title={session.build.built ? `Built from the code of ${new Date(session.build.built).toLocaleString()}` : undefined}>
            Version {session.build.version}
          </span>
        )}
        <button className={current === 'help' ? 'tab active' : 'tab'} aria-current={current === 'help' ? 'page' : undefined} onClick={() => setPage('help')}>Help</button>
        <button className="tab" aria-label="Change password" title="Change password" onClick={() => setPage('account')}>{user.username}</button>
        <button className="ghost" onClick={logout}>Sign out</button>
      </header>
      <main className="content">
        {current === 'live' && <Live canAddCameras={user.permissions.includes('cameras')} canSaveViews={user.permissions.includes('settings')} goToCameras={() => setPage('cameras')}
          openPlayback={user.permissions.includes('playback') ? (id) => { setPlaybackCamera(id); setPage('playback') } : undefined} />}
        {current === 'playback' && <Playback initialCamera={playbackCamera} />}
        {current === 'alarms' && <Alarms onChange={alarms.refresh} />}
        {current === 'cameras' && <Cameras />}
        {current === 'users' && <Users currentId={user.id} />}
        {current === 'system' && <System />}
        {current === 'storage' && <Storage />}
        {current === 'settings' && <Settings />}
        {current === 'help' && <HelpPage permissions={user.permissions} />}
        {current === 'account' && <div className="center"><ChangePassword complex={user.complex_password} onDone={() => setPage(allowed[0]?.id ?? 'account')} onCancel={() => setPage(allowed[0]?.id ?? 'account')} /></div>}
      </main>
    </div>
  )
}

function SignIn({ setup, onDone }: { setup: boolean; onDone: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api(setup ? '/setup' : '/login', 'POST', { username, password })
      onDone()
    } catch (err) {
      setError(errorText(err))
      setBusy(false)
    }
  }
  return (
    <div className="center">
      <form className="card signin" onSubmit={submit}>
        <h1><img className="logo" src="/logo.png" alt="Seelynx, VMS and NVR software" /></h1>
        <p className="muted">{setup ? 'Create the administrator account to get started.' : 'Sign in to continue.'}</p>
        <label>Username<input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus required /></label>
        <label>Password
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete={setup ? 'new-password' : 'current-password'} required minLength={setup ? 10 : undefined} />
        </label>
        <p className="hint">{setup ? 'At least 10 characters.' : 'Forgotten your password? Ask an administrator to set a new one.'}</p>
        {error && <p className="error" role="alert">{error}</p>}
        <button className="primary" disabled={busy}>{setup ? 'Create account' : 'Sign in'}</button>
      </form>
    </div>
  )
}

function ChangePassword({ forced, complex, onDone, onCancel }: { forced?: boolean; complex: boolean; onDone: () => void; onCancel: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    if (next !== repeat) { setError("The new passwords don't match."); return }
    setBusy(true)
    try {
      await api('/password', 'POST', { current, new: next })
      onDone()
    } catch (err) {
      setError(errorText(err))
      setBusy(false)
    }
  }
  return (
    <form className="card signin" onSubmit={submit}>
      <h2>Change password</h2>
      {forced && <p className="muted">You need to choose a new password before continuing.</p>}
      <label>Current password<input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" autoFocus required /></label>
      <label>New password<input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" minLength={10} required /></label>
      <label>Repeat new password<input type="password" value={repeat} onChange={(e) => setRepeat(e.target.value)} autoComplete="new-password" required /></label>
      <p className="hint">At least 10 characters.{complex ? ' Use three of: lowercase letters, capitals, numbers and symbols, and leave out your username.' : ''}</p>
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy}>Change password</button>
      <button type="button" className="ghost" onClick={onCancel}>{forced ? 'Sign out' : 'Cancel'}</button>
    </form>
  )
}
