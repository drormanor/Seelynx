import { useEffect, useState } from 'react'
import { Guide } from './help'
import { api, errorText, type MotionPeriod, type SystemInfo } from './api'

const size = (n: number) => (n >= 1e12 ? `${(n / 1e12).toFixed(2)} TB` : `${(n / 1e9).toFixed(1)} GB`)

export function System() {
  const [info, setInfo] = useState<SystemInfo | null>(null)
  const [motion, setMotion] = useState<MotionPeriod[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    const load = () => api<SystemInfo>('/system')
      .then((i) => { if (alive) { setInfo(i); setError('') } })
      .catch((e) => { if (alive) setError(errorText(e)) })
    const loadMotion = () => api<MotionPeriod[]>('/motion?limit=20').then((m) => { if (alive) setMotion(m) }).catch((e) => { if (alive) setError(errorText(e)) })
    load()
    loadMotion()
    const t2 = setInterval(loadMotion, 5000)
    const t = setInterval(load, 5000)
    return () => { alive = false; clearInterval(t); clearInterval(t2) }
  }, [])

  if (!info) return <div className="center"><p className={error ? 'error' : 'muted'}>{error || 'Loading…'}</p></div>
  const used = 1 - info.disk_free / info.disk_total
  return (
    <div className="page">
      <div className="toolbar"><h2>System</h2></div>
      <Guide topic="system" />
      {error && <p className="error" role="alert">{error}</p>}
      <div className="stats">
        <div className="card stat"><span className="muted">Cameras online</span><strong>{info.online} / {info.cameras}</strong></div>
        <div className="card stat"><span className="muted">Incoming video</span><strong>{info.ingest_mbps.toFixed(1)} Mbit/s</strong></div>
        <div className="card stat"><span className="muted">Recordings</span><strong>{size(info.recorded_bytes)}</strong><span className="hint">Updated every 5 minutes</span></div>
        <div className="card stat"><span className="muted">Disk free</span><strong>{size(info.disk_free)}</strong>
          <div className="meter" role="img" aria-label={`${Math.round(used * 100)}% of disk used`}><div style={{ width: `${used * 100}%` }} /></div>
          <span className="hint">of {size(info.disk_total)}</span>
        </div>
      </div>
      <h3>Recent motion</h3>
      <div className="table-wrap">
        <table>
          <tbody>
            {motion.map((m) => (
              <tr key={m.id}>
                <td className="nowrap muted">{new Date(m.start).toLocaleString()}</td>
                <td>{m.camera_name || 'Removed camera'}</td>
                <td className="num">{m.end ? `${Math.max(1, Math.round((new Date(m.end).getTime() - new Date(m.start).getTime()) / 1000))}s` : 'Now'}</td>
              </tr>
            ))}
            {motion.length === 0 && <tr><td className="muted">No motion recorded yet. Motion needs a camera with ONVIF and a login.</td></tr>}
          </tbody>
        </table>
      </div>
      <h3>Recent events</h3>
      <div className="table-wrap">
        <table>
          <tbody>
            {info.events.map((e) => (
              <tr key={e.id}><td className="nowrap muted">{new Date(e.at).toLocaleString()}</td><td className={e.level === 'warning' ? 'warn' : ''}>{e.message}</td></tr>
            ))}
            {info.events.length === 0 && <tr><td className="muted">No events yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}
