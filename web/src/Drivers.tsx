import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Guide } from './help'
import { api, errorText, type AIService, type Camera, type DriverData, type DriverReport, type Item } from './api'

const defaultModel = 'claude-opus-5-5'

export function AIServiceSettings() {
  const [saved, setSaved] = useState<AIService | null>(null)
  const [provider, setProvider] = useState('anthropic')
  const [model, setModel] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    api<AIService>('/ai').then((a) => {
      setSaved(a)
      if (a.provider) { setProvider(a.provider); setModel(a.model); setBaseUrl(a.base_url) }
    }).catch((e) => setError(errorText(e)))
  }, [])
  useEffect(load, [load])

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setError(''); setNotice('')
    try {
      await api('/ai', 'PUT', { provider, model, base_url: baseUrl, api_key: key })
      setKey('')
      setNotice('Saved. Press Test to check it.')
      load()
    } catch (err) { setError(errorText(err)) }
  }
  const test = async () => {
    setError(''); setNotice(''); setBusy(true)
    try { await api('/ai/test', 'POST'); setNotice('It works. Seelynx reached the AI service.') }
    catch (err) { setError(errorText(err)) }
    setBusy(false)
  }
  const remove = async () => {
    setError(''); setNotice('')
    try {
      await api('/ai', 'PUT', { provider: '' })
      setProvider('anthropic'); setModel(''); setBaseUrl('')
      load()
    } catch (err) { setError(errorText(err)) }
  }
  const compatible = provider === 'openai'
  return (
    <>
      <div className="section-head"><h2>AI service</h2><p className="muted">Seelynx doesn't include an AI service. Connect your own account or local model to build drivers. You pay your provider for what you use.</p></div>
      <Guide topic="ai" />
      {error && <p className="error" role="alert">{error}</p>}
      {notice && <p className="notice" role="status">{notice}</p>}
      <form className="card form" onSubmit={save}>
        <div className="fields">
          <label>Service
            <select value={provider} onChange={(e) => { setProvider(e.target.value); setKey('') }}>
              <option value="anthropic">Claude (Anthropic)</option>
              <option value="openai">OpenAI or a compatible server</option>
            </select>
          </label>
          <label>Model
            <input value={model} onChange={(e) => setModel(e.target.value)} placeholder={compatible ? '' : defaultModel} required={compatible} />
            <span className="hint">{compatible ? 'The exact model name from your service.' : `Leave empty to use ${defaultModel}.`}</span>
          </label>
          {compatible && (
            <label className="wide">Service address
              <input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="http://localhost:11434/v1" />
              <span className="hint">Leave empty for OpenAI. Enter your server's address for a local model.</span>
            </label>
          )}
          <label className="wide">API key
            <input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder={saved?.has_key ? 'Unchanged' : ''} />
            <span className="hint">Stored encrypted and never shown again. A local server may not need one.</span>
          </label>
        </div>
        <div className="row">
          <button className="primary">Save</button>
          {saved?.provider && <button type="button" className="ghost" disabled={busy} onClick={test}>{busy ? 'Testing…' : 'Test'}</button>}
          {saved?.provider && <button type="button" className="ghost" onClick={remove}>Remove</button>}
        </div>
      </form>
    </>
  )
}

const fileName = (d: DriverData) => `${d.manufacturer}-${d.model}.json`.replace(/[^\w.-]+/g, '_')

