import { describe, expect, it } from 'vitest'
import { MAX_PLAINTEXT_BYTES, NonceSequence, ReplayGuard, decryptMessage, deriveSenderKey, encryptMessage, generateRoomMaterial } from './roomCrypto'

const roomId = '0007'
const senderId = '01890f6e-74e8-4c72-8f9c-61fca85b71e7'

describe('room crypto', () => {
  it('round trips and authenticates metadata', async () => {
    const { rootKey } = await generateRoomMaterial()
    const key = await deriveSenderKey(rootKey, roomId, senderId)
    const envelope = await encryptMessage(key, roomId, senderId, 'Hello', new NonceSequence(new Uint8Array(8)))
    await expect(decryptMessage(key, envelope)).resolves.toBe('Hello')
    await expect(decryptMessage(key, { ...envelope, roomId: '0008' })).rejects.toThrow()
    expect(JSON.stringify(envelope)).not.toContain('Hello')
  })

  it('carries an image-sized payload and rejects one past the limit', async () => {
    const { rootKey } = await generateRoomMaterial()
    const key = await deriveSenderKey(rootKey, roomId, senderId)
    const image = JSON.stringify({ t: 'image', m: 'image/jpeg', b: 'A'.repeat(600_000) })
    const envelope = await encryptMessage(key, roomId, senderId, image, new NonceSequence(new Uint8Array(8)))
    await expect(decryptMessage(key, envelope)).resolves.toBe(image)
    await expect(encryptMessage(key, roomId, senderId, 'a'.repeat(MAX_PLAINTEXT_BYTES + 1), new NonceSequence(new Uint8Array(8)))).rejects.toThrow('INVALID_PLAINTEXT_SIZE')
  })

  it('does not reuse an IV in a sender sequence', () => {
    const sequence = new NonceSequence(new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8]))
    const seen = new Set<string>()
    for (let index = 0; index < 10_000; index += 1) {
      const value = Array.from(sequence.next()).join(',')
      expect(seen.has(value)).toBe(false)
      seen.add(value)
    }
  })

  it('fails closed instead of wrapping an exhausted nonce counter', () => {
    const sequence = new NonceSequence(new Uint8Array(8), 0xffff_ffff)
    expect(sequence.next()).toHaveLength(12)
    expect(() => sequence.next()).toThrow('NONCE_EXHAUSTED')
  })

  it('rejects replays within bounded runtime memory', () => {
    const guard = new ReplayGuard(2)
    expect(guard.accept('a', '1')).toBe(true)
    expect(guard.accept('a', '1')).toBe(false)
    expect(guard.accept('a', '2')).toBe(true)
    expect(guard.accept('a', '3')).toBe(true)
    expect(guard.accept('a', '1')).toBe(true)
  })
})
