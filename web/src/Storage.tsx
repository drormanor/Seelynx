import { Fragment, useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Guide } from './help'
import { api, errorText, type FolderListing, type StorageLocation } from './api'

const size = (n: number) => (n >= 1e12 ? `${(n / 1e12).toFixed(2)} TB` : `${(n / 1e9).toFixed(1)} GB`)

interface Draft { id?: number; name: string; path: string; min_free_gb: number; recording: boolean; username: string; password: string; passwordSet: boolean; standby: boolean }
const blank: Draft = { name: '', path: '', min_free_gb: 50, recording: true, username: '', password: '', passwordSet: false, standby: false }
// isShare matches a Windows network share path, such as \\nas\video.
const isShare = (path: string) => /^\\\\[^\\?.]/.test(path.trim())

// How far back a location's video goes, in whole days.
function history(oldest: string | null) {
  if (!oldest) return ''
  const days = Math.floor((Date.now() - new Date(oldest).getTime()) / 86400000)
  return days < 1 ? 'Less than a day' : days === 1 ? '1 day' : `${days} days`
}

export function Storage() {
  const [locations, setLocations] = useState<StorageLocation[] | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [confirming, setConfirming] = useState<number | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(() => api<StorageLocation[]>('/storage').then((l) => { setLocations(l); setError('') }).catch((e) => setError(errorText(e))), [])
  const moving = !!locations?.some((l) => l.moving_to || l.return)
  useEffect(() => {
    load()
    // Faster while video is being moved, to show its progress.
    const t = setInterval(load, moving ? 3000 : 10000)
    return () => clearInterval(t)
  }, [load, moving])

  const act = async (path: string, method: string, body?: unknown) => {
    setConfirming(null)
    setError('')
    try {
      await api(path, method, body)
      load()
    } catch (e) { setError(errorText(e)) }
  }
  const nameOf = (id: number) => locations?.find((l) => l.id === id)?.name ?? 'another location'

  if (!locations) return <div className="center"><p className={error ? 'error' : 'muted'}>{error || 'Loading…'}</p></div>
  return (
    <div className="page">
      <div className="toolbar">
        <h2>Storage <span className="muted">{locations.length}</span></h2>
        <span className="spacer" />
        <button className="primary" onClick={() => setDraft({ ...blank })}>Add location</button>
      </div>
      <Guide topic="storage" />
      {error && <p className="error" role="alert">{error}</p>}
      {draft && <LocationForm key={draft.id ?? 'new'} draft={draft} onClose={() => setDraft(null)} onSaved={() => { setDraft(null); load() }} />}
      <div className="table-wrap storage">
        <table>
          <thead><tr><th>Location</th><th>Status</th><th>Free space</th><th>Video</th><th>Cameras</th><th className="num">Keep free</th><th /></tr></thead>
          <tbody>
            {locations.map((l) => {
              const used = l.total ? 1 - l.free / l.total : 0
              const mv = l.move
              return (
                <Fragment key={l.id}>
                <tr>
                  <td>{l.name}<div className="hint path">{l.path}</div>{l.username && <div className="hint">Signs in as {l.username}</div>}</td>
                  <td>
                    <span className="nowrap"><span className={l.ok ? 'dot on' : 'dot'} /> {!l.ok ? 'Unavailable' : l.moving_to ? 'Moving video' : l.standby ? (l.cameras.length ? 'Recording on standby' : 'Standby') : l.recording ? 'Recording' : 'Playback only'}</span>
                    {!l.ok && <div className="hint">{l.reason}</div>}
                    {l.ok && l.standby && !l.cameras.length && !l.return && <div className="hint">Records only while the other locations are unavailable.</div>}
                    {l.ok && l.standby && l.cameras.length > 0 && <div className="hint warn">The other locations are unavailable. Video recorded here is moved back once they return.</div>}
                    {l.return && <div className="move-progress">
                      <div className="hint">Moving video back{l.return.to ? ` to ${nameOf(l.return.to)}` : ''}: {size(l.return.done)} of {size(Math.max(l.return.total, l.return.done))}</div>
                      <div className="meter" role="progressbar" aria-label={`Moving video back from ${l.name}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={l.return.total ? Math.round(l.return.done / l.return.total * 100) : 0}><div style={{ width: `${l.return.total ? Math.min(l.return.done / l.return.total, 1) * 100 : 0}%` }} /></div>
                      {l.return.waiting && <div className="hint warn">{l.return.waiting}</div>}
                    </div>}
                    {l.moving_to !== 0 && mv && <div className="move-progress">
                      <div className="hint">To {nameOf(l.moving_to)}: {size(mv.done)} of {size(Math.max(mv.total, mv.done))}</div>
                      <div className="meter" role="progressbar" aria-label={`Moving video out of ${l.name}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={mv.total ? Math.round(mv.done / mv.total * 100) : 0}><div style={{ width: `${mv.total ? Math.min(mv.done / mv.total, 1) * 100 : 0}%` }} /></div>
                      {mv.waiting && <div className="hint warn">{mv.waiting}</div>}
                      <div className="hint">Removed once it's empty.</div>
                    </div>}
                    {!l.moving_to && mv?.failed && <div className="hint warn">Moving its video stopped. {mv.failed}</div>}
                    {l.ok && !l.recording && !l.moving_to && !mv?.failed && <div className="hint">Keeps its video, but records nothing new.</div>}
                  </td>
                  <td>
                    {l.total > 0 ? <>
                      <span className="nowrap">{size(l.free)} <span className="hint">of {size(l.total)}</span></span>
                      <div className="meter" role="img" aria-label={`${Math.round(used * 100)}% of ${l.name} used`}><div style={{ width: `${used * 100}%` }} /></div>
                    </> : <span className="muted">Unknown</span>}
                  </td>
                  <td>{l.ok ? <><span className="nowrap">{size(l.recorded_bytes)}</span>{l.oldest && <div className="hint">Goes back {history(l.oldest)}</div>}</> : <span className="muted">Not available for playback</span>}</td>
                  <td title={l.cameras.join(', ')}>{l.cameras.length}</td>
                  <td className="num nowrap">{l.min_free_gb} GB</td>
                  <td className="actions">
                    {l.moving_to
                      ? <button className="ghost" onClick={() => act(`/storage/${l.id}/move/stop`, 'POST')}>Stop moving</button>
                      : <><button className="ghost" onClick={() => { setConfirming(null); setDraft({ id: l.id, name: l.name, path: l.path, min_free_gb: l.min_free_gb, recording: l.recording, username: l.username, password: '', passwordSet: l.has_password, standby: l.standby }) }}>Edit</button>
                        <button className="ghost" aria-expanded={confirming === l.id} onClick={() => setConfirming(confirming === l.id ? null : l.id)}>Remove</button></>}
                  </td>
                </tr>
                {confirming === l.id && (
                  <tr className="remove-row">
                    <td colSpan={7}>
                      <RemovePanel location={l} targets={locations.filter((o) => o.id !== l.id && o.ok && o.recording && !o.moving_to && !o.standby)}
                        onMove={(to) => act(`/storage/${l.id}/move`, 'POST', { to })}
                        onRemove={() => act(`/storage/${l.id}`, 'DELETE')}
                        onCancel={() => setConfirming(null)} />
                    </td>
                  </tr>
                )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      </div>
      <p className="hint">Video in the Video column is updated every 5 minutes. Status is checked every 30 seconds.</p>
    </div>
  )
}

// RemovePanel asks what to do with a location's video when it's removed.
function RemovePanel({ location: l, targets, onMove, onRemove, onCancel }: {
  location: StorageLocation; targets: StorageLocation[]
  onMove: (to: number) => void; onRemove: () => void; onCancel: () => void
}) {
  const roomiest = [...targets].sort((a, b) => (b.free - b.min_free_gb * 1e9) - (a.free - a.min_free_gb * 1e9))[0]
  const [choice, setChoice] = useState<'move' | 'leave'>(roomiest && l.recorded_bytes > 0 ? 'move' : 'leave')
  const [to, setTo] = useState(roomiest?.id ?? 0)
  const radio = `remove-${l.id}`
  return (
    <div className="remove-panel">
      <h3>Remove {l.name}</h3>
      <label className="check">
        <input type="radio" name={radio} checked={choice === 'move'} disabled={!roomiest} onChange={() => setChoice('move')} />
        Move its video to
        <select aria-label="Location to move the video to" value={to} disabled={!roomiest || choice !== 'move'} onChange={(e) => setTo(Number(e.target.value))}>
          {targets.map((t) => <option key={t.id} value={t.id}>{t.name} ({size(Math.max(t.free - t.min_free_gb * 2 ** 30, 0))} available)</option>)}
        </select>
        first
      </label>
      <p className="hint indent">{roomiest
        ? <>{l.recorded_bytes > 0 ? `About ${size(l.recorded_bytes)} of video. ` : ''}{l.name} stops recording now, and its cameras move to other locations. The video is moved in the background and can be played back throughout. {l.name} is removed once it's empty.</>
        : 'There\'s no other location that records and is available to move it to.'}</p>
      <label className="check">
        <input type="radio" name={radio} checked={choice === 'leave'} onChange={() => setChoice('leave')} />
        Remove it now, and leave its video on the disk
      </label>
      <p className="hint indent">Its cameras move to other locations. The video stays on the disk, but you can't play it back unless you add this folder again.</p>
      <div className="row">
        {choice === 'move'
          ? <button className="danger" disabled={!to} onClick={() => onMove(to)}>Move video and remove</button>
          : <button className="danger" onClick={onRemove}>Remove location</button>}
        <button className="ghost" onClick={onCancel}>Keep</button>
      </div>
    </div>
  )
}

function LocationForm({ draft, onClose, onSaved }: { draft: Draft; onClose: () => void; onSaved: () => void }) {
  const [d, setD] = useState(draft)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((p) => ({ ...p, [k]: v }))
  const moving = !!draft.id && d.path.trim() !== draft.path
  const share = isShare(d.path)

  // A share with a login has to be signed in to before its folders can be listed.
  const browse = async () => {
    setError('')
    if (share && d.username.trim()) {
      try {
        await api('/storage/connect', 'POST', {
          path: d.path, username: d.username, id: d.id ?? 0,
          password: d.password || (d.passwordSet && d.username === draft.username ? undefined : ''),
        })
      } catch (err) { setError(errorText(err)); return }
    }
    setBrowsing(true)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      // An untouched password box keeps the stored password.
      const login = share && d.username.trim()
        ? { username: d.username, password: d.password || (d.passwordSet && d.username === draft.username ? undefined : '') }
        : { username: '' }
      await api(d.id ? `/storage/${d.id}` : '/storage', d.id ? 'PUT' : 'POST', { name: d.name, path: d.path, min_free_gb: d.min_free_gb, recording: d.recording || d.standby, standby: d.standby, ...login })
      onSaved()
    } catch (err) {
      setError(errorText(err))
      setBusy(false)
    }
  }
  return (
    <>
    <form className="card form" onSubmit={submit}>
      <h3>{d.id ? `Edit ${draft.name}` : 'Add storage location'}</h3>
      <div className="fields">
        <label>Name<input value={d.name} onChange={(e) => set('name', e.target.value)} maxLength={60} required autoFocus /><span className="hint">Such as NAS or Second disk.</span></label>
        <label>Keep free (GB)
          <input type="number" min={1} max={1000000} value={d.min_free_gb} onChange={(e) => set('min_free_gb', Number(e.target.value))} required />
          <span className="hint">When the disk gets this full, the oldest video here is deleted.</span>
        </label>
        <label className="wide">Folder
          <span className="picker">
            <input value={d.path} onChange={(e) => set('path', e.target.value)} placeholder="D:\Video" required spellCheck={false} />
            <button type="button" className="ghost" onClick={browse}>Browse…</button>
          </span>
          <span className="hint">The full path on the Seelynx server, such as D:\Video, a network share such as \\nas\video, or /mnt/nas/video. With Docker, it's the path inside the container. Seelynx makes the folder if it isn't there.</span>
          {moving && <span className="hint warn">Seelynx doesn't move video. Move the files yourself, or the video in the old folder won't be in Playback.</span>}
        </label>
        {share && <>
          <label>Share username
            <input value={d.username} onChange={(e) => set('username', e.target.value)} autoComplete="off" spellCheck={false} />
            <span className="hint">The NAS account that can change this folder, such as seelynx or NAS\seelynx. Leave it empty if the Seelynx service already has access.</span>
          </label>
          <label>Share password
            <input type="password" value={d.password} onChange={(e) => set('password', e.target.value)} autoComplete="new-password" placeholder={d.passwordSet && d.username === draft.username ? 'Unchanged' : ''} disabled={!d.username.trim()} />
            <span className="hint">Stored encrypted, and never shown again. Seelynx signs in again by itself after a restart.</span>
          </label>
        </>}
        <label className="check wide"><input type="checkbox" checked={d.standby} onChange={(e) => set('standby', e.target.checked)} /> Standby: record here only while the other locations are unavailable
          <span className="hint">For a local disk that covers for network storage. When the other locations are back, the video recorded here is moved to them, and this location is ready for next time.</span>
        </label>
        {!d.standby && <label className="check wide"><input type="checkbox" checked={d.recording} onChange={(e) => set('recording', e.target.checked)} /> Record new video here
          <span className="hint">Untick to stop recording here but keep the video for playback, such as before retiring a disk.</span>
        </label>}
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="row">
        <button className="primary" disabled={busy}>{d.id ? 'Save changes' : 'Add location'}</button>
        <button type="button" className="ghost" onClick={onClose}>Cancel</button>
      </div>
    </form>
    {browsing && <FolderDialog start={d.path.trim()} onClose={() => setBrowsing(false)} onPick={(p) => { set('path', p); setBrowsing(false) }} />}
    </>
  )
}

// FolderDialog browses the Seelynx server's folders. A browser's own folder dialog shows
// the viewing computer, and doesn't give the path, so it can't be used for this.
function FolderDialog({ start, onClose, onPick }: { start: string; onClose: () => void; onPick: (path: string) => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const [listing, setListing] = useState<FolderListing | null>(null)
  const [typed, setTyped] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [naming, setNaming] = useState(false)
  const [newName, setNewName] = useState('')

  const open = useCallback(async (path: string, fallback = false) => {
    setBusy(true)
    setError('')
    try {
      const l = await api<FolderListing>(`/storage/folders?path=${encodeURIComponent(path)}`)
      setListing(l)
      setTyped(l.path)
    } catch (e) {
      if (fallback) return open('')
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    if (ref.current && !ref.current.open) ref.current.showModal()
    // Start in the folder already entered, or the nearest one that's there.
    open(start, true)
  }, [open, start])

  const makeFolder = async (e: FormEvent) => {
    e.preventDefault()
    if (!listing?.path) return
    setError('')
    try {
      const made = await api<{ path: string }>('/storage/folders', 'POST', { parent: listing.path, name: newName })
      setNaming(false)
      setNewName('')
      open(made.path)
    } catch (err) { setError(errorText(err)) }
  }

  return (
    <dialog ref={ref} className="card folder-dialog" aria-label="Choose a folder" onClose={onClose}>
      <h3>Choose a folder on the Seelynx server</h3>
      <form className="row" onSubmit={(e) => { e.preventDefault(); open(typed) }}>
        <button type="button" className="ghost" disabled={busy || !listing?.up} onClick={() => open(listing?.parent ?? '')} aria-label="Up one folder" title="Up one folder">↑ Up</button>
        <input className="grow" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label="Folder path" placeholder="Type a path, such as \\nas\video" spellCheck={false} />
        <button className="ghost" disabled={busy}>Go</button>
      </form>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="folder-list" role="list" aria-label="Folders">
        {listing?.folders.map((f) => (
          <button type="button" role="listitem" key={f.path} className="folder" onClick={() => open(f.path)} disabled={busy}>
            <span aria-hidden="true">{listing.path ? '📁' : '💽'}</span> {f.name}
          </button>
        ))}
        {listing && listing.folders.length === 0 && <p className="muted">{listing.path ? 'No folders in here.' : 'No drives found.'}</p>}
        {!listing && !error && <p className="muted">Loading…</p>}
      </div>
      {!listing?.path && listing && <p className="hint">To use a network share, type its path, such as \\nas\video, and choose Go.</p>}
      {naming && (
        <form className="row" onSubmit={makeFolder}>
          <input className="grow" value={newName} onChange={(e) => setNewName(e.target.value)} aria-label="New folder name" placeholder="New folder name" autoFocus />
          <button className="ghost">Make folder</button>
          <button type="button" className="ghost" onClick={() => { setNaming(false); setNewName('') }}>Cancel</button>
        </form>
      )}
      <div className="row">
        <button type="button" className="primary" disabled={busy || !listing?.path} onClick={() => listing && onPick(listing.path)}>Select this folder</button>
        <button type="button" className="ghost" disabled={busy || !listing?.path || naming} onClick={() => setNaming(true)}>New folder</button>
        <span className="spacer" />
        <button type="button" className="ghost" onClick={() => ref.current?.close()}>Cancel</button>
      </div>
      {listing?.path && <p className="hint path">{listing.path}</p>}
    </dialog>
  )
}
