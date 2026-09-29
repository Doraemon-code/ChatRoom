import { asArrayBuffer, concat, fromBase64Url, toBase64Url } from './encoding'

export const MAX_PLAINTEXT_BYTES = 1 << 20
const textEncoder = new TextEncoder()

export interface CiphertextEnvelope {
  v: 1
  type: 'ciphertext'
  roomId: string
  senderEphemeralId: string
  messageId: string
  iv: string
  ciphertext: string
}

export interface RoomMaterial { rootKey: CryptoKey; secret: string }
export interface WrappedRoomKey { ownerPublicKey: string; iv: string; wrappedRoomKey: string }

export async function generateRoomMaterial(): Promise<RoomMaterial> {
  const raw = crypto.getRandomValues(new Uint8Array(32))
  const secret = toBase64Url(raw)
  return { rootKey: await importRoomKey(secret), secret }
}

export async function importRoomKey(secret: string): Promise<CryptoKey> {
  const raw = fromBase64Url(secret)
  if (raw.length !== 32) throw new Error('INVALID_ROOM_SECRET')
  return crypto.subtle.importKey('raw', asArrayBuffer(raw), 'HKDF', false, ['deriveKey'])
}

export async function generateAdmissionKeyPair(): Promise<CryptoKeyPair> {
  return crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, false, ['deriveBits'])
}

export async function exportAdmissionPublicKey(keys: CryptoKeyPair): Promise<string> {
  return toBase64Url(await crypto.subtle.exportKey('raw', keys.publicKey))
}

async function deriveWrappingKey(privateKey: CryptoKey, peerPublicEncoded: string, roomId: string, requestId: string): Promise<CryptoKey> {
  const raw = fromBase64Url(peerPublicEncoded)
  if (raw.length !== 65 || raw[0] !== 4) throw new Error('INVALID_ADMISSION_PUBLIC_KEY')
  const publicKey = await crypto.subtle.importKey('raw', asArrayBuffer(raw), { name: 'ECDH', namedCurve: 'P-256' }, false, [])
  const shared = await crypto.subtle.deriveBits({ name: 'ECDH', public: publicKey }, privateKey, 256)
  const material = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey'])
  return crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: textEncoder.encode(roomId), info: textEncoder.encode(`lan-chat-room-wrap-v1\n${requestId}`) },
    material,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt']
  )
}

function wrappingAAD(roomId: string, requestId: string): Uint8Array {
  return textEncoder.encode(`lan-chat-room-wrap-v1\n${roomId}\n${requestId}`)
}

export async function wrapRoomKey(secret: string, requesterPublicKey: string, ownerKeys: CryptoKeyPair, roomId: string, requestId: string): Promise<WrappedRoomKey> {
  const rawRoomKey = fromBase64Url(secret)
  if (rawRoomKey.length !== 32) throw new Error('INVALID_ROOM_SECRET')
  const key = await deriveWrappingKey(ownerKeys.privateKey, requesterPublicKey, roomId, requestId)
  const iv = crypto.getRandomValues(new Uint8Array(12))
  const wrapped = await crypto.subtle.encrypt({ name: 'AES-GCM', iv: asArrayBuffer(iv), additionalData: asArrayBuffer(wrappingAAD(roomId, requestId)), tagLength: 128 }, key, asArrayBuffer(rawRoomKey))
  return { ownerPublicKey: await exportAdmissionPublicKey(ownerKeys), iv: toBase64Url(iv), wrappedRoomKey: toBase64Url(wrapped) }
}

export async function unwrapRoomKey(envelope: WrappedRoomKey, requesterKeys: CryptoKeyPair, roomId: string, requestId: string): Promise<RoomMaterial> {
  const iv = fromBase64Url(envelope.iv)
  const wrapped = fromBase64Url(envelope.wrappedRoomKey)
  if (iv.length !== 12 || wrapped.length !== 48) throw new Error('INVALID_WRAPPED_ROOM_KEY')
  const key = await deriveWrappingKey(requesterKeys.privateKey, envelope.ownerPublicKey, roomId, requestId)
  const raw = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: asArrayBuffer(iv), additionalData: asArrayBuffer(wrappingAAD(roomId, requestId)), tagLength: 128 }, key, asArrayBuffer(wrapped)))
  if (raw.length !== 32) throw new Error('INVALID_ROOM_SECRET')
  const secret = toBase64Url(raw)
  return { secret, rootKey: await importRoomKey(secret) }
}

