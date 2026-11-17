import { Guide } from './help'
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { api, errorText, type Camera, type MotionPeriod, type Segment } from './api'
import { Dewarp, LensPicker, useLensViews } from './Fisheye'

const speeds = [1, 2, 4, 8]
const slowMotion = 0.25
// From this speed up only keyframes are played, about one picture a second, because
// browsers can't decode full-HD video that fast and freeze.
const keyframesFrom = 4
const minLeft = 1000 // ms
const skipStep = 10000 // ms, for the back and forward buttons
const pad = (n: number) => String(n).padStart(2, '0')
const toDateInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
const clock = (ms: number) => new Date(ms).toLocaleTimeString([], { hour12: false })

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const minView = MINUTE // closest zoom
const maxExport = 6 * HOUR
const markedLength = 3 * MINUTE // a new marked section
const tickSteps = [5 * SECOND, 10 * SECOND, 30 * SECOND, MINUTE, 2 * MINUTE, 5 * MINUTE, 10 * MINUTE, 15 * MINUTE, 30 * MINUTE, HOUR, 2 * HOUR, 3 * HOUR]

interface Span { start: number; end: number } // epoch ms
type DragKind = 'pan' | 'select' | 'start' | 'end'

const playIcon = <svg viewBox="0 0 12 12" aria-hidden="true"><path d="M2.5 1v10l8-5z" /></svg>
const slowIcon = <svg viewBox="0 0 12 12" aria-hidden="true"><path d="M1 1.5h1.6v9H1zM4.5 1.5v9l7-4.5z" /></svg>
// Circular arrows: anticlockwise for back, clockwise for forward.
const backIcon = <svg className="ring" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 12a8 8 0 1 0 2.5-5.8L4 8.5" /><path d="M4 4v4.5h4.5" /></svg>
const forwardIcon = <svg className="ring" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 12a8 8 0 1 1-2.5-5.8L20 8.5" /><path d="M20 4v4.5h-4.5" /></svg>
const pauseIcon = <svg viewBox="0 0 12 12" aria-hidden="true"><path d="M2 1h3v10H2zM7 1h3v10H7z" /></svg>

