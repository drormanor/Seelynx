import { useCallback, useEffect, useRef, useState } from 'react'
import { readPref, writePref, type Fisheye } from './api'

// A fisheye camera's video is kept as the full circle. These views turn it into a
// normal-looking picture in the browser, on the graphics card, so the server does no
// extra work and any view can be chosen later, from live or recorded video.

export type LensMode = 'circle' | 'panorama' | 'view'
/** How one viewer looks through a fisheye lens. Angles are in degrees. */
export interface LensView { mode: LensMode; yaw: number; pitch: number; fov: number }

export const lensModes: { id: LensMode; label: string }[] = [
  { id: 'circle', label: 'Circle' },
  { id: 'panorama', label: 'Panorama' },
  { id: 'view', label: 'Virtual camera' },
]

export const defaultLens = (): Fisheye => ({ mount: 'wall', x: 0.5, y: 0.5, r: 0.5, fov: 190, tilt: 0 })

/** Where a virtual camera starts: straight ahead, or halfway down for a ceiling camera. */
export function startView(lens: Fisheye, mode: LensMode): LensView {
  const pitch = lens.mount === 'ceiling' ? -40 : lens.mount === 'desk' ? 40 : -lens.tilt
  return { mode, yaw: 0, pitch, fov: lens.mount === 'wall' ? 90 : 80 }
}

const minFov = 20
const maxFov = 140
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v))
const wrap = (deg: number) => ((deg + 540) % 360) - 180

/** Each camera's view, kept in this browser under key. Cameras without a fisheye lens show their circle. */
export function useLensViews(key: string) {
  const [views, setViews] = useState<Record<number, LensView>>(() => readPref(key, {}))
  useEffect(() => writePref(key, views), [key, views])
  const viewOf = useCallback((id: number, lens: Fisheye | null | undefined): LensView | null => {
    if (!lens) return null
    return views[id] ?? startView(lens, 'panorama')
  }, [views])
  const setView = useCallback((id: number, v: LensView) => setViews((all) => ({ ...all, [id]: v })), [])
  return { viewOf, setView }
}

/** The view choice for a fisheye camera's tile or player. */
export function LensPicker({ name, lens, view, onView }: { name: string; lens: Fisheye; view: LensView; onView: (v: LensView) => void }) {
  return (
    <select className="shape" aria-label={`Fisheye view for ${name}`} title="Fisheye view" value={view.mode}
      onChange={(e) => {
        const mode = e.target.value as LensMode
        onView(mode === 'view' && view.mode !== 'view' ? startView(lens, mode) : { ...view, mode })
      }}>
      {lensModes.map((m) => <option key={m.id} value={m.id}>{m.label}</option>)}
    </select>
  )
}

interface Panorama { yaw0: number; yaw1: number; pitch0: number; pitch1: number; rows: number; aspect: number }

/** The part of the world a panorama shows. A camera looking down or up shows all the way
 * round, split into two rows so it fits a screen; a wall camera shows the half in front. */
export function panorama(lens: Fisheye): Panorama {
  const half = lens.fov / 2
  if (lens.mount === 'wall') {
    const pitch0 = Math.max(-lens.tilt - 50, -85)
    const pitch1 = Math.min(-lens.tilt + 40, 85)
    return { yaw0: -90, yaw1: 90, pitch0, pitch1, rows: 1, aspect: 180 / (pitch1 - pitch0) }
  }
  const [pitch0, pitch1] = lens.mount === 'ceiling'
    ? [-65, Math.min(half - 92, 30)]
    : [Math.max(92 - half, -30), 65]
  return { yaw0: -180, yaw1: 0, pitch0, pitch1, rows: 2, aspect: 180 / (2 * (pitch1 - pitch0)) }
}

/** Rows that turn a direction (x right, y up, z ahead) into the lens's own (x right in the
 * picture, y down in the picture, z along the lens). */
function lensRows(lens: Fisheye): number[] {
  if (lens.mount === 'ceiling') return [1, 0, 0, 0, 0, -1, 0, -1, 0]
  if (lens.mount === 'desk') return [1, 0, 0, 0, 0, 1, 0, 1, 0]
  const t = (lens.tilt * Math.PI) / 180
  return [1, 0, 0, 0, -Math.cos(t), -Math.sin(t), 0, -Math.sin(t), Math.cos(t)]
}

