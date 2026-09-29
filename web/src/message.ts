// The plaintext that gets encrypted for each message. Text stays as-is; images
// carry their MIME type and base64 bytes. Wrapping both in one small JSON shape
// keeps the receiver's rendering decision explicit and authenticated by the AEAD.
export type Plaintext =
  | { t: 'text'; b: string }
  | { t: 'image'; m: string; b: string }

export function encodePlaintext(plaintext: Plaintext): string {
  return JSON.stringify(plaintext)
}

export function decodePlaintext(raw: string): Plaintext {
  try {
    const value: unknown = JSON.parse(raw)
    if (typeof value === 'object' && value !== null) {
      const record = value as Record<string, unknown>
      if (record.t === 'text' && typeof record.b === 'string') return { t: 'text', b: record.b }
      if (record.t === 'image' && typeof record.b === 'string' && typeof record.m === 'string') return { t: 'image', m: record.m, b: record.b }
    }
  } catch { /* not our JSON shape */ }
  return { t: 'text', b: raw }
}

export function imageDataURL(mime: string, base64: string): string {
  return `data:${mime};base64,${base64}`
}
