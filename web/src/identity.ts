import { NOVEL_CHARACTER_NAMES } from './data/novelCharacters'

export interface Identity { ephemeralId: string; nickname: string }

// Rooms are fixed and pre-existing; there is no room creation.
export const FIXED_ROOM_IDS = ['0001', '0002', '0003', '0004', '0005', '0006', '0007', '0008'] as const

export function createIdentity(): Identity {
  return { ephemeralId: crypto.randomUUID(), nickname: NOVEL_CHARACTER_NAMES[randomIndex(NOVEL_CHARACTER_NAMES.length)] }
}

function randomIndex(length: number): number {
  const range = 0x1_0000_0000
  const limit = Math.floor(range / length) * length
  let value: number
  do { value = crypto.getRandomValues(new Uint32Array(1))[0] } while (value >= limit)
  return value % length
}

export function normalizeNickname(value: string): string {
  const normalized = value.normalize('NFC').trim()
  const length = [...normalized].length
  if (length < 2 || length > 24 || /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(normalized)) throw new Error('INVALID_NICKNAME')
  return normalized
}
