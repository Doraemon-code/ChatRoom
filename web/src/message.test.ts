import { describe, expect, it } from 'vitest'
import { decodePlaintext, encodePlaintext, imageDataURL } from './message'

describe('plaintext codec', () => {
  it('round trips text and image payloads', () => {
    expect(decodePlaintext(encodePlaintext({ t: 'text', b: '你好' }))).toEqual({ t: 'text', b: '你好' })
    expect(decodePlaintext(encodePlaintext({ t: 'image', m: 'image/jpeg', b: 'AA' }))).toEqual({ t: 'image', m: 'image/jpeg', b: 'AA' })
  })
  it('falls back to text for content that is not our JSON shape', () => {
    expect(decodePlaintext('just text')).toEqual({ t: 'text', b: 'just text' })
    expect(decodePlaintext('{"t":"other","b":"x"}')).toEqual({ t: 'text', b: '{"t":"other","b":"x"}' })
  })
  it('builds a data URL', () => {
    expect(imageDataURL('image/jpeg', 'AA')).toBe('data:image/jpeg;base64,AA')
  })
})
