import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetJobProvenanceResponse, JobArtifactSummary } from '~/api/csilapi/types.gen.ts'
import { describeError } from '~/components/ManagementPage.ts'
import { button, setButtonText } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/** Formats a byte count for people. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`
}

/** The file name of an artifact path, for the saved file. */
export function artifactFileName(path: string): string {
  const parts = path.split('/')
  return parts[parts.length - 1] || 'artifact'
}

/**
 * Reads a complete artifact through read-job-artifact, in parts of at most
 * 4 MiB, and gives each part to `write` before it reads the next one. The
 * browser never gets an object store URL.
 *
 * The CSIL-RPC transport answers one request with one envelope, so a single
 * response cannot stream. The parts are the stream: with a writable file
 * (`write` puts each part on disk) memory holds one part at a time.
 */
export async function readArtifactParts(
  jobId: string,
  artifact: JobArtifactSummary,
  write: (part: Uint8Array) => Promise<void> | void,
  progress?: (done: number, total: number) => void,
): Promise<void> {
  let offset = 0
  for (;;) {
    const response = await api.readJobArtifact({ jobId, artifactId: artifact.artifactId, offset })
    await write(response.data)
    offset += response.data.length
    progress?.(offset, response.sizeBytes)
    if (response.eof || response.data.length === 0) return
  }
}

/** Reads a complete artifact into memory. */
export async function readArtifact(jobId: string, artifact: JobArtifactSummary): Promise<Uint8Array[]> {
  const parts: Uint8Array[] = []
  await readArtifactParts(jobId, artifact, (part) => {
    parts.push(part)
  })
  return parts
}

interface WritableFile {
  write(data: Uint8Array): Promise<void>
  close(): Promise<void>
  abort(reason?: unknown): Promise<void>
}
type SavePicker = (options: { suggestedName: string }) => Promise<{ createWritable(): Promise<WritableFile> }>

/**
 * Opens a file to write the download to, when the browser has the File
 * System Access API (Chromium). It must run before the first await of the
 * click, while the click still counts as a user gesture. Undefined: the
 * browser has no such API (Firefox, Safari), or the person closed the dialog
 * (then `cancelled` is true).
 */
async function openSaveFile(name: string): Promise<{ file?: WritableFile; cancelled: boolean }> {
  const picker = (window as unknown as { showSaveFilePicker?: SavePicker }).showSaveFilePicker
  if (typeof picker !== 'function') return { cancelled: false }
  try {
    const handle = await picker({ suggestedName: name })
    return { file: await handle.createWritable(), cancelled: false }
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') return { cancelled: true }
    return { cancelled: false }
  }
}

function saveFile(parts: Uint8Array[], name: string): void {
  const blob = new Blob(parts as BlobPart[], { type: 'application/octet-stream' })
  const url = URL.createObjectURL(blob)
  const link = h('a', { href: url, download: name })
  document.body.append(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(url), 30_000)
}

/**
 * Downloads an artifact. With a writable file, each part goes to disk as it
 * arrives, so a large artifact does not fill memory. Without one, the parts
 * collect into a Blob and the browser saves it: then the whole file is in
 * memory one time (docs/artifacts-and-tests.md).
 */
export async function downloadArtifact(jobId: string, artifact: JobArtifactSummary, progress?: (done: number, total: number) => void): Promise<void> {
  const name = artifactFileName(artifact.path)
  const { file, cancelled } = await openSaveFile(name)
  if (cancelled) return
  if (file) {
    try {
      await readArtifactParts(jobId, artifact, (part) => file.write(part), progress)
      await file.close()
    } catch (err) {
      await file.abort(err).catch(() => undefined)
      throw err
    }
    return
  }
  const parts: Uint8Array[] = []
  await readArtifactParts(jobId, artifact, (part) => {
    parts.push(part)
  }, progress)
  saveFile(parts, name)
}

interface ArtifactData {
  artifacts: JobArtifactSummary[]
  provenance: GetJobProvenanceResponse | undefined
}

const KIND_LABELS: Record<string, string> = { file: 'File', test_report: 'Test report', provenance: 'Provenance' }

/**
 * The artifacts that a job published, with their SHA-256 digests, and the
 * signed provenance of the job. Hidden when the job has no artifact. Set
 * `jobId`; change `done` when the job finishes, so the list loads again.
 */
export class JobArtifactsCard extends RcElement {
  jobId = ''
  done = false

