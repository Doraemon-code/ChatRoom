export interface ForumPost {
  name: string
  message: string
  replies: ForumReply[]
}

export interface ForumReply {
  name: string
  message: string
}

export const MAX_FORUM_MESSAGE_LENGTH = 2000

export function forumPostsURL(location: Pick<Location, 'origin'>): string {
  const base = import.meta.env.BASE_URL.endsWith('/') ? import.meta.env.BASE_URL : `${import.meta.env.BASE_URL}/`
  return `${location.origin}${base}api/board/posts`
}

export function forumRepliesURL(location: Pick<Location, 'origin'>, index: number): string {
  return `${forumPostsURL(location)}/${index}/replies`
}

export function normalizeForumMessage(value: string): string {
  const normalized = value.normalize('NFC').trim()
  const length = [...normalized].length
  if (length === 0 || length > MAX_FORUM_MESSAGE_LENGTH) throw new Error('INVALID_FORUM_MESSAGE')
  for (const character of normalized) {
    if (/[^\n\r\t\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(character)) continue
    if (character !== '\n' && character !== '\r' && character !== '\t') throw new Error('INVALID_FORUM_MESSAGE')
  }
  return normalized
}

export function parseForumPosts(value: unknown): ForumPost[] {
  if (!Array.isArray(value)) throw new Error('INVALID_FORUM_RESPONSE')
  return value.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) throw new Error('INVALID_FORUM_RESPONSE')
    const record = item as Record<string, unknown>
    const keys = Object.keys(record).sort()
    if (keys.length !== 3 || keys[0] !== 'message' || keys[1] !== 'name' || keys[2] !== 'replies' || typeof record.name !== 'string' || typeof record.message !== 'string' || !Array.isArray(record.replies)) throw new Error('INVALID_FORUM_RESPONSE')
    return { name: record.name, message: record.message, replies: parseForumReplies(record.replies) }
  })
}

export function parseForumReply(value: unknown): ForumReply {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('INVALID_FORUM_RESPONSE')
  const record = value as Record<string, unknown>
  const keys = Object.keys(record).sort()
  if (keys.length !== 2 || keys[0] !== 'message' || keys[1] !== 'name' || typeof record.name !== 'string' || typeof record.message !== 'string') throw new Error('INVALID_FORUM_RESPONSE')
  return { name: record.name, message: record.message }
}

function parseForumReplies(value: unknown): ForumReply[] {
  if (!Array.isArray(value)) throw new Error('INVALID_FORUM_RESPONSE')
  return value.map(parseForumReply)
}
