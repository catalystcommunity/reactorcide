import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { TextArea, TextField } from '@catalystcommunity/controls'
import type { EnvDefaultEntry, ImagePolicySummary, JobLinkSummary } from '~/api/csilapi/types.gen.ts'

let entries: EnvDefaultEntry[] = []
let policy: ImagePolicySummary = { enabled: false, allowedPrefixes: [], requireDigest: false }
let links: JobLinkSummary[] = []

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listEnvDefaults: vi.fn(async () => ({ scope: 'org', entries })),
      putEnvDefault: vi.fn(async (req: { key: string; value: string }) => {
        entries = [...entries.filter((e) => e.key !== req.key), { key: req.key, value: req.value }]
        return { entry: { key: req.key, value: req.value } }
      }),
      deleteEnvDefault: vi.fn(async () => ({ deleted: true })),
      getImagePolicy: vi.fn(async () => ({ policy })),
      putImagePolicy: vi.fn(async (req: { policy: ImagePolicySummary }) => {
        policy = req.policy
        return { policy }
      }),
      listJobLinks: vi.fn(async () => ({ links })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { envDefaultsCard, envKeyError } from './EnvDefaults.ts'
import { imagePolicyCard, splitPrefixes } from './ImagePolicy.ts'
import { jobLinksCard, safeLinkURL } from './JobLinks.ts'

let mounted: Element | null = null

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

function type(el: TextField, value: string): void {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('environment defaults card', () => {
  it('lists the defaults of a worker class and saves a new one in that scope', async () => {
    entries = [{ key: 'GOPROXY', value: 'https://proxy.example/go' }]
    const el = envDefaultsCard({ organization: 'alpha', workerClass: 'fast' })
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('GOPROXY'))
    expect(api.listEnvDefaults).toHaveBeenCalledWith({ organization: 'alpha', workerClass: 'fast' })
    expect(el.textContent).toContain('Values are not secret')
    const [key, value] = [...el.querySelectorAll('text-field')] as TextField[]
    type(key, 'NPM_CONFIG_REGISTRY')
    type(value, 'https://npm.example')
    await settle()
    el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await waitFor(() =>
      expect(api.putEnvDefault).toHaveBeenCalledWith({ organization: 'alpha', workerClass: 'fast', key: 'NPM_CONFIG_REGISTRY', value: 'https://npm.example' }),
    )
    await waitFor(() => expect(el.textContent).toContain('NPM_CONFIG_REGISTRY'))
  })

  it('refuses a reserved key before it asks the coordinator', async () => {
    entries = []
    const el = envDefaultsCard({ organization: 'alpha' })
    mounted = mount(el)
    await settle()
    const key = el.querySelector('text-field') as TextField
    type(key, 'REACTORCIDE_SHA')
    await settle()
    expect(el.textContent).toContain('reserved')
    el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    expect(api.putEnvDefault).not.toHaveBeenCalled()
    expect(envKeyError('RC_WF_VARS_JSON')).toBeTruthy()
    expect(envKeyError('1BAD')).toBeTruthy()
    expect(envKeyError('GOPROXY')).toBeUndefined()
  })

  it('loads nothing with no organization', async () => {
    const el = envDefaultsCard({ organization: '' })
    mounted = mount(el)
    await settle()
    expect(api.listEnvDefaults).not.toHaveBeenCalled()
  })
})

describe('image policy card', () => {
  it('shows the stored policy and saves the form', async () => {
    policy = { enabled: true, allowedPrefixes: ['ghcr.io/acme/'], requireDigest: false }
    const el = imagePolicyCard('alpha')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('Enforced. 1 allowed prefixes.'))
    expect(api.getImagePolicy).toHaveBeenCalledWith({ organization: 'alpha' })
    const area = el.querySelector('text-area') as TextArea
    expect(area.value).toBe('ghcr.io/acme/')
    area.value = 'ghcr.io/acme/\n\n docker.io/library/ '
    el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await waitFor(() =>
      expect(api.putImagePolicy).toHaveBeenCalledWith({
        organization: 'alpha',
        policy: { enabled: true, allowedPrefixes: ['ghcr.io/acme/', 'docker.io/library/'], requireDigest: false },
      }),
    )
    expect(splitPrefixes(' a \n\nb')).toEqual(['a', 'b'])
  })
})

describe('job links card', () => {
  it('shows only http and https links, and hides with none', async () => {
    links = [
      { name: 'Build results', url: 'https://builds.example/1' },
      { name: 'Bad', url: 'javascript:alert(1)' },
    ]
    const el = jobLinksCard('job-1')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(false))
    expect(api.listJobLinks).toHaveBeenCalledWith({ jobId: 'job-1' })
    const anchors = [...el.querySelectorAll('a')]
    expect(anchors).toHaveLength(1)
    expect(anchors[0].getAttribute('href')).toBe('https://builds.example/1')
    expect(anchors[0].getAttribute('rel')).toBe('noopener noreferrer')
    expect(anchors[0].getAttribute('target')).toBe('_blank')
    expect(safeLinkURL('ftp://x')).toBe(false)
  })

  it('stays hidden when the job has no links', async () => {
    links = []
    const el = jobLinksCard('job-2')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(api.listJobLinks).toHaveBeenCalled())
    await settle()
    expect(el.hidden).toBe(true)
  })
})
