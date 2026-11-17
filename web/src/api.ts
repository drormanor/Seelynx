export type Permission = 'live' | 'playback' | 'system' | 'cameras' | 'users' | 'settings'
export interface User {
  id: number; username: string; role: string; role_id: number; permissions: Permission[]
  all_cameras: boolean; camera_ids: number[]; first_name: string; last_name: string; remarks: string
  complex_password: boolean; must_change: boolean; password_max_days: number; expires_at: number
}
export interface RoleItem { id: number; name: string; builtin: boolean; permissions: string[]; all_cameras: boolean; camera_ids: number[] }
export interface DefaultLogin { username: string; has_password: boolean }
export interface Camera {
  id: number; name: string; main_url: string; sub_url: string; username: string
  has_password: boolean; record: boolean; retention_days: number; enabled: boolean
  online: boolean; idle: boolean; motion: boolean; mbps: number; viewers: number
  group_id: number; numeric_id: number; schedule_id: number
  motion_only: boolean; pre_seconds: number; post_seconds: number; outside_mode: number
  fisheye: Fisheye | null
  rotation: Rotation
}
/** Where a fisheye lens's circle sits in the picture: fractions of the width and height, and degrees. */
export interface Fisheye { mount: 'wall' | 'ceiling' | 'desk'; x: number; y: number; r: number; fov: number; tilt: number }
/** Degrees clockwise that viewers turn a camera's picture. */
export type Rotation = 0 | 90 | 180 | 270
export interface Item<T> { id: number; name: string; data: T }
export interface GroupData { parent: number }
export interface ViewData { layout: number; slots: (number | null)[] }
export interface TourData { dwell: number; views: number[] }
export interface Period { days: number[]; start: string; end: string }
export interface ScheduleData { periods: Period[] }
export interface BackupInfo { name: string; size: number; at: string }
export interface Segment { start: string; duration: number }
export interface Device { xaddr: string; host: string; name: string; cameras?: string[] }
export interface Profile { token: string; name: string; codec: string; width: number; height: number; uri: string }
export interface EventItem { id: number; at: string; level: string; message: string }
export interface StorageLocation {
  id: number; name: string; path: string; min_free_gb: number; recording: boolean
  ok: boolean; reason: string; free: number; total: number
  recorded_bytes: number; oldest: string | null; cameras: string[]
  moving_to: number
  move: { to: number; done: number; total: number; waiting: string; failed: string } | null
  username: string; has_password: boolean
  standby: boolean
  return: { to: number; done: number; total: number; waiting: string; failed: string } | null
}
export interface FolderListing { path: string; parent: string; up: boolean; folders: { name: string; path: string }[] }
export interface SystemInfo {
  disk_free: number; disk_total: number; recorded_bytes: number; cameras: number
  online: number; ingest_mbps: number; events: EventItem[]
}

let onSignedOut = () => {}
export function setSignedOutHandler(fn: () => void) { onSignedOut = fn }

export async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const res = await fetch('/api' + path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    if (res.status === 401 && path !== '/login') onSignedOut()
    throw new Error(data?.error ?? `The server returned an error (${res.status}).`)
  }
  return data as T
}

export const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

export function readPref<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : fallback
  } catch { return fallback }
}
export function writePref(key: string, value: unknown) {
  try { localStorage.setItem(key, JSON.stringify(value)) } catch { /* layout just won't persist */ }
}
export interface MotionPeriod { id: number; camera_id: number; camera_name?: string; start: string; end: string | null }
export interface Alarm {
  id: number; at: string; rule: string; camera_id: number; camera_name: string; message: string
  priority: string; acked_by: string; acked_at: string | null; note: string; has_snapshot: boolean
}
export interface ConnectionField { key: string; label: string; secret: boolean; required: boolean; placeholder?: string }
export interface ConnectionType { id: string; label: string; help: string; fields: ConnectionField[] }
export interface ConnectionItem { id: number; name: string; type: string; config: Record<string, string> }
export interface RuleData {
  enabled: boolean; trigger: string; topic: string; camera_ids: number[]; schedule_id: number; cooldown: number
  priority: string; alarm: boolean; message: string; connections: number[]; token?: string; snapshot: boolean; camera_id: number
}
export interface AIService { provider: string; model: string; base_url: string; has_key: boolean }
export interface DriverAnalytic { topic: string; name: string; description: string }
export interface DriverData { manufacturer: string; model: string; analytics: DriverAnalytic[]; notes: string }
export interface DriverReport {
  manufacturer: string; model: string; firmware: string; snapshot: boolean
  profiles: { name: string; codec: string; width: number; height: number }[]
  topics: { topic: string; fields: string[] }[]
}
