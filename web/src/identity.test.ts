import { describe, expect, it } from 'vitest'
import { createIdentity, FIXED_ROOM_IDS, normalizeNickname } from './identity'
import { NOVEL_CHARACTER_NAMES } from './data/novelCharacters'

describe('runtime identity', () => {
  it('generates fresh canonical identities', () => {
    const first = createIdentity(), second = createIdentity()
    expect(first).not.toEqual(second)
    expect(NOVEL_CHARACTER_NAMES).toContain(first.nickname)
    expect(first.ephemeralId).toMatch(/^[0-9a-f-]{36}$/u)
  })
  it('contains exactly 3,544 unique classic-novel aliases', () => {
    expect(NOVEL_CHARACTER_NAMES).toHaveLength(3544)
    expect(new Set(NOVEL_CHARACTER_NAMES).size).toBe(3544)
    for (const name of NOVEL_CHARACTER_NAMES) expect(normalizeNickname(name)).toBe(name)
  })
  it('normalizes a manually edited in-memory name and rejects unsafe values', () => {
    expect(normalizeNickname('  福尔摩斯  ')).toBe('福尔摩斯')
    expect(() => normalizeNickname('A')).toThrow('INVALID_NICKNAME')
    expect(() => normalizeNickname('Alice\nBob')).toThrow('INVALID_NICKNAME')
  })
  it('exposes exactly the eight fixed rooms', () => {
    expect(FIXED_ROOM_IDS).toEqual(['0001', '0002', '0003', '0004', '0005', '0006', '0007', '0008'])
    for (const id of FIXED_ROOM_IDS) expect(id).toMatch(/^000[1-8]$/u)
  })
})
