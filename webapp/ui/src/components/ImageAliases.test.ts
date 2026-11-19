import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { CheckBox, TextField } from '@catalystcommunity/controls'
import type { ImageAliasRollout, ImageAliasSummary } from '~/api/csilapi/types.gen.ts'

const D1 = 'sha256:' + '1'.repeat(64)
const D2 = 'sha256:' + '2'.repeat(64)
const REF1 = `registry.example.com/vm/win:2026.11@${D1}`
const REF2 = `registry.example.com/vm/win:2026.12@${D2}`

function alias(extra: Partial<ImageAliasSummary> = {}): ImageAliasSummary {
  return {
    aliasId: 'a-win',
    orgId: 'org-a',
    scope: 'organization',
    name: 'win',
    kind: 'vm',
    os: 'windows',
    arch: 'amd64',
    characteristics: [{ key: 'worker_class', value: 'win-vs' }],
    activeRef: REF1,
    activeDigest: D1,
    createdAt: '2026-11-01T00:00:00Z',
    updatedAt: '2026-11-01T00:00:00Z',
    updatedBy: 'u1',
    ...extra,
  }
}

function rollout(a: ImageAliasSummary, extra: Partial<ImageAliasRollout> = {}): ImageAliasRollout {
  return {
    alias: a,
    workers: [
      {
        workerId: 'w-1',
        hostname: 'mac-1',
        poolId: 'pool',
        online: true,
        imageStatus: 'updating images',
        imageSetVersion: 4,
        imageReportedVersion: 3,
        hasActive: true,
        hasPending: false,
        ready: false,
      },
    ],
    onlineWorkers: 1,
    readyWorkers: 0,
    promotable: false,
    blocked: 'mac-1 does not have the pending image.',
    ...extra,
  }
}

let aliases: ImageAliasSummary[] = []
let rollouts: ImageAliasRollout[] = []

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {
    constructor(
      readonly code: string,
      message: string,
    ) {
      super(message)
    }
  }
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listImageAliases: vi.fn(async () => ({ aliases })),
      getImageRolloutStatus: vi.fn(async () => ({ rollouts })),
      setImageAlias: vi.fn(async (req: { name: string }) => ({ alias: alias({ name: req.name }), outcome: 'pending' })),
      deleteImageAlias: vi.fn(async () => ({ deleted: true })),
    },
  }
})

vi.mock('~/ui/controls.ts', async (importOriginal) => {
  const real = await importOriginal<typeof import('~/ui/controls.ts')>()
  return { ...real, confirmAction: vi.fn(async () => true) }
})

import { ServiceError, api } from '~/api/client.ts'
import { confirmAction } from '~/ui/controls.ts'
import { mount, settle } from '~/test/mount.ts'
import { imageAliasesPanel, type ImageAliasesPanel } from './ImageAliases.ts'

function control(root: Element, label: string): TextField {
  const f = [...root.querySelectorAll('.field')].find((el) => el.querySelector('.field-label span')?.textContent === label)
  if (!f) throw new Error(`no field ${label}`)
  return f.querySelector('text-field, combo-box') as TextField
}

