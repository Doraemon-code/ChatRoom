import { describe, expect, it } from 'vitest'
import { decodePlaintext, encodePlaintext, imageDataURL } from './message'

const fileAnnouncement = { t: 'file', f: '01890f6e-74e8-4c72-8f9c-61fca85b71e7', n: '报表.pdf', m: 'application/pdf', s: 1048576, c: 1, e: 1750000000000 } as const

describe('plaintext codec', () => {
  it('round trips text and image payloads', () => {
    expect(decodePlaintext(encodePlaintext({ t: 'text', b: '你好' }))).toEqual({ t: 'text', b: '你好' })
    expect(decodePlaintext(encodePlaintext({ t: 'image', m: 'image/jpeg', b: 'AA' }))).toEqual({ t: 'image', m: 'image/jpeg', b: 'AA' })
  })
  it('round trips a file announcement', () => {
    expect(decodePlaintext(encodePlaintext(fileAnnouncement))).toEqual(fileAnnouncement)
  })
  it('drops invalid file announcements instead of rendering them as text', () => {
    const invalid = [
      JSON.stringify({ ...fileAnnouncement, f: 'not-a-uuid' }),
      JSON.stringify({ ...fileAnnouncement, n: '' }),
      JSON.stringify({ ...fileAnnouncement, n: 'x'.repeat(256) }),
      JSON.stringify({ ...fileAnnouncement, n: 'bad\nname' }),
      JSON.stringify({ ...fileAnnouncement, m: 'not a mime' }),
      JSON.stringify({ ...fileAnnouncement, s: -1 }),
      JSON.stringify({ ...fileAnnouncement, s: 1.5 }),
      JSON.stringify({ ...fileAnnouncement, c: 0 }),
      JSON.stringify({ ...fileAnnouncement, e: 0 }),
      JSON.stringify({ t: 'file' }),
    ]
    for (const raw of invalid) expect(() => decodePlaintext(raw)).toThrow('INVALID_FILE_ANNOUNCEMENT')
  })
  it('falls back to text for content that is not our JSON shape', () => {
    expect(decodePlaintext('just text')).toEqual({ t: 'text', b: 'just text' })
    expect(decodePlaintext('{"t":"other","b":"x"}')).toEqual({ t: 'text', b: '{"t":"other","b":"x"}' })
  })
  it('builds a data URL', () => {
    expect(imageDataURL('image/jpeg', 'AA')).toBe('data:image/jpeg;base64,AA')
  })
})
