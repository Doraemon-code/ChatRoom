import { describe, expect, it } from 'vitest'
import { CHUNK_PLAINTEXT_SIZE, FrameReader, chunkCountFor, encodeFrame, formatBytes, formatCountdown, FRAME_HEADER_SIZE } from './file'

function frame(body: Uint8Array): Uint8Array {
  return encodeFrame(crypto.getRandomValues(new Uint8Array(12)), concatTag(body))
}

// GCM appends a 16-byte tag to the body; any body ≥ 0 bytes is a legal frame.
function concatTag(body: Uint8Array): Uint8Array {
  const out = new Uint8Array(body.length + 16)
  out.set(body)
  return out
}

describe('file framing', () => {
  it('counts chunks for any size without a ceiling', () => {
    expect(chunkCountFor(0)).toBe(1)
    expect(chunkCountFor(1)).toBe(1)
    expect(chunkCountFor(CHUNK_PLAINTEXT_SIZE)).toBe(1)
    expect(chunkCountFor(CHUNK_PLAINTEXT_SIZE + 1)).toBe(2)
    expect(chunkCountFor(CHUNK_PLAINTEXT_SIZE * 4096)).toBe(4096)
    expect(() => chunkCountFor(-1)).toThrow('INVALID_FILE_SIZE')
    expect(() => chunkCountFor(1.5)).toThrow('INVALID_FILE_SIZE')
  })

  it('encodes and re-parses frames across arbitrary chunk splits', () => {
    const first = frame(new Uint8Array(64).fill(0xAB))
    const second = frame(new Uint8Array(0))
    const stream = new Uint8Array(first.length + second.length)
    stream.set(first)
    stream.set(second, first.length)

    const reader = new FrameReader()
    const whole = reader.push(stream)
    expect(whole).toHaveLength(2)
    expect(whole[0].ciphertext).toHaveLength(64 + 16)
    expect(whole[1].ciphertext).toHaveLength(16)
    reader.finish()
    expect(reader.push(new Uint8Array(0))).toStrictEqual([])

    // Byte-dribbled delivery yields the same frames.
    const dribbled = new FrameReader()
    const collected = []
    for (let i = 0; i < stream.length; i += 7) collected.push(...dribbled.push(stream.subarray(i, i + 7)))
    dribbled.finish()
    expect(collected).toHaveLength(2)
    expect(Array.from(collected[0].iv)).toStrictEqual(Array.from(whole[0].iv))
    expect(Array.from(collected[1].ciphertext)).toStrictEqual(Array.from(whole[1].ciphertext))
  })

  it('rejects absurd lengths and truncation', () => {
    const reader = new FrameReader()
    const header = new Uint8Array(FRAME_HEADER_SIZE)
    new DataView(header.buffer).setUint32(12, 0x7fff_ffff, false)
    expect(() => reader.push(header)).toThrow('INVALID_FRAME')

    const truncated = new FrameReader()
    truncated.push(frame(new Uint8Array(32)).subarray(0, 20))
    expect(() => truncated.finish()).toThrow('TRUNCATED_FILE')
  })

  it('formats sizes and countdowns for display', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(CHUNK_PLAINTEXT_SIZE)).toBe('1 MB')
    expect(formatBytes(3 * 1024 * 1024 + 512 * 1024)).toBe('3.5 MB')
    expect(formatBytes(5 * 1024 ** 4)).toBe('5 TB')
    expect(() => formatBytes(-1)).toThrow('INVALID_FILE_SIZE')
    expect(formatCountdown(300_000)).toBe('05:00')
    expect(formatCountdown(0)).toBe('00:00')
    expect(formatCountdown(-5_000)).toBe('00:00')
    expect(formatCountdown(59_999)).toBe('00:59')
    expect(formatCountdown(61_000)).toBe('01:01')
  })
})
