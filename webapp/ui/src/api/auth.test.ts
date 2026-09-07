import { describe, expect, it } from 'vitest'
import { camelizeKeys, decodeSession } from './auth.ts'
import type { GetCapabilitiesResponse } from './csilapi/types.gen.ts'

/**
 * The session JSON's capabilities object is written by Go with snake_case keys
 * and read here through the generated camelCase type. This is the exact key
 * set `webapp/internal/handlers` emits (its own test pins the same list from
 * the Go struct's json tags). If a capability is added to the CSIL, both lists
 * change together.
 */
const GO_CAPABILITY_KEYS = [
  'view_private',
  'cancel_job',
  'kill_job',
  'retry_job',
  'create_project',
  'delete_project',
  'manage_webhook_secrets',
  'manage_vcs_credentials',
  'manage_secrets',
  'manage_groups',
  'manage_workers',
  'manage_project_settings',
  'manage_trusted_identities',
  'manage_global_settings',
  'is_global_admin',
  'is_org_admin',
  'is_project_owner',
] as const

/** Every camelCase field the SPA reads; must equal the converted Go key set. */
const TS_CAPABILITY_KEYS: (keyof GetCapabilitiesResponse)[] = [
  'viewPrivate',
  'cancelJob',
  'killJob',
  'retryJob',
  'createProject',
  'deleteProject',
  'manageWebhookSecrets',
  'manageVcsCredentials',
  'manageSecrets',
  'manageGroups',
  'manageWorkers',
  'manageProjectSettings',
  'manageTrustedIdentities',
  'manageGlobalSettings',
  'isGlobalAdmin',
  'isOrgAdmin',
  'isProjectOwner',
]

describe('decodeSession', () => {
  it('maps every Go capability key onto the generated camelCase field', () => {
    const capabilities = Object.fromEntries(GO_CAPABILITY_KEYS.map((key) => [key, true]))
    const session = decodeSession({ logged_in: true, is_global_admin: true, capabilities, roles: [] })

    const camel = Object.keys(session.capabilities).sort()
    expect(camel).toEqual([...TS_CAPABILITY_KEYS].sort())
    for (const key of TS_CAPABILITY_KEYS) expect(session.capabilities[key]).toBe(true)
  })

  it('reads the production shape: a signed-out visitor with every capability false', () => {
    // Copied from what /app/auth/session actually returned to a browser with
    // no cookie. The bug this guards against is that `cancelJob` read as
    // undefined off this object and every gate was silently false.
    const session = decodeSession({
      logged_in: false,
      is_global_admin: false,
      capabilities: { cancel_job: false, retry_job: true, manage_groups: false },
    })
    expect(session.logged_in).toBe(false)
    expect(session.capabilities.cancelJob).toBe(false)
    expect(session.capabilities.retryJob).toBe(true)
    expect(session.capabilities.manageGroups).toBe(false)
    expect(session.roles).toEqual([])
  })

  it('keeps roles, dropping an empty scope id', () => {
    const session = decodeSession({
      logged_in: true,
      user_id: 'u1',
      display_name: 'Tod',
      is_global_admin: false,
      capabilities: {},
      roles: [
        { scope_type: 'org', scope_id: 'org-1', role: 'admin' },
        { scope_type: 'global', scope_id: '', role: 'admin' },
      ],
    })
    expect(session.user_id).toBe('u1')
    expect(session.roles).toEqual([
      { scope_type: 'org', scope_id: 'org-1', role: 'admin' },
      { scope_type: 'global', scope_id: undefined, role: 'admin' },
    ])
  })

  it('tolerates a body with nothing in it', () => {
    const session = decodeSession(null)
    expect(session.logged_in).toBe(false)
    expect(session.capabilities).toEqual({})
    expect(session.roles).toEqual([])
  })
})

describe('camelizeKeys', () => {
  it('converts snake_case including digits and leaves values alone', () => {
    expect(camelizeKeys({ a_b: 1, c_2d: 'x', already: true })).toEqual({ aB: 1, c2d: 'x', already: true })
  })

  it('returns an empty object for anything that is not a plain object', () => {
    expect(camelizeKeys(undefined)).toEqual({})
    expect(camelizeKeys([1])).toEqual({})
    expect(camelizeKeys('x')).toEqual({})
  })
})
