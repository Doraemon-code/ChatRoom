// The plaintext that gets encrypted for each message. Text stays as-is; images
// carry their MIME type and base64 bytes; files announce the metadata of a
// chunk-encrypted upload (the bytes travel over the HTTP file channel, not the
// message envelope). Wrapping these in one small JSON shape keeps the
// receiver's rendering decision explicit and authenticated by the AEAD.
export type Plaintext =
  | { t: 'text'; b: string }
  | { t: 'image'; m: string; b: string }
  | { t: 'file'; f: string; n: string; m: string; s: number; c: number; e: number }

export function encodePlaintext(plaintext: Plaintext): string {
  return JSON.stringify(plaintext)
}

const FILE_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u
const MIME_PATTERN = /^[A-Za-z0-9!#$%&'*+.^_-]+\/[A-Za-z0-9!#$%&'*+.^_-]+$/u

// A file announcement is only ever produced by our own sender, so a malformed
// one means the ciphertext was tampered with or came from a hostile "client":
// reject it outright instead of rendering anything.
function decodeFileAnnouncement(record: Record<string, unknown>): Plaintext {
  const { f, n, m, s, c, e } = record
  if (
    typeof f !== 'string' || !FILE_ID_PATTERN.test(f) ||
    typeof n !== 'string' || n.length < 1 || n.length > 255 || [...n].some((ch) => ch.charCodeAt(0) < 0x20 || ch.charCodeAt(0) === 0x7f) ||
    typeof m !== 'string' || !MIME_PATTERN.test(m) ||
    typeof s !== 'number' || !Number.isSafeInteger(s) || s < 0 ||
    typeof c !== 'number' || !Number.isSafeInteger(c) || c < 1 ||
    typeof e !== 'number' || !Number.isSafeInteger(e) || e <= 0
  ) throw new Error('INVALID_FILE_ANNOUNCEMENT')
  return { t: 'file', f, n, m, s, c, e }
}

export function decodePlaintext(raw: string): Plaintext {
  try {
    const value: unknown = JSON.parse(raw)
    if (typeof value === 'object' && value !== null) {
      const record = value as Record<string, unknown>
      if (record.t === 'file') return decodeFileAnnouncement(record)
      if (record.t === 'text' && typeof record.b === 'string') return { t: 'text', b: record.b }
      if (record.t === 'image' && typeof record.b === 'string' && typeof record.m === 'string') return { t: 'image', m: record.m, b: record.b }
    }
  } catch (error) {
    // Invalid file announcements must be dropped by the caller, not folded
    // back into a text bubble.
    if (error instanceof Error && error.message === 'INVALID_FILE_ANNOUNCEMENT') throw error
  }
  return { t: 'text', b: raw }
}

export function imageDataURL(mime: string, base64: string): string {
  return `data:${mime};base64,${base64}`
}
