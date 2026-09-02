import { RpcRequest, RpcResponse } from './transport/rpc.ts'
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
    return decoded.payload
  }
}

/**
 * Decodes the ServiceError arm.
 *
 * Hand-decoded rather than routed through the generated codec because the
 * generated per-op decoders each expect their own success type; the error arm
 * is one shared shape ({code, message}) across every operation. Keeping this
 * here means one small decoder instead of a special case in ninety call sites.
 */
function decodeServiceError(payload: Uint8Array): ServiceError {
  try {
    const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength)
    const fields = decodeSmallTextMap(payload, view)
    return new ServiceError(fields.code ?? 'unknown', fields.message ?? 'The request failed.')
  } catch {
    return new ServiceError('unknown', 'The request failed.')
  }
}

/**
 * A minimal CBOR reader for a flat map of short text keys to text values.
 *
 * Deliberately narrow: it decodes exactly the ServiceError shape and nothing
 * else. A general decoder is already vendored for real payloads; this exists so
 * the error path cannot itself throw something unhelpful.
 */
function decodeSmallTextMap(bytes: Uint8Array, view: DataView): Record<string, string> {
  let offset = 0
  const readHead = (): [number, number] => {
    const initial = bytes[offset++]
    const major = initial >> 5
    const low = initial & 0x1f
    if (low < 24) return [major, low]
    if (low === 24) return [major, bytes[offset++]]
    if (low === 25) {
      const value = view.getUint16(offset)
      offset += 2
      return [major, value]
    }
    throw new Error('unsupported CBOR head')
  }
  const readText = (length: number): string => {
    const text = new TextDecoder().decode(bytes.subarray(offset, offset + length))
    offset += length
    return text
  }

  const [major, count] = readHead()
  if (major !== 5) throw new Error('not a map')
  const out: Record<string, string> = {}
  for (let i = 0; i < count; i++) {
    const [keyMajor, keyLength] = readHead()
    if (keyMajor !== 3) throw new Error('non-text key')
    const key = readText(keyLength)
    const [valueMajor, valueLength] = readHead()
    if (valueMajor !== 3) throw new Error('non-text value')
    out[key] = readText(valueLength)
  }
  return out
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
