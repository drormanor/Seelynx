import { Guide } from './help'
import { useCallback, useEffect, useMemo, useState, type DragEvent } from 'react'
import { api, errorText, readPref, writePref, type Camera, type GroupData, type Item, type TourData, type ViewData } from './api'
import { VideoTile, type Shape } from './VideoTile'
import { LensPicker, useLensViews } from './Fisheye'

const layouts = [1, 4, 9, 16]
type Slots = (number | null)[]
const shapes: { id: Shape; label: string }[] = [
  { id: 'original', label: 'Original' },
  { id: 'fill', label: 'Fill' },
  { id: 'stretch', label: 'Stretch' },
  { id: '16x9', label: '16:9' },
  { id: '4x3', label: '4:3' },
]

export function Live({ canAddCameras, canSaveViews, goToCameras, openPlayback }: { canAddCameras: boolean; canSaveViews: boolean; goToCameras: () => void; openPlayback?: (id: number) => void }) {
  const [cameras, setCameras] = useState<Camera[] | null>(null)
  const [count, setCount] = useState<number>(() => readPref('live.count', 4))
  const [slots, setSlots] = useState<Slots>(() => readPref('live.slots', []))
  const [focused, setFocused] = useState<number | null>(null)
  const [filter, setFilter] = useState('')
  const [error, setError] = useState('')
  const [groups, setGroups] = useState<Item<GroupData>[]>([])
  const [views, setViews] = useState<Item<ViewData>[]>([])
  const [tours, setTours] = useState<Item<TourData>[]>([])
  const [touring, setTouring] = useState<number | null>(null)
  const [naming, setNaming] = useState(false)
  const [viewName, setViewName] = useState('')
  const [cameraShapes, setCameraShapes] = useState<Record<number, Shape>>(() => readPref('live.shapes', {}))
  const lenses = useLensViews('live.lens')

  const loadViews = useCallback(() => {
    api<Item<ViewData>[]>('/config/views').then(setViews).catch((e) => setError(errorText(e)))
  }, [])
  useEffect(() => {
    loadViews()
    api<Item<GroupData>[]>('/config/groups').then(setGroups).catch((e) => setError(errorText(e)))
    api<Item<TourData>[]>('/config/tours').then(setTours).catch((e) => setError(errorText(e)))
  }, [loadViews])

  const showView = useCallback((v: Item<ViewData>) => {
    setFocused(null)
    setCount(v.data.layout)
    setSlots(v.data.slots)
  }, [])

  useEffect(() => {
    const tour = tours.find((t) => t.id === touring)
    if (!tour) return
    const list = tour.data.views.map((id) => views.find((v) => v.id === id)).filter((v) => v !== undefined)
    if (list.length === 0) return
    let i = 0
    showView(list[0])
    const t = setInterval(() => {
      i = (i + 1) % list.length
      showView(list[i])
    }, tour.data.dwell * 1000)
    return () => clearInterval(t)
  }, [touring, tours, views, showView])

  useEffect(() => {
    let alive = true
    const load = () => api<Camera[]>('/cameras')
      .then((c) => { if (alive) { setCameras(c); setError('') } })
      .catch(() => { if (alive) setError("Can't refresh camera status.") })
    load()
    const t = setInterval(load, 5000)
    return () => { alive = false; clearInterval(t) }
  }, [])

  useEffect(() => writePref('live.count', count), [count])
  useEffect(() => writePref('live.slots', slots), [slots])
  useEffect(() => writePref('live.shapes', cameraShapes), [cameraShapes])

  const byId = useMemo(() => new Map((cameras ?? []).map((c) => [c.id, c])), [cameras])
  const shown = useMemo(() => Array.from({ length: count }, (_, i) => {
    const id = slots[i]
    return id != null && byId.has(id) ? id : null
  }), [slots, count, byId])

  const place = (slot: number, id: number | null) => {
    const next = [...shown]
    const existing = id == null ? -1 : next.indexOf(id)
    if (existing >= 0) next[existing] = next[slot] // swap when it's already on screen
    next[slot] = id
    setSlots(next)
  }
  const addToFirstEmpty = (id: number) => {
    if (shown.includes(id)) return
    const empty = shown.indexOf(null)
    if (empty >= 0) place(empty, id)
    else setFocused(id)
  }

  const visible = (cameras ?? []).filter((c) => c.enabled && c.name.toLowerCase().includes(filter.toLowerCase()))
  const sections = [
    ...groups.map((g) => ({ key: g.id, title: g.name, cams: visible.filter((c) => c.group_id === g.id) })),
    { key: 0, title: groups.length > 0 ? 'Ungrouped' : '', cams: visible.filter((c) => !groups.some((g) => g.id === c.group_id)) },
  ].filter((s) => s.cams.length > 0)

  const saveView = async () => {
    setError('')
    try {
      await api('/config/views', 'POST', { name: viewName, data: { layout: count, slots: shown } })
      setNaming(false)
      setViewName('')
      loadViews()
    } catch (e) { setError(errorText(e)) }
  }
  const drop =(e: DragEvent, slot: number) => {
    e.preventDefault()
    const id = Number(e.dataTransfer.getData('text/plain'))
    if (id) place(slot, id)
  }

  if (cameras && cameras.length === 0) {
    return (
      <div className="center">
        <div className="empty">
          <h2>No cameras yet</h2>
          {canAddCameras
            ? <><p className="muted">Add a camera to start watching and recording.</p><button className="primary" onClick={goToCameras}>Add a camera</button></>
            : <p className="muted">Ask an administrator to add cameras.</p>}
        </div>
      </div>
    )
  }

  const shapeOf = (id: number) => cameraShapes[id] ?? 'original'
  const lensProps = (cam: Camera) => ({ rotation: cam.rotation, lens: cam.fisheye, view: lenses.viewOf(cam.id, cam.fisheye), onView: (v: Parameters<typeof lenses.setView>[1]) => lenses.setView(cam.id, v) })
  const cameraTools = (cam: Camera) => {
    const view = lenses.viewOf(cam.id, cam.fisheye)
    return (
    <>
      {cam.fisheye && view && <LensPicker name={cam.name} lens={cam.fisheye} view={view} onView={(v) => lenses.setView(cam.id, v)} />}
      {(!view || view.mode === 'circle') && <select className="shape" aria-label={`Picture shape for ${cam.name}`} title="Picture shape" value={shapeOf(cam.id)} onChange={(e) => setCameraShapes((s) => ({ ...s, [cam.id]: e.target.value as Shape }))}>
        {shapes.map((s) => <option key={s.id} value={s.id}>{s.label}</option>)}
      </select>}
      {openPlayback && <button className="tile-action" aria-label={`Open ${cam.name} in Playback`} title="Open this camera in Playback" onClick={() => openPlayback(cam.id)}>Playback</button>}
    </>
    )
  }
  const focusedCam = focused != null ? byId.get(focused) : undefined
  const cols = Math.sqrt(count)
  return (
    <div className="live">
      <aside className="sidebar">
        <input placeholder="Filter cameras" aria-label="Filter cameras" value={filter} onChange={(e) => setFilter(e.target.value)} />
        <div className="camscroll">
          {sections.map((s) => (
            <section key={s.key}>
              {s.title && <h3 className="group-title">{s.title}</h3>}
              <ul className="camlist">
                {s.cams.map((c) => (
                  <li key={c.id}>
                    <button draggable onDragStart={(e) => e.dataTransfer.setData('text/plain', String(c.id))} onClick={() => addToFirstEmpty(c.id)}>
                      <span className={c.online ? 'dot on' : c.idle ? 'dot idle' : 'dot'} title={c.online ? 'Online' : c.idle ? 'Standby: connects when viewed' : 'Offline'} />
                      <span className="truncate">{c.name}</span>
                      {c.numeric_id > 0 && <span className="muted small">{c.numeric_id}</span>}
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
        {error && <p className="error small" role="alert">{error}</p>}
      </aside>
      <section className="stage">
        <Guide topic="live" />
        <div className="toolbar">
          {focusedCam
            ? <><button className="ghost" onClick={() => setFocused(null)}>Back to grid</button><strong className="truncate">{focusedCam.name}</strong><div className="focus-tools">{cameraTools(focusedCam)}</div></>
            : <div className="segmented" role="group" aria-label="Grid size">
                {layouts.map((n) => <button key={n} className={n === count ? 'active' : ''} aria-pressed={n === count} onClick={() => setCount(n)}>{n}</button>)}
              </div>}
          {!focusedCam && views.length > 0 && (
            <select aria-label="Open a saved view" className="compact" value="" onChange={(e) => {
              const v = views.find((x) => x.id === Number(e.target.value))
              if (v) { setTouring(null); showView(v) }
            }}>
              <option value="">Open view…</option>
              {views.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}
            </select>
          )}
          {!focusedCam && tours.length > 0 && (
            <select aria-label="Run a tour" className="compact" value={touring ?? ''} onChange={(e) => setTouring(e.target.value ? Number(e.target.value) : null)}>
              <option value="">No tour</option>
              {tours.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
            </select>
          )}
          {!focusedCam && canSaveViews && (naming
            ? <form className="row" onSubmit={(e) => { e.preventDefault(); saveView() }}>
                <input className="compact" aria-label="View name" placeholder="View name" value={viewName} onChange={(e) => setViewName(e.target.value)} autoFocus required />
                <button className="primary">Save</button>
                <button type="button" className="ghost" onClick={() => setNaming(false)}>Cancel</button>
              </form>
            : <button className="ghost" onClick={() => setNaming(true)}>Save view</button>)}
          <span className="spacer" />
          {!focusedCam && <span className="muted small">Click a camera to add it. Double-click a tile for full quality.</span>}
        </div>
        {focusedCam
          ? <div className="grid" style={{ gridTemplateColumns: '1fr', gridTemplateRows: '1fr' }}>
              <div className="tile"><VideoTile key={focusedCam.id} cameraId={focusedCam.id} stream="main" shape={shapeOf(focusedCam.id)} {...lensProps(focusedCam)} /></div>
            </div>
          : <div className="grid" style={{ gridTemplateColumns: `repeat(${cols}, 1fr)`, gridTemplateRows: `repeat(${cols}, 1fr)` }}>
              {shown.map((id, i) => {
                const cam = id != null ? byId.get(id) : undefined
                return (
                  <div key={i} className={cam ? 'tile' : 'tile empty-tile'} onDragOver={(e) => e.preventDefault()} onDrop={(e) => drop(e, i)} onDoubleClick={() => cam && setFocused(cam.id)}>
                    {cam
                      ? <>
                          <VideoTile key={cam.id} cameraId={cam.id} stream={count === 1 ? 'main' : 'sub'} shape={shapeOf(cam.id)} {...lensProps(cam)} />
                          <div className="tile-label"><span className="truncate">{cam.motion && <span className="motion-badge">Motion</span>}{cam.name}</span>
                            <div className="tile-tools" onDoubleClick={(e) => e.stopPropagation()}>
                              {cameraTools(cam)}
                              <button aria-label={`Remove ${cam.name} from grid`} onClick={() => place(i, null)}>×</button>
                            </div>
                          </div>
                        </>
                      : <span className="muted small">Empty</span>}
                  </div>
                )
              })}
            </div>}
      </section>
    </div>
  )
}
