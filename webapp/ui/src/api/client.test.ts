import { afterEach, describe, expect, it, vi } from 'vitest'
import { RpcResponse } from './transport/rpc.ts'
import { toGetAuthConfigResponseCbor, toServiceErrorCbor } from './csilapi/codec.gen.ts'
import { authApi, ServiceError } from './client.ts'

/** Makes the bridge answer with one CSIL-RPC response envelope. */
function bridgeAnswers(response: RpcResponse): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(response.encode() as unknown as BodyInit, { status: 200, headers: { 'Content-Type': 'application/cbor' } })),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('bridge transport', () => {
  it('decodes the success arm', async () => {
    bridgeAnswers(
      RpcResponse.ok('GetAuthConfigResponse', toGetAuthConfigResponseCbor({ authMode: 'none', hasGlobalAdmin: true, bootstrapAdminAvailable: false })),
    )
    await expect(authApi.getAuthConfig({})).resolves.toEqual({ authMode: 'none', hasGlobalAdmin: true, bootstrapAdminAvailable: false })
  })

  it('throws the ServiceError arm as this module’s ServiceError', async () => {
    bridgeAnswers(RpcResponse.ok('ServiceError', toServiceErrorCbor({ code: 'forbidden', message: 'no' })))
    const error = await authApi.getAuthConfig({}).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ServiceError)
    expect((error as ServiceError).isForbidden).toBe(true)
    expect((error as ServiceError).message).toBe('no')
  })

  it('refuses a reply arm the operation does not declare', async () => {
    bridgeAnswers(RpcResponse.ok('SomethingElse', new Uint8Array([0xa0])))
    await expect(authApi.getAuthConfig({})).rejects.toThrow(/variant/)
  })
})
