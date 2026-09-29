import type { CiphertextEnvelope } from './crypto/roomCrypto'

export interface PresenceMember { ephemeralId: string; nickname: string }
export interface JoinRequest { v: 1; type: 'join_request'; requestId: string; ephemeralId: string; nickname: string; publicKey: string }
export type ServerMessage =
  | { v: 1; type: 'joined' }
  | { v: 1; type: 'error'; code: string }
  | { v: 1; type: 'join_pending' }
  | JoinRequest
  | { v: 1; type: 'join_approved'; requestId: string; ownerPublicKey: string; iv: string; wrappedRoomKey: string }
  | { v: 1; type: 'presence'; ownerEphemeralId: string; members: PresenceMember[] }
  | CiphertextEnvelope

function record(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value) }
function strings(value: unknown, names: string[]): value is Record<string, string> { return record(value) && names.every((name) => typeof value[name] === 'string') }

export function parseServerMessage(raw: string): ServerMessage {
  const value: unknown = JSON.parse(raw)
  if (!record(value) || value.v !== 1 || typeof value.type !== 'string') throw new Error('INVALID_SERVER_MESSAGE')
  switch (value.type) {
    case 'joined': case 'join_pending': return value as ServerMessage
    case 'error': if (strings(value, ['code'])) return { v: 1, type: 'error', code: value.code }; break
    case 'ciphertext': if (strings(value, ['roomId', 'senderEphemeralId', 'messageId', 'iv', 'ciphertext'])) return { v: 1, type: 'ciphertext', roomId: value.roomId, senderEphemeralId: value.senderEphemeralId, messageId: value.messageId, iv: value.iv, ciphertext: value.ciphertext }; break
    case 'join_request': if (strings(value, ['requestId', 'ephemeralId', 'nickname', 'publicKey'])) return { v: 1, type: 'join_request', requestId: value.requestId, ephemeralId: value.ephemeralId, nickname: value.nickname, publicKey: value.publicKey }; break
    case 'join_approved': if (strings(value, ['requestId', 'ownerPublicKey', 'iv', 'wrappedRoomKey'])) return { v: 1, type: 'join_approved', requestId: value.requestId, ownerPublicKey: value.ownerPublicKey, iv: value.iv, wrappedRoomKey: value.wrappedRoomKey }; break
    case 'presence':
      if (typeof value.ownerEphemeralId === 'string' && Array.isArray(value.members) && value.members.every((member) => strings(member, ['ephemeralId', 'nickname']))) return { v: 1, type: 'presence', ownerEphemeralId: value.ownerEphemeralId, members: value.members.map((member) => ({ ephemeralId: String(member.ephemeralId), nickname: String(member.nickname) })) }
      break
  }
  throw new Error('INVALID_SERVER_MESSAGE')
}

export function websocketURL(location: Pick<Location, 'protocol' | 'host'>): string {
  const base = import.meta.env.BASE_URL.endsWith('/') ? import.meta.env.BASE_URL : `${import.meta.env.BASE_URL}/`
  return `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}${base}ws`
}
