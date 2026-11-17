import { useCallback, useEffect, useState } from 'react'
import { Guide } from './help'
import { api, errorText, type Alarm } from './api'

export function Alarms({ onChange }: { onChange: () => void }) {
  const [alarms, setAlarms] = useState<Alarm[] | null>(null)
  const [error, setError] = useState('')
  const [notes, setNotes] = useState<Record<number, string>>({})

  const load = useCallback(() => {
    api<Alarm[]>('/alarms?limit=200').then((a) => { setAlarms(a); setError('') }).catch((e) => setError(errorText(e)))
  }, [])
  useEffect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  const ack = async (id: number) => {
    setError('')
    try {
      await api(`/alarms/${id}/ack`, 'POST', { note: notes[id] ?? '' })
      load()
      onChange()
    } catch (e) { setError(errorText(e)) }
  }

  const open = (alarms ?? []).filter((a) => !a.acked_at)
  const done = (alarms ?? []).filter((a) => a.acked_at)
  return (
    <div className="page">
      <div className="toolbar"><h2>Alarms <span className="muted">{open.length} open</span></h2></div>
      <Guide topic="alarms" />
      {error && <p className="error" role="alert">{error}</p>}
      {alarms && alarms.length === 0 && <p className="muted">No alarms yet. Create a rule in Settings to raise one.</p>}
      {open.map((a) => (
        <div className={`card alarm ${a.priority}`} key={a.id}>
          <div className="row">
            <span className={`pill ${a.priority}`}>{a.priority}</span>
            <strong className="truncate">{a.rule}</strong>
            {a.camera_name && <span className="muted">{a.camera_name}</span>}
            <span className="spacer" />
            <span className="muted small nowrap">{new Date(a.at).toLocaleString()}</span>
          </div>
          <p>{a.message}</p>
          {a.has_snapshot && <a href={`/api/alarms/${a.id}/snapshot`} target="_blank" rel="noreferrer"><img className="snapshot" src={`/api/alarms/${a.id}/snapshot`} alt={`Snapshot from ${a.camera_name}`} loading="lazy" /></a>}
          <div className="row">
            <input className="grow" aria-label={`Note for alarm ${a.id}`} placeholder="Add a note (optional)" value={notes[a.id] ?? ''} onChange={(e) => setNotes({ ...notes, [a.id]: e.target.value })} />
            <button className="primary" onClick={() => ack(a.id)}>Acknowledge</button>
          </div>
        </div>
      ))}
      {done.length > 0 && <>
        <h3>Acknowledged</h3>
        <div className="table-wrap">
          <table>
            <tbody>
              {done.map((a) => (
                <tr key={a.id}>
                  <td className="nowrap muted">{new Date(a.at).toLocaleString()}</td>
                  <td>{a.rule}{a.camera_name ? ` on ${a.camera_name}` : ''}</td>
                  <td className="muted">{a.message}</td>
                  <td className="muted">{a.acked_by}{a.note ? `: ${a.note}` : ''}</td>
                  <td className="nowrap">{a.has_snapshot && <a href={`/api/alarms/${a.id}/snapshot`} target="_blank" rel="noreferrer">Picture</a>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </>}
    </div>
  )
}

/** Keeps the menu's open-alarm count current and sounds a tone when it goes up. */
export function useOpenAlarms(enabled: boolean) {
  const [count, setCount] = useState(0)
  const refresh = useCallback(() => {
    if (!enabled) return
    api<Alarm[]>('/alarms?limit=200').then((a) => {
      const open = a.filter((x) => !x.acked_at).length
      setCount((previous) => {
        if (open > previous) beep()
        return open
      })
    }).catch(() => { /* the Alarms page shows errors */ })
  }, [enabled])
  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 5000)
    return () => clearInterval(t)
  }, [refresh])
  return { count, refresh }
}

// Browsers block sound until someone has used the page, so this can stay silent.
function beep() {
  try {
    const ctx = new AudioContext()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    gain.gain.value = 0.08
    osc.frequency.value = 880
    osc.connect(gain).connect(ctx.destination)
    osc.start()
    osc.stop(ctx.currentTime + 0.25)
    osc.onended = () => { ctx.close().catch(() => {}) }
  } catch { /* no sound available */ }
}