const vertexShader = `
attribute vec2 corner;
varying vec2 pos;
void main() { pos = corner * 0.5 + 0.5; gl_Position = vec4(corner, 0.0, 1.0); }`

// Each pixel on screen is a direction. It's turned into the lens's frame, and an
// equidistant fisheye puts it at a distance from the centre in proportion to its angle
// from the lens's axis.
const fragmentShader = `
precision highp float;
varying vec2 pos;
uniform sampler2D picture;
uniform vec2 size;
uniform vec3 circle;
uniform float halfFov;
uniform vec3 row0;
uniform vec3 row1;
uniform vec3 row2;
uniform int mode;
uniform vec4 shape;
uniform float rows;
uniform vec2 turn;
void main() {
  vec3 d;
  if (mode == 0) {
    vec3 v = normalize(vec3((pos.x * 2.0 - 1.0) * shape.z, (pos.y * 2.0 - 1.0) * shape.w, 1.0));
    float cp = cos(shape.y), sp = sin(shape.y);
    v = vec3(v.x, v.y * cp + v.z * sp, -v.y * sp + v.z * cp);
    float cy = cos(shape.x), sy = sin(shape.x);
    d = vec3(v.x * cy + v.z * sy, v.y, -v.x * sy + v.z * cy);
  } else {
    float row = rows > 1.5 && pos.y < 0.5 ? 1.0 : 0.0;
    float y = rows > 1.5 ? fract(pos.y * 2.0) : pos.y;
    float span = shape.y - shape.x;
    float yaw = shape.x + (row + pos.x) * span;
    float pitch = mix(shape.z, shape.w, y);
    d = vec3(sin(yaw) * cos(pitch), sin(pitch), cos(yaw) * cos(pitch));
  }
  vec3 c = vec3(dot(row0, d), dot(row1, d), dot(row2, d));
  float theta = acos(clamp(c.z, -1.0, 1.0));
  float across = length(c.xy);
  // A picture turned for viewing is turned back to find the pixel in the video.
  vec2 o = across > 1e-6 ? c.xy / across : vec2(0.0);
  o = vec2(o.x * turn.x + o.y * turn.y, -o.x * turn.y + o.y * turn.x);
  vec2 uv = (circle.xy + o * (theta / halfFov) * circle.z) / size;
  if (theta > halfFov || uv.x < 0.0 || uv.y < 0.0 || uv.x > 1.0 || uv.y > 1.0) {
    gl_FragColor = vec4(0.0, 0.0, 0.0, 1.0);
  } else {
    gl_FragColor = texture2D(picture, uv);
  }
}`

type Source = HTMLVideoElement | HTMLImageElement

function sourceSize(s: Source): [number, number] {
  return s instanceof HTMLVideoElement ? [s.videoWidth, s.videoHeight] : [s.naturalWidth, s.naturalHeight]
}

interface Renderer { draw: (source: Source | null, lens: Fisheye, view: LensView, rotation: number) => void; dispose: () => void }

