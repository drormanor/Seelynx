import { useEffect, useRef, useState } from 'react'
import type { Fisheye, Rotation } from './api'
import { Dewarp, type LensView } from './Fisheye'

type State = 'connecting' | 'playing' | 'unavailable'
export type Shape = 'original' | 'fill' | 'stretch' | '16x9' | '4x3'

/** Plays one camera over WebRTC and reconnects by itself after a failure. A fisheye
 * camera can be shown dewarped, through lens and view. Rotation turns the picture. */
export function VideoTile({ cameraId, stream, shape = 'original', lens, view, onView, rotation = 0 }: {
  cameraId: number; stream: 'main' | 'sub'; shape?: Shape; rotation?: Rotation
  lens?: Fisheye | null; view?: LensView | null; onView?: (v: LensView) => void
}) {
  const video = useRef<HTMLVideoElement>(null)
  const [element, setElement] = useState<HTMLVideoElement | null>(null)
  const dewarp = lens && view && view.mode !== 'circle'
  const [state, setState] = useState<State>('connecting')

  useEffect(() => {
    let stopped = false
    let pc: RTCPeerConnection | null = null
    let retry: ReturnType<typeof setTimeout> | undefined

    const scheduleRetry = () => {
      if (stopped) return
      pc?.close()
      pc = null
      setState('unavailable')
      clearTimeout(retry)
      retry = setTimeout(connect, 4000)
    }

    async function connect() {
      if (stopped) return
      const conn = new RTCPeerConnection()
      pc = conn
      conn.addTransceiver('video', { direction: 'recvonly' })
      conn.ontrack = (e) => { if (video.current) video.current.srcObject = e.streams[0] }
      conn.onconnectionstatechange = () => {
        if (conn !== pc) return
        if (conn.connectionState === 'connected') setState('playing')
        if (conn.connectionState === 'failed' || conn.connectionState === 'disconnected') scheduleRetry()
      }
      try {
        await conn.setLocalDescription(await conn.createOffer())
        await iceGathered(conn)
        const res = await fetch(`/api/cameras/${cameraId}/whep?stream=${stream}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/sdp' },
          body: conn.localDescription!.sdp,
        })
        if (!res.ok) throw new Error(String(res.status))
        if (conn !== pc) return
        await conn.setRemoteDescription({ type: 'answer', sdp: await res.text() })
      } catch {
        if (conn === pc) scheduleRetry()
      }
    }

    setState('connecting')
    connect()
    return () => {
      stopped = true
      clearTimeout(retry)
      pc?.close()
    }
  }, [cameraId, stream])

  return (
    <>
      <video ref={(v) => { video.current = v; setElement(v) }} className={dewarp ? 'shape-original' : `shape-${shape} rot-${rotation}`} autoPlay muted playsInline />
      {dewarp && <Dewarp source={element} lens={lens} view={view} onView={onView} rotation={rotation} />}
      {state !== 'playing' && <div className="tile-status">{state === 'connecting' ? 'Connecting…' : 'No video. Retrying…'}</div>}
    </>
  )
}

function iceGathered(pc: RTCPeerConnection) {
  return new Promise<void>((resolve) => {
    if (pc.iceGatheringState === 'complete') return resolve()
    const timer = setTimeout(resolve, 1500)
    pc.addEventListener('icegatheringstatechange', () => {
      if (pc.iceGatheringState === 'complete') { clearTimeout(timer); resolve() }
    })
  })
}
