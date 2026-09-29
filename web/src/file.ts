import { concat } from './crypto/encoding'

// Client-side framing for the RAM-only file channel. The relay stores and
// replays these bytes without reading them; only the endpoints know the shape:
// [12-byte IV][4-byte big-endian ciphertext length][AES-GCM ciphertext].
export const CHUNK_PLAINTEXT_SIZE = 1 << 20
export const FILE_TTL_MS = 300_000
const IV_SIZE = 12
const LENGTH_SIZE = 4
export const FRAME_HEADER_SIZE = IV_SIZE + LENGTH_SIZE
const GCM_TAG_SIZE = 16
const MAX_CIPHERTEXT = CHUNK_PLAINTEXT_SIZE + GCM_TAG_SIZE

export function chunkCountFor(size: number): number {
  if (!Number.isSafeInteger(size) || size < 0) throw new Error('INVALID_FILE_SIZE')
  return Math.max(1, Math.ceil(size / CHUNK_PLAINTEXT_SIZE))
}

export function encodeFrame(iv: Uint8Array, ciphertext: Uint8Array): Uint8Array<ArrayBuffer> {
  if (iv.length !== IV_SIZE) throw new Error('INVALID_IV')
  if (ciphertext.length < GCM_TAG_SIZE || ciphertext.length > MAX_CIPHERTEXT) throw new Error('INVALID_CIPHERTEXT')
  const length = new Uint8Array(LENGTH_SIZE)
  new DataView(length.buffer).setUint32(0, ciphertext.length, false)
  return concat(iv, length, ciphertext)
}

export interface Frame {
  iv: Uint8Array
  ciphertext: Uint8Array
}

// FrameReader incrementally parses a download stream, yielding complete frames
// as soon as their bytes arrive. It rejects absurd lengths immediately (before
// the rest of the frame downloads) and half-frames at stream end.
export class FrameReader {
  #pending: Uint8Array | null = null
  #offset = 0

  push(chunk: Uint8Array): Frame[] {
    const base = this.#pending === null || this.#offset === this.#pending.length ? chunk : concat(this.#pending.subarray(this.#offset), chunk)
    this.#pending = base
    this.#offset = 0
    const frames: Frame[] = []
    while (base.length - this.#offset >= FRAME_HEADER_SIZE) {
      const view = new DataView(base.buffer, base.byteOffset + this.#offset)
      const length = view.getUint32(IV_SIZE, false)
      if (length < GCM_TAG_SIZE || length > MAX_CIPHERTEXT) throw new Error('INVALID_FRAME')
      const end = this.#offset + FRAME_HEADER_SIZE + length
      if (base.length < end) break
      const start = this.#offset
      frames.push({ iv: base.slice(start, start + IV_SIZE), ciphertext: base.slice(start + FRAME_HEADER_SIZE, end) })
      this.#offset = end
    }
    return frames
  }

  // Throws if the stream ended mid-frame; resets the retained buffer either
  // way so no ciphertext slice outlives the download.
  finish(): void {
    if (this.#pending !== null && this.#pending.length !== this.#offset) throw new Error('TRUNCATED_FILE')
    this.#pending = null
    this.#offset = 0
  }
}

export function formatBytes(size: number): string {
  if (!Number.isSafeInteger(size) || size < 0) throw new Error('INVALID_FILE_SIZE')
  if (size < 1024) return `${size} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = size
  let unit = -1
  do {
    value /= 1024
    unit += 1
  } while (value >= 1024 && unit < units.length - 1)
  return `${value >= 100 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`
}

export function formatCountdown(remainingMs: number): string {
  const total = Math.max(0, Math.floor(remainingMs / 1000))
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
}