  #load = new Loader<string, ArtifactData>(this, async (key) => {
    const jobId = key.split('\n')[0]
    const [list, provenance] = await Promise.all([
      api.listJobArtifacts({ jobId }),
      api.getJobProvenance({ jobId }).catch(() => undefined),
    ])
    return { artifacts: list.artifacts, provenance }
  })
  #busy = ''
  #percent = 0
  #error = ''
  #errorEl!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #provenance!: HTMLElement
  #provenanceText!: HTMLElement
  #statement!: HTMLPreElement
  #statementToggle!: PushButton
  #showStatement = false

  setJob(jobId: string, done: boolean): void {
    if (this.jobId === jobId && this.done === done) return
    this.jobId = jobId
    this.done = done
    this.invalidate()
  }

  protected build(): void {
    this.classList.add('card')
    this.hidden = true
    this.#errorEl = h('div', { class: 'error-text', role: 'alert' })
    this.#tbody = h('tbody')
    this.#provenanceText = h('span')
    this.#statementToggle = button('Show statement', { small: true, onClick: () => this.#toggleStatement() })
    this.#statement = h('pre', { class: 'mono provenance-statement' })
    this.#provenance = h('div', { class: 'provenance' }, h('strong', null, 'Provenance: '), this.#provenanceText, ' ', this.#statementToggle, this.#statement)
    this.append(
      h('h3', { class: 'section-title' }, 'Artifacts'),
      this.#errorEl,
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table job-artifacts' },
          h('thead', null, h('tr', null, h('th', null, 'Path'), h('th', null, 'Kind'), h('th', null, 'Size'), h('th', null, 'SHA-256'), h('th'))),
          this.#tbody,
        ),
      ),
      this.#provenance,
    )
  }

  #toggleStatement(): void {
    this.#showStatement = !this.#showStatement
    this.invalidate()
  }

  async #download(artifact: JobArtifactSummary): Promise<void> {
    this.#busy = artifact.artifactId
    this.#percent = 0
    this.#error = ''
    this.invalidate()
    try {
      await downloadArtifact(this.jobId, artifact, (done, total) => {
        const percent = total > 0 ? Math.floor((done / total) * 100) : 100
        if (percent !== this.#percent) {
          this.#percent = percent
          this.invalidate()
        }
      })
    } catch (err) {
      if (this.isActive()) this.#error = describeError(err, 'The artifact could not be downloaded.')
    } finally {
      if (this.isActive()) {
        this.#busy = ''
        this.invalidate()
      }
    }
  }

  render(): void {
    this.#load.sync(this.jobId ? `${this.jobId}\n${this.done}` : undefined)
    const data = this.#load.state.data
    const artifacts = (data?.artifacts ?? []).filter((a) => a.kind !== 'provenance')
    const provenance = data?.provenance
    show(this, artifacts.length > 0 || Boolean(provenance?.present))
    setText(this.#errorEl, this.#error)
    show(this.#errorEl, this.#error !== '')
    syncKeyed(
      this.#tbody,
      artifacts,
      (a) => a.artifactId,
      (a) =>
        h(
          'tr',
          null,
          h('td', { class: 'mono artifact-path truncate' }),
          h('td', { class: 'artifact-kind' }),
          h('td', { class: 'artifact-size' }),
          h('td', { class: 'mono artifact-digest' }),
          h('td', null, button('Download', { small: true, ariaLabel: `Download ${a.path}`, onClick: () => void this.#download(a) })),
        ),
      (row, a) => {
        setText(row.querySelector('.artifact-path')!, a.path)
        const kind = KIND_LABELS[a.kind] ?? a.kind
        setText(row.querySelector('.artifact-kind')!, a.status === 'complete' ? kind : `${kind} (${a.status}${a.statusReason ? `: ${a.statusReason}` : ''})`)
        setText(row.querySelector('.artifact-size')!, formatBytes(a.sizeBytes))
        const digest = row.querySelector('.artifact-digest') as HTMLElement
        setText(digest, a.sha256 ? `${a.sha256.slice(0, 12)}…` : '-')
        digest.title = a.sha256
        const download = row.querySelector('push-button') as PushButton
        download.disabled = a.status !== 'complete' || this.#busy !== ''
        setButtonText(download, this.#busy === a.artifactId ? `Downloading… ${this.#percent} %` : 'Download')
      },
    )
    show(this.#provenance, Boolean(provenance?.present))
    if (provenance?.present) {
      setText(
        this.#provenanceText,
        provenance.verified ? `signed by the coordinator, signature verified (key ${provenance.keyId ?? ''})` : `not verified: ${provenance.error ?? 'unknown error'}`,
      )
      setText(this.#statement, provenance.statementJson ?? '')
      show(this.#statementToggle, Boolean(provenance.statementJson))
      setButtonText(this.#statementToggle, this.#showStatement ? 'Hide statement' : 'Show statement')
      show(this.#statement, this.#showStatement && Boolean(provenance.statementJson))
    }
  }
}

export const jobArtifactsCardTag = 'rc-job-artifacts-card'
define(jobArtifactsCardTag, JobArtifactsCard)

export function jobArtifactsCard(jobId: string): JobArtifactsCard {
  const el = document.createElement(jobArtifactsCardTag) as JobArtifactsCard
  el.jobId = jobId
  return el
}
