import { describe, expect, it } from 'vitest'
import { MAX_FORUM_MESSAGE_LENGTH, normalizeForumMessage, parseForumPosts } from './forum'

describe('public forum data boundary', () => {
  it('accepts messages without creating metadata fields', () => {
    expect(parseForumPosts([{ name: '匿名甲', message: '这是一条吐槽', replies: [] }])).toEqual([{ name: '匿名甲', message: '这是一条吐槽', replies: [] }])
  })

  it('rejects timestamp, id, and other response fields', () => {
    expect(() => parseForumPosts([{ name: '匿名甲', message: '吐槽', replies: [], publishedAt: 'now' }])).toThrow('INVALID_FORUM_RESPONSE')
  })

  it('normalizes and bounds messages', () => {
    expect(normalizeForumMessage('  有点拥挤\n希望改善  ')).toBe('有点拥挤\n希望改善')
    expect(() => normalizeForumMessage('x'.repeat(MAX_FORUM_MESSAGE_LENGTH + 1))).toThrow('INVALID_FORUM_MESSAGE')
    expect(() => normalizeForumMessage('bad\u0000text')).toThrow('INVALID_FORUM_MESSAGE')
  })
})