function renderer(canvas: HTMLCanvasElement): Renderer | null {
  const gl = canvas.getContext('webgl', { antialias: false, preserveDrawingBuffer: false })
  if (!gl) return null
  const compile = (type: number, text: string) => {
    const s = gl.createShader(type)!
    gl.shaderSource(s, text)
    gl.compileShader(s)
    return s
  }
  const program = gl.createProgram()!
  gl.attachShader(program, compile(gl.VERTEX_SHADER, vertexShader))
  gl.attachShader(program, compile(gl.FRAGMENT_SHADER, fragmentShader))
  gl.linkProgram(program)
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) return null
  gl.useProgram(program)
  const buffer = gl.createBuffer()
  gl.bindBuffer(gl.ARRAY_BUFFER, buffer)
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW)
  const corner = gl.getAttribLocation(program, 'corner')
  gl.enableVertexAttribArray(corner)
  gl.vertexAttribPointer(corner, 2, gl.FLOAT, false, 0, 0)
  const texture = gl.createTexture()
  gl.bindTexture(gl.TEXTURE_2D, texture)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
  const at = (n: string) => gl.getUniformLocation(program, n)
  const u = {
    size: at('size'), circle: at('circle'), halfFov: at('halfFov'), row0: at('row0'), row1: at('row1'), row2: at('row2'),
    mode: at('mode'), shape: at('shape'), rows: at('rows'), turn: at('turn'),
  }
  const rad = Math.PI / 180
  let loaded: [number, number] = [0, 0]

  return {
    draw(source, lens, view, rotation) {
      gl.viewport(0, 0, canvas.width, canvas.height)
      gl.clearColor(0, 0, 0, 1)
      gl.clear(gl.COLOR_BUFFER_BIT)
      if (source) {
        const [w, h] = sourceSize(source)
        if (w && h) {
          try {
            gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source)
            loaded = [w, h]
          } catch { /* the frame isn't ready yet */ }
        }
      }
      const [w, h] = loaded
      if (!w) return
      gl.uniform2f(u.size, w, h)
      gl.uniform3f(u.circle, lens.x * w, lens.y * h, lens.r * h)
      gl.uniform1f(u.halfFov, (lens.fov / 2) * rad)
      const r = lensRows(lens)
      gl.uniform3f(u.row0, r[0], r[1], r[2])
      gl.uniform3f(u.row1, r[3], r[4], r[5])
      gl.uniform3f(u.row2, r[6], r[7], r[8])
      gl.uniform2f(u.turn, Math.cos(rotation * rad), Math.sin(rotation * rad))
      if (view.mode === 'view') {
        const tx = Math.tan((view.fov / 2) * rad)
        gl.uniform1i(u.mode, 0)
        gl.uniform4f(u.shape, view.yaw * rad, view.pitch * rad, tx, tx * canvas.height / canvas.width)
        gl.uniform1f(u.rows, 1)
      } else {
        // The panorama keeps its shape, with bands above and below or at the sides.
        const p = panorama(lens)
        const turn = p.rows > 1 ? view.yaw : 0
        let vw = canvas.width
        let vh = Math.round(vw / p.aspect)
        if (vh > canvas.height) { vh = canvas.height; vw = Math.round(vh * p.aspect) }
        gl.viewport((canvas.width - vw) >> 1, (canvas.height - vh) >> 1, vw, vh)
        gl.uniform1i(u.mode, 1)
        gl.uniform4f(u.shape, (p.yaw0 + turn) * rad, (p.yaw1 + turn) * rad, p.pitch0 * rad, p.pitch1 * rad)
        gl.uniform1f(u.rows, p.rows)
      }
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4)
    },
    dispose() {
      gl.deleteTexture(texture)
      gl.deleteBuffer(buffer)
      gl.deleteProgram(program)
    },
  }
}

/**
 * Dewarp draws a normal view of a fisheye picture or video over its parent. In the
 * virtual camera, dragging looks around and the scroll wheel zooms; a panorama of a
 * camera looking down or up turns round when dragged sideways. Rotation is how far the
 * camera's picture is turned for viewing, such as 180 for a camera mounted upside down.
 */
