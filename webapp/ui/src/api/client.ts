import { RpcRequest, RpcResponse } from './transport/rpc.ts'
import { decode, fromServiceErrorCborValue } from './csilapi/codec.gen.ts'
import {
  ReactorcideUiAsyncClient,
  ReactorcideAuthAsyncClient,
  type AsyncServiceTransport,
} from './csilapi/client.async.gen.ts'

/**
 * The browser's CSIL-RPC carrier.
 *
 * It posts to the webapp's bridge at /app/rpc, NOT to the coordinator. That
 * matters: the bridge replaces whatever credential this envelope carries with
 * the one bound to the browser's HttpOnly session cookie, which page JavaScript
 * cannot read. So this transport never sets an auth field, and there is nothing
 * here for an XSS to steal — the session is not reachable from script at all.
 */
const BRIDGE_PATH = '/app/rpc'

/** An application-level failure: the coordinator's own ServiceError arm. */
export class ServiceError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ServiceError'
  }

  /** True when the caller needs to sign in for this to succeed. */
  get isUnauthorized(): boolean {
    return this.code === 'unauthorized'
  }

  /** True when the caller is signed in but not allowed. */
  get isForbidden(): boolean {
    return this.code === 'forbidden'
  }

  get isNotFound(): boolean {
    return this.code === 'not_found'
  }
}

/** A carrier failure: the request never got a typed answer. */
export class TransportFailure extends Error {
  constructor(
    message: string,
    readonly status?: number,
  ) {
    super(message)
    this.name = 'TransportFailure'
  }
}

class BridgeTransport implements AsyncServiceTransport {
  async call(service: string, op: string, req: Uint8Array): Promise<Uint8Array> {
    return (await this.callReply(service, op, req)).payload
  }

  /**
   * The reply arm and its bytes. The generated client checks that the
   * variant names the success arm. The ServiceError arm is thrown here as
   * this module's ServiceError, so every caller can test `instanceof
   * ServiceError`; the generated client would throw its own type.
   */
  async callReply(service: string, op: string, req: Uint8Array): Promise<{ variant?: string | null; payload: Uint8Array }> {
    const envelope = new RpcRequest(service, op, req).encode()

    let response: Response
    try {
      response = await fetch(BRIDGE_PATH, {
        method: 'POST',
        // application/cbor is not a CORS "simple" content type, so a
        // cross-origin page cannot send this request without a preflight the
        // bridge refuses. That is one of the three CSRF defences; the cookie's
        // SameSite=Lax and the bridge's Origin check are the others.
        headers: { 'Content-Type': 'application/cbor', Accept: 'application/cbor' },
        // The session cookie must ride along. It is HttpOnly, so this is the
        // only way it travels.
        credentials: 'same-origin',
        body: envelope as unknown as BodyInit,
      })
    } catch (cause) {
      throw new TransportFailure(`${service}/${op}: the server could not be reached`)
    }

    if (!response.ok) {
      throw new TransportFailure(
        `${service}/${op}: request failed (${response.status})`,
        response.status,
      )
    }

    const decoded = RpcResponse.decode(new Uint8Array(await response.arrayBuffer()))
    if (decoded.status !== 0) {
      throw new TransportFailure(decoded.error ?? `${service}/${op}: transport status ${decoded.status}`)
    }
    if (decoded.variant === 'ServiceError') {
      throw decodeServiceError(decoded.payload)
    }
    return { variant: decoded.variant, payload: decoded.payload }
  }
}

/**
 * Decodes the ServiceError arm with the generated codec. A payload that does
 * not decode still gives a ServiceError, so the error path cannot itself
 * throw something unhelpful.
 */
function decodeServiceError(payload: Uint8Array): ServiceError {
  try {
    const value = fromServiceErrorCborValue(decode(payload))
    return new ServiceError(value.code || 'unknown', value.message || 'The request failed.')
  } catch {
    return new ServiceError('unknown', 'The request failed.')
  }
}

const transport = new BridgeTransport()

/** The coordinator's UI service: everything the SPA reads and writes. */
export const api = new ReactorcideUiAsyncClient(transport)

/**
 * The auth service.
 *
 * Most of it is unreachable through the bridge on purpose: begin-login,
 * complete-login, logout and bootstrap-admin all set or clear the session
 * cookie, which a CSIL op cannot do, so each has a dedicated endpoint in
 * `~/api/auth.ts`. What remains callable here is the read-only part.
 */
export const authApi = new ReactorcideAuthAsyncClient(transport)