export async function deriveSenderKey(rootKey: CryptoKey, roomId: string, senderEphemeralId: string): Promise<CryptoKey> {
  return crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: textEncoder.encode(roomId), info: textEncoder.encode(`lan-chat-message-v1\n${senderEphemeralId}`) },
    rootKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt']
  )
}

function lengthPrefix(value: string): Uint8Array {
  const encoded = textEncoder.encode(value)
  const length = new Uint8Array(4)
  new DataView(length.buffer).setUint32(0, encoded.length, false)
  return concat(length, encoded)
}

export function aadFor(envelope: Pick<CiphertextEnvelope, 'v' | 'type' | 'roomId' | 'senderEphemeralId' | 'messageId'>): Uint8Array {
  return concat(new Uint8Array([envelope.v]), lengthPrefix(envelope.type), lengthPrefix(envelope.roomId), lengthPrefix(envelope.senderEphemeralId), lengthPrefix(envelope.messageId))
}

export class NonceSequence {
  readonly #prefix: Uint8Array
  #counter = 0
  constructor(prefix = crypto.getRandomValues(new Uint8Array(8)), initialCounter = 0) {
    if (prefix.length !== 8) throw new Error('INVALID_NONCE_PREFIX')
    if (!Number.isSafeInteger(initialCounter) || initialCounter < 0 || initialCounter > 0x1_0000_0000) throw new Error('INVALID_NONCE_COUNTER')
    this.#prefix = new Uint8Array(prefix)
    this.#counter = initialCounter
  }
  next(): Uint8Array {
    if (this.#counter > 0xffff_ffff) throw new Error('NONCE_EXHAUSTED')
    const iv = new Uint8Array(12)
    iv.set(this.#prefix)
    new DataView(iv.buffer).setUint32(8, this.#counter, false)
    this.#counter += 1
    return iv
  }
}

export async function encryptMessage(key: CryptoKey, roomId: string, senderEphemeralId: string, plaintext: string, nonces: NonceSequence): Promise<CiphertextEnvelope> {
  const bytes = textEncoder.encode(plaintext)
  if (bytes.length === 0 || bytes.length > MAX_PLAINTEXT_BYTES) throw new Error('INVALID_PLAINTEXT_SIZE')
  const envelope = { v: 1 as const, type: 'ciphertext' as const, roomId, senderEphemeralId, messageId: crypto.randomUUID() }
  const iv = nonces.next()
  const ciphertext = await crypto.subtle.encrypt({ name: 'AES-GCM', iv: asArrayBuffer(iv), additionalData: asArrayBuffer(aadFor(envelope)), tagLength: 128 }, key, asArrayBuffer(bytes))
  return { ...envelope, iv: toBase64Url(iv), ciphertext: toBase64Url(ciphertext) }
}

export async function decryptMessage(key: CryptoKey, envelope: CiphertextEnvelope): Promise<string> {
  const iv = fromBase64Url(envelope.iv)
  if (iv.length !== 12) throw new Error('INVALID_IV')
  const plaintext = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: asArrayBuffer(iv), additionalData: asArrayBuffer(aadFor(envelope)), tagLength: 128 }, key, asArrayBuffer(fromBase64Url(envelope.ciphertext)))
  return new TextDecoder('utf-8', { fatal: true }).decode(plaintext)
}

export class ReplayGuard {
  readonly #seen = new Set<string>()
  readonly #order: string[] = []
  constructor(readonly capacity = 4096) {}
  accept(senderId: string, messageId: string): boolean {
    const id = `${senderId}:${messageId}`
    if (this.#seen.has(id)) return false
    this.#seen.add(id); this.#order.push(id)
    if (this.#order.length > this.capacity) { const oldest = this.#order.shift(); if (oldest) this.#seen.delete(oldest) }
    return true
  }
  clear(): void { this.#seen.clear(); this.#order.length = 0 }
}