export function Dewarp({ source, lens, view, onView, rotation = 0 }: { source: Source | null; lens: Fisheye; view: LensView; onView?: (v: LensView) => void; rotation?: number }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const gl = useRef<Renderer | null>(null)
  const [failed, setFailed] = useState(false)
  const latest = useRef({ lens, view, onView, rotation })
  latest.current = { lens, view, onView, rotation }

  useEffect(() => {
    const c = canvas.current!
    const r = renderer(c)
    if (!r) { setFailed(true); return }
    gl.current = r
    return () => { r.dispose(); gl.current = null }
  }, [])

  const redraw = useCallback((fresh: boolean) => {
    gl.current?.draw(fresh ? source : null, latest.current.lens, latest.current.view, latest.current.rotation)
  }, [source])

  // The canvas matches its size on screen, within reason for the graphics card.
  useEffect(() => {
    const c = canvas.current!
    const fit = () => {
      const scale = Math.min(window.devicePixelRatio || 1, 2)
      const w = Math.max(1, Math.min(Math.round(c.clientWidth * scale), 2560))
      const h = Math.max(1, Math.min(Math.round(c.clientHeight * scale), 1600))
      if (c.width !== w || c.height !== h) { c.width = w; c.height = h; redraw(true) }
    }
    fit()
    const o = new ResizeObserver(fit)
    o.observe(c)
    return () => o.disconnect()
  }, [redraw])

  // Each new frame of a video is drawn as it arrives; a picture is drawn once it loads.
  useEffect(() => {
    if (!source) return
    let stopped = false
    if (source instanceof HTMLVideoElement) {
      const v = source as HTMLVideoElement & { requestVideoFrameCallback?: (cb: () => void) => number; cancelVideoFrameCallback?: (n: number) => void }
      let handle = 0
      if (v.requestVideoFrameCallback) {
        const frame = () => { if (stopped) return; redraw(true); handle = v.requestVideoFrameCallback!(frame) }
        handle = v.requestVideoFrameCallback(frame)
        if (v.readyState >= 2) redraw(true)
        return () => { stopped = true; v.cancelVideoFrameCallback?.(handle) }
      }
      const tick = () => { if (stopped) return; if (v.readyState >= 2) redraw(true); handle = requestAnimationFrame(tick) }
      handle = requestAnimationFrame(tick)
      return () => { stopped = true; cancelAnimationFrame(handle) }
    }
    const img = source as HTMLImageElement
    const loaded = () => redraw(true)
    if (img.complete) loaded()
    img.addEventListener('load', loaded)
    return () => img.removeEventListener('load', loaded)
  }, [source, redraw])

  // A changed view or lens is drawn from the last frame, so a paused video follows too.
  useEffect(() => { redraw(false) }, [lens, view, rotation, redraw])

  // Dragging and zooming.
  useEffect(() => {
    const c = canvas.current!
    let from: { x: number; y: number; view: LensView } | null = null
    const turns = () => latest.current.view.mode === 'view' || (latest.current.view.mode === 'panorama' && latest.current.lens.mount !== 'wall')
    const down = (e: PointerEvent) => {
      if (!latest.current.onView || !turns() || e.button !== 0) return
      from = { x: e.clientX, y: e.clientY, view: latest.current.view }
      c.setPointerCapture(e.pointerId)
      e.preventDefault()
    }
    const move = (e: PointerEvent) => {
      if (!from) return
      const v = from.view
      const dx = e.clientX - from.x
      const dy = e.clientY - from.y
      if (v.mode === 'view') {
        const perPixel = v.fov / c.clientWidth
        latest.current.onView?.({ ...v, yaw: wrap(v.yaw - dx * perPixel), pitch: clamp(v.pitch + dy * perPixel, -90, 90) })
      } else {
        latest.current.onView?.({ ...v, yaw: wrap(v.yaw - (dx / c.clientWidth) * 180) })
      }
    }
    const up = () => { from = null }
    const wheel = (e: WheelEvent) => {
      const v = latest.current.view
      if (!latest.current.onView || v.mode !== 'view') return
      e.preventDefault()
      latest.current.onView({ ...v, fov: clamp(v.fov * Math.exp(e.deltaY * 0.001), minFov, maxFov) })
    }
    c.addEventListener('pointerdown', down)
    c.addEventListener('pointermove', move)
    c.addEventListener('pointerup', up)
    c.addEventListener('pointercancel', up)
    c.addEventListener('wheel', wheel, { passive: false })
    return () => {
      c.removeEventListener('pointerdown', down)
      c.removeEventListener('pointermove', move)
      c.removeEventListener('pointerup', up)
      c.removeEventListener('pointercancel', up)
      c.removeEventListener('wheel', wheel)
    }
  }, [])

  const grab = onView && (view.mode === 'view' || (view.mode === 'panorama' && lens.mount !== 'wall'))
  return (
    <>
      <canvas ref={canvas} className={`dewarp${grab ? ' grab' : ''}`} data-lens-view={view.mode}
        title={grab ? (view.mode === 'view' ? 'Drag to look around. Scroll to zoom.' : 'Drag to turn.') : undefined} />
      {failed && <div className="tile-status">This browser can't draw the fisheye view. Choose Circle instead.</div>}
    </>
  )
}

/**
 * findCircle looks for the edge of the fisheye circle in a picture: the black around it
 * is dark on every row. It fits a circle to the edges, so one cut off at the top or
 * bottom of the picture is found too. Null when no circle stands out.
 */
export function findCircle(img: HTMLImageElement): { x: number; y: number; r: number } | null {
  const h = 240
  const w = Math.round((img.naturalWidth / img.naturalHeight) * h)
  if (!w) return null
  const c = document.createElement('canvas')
  c.width = w
  c.height = h
  const ctx = c.getContext('2d', { willReadFrequently: true })
  if (!ctx) return null
  ctx.drawImage(img, 0, 0, w, h)
  const data = ctx.getImageData(0, 0, w, h).data
  const level = (x: number, y: number) => {
    const i = (y * w + x) * 4
    return Math.max(data[i], data[i + 1], data[i + 2])
  }
  // The lens's edge fades from the picture to black, darkest where the scene is dark,
  // so lit means just brighter than the black in the darkest corner.
  let black = 255
  for (const [cx0, cy0] of [[0, 0], [w - 6, 0], [0, h - 6], [w - 6, h - 6]]) {
    let m = 0
    for (let y = cy0; y < cy0 + 6; y++) for (let x = cx0; x < cx0 + 6; x++) m = Math.max(m, level(x, y))
    black = Math.min(black, m)
  }
  const threshold = Math.max(black + 12, 15)
  const lit = (x: number, y: number) => level(x, y) > threshold
  // Each row's leftmost and rightmost lit pixel, where both are clear of the frame's edge.
  const ys: number[] = []
  const mids: number[] = []
  const halves: number[] = []
  for (let y = 0; y < h; y++) {
    let l = -1
    let r = -1
    for (let x = 0; x < w; x++) if (lit(x, y)) { l = x; break }
    for (let x = w - 1; x >= 0; x--) if (lit(x, y)) { r = x; break }
    if (l > 1 && r < w - 2 && r - l > w * 0.1) { ys.push(y); mids.push((l + r) / 2); halves.push((r - l) / 2) }
  }
  if (ys.length < h * 0.3) return null
  const sorted = [...mids].sort((a, b) => a - b)
  const cx = sorted[sorted.length >> 1]
  // half² + y² = 2·cy·y + (r² − cy²): a straight line through the rows gives cy and r.
  let n = 0, sy = 0, sv = 0, syy = 0, syv = 0
  ys.forEach((y, i) => {
    const v = halves[i] * halves[i] + y * y
    n++; sy += y; sv += v; syy += y * y; syv += y * v
  })
  const slope = (n * syv - sy * sv) / (n * syy - sy * sy)
  const cy = slope / 2
  const intercept = (sv - slope * sy) / n
  const r2 = intercept + cy * cy
  if (!isFinite(r2) || r2 <= 0) return null
  const r = Math.sqrt(r2)
  return { x: cx / w, y: cy / h, r: r / h }
}

/**
 * LensSettings is the Camera page's fisheye section. The circle is found in the camera's
 * picture, drawn over it to check, and can be nudged by hand; the preview beside it
 * shows the result.
 */
export function LensSettings({ value, onChange, picture }: { value: Fisheye | null; onChange: (f: Fisheye | null) => void; picture: string | null }) {
  const [img, setImg] = useState<HTMLImageElement | null>(null)
  const [size, setSize] = useState<[number, number]>([0, 0])
  const [note, setNote] = useState('')
  const [preview, setPreview] = useState<LensView | null>(null)
  const lens = value
  const shown = lens ? (preview ?? startView(lens, 'panorama')) : null

  const find = (base: Fisheye) => {
    const found = img && img.complete && img.naturalWidth ? findCircle(img) : null
    if (!found) {
      setNote(picture ? "Couldn't find the circle in the picture. Set it by hand below." : 'No picture from the camera yet. Set the circle by hand, or take a picture first.')
      onChange(base)
      return
    }
    setNote('Found the circle. Check that the red line sits on the edge of the picture.')
    onChange({ ...base, x: round(found.x), y: round(found.y), r: round(found.r) })
  }
  const set = <K extends keyof Fisheye>(k: K, v: Fisheye[K]) => { if (lens) onChange({ ...lens, [k]: v }) }
  const percent = (lens: Fisheye, k: 'x' | 'y' | 'r', label: string, hint: string, max = 100) => (
    <label>{label}
      <input type="number" step={0.1} min={0} max={max} value={Math.round(lens[k] * 1000) / 10} onChange={(e) => set(k, Number(e.target.value) / 100)} />
      <span className="hint">{hint}</span>
    </label>
  )

  return (
    <fieldset className="lens wide">
      <legend>Fisheye lens</legend>
      <label className="check"><input type="checkbox" checked={!!lens} onChange={(e) => {
        setNote('')
        setPreview(null)
        if (e.target.checked) find(defaultLens()); else onChange(null)
      }} /> This camera has a fisheye lens <span className="hint">Live and Playback can then show a panorama, or a virtual camera you aim by dragging. The recording keeps the whole circle.</span></label>
      {picture && lens && (
        <div className="lens-preview">
          <div className="lens-picture">
            <img ref={setImg} src={picture} alt="The camera's picture, with the lens circle" onLoad={(e) => setSize([e.currentTarget.naturalWidth, e.currentTarget.naturalHeight])} />
            {size[0] > 0 && (
              <svg viewBox={`0 0 ${size[0]} ${size[1]}`} aria-hidden="true">
                <circle cx={lens.x * size[0]} cy={lens.y * size[1]} r={lens.r * size[1]} />
                <path d={`M${lens.x * size[0] - 12} ${lens.y * size[1]}h24M${lens.x * size[0]} ${lens.y * size[1] - 12}v24`} />
              </svg>
            )}
          </div>
          {shown && (
            <div className="lens-result">
              <Dewarp source={img} lens={lens} view={shown} onView={setPreview} />
            </div>
          )}
        </div>
      )}
      {/* Kept loaded while the lens is off, so the circle can be found as soon as it's ticked. */}
      {picture && !lens && <img ref={setImg} src={picture} alt="" hidden />}
      {lens && shown && <>
        {note && <p className="hint">{note}</p>}
        <div className="fields">
          <label>Mounted
            <select value={lens.mount} onChange={(e) => { set('mount', e.target.value as Fisheye['mount']); setPreview(null) }}>
              <option value="wall">On a wall, looking out</option>
              <option value="ceiling">On the ceiling, looking down</option>
              <option value="desk">On a desk or the floor, looking up</option>
            </select>
          </label>
          <label>Preview
            <select value={shown.mode === 'view' ? 'view' : 'panorama'} onChange={(e) => setPreview(startView(lens, e.target.value as LensMode))}>
              <option value="panorama">Panorama</option>
              <option value="view">Virtual camera (drag it)</option>
            </select>
          </label>
          <label>Lens angle (degrees)
            <input type="number" min={120} max={240} value={lens.fov} onChange={(e) => set('fov', Number(e.target.value))} />
            <span className="hint">How much the circle covers, edge to edge. Most fisheyes are 180 to 200. Adjust it until straight lines look straight.</span>
          </label>
          {lens.mount === 'wall' && (
            <label>Tilt down (degrees)
              <input type="number" min={-60} max={60} value={lens.tilt} onChange={(e) => set('tilt', Number(e.target.value))} />
              <span className="hint">How far the camera points below level. Adjust it until the horizon in the panorama is level.</span>
            </label>
          )}
          {percent(lens, 'x', 'Circle centre, across (%)', 'From the left edge of the picture.')}
          {percent(lens, 'y', 'Circle centre, down (%)', 'From the top edge.')}
          {percent(lens, 'r', 'Circle radius (% of height)', 'The red circle should sit on the edge of the picture.', 150)}
          <div className="row"><button type="button" className="ghost" disabled={!picture} onClick={() => find(lens)}>Find the circle again</button></div>
        </div>
      </>}
    </fieldset>
  )
}

const round = (v: number) => Math.round(v * 10000) / 10000