function type(el: Element, value: string): void {
  ;(el as TextField).value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function byLabel(root: Element, label: string): HTMLElement {
  const el = [...root.querySelectorAll('push-button')].find((b) => b.getAttribute('aria-label') === label)
  if (!el) throw new Error(`no button ${label}`)
  return el as HTMLElement
}

function submit(root: Element): void {
  root.querySelector('form.image-alias-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}

async function panel(organization = 'acme', projectId = ''): Promise<ImageAliasesPanel> {
  const el = mount(imageAliasesPanel(organization, projectId))
  await settle()
  return el
}

beforeEach(() => {
  vi.clearAllMocks()
  document.body.replaceChildren()
  const pending = alias({ pendingRef: REF2, pendingDigest: D2, pendingSince: new Date().toISOString() })
  aliases = [pending]
  rollouts = [rollout(pending)]
})

afterEach(() => {
  vi.useRealTimers()
})

describe('image aliases panel', () => {
  it('lists the aliases with the active pin, the pending pin and the rollout progress', async () => {
    const el = await panel()
    expect(api.listImageAliases).toHaveBeenCalledWith({ organization: 'acme' })
    expect(api.getImageRolloutStatus).toHaveBeenCalledWith({ organization: 'acme' })
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    const row = el.querySelector('tr.image-alias-row')!
    expect(row.querySelector('.alias-name')!.textContent).toBe('win')
    expect(row.querySelector('.alias-platform')!.textContent).toBe('windows/amd64')
    expect(row.querySelector('.alias-chars')!.textContent).toBe('worker_class=win-vs')
    expect(row.querySelector('.alias-active')!.textContent).toBe(REF1)
    expect(row.querySelector('.alias-pending')!.textContent).toBe(REF2)
    expect(row.querySelector('.alias-progress')!.textContent).toContain('0 of 1 online worker ready. mac-1 does not have the pending image.')
    expect(byLabel(el, 'Change alias win').hidden).toBe(false)
    expect(byLabel(el, 'Delete alias win').hidden).toBe(false)
    expect(el.querySelector('form.image-alias-form')).toBeTruthy()
  })

  it('shows the serving workers of an alias in the rollout view', async () => {
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    byLabel(el, 'Rollout of alias win').click()
    await settle()
    const view = el.querySelector('tr.rollout-row')!
    expect(view.querySelector('.rollout-worker')!.textContent).toBe('mac-1')
    expect(view.querySelector('.rollout-worker')!.getAttribute('href')).toBe('/app/workers/w-1')
    expect(view.querySelector('.rollout-status')!.textContent).toBe('updating images')
    expect(view.querySelector('.rollout-version')!.textContent).toBe('v4 (reported v3)')
    expect(view.querySelector('.rollout-ready')!.textContent).toBe('no')
    byLabel(el, 'Rollout of alias win').click()
    await settle()
    expect(el.querySelector('tr.rollout-row')).toBeNull()
  })

  it('shows worker failures, the problems of the alias, and counts only for other pools', async () => {
    vi.useFakeTimers({ toFake: ['setInterval'] })
    const pending = alias({ pendingRef: REF2, pendingDigest: D2 })
    aliases = [pending]
    const base = rollout(pending)
    rollouts = [
      rollout(pending, {
        workers: [{ ...base.workers[0], failed: [{ digest: D2, permanent: true, detail: 'manifest unknown' }] }],
        problems: ['1 serving worker cannot pull the active pin: manifest unknown'],
        otherPools: [{ poolName: 'hosted-mac', workers: 3, onlineWorkers: 2, readyWorkers: 1, failedWorkers: 1 }],
      }),
    ]
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    byLabel(el, 'Rollout of alias win').click()
    await settle()
    const view = el.querySelector('tr.rollout-row')!
    const problems = view.querySelector('.rollout-problems') as HTMLElement
    expect(problems.hidden).toBe(false)
    expect(problems.getAttribute('role')).toBe('alert')
    expect(problems.textContent).toContain('cannot pull the active pin: manifest unknown')
    const failures = view.querySelector('.rollout-failures') as HTMLElement
    expect(failures.hidden).toBe(false)
    expect(failures.textContent).toContain('sha256:222222222222')
    expect(failures.textContent).toContain('failed (permanent): manifest unknown')
    const pools = view.querySelector('.rollout-other-pools') as HTMLElement
    expect(pools.hidden).toBe(false)
    expect([...pools.querySelectorAll('li')].map((li) => li.textContent)).toEqual(['hosted-mac: 3 workers, 2 online, 1 ready, 1 failed'])
    // Rows of other pools carry no worker IDs.
    expect(view.querySelectorAll('.rollout-worker').length).toBe(1)

    // A refresh with no problems hides the warning in place.
    rollouts = [rollout(pending)]
    await vi.advanceTimersByTimeAsync(15_000)
    await settle()
    await waitFor(() => expect(problems.hidden).toBe(true))
    expect(el.querySelector('tr.rollout-row .rollout-problems')).toBe(problems)
    expect((view.querySelector('.rollout-failures') as HTMLElement).hidden).toBe(true)
    expect(pools.hidden).toBe(true)
  })

  it('changes rows in place on a refresh and keeps the typed text of the form', async () => {
    vi.useFakeTimers({ toFake: ['setInterval'] })
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    const row = el.querySelector('tr.image-alias-row')!
    const form = el.querySelector('form.image-alias-form')!
    const ref = control(form, 'Pinned reference')
    type(control(form, 'Name'), 'linux-build')
    type(ref, 'registry.example.com/half-typed')
    await settle()

    const promoted = alias({ activeRef: REF2, activeDigest: D2 })
    aliases = [promoted, alias({ aliasId: 'a-mac', name: 'mac', os: 'macos', arch: 'arm64', characteristics: [] })]
    rollouts = [rollout(promoted, { readyWorkers: 1, blocked: undefined })]
    await vi.advanceTimersByTimeAsync(15_000)
    await settle()

    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(2))
    expect(el.querySelector('tr.image-alias-row')).toBe(row)
    expect(row.querySelector('.alias-active')!.textContent).toBe(REF2)
    expect(row.querySelector('.alias-pending')!.textContent).toBe('—')
    expect((row.querySelector('.alias-progress') as HTMLElement).hidden).toBe(true)
    expect(control(form, 'Pinned reference')).toBe(ref)
    expect(ref.value).toBe('registry.example.com/half-typed')
    expect(control(form, 'Name').value).toBe('linux-build')
  })

  it('hides the edit controls and says why when the coordinator refuses the list', async () => {
    vi.mocked(api.listImageAliases).mockRejectedValueOnce(new ServiceError('forbidden', 'you do not have permission to perform this operation'))
    vi.mocked(api.getImageRolloutStatus).mockRejectedValueOnce(new ServiceError('forbidden', 'you do not have permission to perform this operation'))
    const el = await panel()
    await waitFor(() => expect((el.querySelector('.image-aliases-refused') as HTMLElement).hidden).toBe(false))
    const form = el.querySelector('form.image-alias-form') as HTMLElement
    expect(form.closest('[hidden]')).toBeTruthy()
    expect(el.querySelectorAll('tr.image-alias-row').length).toBe(0)
  })

  it('shows the refusal message of the coordinator when a delete is refused', async () => {
    vi.mocked(api.deleteImageAlias).mockRejectedValueOnce(new ServiceError('failed_precondition', 'image alias win is used by 2 jobs that are not terminal'))
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    byLabel(el, 'Delete alias win').click()
    await waitFor(() => expect(api.deleteImageAlias).toHaveBeenCalledWith({ organization: 'acme', name: 'win' }))
    await settle()
    const error = el.querySelector('tr.image-alias-row .alert-error') as HTMLElement
    expect(error.hidden).toBe(false)
    expect(error.textContent).toBe('image alias win is used by 2 jobs that are not terminal')
    // The row stays.
    expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1)
  })

  it('creates an alias, and asks before a forced promotion', async () => {
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    const form = el.querySelector('form.image-alias-form')!
    type(control(form, 'Name'), 'linux-build')
    type(control(form, 'Pinned reference'), `oci://registry.example.com/build:1@${D2}`)
    type(control(form, 'Os'), 'linux')
    type(control(form, 'Arch'), 'arm64')
    type(control(form, 'Characteristics'), 'worker_class=big, region=us')
    const force = [...form.querySelectorAll('check-box')][0] as CheckBox
    force.checked = true
    force.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()

    vi.mocked(confirmAction).mockResolvedValueOnce(false)
    submit(el)
    await waitFor(() => expect(confirmAction).toHaveBeenCalledTimes(1))
    await settle()
    expect(api.setImageAlias).not.toHaveBeenCalled()

    submit(el)
    await waitFor(() => expect(api.setImageAlias).toHaveBeenCalled())
    expect(vi.mocked(api.setImageAlias).mock.calls[0][0]).toStrictEqual({
      organization: 'acme',
      name: 'linux-build',
      ref: `oci://registry.example.com/build:1@${D2}`,
      os: 'linux',
      arch: 'arm64',
      characteristics: [
        { key: 'worker_class', value: 'big' },
        { key: 'region', value: 'us' },
      ],
      force: true,
    })
    await settle()
    expect(el.querySelector('.image-alias-result')!.textContent).toContain('The new pin of linux-build is pending.')
    expect(control(form, 'Name').value).toBe('')
  })

  it('an edit changes only the pin: kind, os, arch, and characteristics are locked', async () => {
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    byLabel(el, 'Change alias win').click()
    await settle()
    const form = el.querySelector('form.image-alias-form')!
    expect(control(form, 'Os').disabled).toBe(true)
    expect(control(form, 'Arch').disabled).toBe(true)
    expect(control(form, 'Characteristics').disabled).toBe(true)
    type(control(form, 'Pinned reference'), REF1)
    await settle()
    submit(el)
    await waitFor(() => expect(api.setImageAlias).toHaveBeenCalled())
    expect(vi.mocked(api.setImageAlias).mock.calls[0][0]).toStrictEqual({ organization: 'acme', name: 'win', ref: REF1 })
  })

  it('refuses a reference without a digest before the round trip', async () => {
    const el = await panel()
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(1))
    const form = el.querySelector('form.image-alias-form')!
    type(control(form, 'Name'), 'linux-build')
    type(control(form, 'Pinned reference'), 'alpine:3.20')
    await settle()
    expect(form.querySelector('.alert-error')!.textContent).toContain('@sha256:')
    submit(el)
    await settle()
    expect(api.setImageAlias).not.toHaveBeenCalled()
  })

  it('in a project, shows organization aliases as inherited and overrides one as a project alias', async () => {
    aliases = [alias(), alias({ aliasId: 'a-proj', name: 'mac', scope: 'project', projectId: 'p1', os: 'macos', arch: 'arm64', characteristics: [] })]
    rollouts = []
    const el = await panel('acme', 'p1')
    expect(api.listImageAliases).toHaveBeenCalledWith({ organization: 'acme', projectId: 'p1' })
    await waitFor(() => expect(el.querySelectorAll('tr.image-alias-row').length).toBe(2))
    expect(el.querySelector('tr.image-alias-row .alias-scope')!.textContent).toBe('organization (inherited)')
    expect(byLabel(el, 'Delete alias win').hidden).toBe(true)
    expect(byLabel(el, 'Change alias win').hidden).toBe(true)
    expect(byLabel(el, 'Override alias win for this project').hidden).toBe(false)
    expect(byLabel(el, 'Delete alias mac').hidden).toBe(false)

    byLabel(el, 'Override alias win for this project').click()
    await settle()
    const form = el.querySelector('form.image-alias-form')!
    expect(control(form, 'Name').value).toBe('win')
    type(control(form, 'Pinned reference'), REF2)
    await settle()
    submit(el)
    await waitFor(() => expect(api.setImageAlias).toHaveBeenCalled())
    expect(vi.mocked(api.setImageAlias).mock.calls[0][0]).toStrictEqual({
      organization: 'acme',
      projectId: 'p1',
      name: 'win',
      ref: REF2,
      kind: 'vm',
      os: 'windows',
      arch: 'amd64',
      characteristics: [{ key: 'worker_class', value: 'win-vs' }],
    })

    byLabel(el, 'Delete alias mac').click()
    await waitFor(() => expect(api.deleteImageAlias).toHaveBeenCalledWith({ organization: 'acme', projectId: 'p1', name: 'mac' }))
  })
})