const length = (ms: number) => {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

export function Playback({ initialCamera = null }: { initialCamera?: number | null }) {
  const [cameras, setCameras] = useState<Camera[] | null>(null)
  const [cameraId, setCameraId] = useState<number | null>(initialCamera)
  const [date, setDate] = useState(() => toDateInput(new Date()))
  const [spans, setSpans] = useState<Span[] | null>(null)
  const [motion, setMotion] = useState<Span[]>([])
  const [error, setError] = useState('')
  const [source, setSource] = useState<{ url: string; start: number; end: number } | null>(null)
  const [position, setPosition] = useState<number | null>(null)
  const [paused, setPaused] = useState(false)
  const [speed, setSpeed] = useState(1)
  const [view, setView] = useState<Span | null>(null) // null shows the whole day
  const [selection, setSelection] = useState<Span | null>(null)
  const video = useRef<HTMLVideoElement>(null)
  const [element, setElement] = useState<HTMLVideoElement | null>(null)
  const lenses = useLensViews('playback.lens')
  const strip = useRef<HTMLDivElement>(null)
  const drag = useRef<{ kind: DragKind; x: number; start: number; end: number; from: number; edge: number; moved: boolean } | null>(null)
  const [dragging, setDragging] = useState<DragKind | null>(null)
  const [shift, setShift] = useState(false)

  const [dayStart, dayEnd] = useMemo(() => {
    const [y, m, d] = date.split('-').map(Number)
    return [new Date(y, m - 1, d).getTime(), new Date(y, m - 1, d + 1).getTime()]
  }, [date])
  const viewStart = view?.start ?? dayStart
  const viewEnd = view?.end ?? dayEnd

  useEffect(() => {
    api<Camera[]>('/cameras').then((c) => {
      setCameras(c)
      setCameraId((id) => (c.some((x) => x.id === id) ? id : c[0]?.id ?? null))
    }).catch((e) => setError(errorText(e)))
  }, [])

  useEffect(() => {
    if (cameraId == null) return
    let alive = true
    const load = () => {
      const q = `start=${new Date(dayStart).toISOString()}&end=${new Date(dayEnd).toISOString()}`
      api<Segment[]>(`/cameras/${cameraId}/recordings?${q}`)
        .then((s) => {
          if (!alive) return
          setSpans(s.map((x) => {
            const start = new Date(x.start).getTime()
            return { start, end: start + x.duration * 1000 }
          }))
          setError('')
        })
        .catch((e) => { if (alive) setError(errorText(e)) })
      api<MotionPeriod[]>(`/cameras/${cameraId}/motion?${q}`)
        .then((list) => {
          if (alive) setMotion(list.map((m) => ({ start: new Date(m.start).getTime(), end: m.end ? new Date(m.end).getTime() : Date.now() })))
        })
        .catch((e) => { if (alive) setError(errorText(e)) })
    }
    setSpans(null)
    setMotion([])
    setSource(null)
    setPosition(null)
    setView(null)
    setSelection(null)
    load()
    const t = setInterval(load, 30000) // today's bar keeps growing
    return () => { alive = false; clearInterval(t) }
  }, [cameraId, dayStart, dayEnd])

  const fast = speed >= keyframesFrom
  const seek = useCallback((ms: number) => {
    if (cameraId == null || !spans) return
    // Less than a second of a span holds no keyframe, and at fast speeds the video ends at
    // the last keyframe, up to a few seconds before the span does. So a span with under a
    // second left is skipped for the next one.
    const span = spans.find((s) => ms < s.end - minLeft)
    if (!span) { setError('No recording after this time.'); return }
    const start = Math.max(ms, span.start)
    const seconds = Math.min((span.end - start) / 1000, 3600)
    if (seconds <= 0) return
    setError('')
    setPaused(false)
    setPosition(start)
    setSource({ url: `/api/cameras/${cameraId}/playback?start=${encodeURIComponent(new Date(start).toISOString())}&duration=${seconds.toFixed(1)}${fast ? '&keyframes=1' : ''}`, start, end: start + seconds * 1000 })
  }, [cameraId, spans, fast])

  // Crossing into or out of the fast speeds switches between the whole video and its
  // keyframes, from where it's got to.
  const here = useRef({ position, playing: !!source })
  here.current = { position, playing: !!source }
  const lastFast = useRef(fast)
  useEffect(() => {
    if (lastFast.current === fast) return
    lastFast.current = fast
    const { position: at, playing } = here.current
    if (playing && at != null) seek(at)
  }, [fast, seek])

  useEffect(() => { if (video.current) video.current.playbackRate = speed }, [speed, source])

  // A video taken off the page keeps downloading until the browser gets round to
  // clearing it away, and a browser opens only six connections to a server. A few quick
  // jumps then left the new video waiting behind old ones, frozen. So each video is
  // told to stop as soon as it's replaced.
  useEffect(() => {
    const v = video.current
    return () => {
      if (!v) return
      v.pause()
      v.removeAttribute('src')
      v.load()
    }
  }, [source])

  const showRange = useCallback((start: number, end: number) => {
    const size = Math.min(Math.max(end - start, minView), dayEnd - dayStart)
    if (size >= dayEnd - dayStart) { setView(null); return }
    const from = Math.min(Math.max(start, dayStart), dayEnd - size)
    setView({ start: from, end: from + size })
  }, [dayStart, dayEnd])

  const zoom = useCallback((factor: number, around?: number) => {
    const center = around ?? position ?? (viewStart + viewEnd) / 2
    const ratio = (center - viewStart) / (viewEnd - viewStart)
    const size = (viewEnd - viewStart) * factor
    const clamped = Math.min(Math.max(size, minView), dayEnd - dayStart)
    showRange(center - ratio * clamped, center - ratio * clamped + clamped)
  }, [position, viewStart, viewEnd, dayStart, dayEnd, showRange])

  // Keep the playhead in view while playing on a zoomed strip. This follows the playhead
  // only as it moves, so someone can still drag the strip away from it.
  const viewNow = useRef(view)
  viewNow.current = view
  useEffect(() => {
    const v = viewNow.current
    if (v && !drag.current && position != null && (position < v.start || position > v.end)) {
      const size = v.end - v.start
      showRange(position - size * 0.1, position + size * 0.9)
    }
  }, [position, showRange])

  // React's own wheel listener can't stop the page scrolling.
  useEffect(() => {
    const el = strip.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const box = el.getBoundingClientRect()
      zoom(e.deltaY < 0 ? 0.7 : 1 / 0.7, viewStart + ((e.clientX - box.left) / box.width) * (viewEnd - viewStart))
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [zoom, viewStart, viewEnd])

  // The pointer changes as soon as Shift is held, even if the mouse isn't moving.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => setShift(e.shiftKey)
    const onBlur = () => setShift(false)
    window.addEventListener('keydown', onKey)
    window.addEventListener('keyup', onKey)
    window.addEventListener('blur', onBlur)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('keyup', onKey)
      window.removeEventListener('blur', onBlur)
    }
  }, [])

  const timeAt = (x: number, box: DOMRect, start = viewStart, end = viewEnd) =>
    Math.min(Math.max(start + ((x - box.left) / box.width) * (end - start), dayStart), dayEnd)

  const pointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId)
    const box = e.currentTarget.getBoundingClientRect()
    const handle = (e.target as HTMLElement).dataset.handle as 'start' | 'end' | undefined
    const kind: DragKind = handle ?? (e.shiftKey ? 'select' : 'pan')
    drag.current = { kind, x: e.clientX, start: viewStart, end: viewEnd, from: timeAt(e.clientX, box), edge: handle && selection ? selection[handle] : 0, moved: false }
    setDragging(kind === 'pan' && !view ? null : kind) // the whole day has nowhere to move to
  }
  const pointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (!d) return
    if (Math.abs(e.clientX - d.x) > 4) d.moved = true
    if (!d.moved) return
    const box = e.currentTarget.getBoundingClientRect()
    const at = timeAt(Math.min(Math.max(e.clientX, box.left), box.right), box, d.start, d.end)
    if (d.kind === 'select') {
      setSelection({ start: Math.min(d.from, at), end: Math.max(d.from, at) })
    } else if (d.kind === 'start' || d.kind === 'end') {
      const edge = Math.min(Math.max(d.edge + at - d.from, dayStart), dayEnd)
      setSelection((s) => s && (d.kind === 'start'
        ? { ...s, start: Math.min(edge, s.end - SECOND) }
        : { ...s, end: Math.max(edge, s.start + SECOND) }))
    } else {
      const shift = ((d.x - e.clientX) / box.width) * (d.end - d.start)
      showRange(d.start + shift, d.end + shift)
    }
  }
  const pointerUp = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    drag.current = null
    setDragging(null)
    if (d && !d.moved && d.kind === 'pan') seek(timeAt(e.clientX, e.currentTarget.getBoundingClientRect(), d.start, d.end))
  }
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = (viewEnd - viewStart) * 0.2
    if (e.key === '+' || e.key === '=') zoom(0.5)
    else if (e.key === '-') zoom(2)
    else if (e.key === '0') setView(null)
    else if (e.key === 'ArrowLeft') showRange(viewStart - step, viewEnd - step)
    else if (e.key === 'ArrowRight') showRange(viewStart + step, viewEnd + step)
    else return
    e.preventDefault()
  }

  const pct = (ms: number) => ((ms - viewStart) / (viewEnd - viewStart)) * 100
  const visible = (s: Span) => s.end > viewStart && s.start < viewEnd
  // skip loads from the new time. The video is sent as one stream, which the browser
  // can't move around in. Going back into a gap lands in the last seconds of the
  // recording before it, not on the recording after.
  const skip = (delta: number) => {
    if (position == null || !spans) return
    let to = Math.min(Math.max(position + delta, dayStart), dayEnd)
    if (delta < 0 && !spans.some((s) => to >= s.start && to < s.end)) {
      const before = [...spans].reverse().find((s) => s.end <= to)
      if (before) to = Math.max(before.start, before.end + delta)
    }
    seek(to)
  }
  const play = (rate: number) => {
    setSpeed(rate)
    if (video.current) video.current.play().catch(() => {})
    else seek(selection?.start ?? position ?? dayStart)
  }

  const ticks = useMemo(() => {
    const step = tickSteps.find((s) => (viewEnd - viewStart) / s <= 10) ?? tickSteps[tickSteps.length - 1]
    const out: number[] = []
    for (let t = dayStart + Math.ceil((viewStart - dayStart) / step) * step; t <= viewEnd; t += step) out.push(t)
    return { times: out, seconds: step < MINUTE }
  }, [viewStart, viewEnd, dayStart])

  const timeText = (ms: number) => new Date(ms).toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
  const setSelectionTime = (which: 'start' | 'end', value: string) => {
    const [h, m, s] = value.split(':').map(Number)
    if (Number.isNaN(h) || Number.isNaN(m) || !selection) return
    const d = new Date(dayStart)
    d.setHours(h, m, s || 0, 0)
    setSelection({ ...selection, [which]: d.getTime() })
  }
  const mark = (which: 'start' | 'end') => {
    if (position == null) return
    const s = selection
    const next = which === 'start'
      ? { start: position, end: s && s.end > position ? s.end : Math.min(position + markedLength, dayEnd - SECOND) }
      : { start: s && s.start < position ? s.start : Math.max(position - markedLength, dayStart), end: position }
    setSelection(next)
    // Three minutes is only a few pixels on a whole day, too small to adjust.
    const size = next.end - next.start
    if (size < (viewEnd - viewStart) * 0.1) showRange(next.start - size * 2, next.end + size * 2)
  }
  const recorded = useMemo(() => {
    if (!selection || !spans) return 0
    return spans.reduce((sum, s) => sum + Math.max(0, Math.min(s.end, selection.end) - Math.max(s.start, selection.start)), 0)
  }, [selection, spans])
  const selectionLength = selection ? selection.end - selection.start : 0
  const problem = !selection ? ''
    : selectionLength <= 0 ? 'The end must be after the start.'
      : selectionLength > maxExport ? 'Exports can be up to 6 hours.'
        : recorded <= 0 ? 'There\'s no recording in this range.' : ''
  const exportUrl = selection && !problem
    ? `/api/cameras/${cameraId}/export?start=${encodeURIComponent(new Date(selection.start).toISOString())}&duration=${(selectionLength / 1000).toFixed(1)}`
    : undefined

  // A video element only says that it failed. Asking again for the headers tells a missing
  // recording or a server error apart from video the browser can't decode, and the
  // details let a failure be traced.
  const explainError = async (src: { url: string; start: number; end: number }, err: MediaError | null, at: number | null) => {
    const where = `at ${clock(at ?? src.start)}, ${fast ? `${speed}× keyframes` : `${speed}×`}`
    let status = 0
    const stop = new AbortController()
    try { status = (await fetch(src.url, { signal: stop.signal })).status } catch { /* left as 0 */ }
    stop.abort()
    setError(status === 404 ? `No recording here (${where}).`
      : status && status !== 200 ? `The server couldn't send this recording, error ${status} (${where}).`
        : `This recording can't be played in this browser (${where}, browser error ${err?.code ?? '?'}${err?.message ? ': ' + err.message : ''}).`)
  }

  const camera = cameras?.find((c) => c.id === cameraId)
  const lens = camera?.fisheye ?? null
  const lensView = camera ? lenses.viewOf(camera.id, lens) : null
  const dewarp = lens && lensView && lensView.mode !== 'circle'
  const playing = !!source && !paused
  const pointer = dragging ?? (shift ? 'select' : view ? 'grab' : 'seek')

  if (cameras?.length === 0) return <div className="center"><p className="muted">No cameras to play back.</p></div>
  return (
    <div className="playback">
      <Guide topic="playback" />
      <div className="toolbar">
        <label className="inline">Camera
          <select value={cameraId ?? ''} onChange={(e) => setCameraId(Number(e.target.value))}>
            {(cameras ?? []).map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
        </label>
        <label className="inline">Date
          <input type="date" value={date} max={toDateInput(new Date())} onChange={(e) => e.target.value && setDate(e.target.value)} />
        </label>
        {camera && lens && lensView && <LensPicker name={camera.name} lens={lens} view={lensView} onView={(v) => lenses.setView(camera.id, v)} />}
        <span className="spacer" />
        {position != null && <strong className="timecode">{clock(position)}</strong>}
      </div>
      <div className="player">
        {source
          ? <video key={source.url} ref={(v) => { video.current = v; setElement(v) }} src={source.url} className={dewarp ? undefined : `rot-${camera?.rotation ?? 0}`} autoPlay muted playsInline
              onTimeUpdate={(e) => setPosition(source.start + e.currentTarget.currentTime * 1000)}
              onPlay={() => setPaused(false)} onPause={() => setPaused(true)}
              onEnded={() => seek(Math.max((position ?? source.start) + SECOND, source.end))}
              onError={(e) => explainError(source, e.currentTarget.error, position)} />
          : <p className="muted">{spans && spans.length === 0 ? 'No recordings on this day.' : 'Click the timeline to start playback.'}</p>}
        {source && dewarp && camera && <Dewarp source={element} lens={lens} view={lensView} onView={(v) => lenses.setView(camera.id, v)} rotation={camera.rotation} />}
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="controls">
        <div className="controls-side">
          <div className="segmented" role="group" aria-label="Playback speed">
            {speeds.map((s) => <button key={s} className={s === speed ? 'active' : ''} aria-pressed={s === speed} onClick={() => setSpeed(s)}>{s}×</button>)}
          </div>
        </div>
        <div className="segmented transport" role="group" aria-label="Playback">
          <button aria-label="Back 10 seconds" title="Back 10 seconds" disabled={position == null} onClick={() => skip(-skipStep)}>{backIcon}10s</button>
          <button className={playing && speed >= 1 ? 'active' : ''} aria-pressed={playing && speed >= 1} disabled={!spans?.length} onClick={() => play(speed < 1 ? 1 : speed)}>{playIcon}Play</button>
          <button className={playing && speed < 1 ? 'active' : ''} aria-pressed={playing && speed < 1} disabled={!spans?.length} title="Plays at a quarter of normal speed" onClick={() => play(slowMotion)}>{slowIcon}Slow motion</button>
          <button className={source && paused ? 'active' : ''} aria-pressed={!!source && paused} disabled={!source} onClick={() => video.current?.pause()}>{pauseIcon}Pause</button>
          <button aria-label="Forward 10 seconds" title="Forward 10 seconds" disabled={position == null} onClick={() => skip(skipStep)}>10s{forwardIcon}</button>
        </div>
        <div className="controls-side end">
        <button className="ghost" disabled={position == null} onClick={() => mark('start')}>Mark start</button>
        <button className="ghost" disabled={position == null} onClick={() => mark('end')}>Mark end</button>
        <div className="segmented" role="group" aria-label="Timeline zoom">
          <button aria-label="Zoom out" disabled={!view} onClick={() => zoom(2)}>−</button>
          <button aria-label="Zoom in" disabled={viewEnd - viewStart <= minView} onClick={() => zoom(0.5)}>+</button>
          <button aria-label="Show the whole day" disabled={!view} onClick={() => setView(null)}>Fit</button>
        </div>
        </div>
      </div>
      <div ref={strip} className={`timeline pointer-${pointer}`} onPointerDown={pointerDown} onPointerMove={pointerMove} onPointerUp={pointerUp} onPointerCancel={() => { drag.current = null; setDragging(null) }} onDragStart={(e) => e.preventDefault()} onKeyDown={key}
        role="slider" aria-label="Recording timeline" aria-valuemin={0} aria-valuemax={100} aria-valuenow={position ? Math.round(pct(position)) : 0} tabIndex={0}>
        {(spans ?? []).filter(visible).map((s) => <div key={s.start} className="span" style={{ left: `${pct(s.start)}%`, width: `${Math.max(pct(s.end) - pct(s.start), 0.1)}%` }} />)}
        {motion.filter(visible).map((m) => <div key={m.start} className="motion" style={{ left: `${pct(m.start)}%`, width: `${Math.max(pct(m.end) - pct(m.start), 0.08)}%` }} />)}
        {selection && (
          <div className="selection" style={{ left: `${pct(selection.start)}%`, width: `${pct(selection.end) - pct(selection.start)}%` }}>
            <div className="handle start" data-handle="start" title="Drag to change the start" />
            <div className="handle end" data-handle="end" title="Drag to change the end" />
          </div>
        )}
        {position != null && <div className="cursor" style={{ left: `${pct(position)}%` }} />}
      </div>
      <div className="ticks" aria-hidden="true">
        {ticks.times.map((t) => <span key={t} style={{ left: `${pct(t)}%`, transform: `translateX(${pct(t) < 3 ? 0 : pct(t) > 97 ? -100 : -50}%)` }}>{new Date(t).toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', ...(ticks.seconds ? { second: '2-digit' } : {}) })}</span>)}
      </div>
      {selection && (
        <div className="row export-bar">
          <label className="inline">From<input type="time" step={1} aria-label="Export from" value={timeText(selection.start)} onChange={(e) => setSelectionTime('start', e.target.value)} /></label>
          <label className="inline">To<input type="time" step={1} aria-label="Export to" value={timeText(selection.end)} onChange={(e) => setSelectionTime('end', e.target.value)} /></label>
          {!problem && <span className="muted small">{length(recorded)} of video{recorded < selectionLength - SECOND ? `, with gaps in this ${length(selectionLength)} range` : ''}</span>}
          {problem && <span className="error small" role="alert">{problem}</span>}
          <span className="spacer" />
          {exportUrl ? <a className="primary button" href={exportUrl} download>Export video</a> : <button className="primary" disabled>Export video</button>}
          <button className="ghost" onClick={() => setSelection(null)}>Clear</button>
        </div>
      )}
    </div>
  )
}