export function Drivers() {
  const [list, setList] = useState<Item<DriverData>[]>([])
  const [cameras, setCameras] = useState<Camera[]>([])
  const [ai, setAI] = useState<AIService | null>(null)
  const [cameraId, setCameraId] = useState(0)
  const [report, setReport] = useState<DriverReport | null>(null)
  const [draft, setDraft] = useState<DriverData | null>(null)
  const [editId, setEditId] = useState(0)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(() => { api<Item<DriverData>[]>('/config/drivers').then(setList).catch((e) => setError(errorText(e))) }, [])
  useEffect(() => {
    load()
    api<Camera[]>('/cameras').then(setCameras).catch((e) => setError(errorText(e)))
    api<AIService>('/ai').then(setAI).catch((e) => setError(errorText(e)))
  }, [load])

  const run = async (label: string, fn: () => Promise<void>) => {
    setError(''); setBusy(label)
    try { await fn() } catch (err) { setError(errorText(err)) }
    setBusy('')
  }
  const read = () => run('Reading the camera…', async () => {
    setDraft(null)
    setReport(await api<DriverReport>('/drivers/probe', 'POST', { camera_id: cameraId }))
  })
  const build = () => run('Waiting for the AI service…', async () => {
    setDraft(await api<DriverData>('/drivers/build', 'POST', report))
    setEditId(list.find((d) => d.data.manufacturer === report?.manufacturer && d.data.model === report?.model)?.id ?? 0)
  })
  const save = () => run('Saving…', async () => {
    if (!draft) return
    const name = `${draft.manufacturer} ${draft.model}`
    await api(editId ? `/config/drivers/${editId}` : '/config/drivers', editId ? 'PUT' : 'POST', { name, data: draft })
    setDraft(null); setReport(null); setEditId(0)
    load()
  })
  const remove = (id: number) => run('', async () => { await api(`/config/drivers/${id}`, 'DELETE'); load() })
  const download = (d: DriverData) => {
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([JSON.stringify(d, null, 2)], { type: 'application/json' }))
    a.download = fileName(d)
    a.click()
    URL.revokeObjectURL(a.href)
  }
  const setAnalytic = (i: number, field: 'name' | 'description', value: string) => {
    if (!draft) return
    setDraft({ ...draft, analytics: draft.analytics.map((a, n) => (n === i ? { ...a, [field]: value } : a)) })
  }
  const connected = !!ai?.provider
  return (
    <>
      <div className="section-head"><h2>Drivers</h2><p className="muted">A driver gives a camera model's analytics names people understand, for rules and alerts. Cameras work without one.</p></div>
      <Guide topic="drivers" />
      {error && <p className="error" role="alert">{error}</p>}
      {ai && !connected && <p className="notice" role="status">Connect an AI service to build drivers. You can still use drivers you've already saved.</p>}

      {connected && !draft && (
        <div className="card form">
          <h3>Build a driver</h3>
          <div className="row">
            <label className="inline">Camera
              <select aria-label="Camera" value={cameraId} onChange={(e) => { setCameraId(Number(e.target.value)); setReport(null) }}>
                <option value={0}>Choose a camera</option>
                {cameras.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            </label>
            <button className="ghost" disabled={!cameraId || !!busy} onClick={read}>Read camera</button>
            {busy && <span className="muted">{busy}</span>}
          </div>
          {report && <>
            <p><strong>{report.manufacturer} {report.model}</strong> <span className="muted">reports {report.topics.length} analytics.</span></p>
            <details className="guide" open>
              <summary>Exactly what will be sent to your AI service</summary>
              <pre>{JSON.stringify(report, null, 2)}</pre>
            </details>
            <div className="row"><button className="primary" disabled={!!busy} onClick={build}>Build driver</button></div>
          </>}
        </div>
      )}

      {draft && (
        <div className="card form">
          <h3>Review {draft.manufacturer} {draft.model}</h3>
          <p className="hint">These names were written by AI, so check that they make sense before saving. {editId ? 'Saving replaces the driver you have for this model.' : ''}</p>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Name</th><th>Description</th><th>Camera's event</th></tr></thead>
              <tbody>
                {draft.analytics.map((a, i) => (
                  <tr key={a.topic}>
                    <td><input aria-label={`Name for ${a.topic}`} value={a.name} maxLength={60} onChange={(e) => setAnalytic(i, 'name', e.target.value)} /></td>
                    <td><input aria-label={`Description for ${a.topic}`} value={a.description} maxLength={200} onChange={(e) => setAnalytic(i, 'description', e.target.value)} /></td>
                    <td className="muted small">{a.topic}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {draft.notes && <p className="hint">{draft.notes}</p>}
          <div className="row">
            <button className="primary" disabled={!!busy} onClick={save}>Save driver</button>
            <button className="ghost" onClick={() => { setDraft(null); setEditId(0) }}>Cancel</button>
          </div>
        </div>
      )}

      <div className="table-wrap">
        <table>
          <tbody>
            {list.map((d) => (
              <tr key={d.id}>
                <td>{d.name}</td>
                <td className="muted">{d.data.analytics.length} analytics</td>
                <td className="actions">
                  <button className="ghost" aria-label={`Edit ${d.name}`} onClick={() => { setDraft(d.data); setEditId(d.id) }}>Edit</button>
                  <button className="ghost" aria-label={`Download ${d.name}`} onClick={() => download(d.data)}>Download</button>
                  <button className="ghost" aria-label={`Remove ${d.name}`} onClick={() => remove(d.id)}>Remove</button>
                </td>
              </tr>
            ))}
            {list.length === 0 && <tr><td className="muted">No drivers yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </>
  )
}
