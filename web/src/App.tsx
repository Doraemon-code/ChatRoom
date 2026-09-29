import { type FormEvent, useEffect, useRef, useState } from 'react'
import { createIdentity, FIXED_ROOM_IDS, normalizeNickname, type Identity } from './identity'
import { decryptChunk, decryptMessage, deriveFileKey, deriveSenderKey, encryptChunk, encryptMessage, exportAdmissionPublicKey, generateAdmissionKeyPair, generateRoomMaterial, NonceSequence, ReplayGuard, unwrapRoomKey, wrapRoomKey, type CiphertextEnvelope } from './crypto/roomCrypto'
import { decodePlaintext, encodePlaintext, imageDataURL } from './message'
import { encodeImageFile, MAX_IMAGE_BYTES } from './image'
import { CHUNK_PLAINTEXT_SIZE, FILE_TTL_MS, FrameReader, chunkCountFor, encodeFrame, formatBytes, formatCountdown } from './file'
import { forumPostsURL, forumRepliesURL, normalizeForumMessage, parseForumPosts, parseForumReply, type ForumPost } from './forum'
import { downloadURL, parseServerMessage, uploadURL, websocketURL, type JoinRequest, type PresenceMember, type ServerMessage } from './protocol'

type View = 'home' | 'forum' | 'connecting' | 'chat'
type MessageKind = 'text' | 'image' | 'file'
interface FileCardBody { fileId: string; name: string; mime: string; size: number; chunkCount: number; expiresAt: number; expired: boolean; senderId: string }
interface ChatMessage { id: string; senderId: string; nickname: string; kind: MessageKind; body: string; own: boolean; file?: FileCardBody }
interface TokenWaiter { resolve: (token: string) => void; reject: (error: Error) => void }

const MIME_PATTERN = /^[A-Za-z0-9!#$%&'*+.^_-]+\/[A-Za-z0-9!#$%&'*+.^_-]+$/u
const TOKEN_TIMEOUT_MS = 5_000

// Chromium/Firefox stream the upload body chunk by chunk; older Safari does
// not accept a ReadableStream body and falls back to buffering.
const STREAMING_UPLOAD = (() => {
  try {
    return new Request('https://invalid.example', { method: 'POST', body: new ReadableStream<Uint8Array>(), duplex: 'half' } as RequestInit & { duplex: 'half' }).body instanceof ReadableStream
  } catch {
    return false
  }
})()

// The receiver re-derives the announcement fields strictly; mirror that here so
// our own file cards never get dropped on arrival.
function safeFileName(name: string): string {
  const cleaned = [...name].filter((ch) => ch.charCodeAt(0) >= 0x20 && ch.charCodeAt(0) !== 0x7f).join('').trim()
  const characters = [...cleaned]
  if (characters.length === 0) return 'file'
  return characters.length <= 255 ? cleaned : `${characters.slice(0, 250).join('')}…`
}

export default function App() {
  const [identity, setIdentity] = useState<Identity>(createIdentity)
  const [editingName, setEditingName] = useState(false)
  const [nameDraft, setNameDraft] = useState('')
  const [view, setView] = useState<View>('home')
  const [forumName, setForumName] = useState(identity.nickname)
  const [forumDraft, setForumDraft] = useState('')
  const [forumPosts, setForumPosts] = useState<ForumPost[]>([])
  const [forumLoading, setForumLoading] = useState(false)
  const [forumSaving, setForumSaving] = useState(false)
  const [replyingPost, setReplyingPost] = useState<number | null>(null)
  const [replyDraft, setReplyDraft] = useState('')
  const [replySaving, setReplySaving] = useState(false)
  const [roomId, setRoomId] = useState('')
  const [members, setMembers] = useState<PresenceMember[]>([])
  const [ownerEphemeralId, setOwnerEphemeralId] = useState('')
  const [joinRequests, setJoinRequests] = useState<JoinRequest[]>([])
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [draft, setDraft] = useState('')
  const [notice, setNotice] = useState('')
  const [upload, setUpload] = useState<{ name: string; sent: number; total: number } | null>(null)
  const [fileProgress, setFileProgress] = useState<{ fileId: string; received: number } | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const membersRef = useRef<PresenceMember[]>([])
  const rootKeyRef = useRef<CryptoKey | null>(null)
  const roomSecretRef = useRef('')
  const admissionKeysRef = useRef<CryptoKeyPair | null>(null)
  const senderKeysRef = useRef(new Map<string, CryptoKey>())
  const nonceRef = useRef(new NonceSequence())
  const replayRef = useRef(new ReplayGuard())
  const intentionalCloseRef = useRef(false)
  const ownerKeyPromiseRef = useRef<Promise<void> | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const fileSendInputRef = useRef<HTMLInputElement | null>(null)
  const fileTokenWaiterRef = useRef<TokenWaiter | null>(null)
  const uploadAbortRef = useRef<AbortController | null>(null)
  const downloadAbortRef = useRef(new Map<string, AbortController>())

  useEffect(() => () => { intentionalCloseRef.current = true; socketRef.current?.close(1000, '') }, [])

  useEffect(() => {
    if (view !== 'forum') return
    let active = true
    setForumLoading(true)
    void fetch(forumPostsURL(window.location), { cache: 'no-store', headers: { Accept: 'application/json' } })
      .then(async (response) => {
        if (!response.ok) throw new Error('FORUM_LOAD_FAILED')
        return parseForumPosts(await response.json())
      })
      .then((posts) => { if (active) { setForumPosts(posts); setNotice('') } })
      .catch(() => { if (active) setNotice('公共吐槽板暂时无法加载，请稍后重试。') })
      .finally(() => { if (active) setForumLoading(false) })
    return () => { active = false }
  }, [view])

  function reset(reason = '') {
    intentionalCloseRef.current = true
    socketRef.current?.close(1000, '')
    socketRef.current = null; rootKeyRef.current = null; roomSecretRef.current = ''; admissionKeysRef.current = null
    senderKeysRef.current.clear(); replayRef.current.clear(); ownerKeyPromiseRef.current = null
    membersRef.current = []
    fileTokenWaiterRef.current?.reject(new Error('RESET'))
    fileTokenWaiterRef.current = null
    uploadAbortRef.current?.abort()
    uploadAbortRef.current = null
    for (const controller of downloadAbortRef.current.values()) controller.abort()
    downloadAbortRef.current.clear()
    setMessages([]); setMembers([]); setOwnerEphemeralId(''); setJoinRequests([]); setDraft(''); setRoomId(''); setUpload(null); setFileProgress(null); setView('home'); setNotice(reason)
  }

  function openForum() {
    setForumName(identity.nickname)
    setReplyingPost(null)
    setReplyDraft('')
    setNotice('')
    setView('forum')
  }

  function returnToChat() {
    const socket = socketRef.current
    if (roomId && socket) {
      setView(socket.readyState === WebSocket.OPEN ? 'chat' : 'connecting')
      return
    }
    setView('home')
  }

  // The socket outlives the render that opened it, so its handler must not read
  // render-scoped state: bind the room this socket belongs to as an argument.
  async function openSocket(roomId: string, onOpen: (socket: WebSocket) => void) {
    intentionalCloseRef.current = false
    const socket = new WebSocket(websocketURL(window.location))
    socketRef.current = socket
    socket.addEventListener('open', () => onOpen(socket), { once: true })
    socket.addEventListener('message', (event) => {
      if (typeof event.data !== 'string') return
      void handleServerMessage(socket, roomId, parseServerMessage(event.data)).catch(() => reset('中转服务器返回了无效协议消息。'))
    })
    socket.addEventListener('close', () => { if (!intentionalCloseRef.current) reset('连接已断开，房间可能已经销毁。') })
    socket.addEventListener('error', () => setNotice('无法建立安全的房间连接。'))
  }

  // The owner is the first entrant; it generates the RoomKey once, and reuses
  // it after an owner transfer (remaining members already hold the key).
  function ensureOwnerKey(): Promise<void> {
    if (rootKeyRef.current) return Promise.resolve()
    if (!ownerKeyPromiseRef.current) {
      ownerKeyPromiseRef.current = generateRoomMaterial().then((material) => {
        rootKeyRef.current = material.rootKey; roomSecretRef.current = material.secret
      })
    }
    return ownerKeyPromiseRef.current
  }

  // One outstanding token request at a time: the reply carries no correlation
  // id, so concurrent requests could not be matched to their callers.
  async function requestFileToken(socket: WebSocket): Promise<string> {
    const slotDeadline = Date.now() + TOKEN_TIMEOUT_MS
    while (fileTokenWaiterRef.current) {
      if (Date.now() > slotDeadline) throw new Error('FILE_TOKEN_BUSY')
      await new Promise((resolve) => setTimeout(resolve, 50))
    }
    return new Promise<string>((resolve, reject) => {
      const timer = window.setTimeout(() => {
        if (fileTokenWaiterRef.current === waiter) fileTokenWaiterRef.current = null
        reject(new Error('FILE_TOKEN_TIMEOUT'))
      }, TOKEN_TIMEOUT_MS)
      const waiter: TokenWaiter = {
        resolve: (token) => { window.clearTimeout(timer); fileTokenWaiterRef.current = null; resolve(token) },
        reject: (error) => { window.clearTimeout(timer); fileTokenWaiterRef.current = null; reject(error) },
      }
      fileTokenWaiterRef.current = waiter
      socket.send(JSON.stringify({ v: 1, type: 'file_token_request' }))
    })
  }

  async function handleServerMessage(socket: WebSocket, roomId: string, message: ServerMessage) {
    if (message.type === 'error') { reset(errorText(message.code)); return }
    if (message.type === 'joined') { setView('chat'); setNotice(''); return }
    if (message.type === 'presence') {
      membersRef.current = message.members; setMembers(message.members); setOwnerEphemeralId(message.ownerEphemeralId)
      if (message.ownerEphemeralId !== identity.ephemeralId) setJoinRequests([])
      else if (!rootKeyRef.current) await ensureOwnerKey()
      return
    }
    if (message.type === 'join_pending') { setNotice('正在等待房主确认。'); setView('connecting'); return }
    if (message.type === 'join_request') {
      setJoinRequests((current) => current.some((item) => item.requestId === message.requestId) ? current : [...current, message])
      return
    }
    if (message.type === 'join_approved') {
      const keys = admissionKeysRef.current
      if (!keys) throw new Error('missing admission key')
      const material = await unwrapRoomKey(message, keys, roomId, message.requestId)
      rootKeyRef.current = material.rootKey; roomSecretRef.current = material.secret
      socket.send(JSON.stringify({ v: 1, type: 'admission_complete' }))
      return
    }
    if (message.type === 'file_token') { fileTokenWaiterRef.current?.resolve(message.token); return }
    if (message.type === 'file_expired') {
      // The relay wiped the file; unknown ids (already left, never seen) are
      // harmless no-ops.
      setMessages((current) => current.map((item) => item.kind === 'file' && item.file?.fileId === message.fileId && !item.file.expired ? { ...item, file: { ...item.file, expired: true } } : item))
      return
    }
    if (message.type === 'ciphertext') await receiveCiphertext(message)
  }

  async function receiveCiphertext(envelope: CiphertextEnvelope) {
    if (!replayRef.current.accept(envelope.senderEphemeralId, envelope.messageId)) return
    const rootKey = rootKeyRef.current
    if (!rootKey) return
    try {
      let key = senderKeysRef.current.get(envelope.senderEphemeralId)
      if (!key) { key = await deriveSenderKey(rootKey, envelope.roomId, envelope.senderEphemeralId); senderKeysRef.current.set(envelope.senderEphemeralId, key) }
      const plaintext = decodePlaintext(await decryptMessage(key, envelope))
      const nickname = membersRef.current.find((member) => member.ephemeralId === envelope.senderEphemeralId)?.nickname ?? '未知人物'
      if (plaintext.t === 'file') {
        const file: FileCardBody = { fileId: plaintext.f, name: plaintext.n, mime: plaintext.m, size: plaintext.s, chunkCount: plaintext.c, expiresAt: plaintext.e, expired: plaintext.e <= Date.now(), senderId: envelope.senderEphemeralId }
        setMessages((current) => [...current, { id: envelope.messageId, senderId: envelope.senderEphemeralId, nickname, kind: 'file', body: '', own: false, file }])
      } else {
        const body = plaintext.t === 'image' ? imageDataURL(plaintext.m, plaintext.b) : plaintext.b
        setMessages((current) => [...current, { id: envelope.messageId, senderId: envelope.senderEphemeralId, nickname, kind: plaintext.t, body, own: false }])
      }
    } catch { setNotice('收到一条无法认证的消息，已丢弃。') }
  }

  async function enterRoom(target: string) {
    reset(); setRoomId(target); setView('connecting'); setNotice('正在进入房间……')
    try {
      const keys = await generateAdmissionKeyPair(); admissionKeysRef.current = keys
      const publicKey = await exportAdmissionPublicKey(keys)
      await openSocket(target, (socket) => socket.send(JSON.stringify({ v: 1, type: 'join_room', roomId: target, ephemeralId: identity.ephemeralId, nickname: identity.nickname, publicKey })))
    } catch { reset('进入房间失败，请重试。') }
  }

  async function decideJoin(request: JoinRequest, approved: boolean) {
    const socket = socketRef.current
    if (!socket || socket.readyState !== WebSocket.OPEN) return
    try {
      if (!approved) {
        socket.send(JSON.stringify({ v: 1, type: 'admission_decision', requestId: request.requestId, approved: false }))
      } else {
        if (!roomSecretRef.current) await ensureOwnerKey()
        const secret = roomSecretRef.current
        const keys = admissionKeysRef.current
        if (!secret || !keys) throw new Error('missing owner key')
        const wrapped = await wrapRoomKey(secret, request.publicKey, keys, roomId, request.requestId)
        socket.send(JSON.stringify({ v: 1, type: 'admission_decision', requestId: request.requestId, approved: true, ...wrapped }))
      }
      setJoinRequests((current) => current.filter((item) => item.requestId !== request.requestId))
    } catch { setNotice('处理入房申请失败。') }
  }

  async function encryptFor(plaintext: string): Promise<CiphertextEnvelope> {
    const rootKey = rootKeyRef.current
    if (!rootKey) throw new Error('missing room key')
    let key = senderKeysRef.current.get(identity.ephemeralId)
    if (!key) { key = await deriveSenderKey(rootKey, roomId, identity.ephemeralId); senderKeysRef.current.set(identity.ephemeralId, key) }
    return encryptMessage(key, roomId, identity.ephemeralId, plaintext, nonceRef.current)
  }

  async function sendMessage(event: FormEvent) {
    event.preventDefault()
    const body = draft.trim(), socket = socketRef.current
    if (!body || !rootKeyRef.current || !socket || socket.readyState !== WebSocket.OPEN) return
    try {
      const envelope = await encryptFor(encodePlaintext({ t: 'text', b: body }))
      socket.send(JSON.stringify(envelope))
      setMessages((current) => [...current, { id: envelope.messageId, senderId: identity.ephemeralId, nickname: identity.nickname, kind: 'text', body, own: true }])
      setDraft('')
    } catch { setNotice('消息发送失败，请重试。') }
  }

  async function sendImage(file: File) {
    const socket = socketRef.current
    if (!rootKeyRef.current || !socket || socket.readyState !== WebSocket.OPEN) return
    try {
      const image = await encodeImageFile(file)
      const envelope = await encryptFor(encodePlaintext({ t: 'image', m: image.mime, b: image.base64 }))
      socket.send(JSON.stringify(envelope))
      setMessages((current) => [...current, { id: envelope.messageId, senderId: identity.ephemeralId, nickname: identity.nickname, kind: 'image', body: imageDataURL(image.mime, image.base64), own: true }])
      setNotice('')
    } catch (error) {
      setNotice(error instanceof Error && error.message === 'NOT_IMAGE' ? '请选择图片文件。' : `图片太大或无法处理（上限约 ${Math.round(MAX_IMAGE_BYTES / 1024)}KB）。`)
    }
  }

  // One upload at a time: the announcement is only meaningful once the relay
  // holds the complete ciphertext, and parallel large uploads would multiply
  // sender RAM for the buffered fallback.
  async function sendFile(file: File) {
    const socket = socketRef.current
    const rootKey = rootKeyRef.current
    if (!rootKey || !socket || socket.readyState !== WebSocket.OPEN || uploadAbortRef.current) return
    const fileId = crypto.randomUUID()
    const chunkCount = chunkCountFor(file.size)
    const name = safeFileName(file.name)
    const mime = MIME_PATTERN.test(file.type) ? file.type : 'application/octet-stream'
    const controller = new AbortController()
    uploadAbortRef.current = controller
    setUpload({ name, sent: 0, total: file.size })
    try {
      const key = await deriveFileKey(rootKey, roomId, identity.ephemeralId, fileId)
      // Chunks are read, encrypted (which zeroes the plaintext copy), framed,
      // and handed to the request body one at a time; only the ciphertext
      // frame exists in sender memory, and it is zeroed right after enqueue.
      async function* encryptedFrames(): AsyncGenerator<Uint8Array<ArrayBuffer>> {
        for (let index = 0; index < chunkCount; index += 1) {
          const blob = file.slice(index * CHUNK_PLAINTEXT_SIZE, (index + 1) * CHUNK_PLAINTEXT_SIZE)
          const plaintext = new Uint8Array(await blob.arrayBuffer())
          const { iv, ciphertext } = await encryptChunk(key, fileId, index, chunkCount, plaintext)
          const frame = encodeFrame(iv, ciphertext)
          iv.fill(0); ciphertext.fill(0)
          setUpload((current) => current ? { ...current, sent: Math.min(file.size, (index + 1) * CHUNK_PLAINTEXT_SIZE) } : current)
          yield frame
        }
      }
      // The token is minted right before the fetch: it lives 60 seconds and
      // must not expire while a large file is still being encrypted.
      const token = await requestFileToken(socket)
      let body: BodyInit
      if (STREAMING_UPLOAD) {
        const source = encryptedFrames()
        body = new ReadableStream<Uint8Array>({
          async pull(frameController) {
            const { done, value } = await source.next()
            if (done) frameController.close()
            else frameController.enqueue(value)
          },
          async cancel() { await source.return(undefined) },
        })
      } else {
        const parts: Uint8Array<ArrayBuffer>[] = []
        for await (const frame of encryptedFrames()) parts.push(frame)
        body = new Blob(parts)
      }
      const response = await fetch(uploadURL(window.location, fileId), {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body,
        cache: 'no-store',
        signal: controller.signal,
        ...(STREAMING_UPLOAD ? { duplex: 'half' as const } : {}),
      })
      if (!response.ok) throw new Error(`UPLOAD_FAILED_${response.status}`)
      // The five-minute lifetime starts when the relay received the whole
      // file; computing it after the upload keeps slow transfers from eating
      // into the recipient's window.
      const expiresAt = Date.now() + FILE_TTL_MS
      const envelope = await encryptFor(encodePlaintext({ t: 'file', f: fileId, n: name, m: mime, s: file.size, c: chunkCount, e: expiresAt }))
      socket.send(JSON.stringify(envelope))
      setMessages((current) => [...current, {
        id: envelope.messageId, senderId: identity.ephemeralId, nickname: identity.nickname, kind: 'file', body: '', own: true,
        file: { fileId, name, mime, size: file.size, chunkCount, expiresAt, expired: false, senderId: identity.ephemeralId },
      }])
      setNotice('')
    } catch (error) {
      const code = error instanceof Error ? error.message : ''
      if (code === 'UPLOAD_FAILED_401') setNotice('文件上传凭证无效，请重新发送。')
      else if (code === 'UPLOAD_FAILED_409') setNotice('文件发送出现冲突，请重新发送。')
      else if (code === 'FILE_TOKEN_LIMIT') setNotice('文件传输请求过于频繁，请稍后再试。')
      else if (code === 'FILE_TOKEN_TIMEOUT' || code === 'FILE_TOKEN_BUSY') setNotice('文件传输通道繁忙，请稍后再试。')
      else if (error instanceof DOMException && error.name === 'AbortError') setNotice('已取消文件发送。')
      else setNotice('文件发送失败，请重试。')
    } finally {
      uploadAbortRef.current = null
      setUpload(null)
    }
  }

  async function downloadFile(file: FileCardBody) {
    const socket = socketRef.current
    const rootKey = rootKeyRef.current
    if (file.expired || !rootKey || !socket || socket.readyState !== WebSocket.OPEN || downloadAbortRef.current.has(file.fileId)) return
    const controller = new AbortController()
    downloadAbortRef.current.set(file.fileId, controller)
    setFileProgress({ fileId: file.fileId, received: 0 })
    try {
      const token = await requestFileToken(socket)
      const response = await fetch(downloadURL(window.location, file.fileId), { headers: { Authorization: `Bearer ${token}` }, cache: 'no-store', signal: controller.signal })
      if (response.status === 410 || response.status === 404) {
        setMessages((current) => current.map((item) => item.kind === 'file' && item.file?.fileId === file.fileId ? { ...item, file: { ...item.file, expired: true } } : item))
        throw new Error('FILE_EXPIRED')
      }
      if (!response.ok) throw new Error(`DOWNLOAD_FAILED_${response.status}`)
      const bodyReader = response.body?.getReader()
      if (!bodyReader) throw new Error('DOWNLOAD_FAILED_NO_STREAM')
      const key = await deriveFileKey(rootKey, roomId, file.senderId, file.fileId)
      const reader = new FrameReader()
      const parts: Uint8Array<ArrayBuffer>[] = []
      let index = 0
      let received = 0
      for (;;) {
        const { done, value } = await bodyReader.read()
        if (done) break
        for (const frame of reader.push(value)) {
          const part = await decryptChunk(key, file.fileId, index, file.chunkCount, frame)
          frame.iv.fill(0); frame.ciphertext.fill(0)
          parts.push(part)
          index += 1
        }
        received += value.byteLength
        setFileProgress({ fileId: file.fileId, received })
      }
      reader.finish()
      if (index !== file.chunkCount) throw new Error('DOWNLOAD_TRUNCATED')
      const blob = new Blob(parts, { type: file.mime })
      for (const part of parts) part.fill(0)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url; anchor.download = file.name
      document.body.appendChild(anchor); anchor.click(); anchor.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 1_000)
      setNotice('文件已保存到本地。')
    } catch (error) {
      const code = error instanceof Error ? error.message : ''
      if (code === 'FILE_EXPIRED') setNotice('文件已过期，服务器已删除。')
      else if (error instanceof DOMException && error.name === 'AbortError') setNotice('已取消文件下载。')
      else if (code.startsWith('DOWNLOAD_FAILED')) setNotice('文件下载失败，请重试。')
      else setNotice('文件下载失败：内容未通过校验，已丢弃。')
    } finally {
      downloadAbortRef.current.delete(file.fileId)
      setFileProgress(null)
    }
  }

  function beginRename() { setNameDraft(identity.nickname); setEditingName(true) }
  function saveNickname(event: FormEvent) {
    event.preventDefault()
    try {
      const nickname = normalizeNickname(nameDraft)
      const socket = socketRef.current
      if (roomId && (!socket || socket.readyState !== WebSocket.OPEN)) throw new Error('socket unavailable')
      if (roomId && socket) {
        socket.send(JSON.stringify({ v: 1, type: 'rename', nickname }))
      }
      setIdentity((current) => ({ ...current, nickname })); setForumName(nickname); setEditingName(false); setNotice('名字已更新，仅在本次页面会话中有效。')
    } catch { setNotice('名字必须包含 2–24 个可见字符。') }
  }

  async function submitForumPost(event: FormEvent) {
    event.preventDefault()
    if (forumSaving) return
    try {
      const name = normalizeNickname(forumName)
      const message = normalizeForumMessage(forumDraft)
      setForumSaving(true)
      const response = await fetch(forumPostsURL(window.location), {
        method: 'POST',
        cache: 'no-store',
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, message }),
      })
      if (!response.ok) throw new Error('FORUM_SAVE_FAILED')
      const post = parseForumPosts([await response.json()])[0]
      setForumPosts((current) => [...current, post])
      setForumDraft('')
      setNotice('留言已保存。系统只保存虚拟名字和留言内容。')
    } catch (error) {
      const code = error instanceof Error ? error.message : ''
      setNotice(code === 'INVALID_NICKNAME' ? '虚拟名字必须包含 2–24 个可见字符。' : code === 'INVALID_FORUM_MESSAGE' ? '留言不能为空，且不能超过 2000 个字符。' : '留言保存失败，请稍后重试。')
    } finally {
      setForumSaving(false)
    }
  }

  async function submitForumReply(event: FormEvent) {
    event.preventDefault()
    if (replySaving || replyingPost === null) return
    try {
      const name = normalizeNickname(forumName)
      const message = normalizeForumMessage(replyDraft)
      const postIndex = replyingPost
      setReplySaving(true)
      const response = await fetch(forumRepliesURL(window.location, postIndex), {
        method: 'POST',
        cache: 'no-store',
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, message }),
      })
      if (!response.ok) throw new Error('FORUM_REPLY_FAILED')
      const reply = parseForumReply(await response.json())
      setForumPosts((current) => current.map((post, index) => index === postIndex ? { ...post, replies: [...post.replies, reply] } : post))
      setReplyDraft('')
      setReplyingPost(null)
      setNotice('回复已保存。系统只保存虚拟名字和回复内容。')
    } catch (error) {
      const code = error instanceof Error ? error.message : ''
      setNotice(code === 'INVALID_NICKNAME' ? '虚拟名字必须包含 2–24 个可见字符。' : code === 'INVALID_FORUM_MESSAGE' ? '回复不能为空，且不能超过 2000 个字符。' : '回复保存失败，请稍后重试。')
    } finally {
      setReplySaving(false)
    }
  }

  const isOwner = ownerEphemeralId === identity.ephemeralId
  return <main className="shell">
    <header><a className="brand" href={import.meta.env.BASE_URL} onClick={(event) => { event.preventDefault(); reset() }}>局域网临时聊天室</a><div className="headerActions"><nav className="mainNav" aria-label="主导航"><button className="secondary compact" type="button" onClick={openForum}>公共吐槽板</button>{view === 'forum' && <button className="secondary compact" type="button" onClick={returnToChat}>聊天室</button>}</nav><div className="identityControl">{editingName && view !== 'connecting' ? <form className="renameForm" onSubmit={saveNickname}><input aria-label="临时显示名" value={nameDraft} onChange={(event) => setNameDraft(event.target.value)} maxLength={48} autoFocus /><button className="compact" type="submit">保存</button><button className="secondary compact" type="button" onClick={() => setEditingName(false)}>取消</button></form> : <><span className="identity">{identity.nickname}</span><button className="secondary compact renameButton" type="button" disabled={view === 'connecting'} onClick={beginRename}>改名</button></>}</div></div></header>
    {notice && <p className="notice" role="status">{notice}</p>}
    {view === 'home' && <section className="hero card"><span className="eyebrow">匿名 · 端到端加密 · 临时</span><h1>只在有人在线时存在的聊天室。</h1><p>网络管理员仍可能观察连接元数据。进入已经有人在的房间，需要房主同意。</p><div className="homeActions"><button type="button" onClick={openForum}>进入公共吐槽板</button><span>吐槽板留言会公开展示并持久保存。</span></div><div className="roomGrid">{FIXED_ROOM_IDS.map((id) => <button className="roomCard" key={id} type="button" onClick={() => void enterRoom(id)}><span className="eyebrow">房间号</span><strong>{id}</strong></button>)}</div></section>}
    {view === 'forum' && <section className="forum card"><div className="forumHeader"><div><span className="eyebrow">公开 · 虚拟名字 · 可保存</span><h1>公共吐槽板</h1></div><p>所有人都可以留下吐槽。这里不需要注册，留言和回复只保存虚拟名字与内容，不显示或保存发表时间。</p></div><form className="forumComposer" onSubmit={(event) => void submitForumPost(event)}><label><span>虚拟名字</span><input value={forumName} onChange={(event) => setForumName(event.target.value)} maxLength={48} placeholder="输入 2–24 个可见字符" aria-label="吐槽板虚拟名字" /></label><label><span>留言</span><textarea value={forumDraft} onChange={(event) => setForumDraft(event.target.value)} maxLength={2000} rows={5} placeholder="想说点什么？" aria-label="吐槽板留言" /></label><button type="submit" disabled={forumSaving}>{forumSaving ? '保存中……' : '发布留言'}</button></form><div className="forumPosts" aria-live="polite"><div className="forumPostsTitle"><h2>全部留言</h2><span>{forumPosts.length} 条</span></div>{forumLoading && <p className="empty">正在加载留言……</p>}{!forumLoading && forumPosts.length === 0 && <p className="empty">还没有留言，来留下第一条吧。</p>}{!forumLoading && forumPosts.map((post, index) => <article className="forumPost" key={`${post.name}-${index}`}><div className="forumPostTop"><div><strong>{post.name}</strong><p>{post.message}</p></div><button className="secondary compact" type="button" onClick={() => { setReplyingPost(replyingPost === index ? null : index); setReplyDraft('') }}>回复</button></div>{post.replies.length > 0 && <div className="forumReplies">{post.replies.map((reply, replyIndex) => <div className="forumReply" key={`${reply.name}-${replyIndex}`}><strong>{reply.name}</strong><p>{reply.message}</p></div>)}</div>}{replyingPost === index && <form className="replyComposer" onSubmit={(event) => void submitForumReply(event)}><label><span>回复名字</span><input value={forumName} onChange={(event) => setForumName(event.target.value)} maxLength={48} aria-label={`回复第 ${index + 1} 条留言的虚拟名字`} /></label><label><span>回复内容</span><textarea value={replyDraft} onChange={(event) => setReplyDraft(event.target.value)} maxLength={2000} rows={3} placeholder="写下回复……" aria-label={`回复第 ${index + 1} 条留言`} /></label><div className="replyActions"><button type="submit" disabled={replySaving}>{replySaving ? '保存中……' : '发布回复'}</button><button className="secondary" type="button" onClick={() => { setReplyingPost(null); setReplyDraft('') }}>取消</button></div></form>}</article>)}</div></section>}
    {view === 'connecting' && <section className="card narrow"><h2>正在进入房间……</h2><p>{notice || '正在连接。'}</p><button className="secondary" type="button" onClick={() => reset()}>取消</button></section>}
    {view === 'chat' && <section className="chat card" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); const file = event.dataTransfer.files?.[0]; if (!file) return; if (file.type.startsWith('image/') && file.size <= MAX_IMAGE_BYTES) void sendImage(file); else void sendFile(file) }}>
      <div className="roomHeader"><div><span className="eyebrow">房间号</span><h2>{roomId}</h2></div><button className="secondary compact" onClick={() => void navigator.clipboard.writeText(roomId).then(() => setNotice('房间号已复制。'), () => setNotice('复制失败，请手动记录房间号。'))}>复制房间号</button><div className="online">在线：{members.length}</div>{isOwner && <span className="ownerBadge">房主</span>}<button className="secondary compact" onClick={() => reset()}>离开</button></div>
      {isOwner && joinRequests.length > 0 && <div className="joinRequests"><strong>入房申请</strong>{joinRequests.map((request) => <div className="joinRequest" key={request.requestId}><span>{request.nickname}</span><button className="compact" onClick={() => void decideJoin(request, true)}>同意</button><button className="secondary compact" onClick={() => void decideJoin(request, false)}>拒绝</button></div>)}</div>}
      <div className="memberLine">{members.map((member) => `${member.nickname}${member.ephemeralId === ownerEphemeralId ? '（房主）' : ''}`).join(' · ')}</div>
      <div className="messages" aria-live="polite">{messages.length === 0 && <p className="empty">没有历史消息，新消息会显示在这里。</p>}{messages.map((message) => <p className={`message ${message.own ? 'own' : ''}`} key={message.id}><strong>{message.nickname}</strong>{message.kind === 'image' ? <img className="messageImage" src={message.body} alt="图片消息" /> : message.kind === 'file' && message.file ? <FileCardView file={message.file} progress={fileProgress?.fileId === message.file.fileId ? fileProgress.received : null} onCancel={() => downloadAbortRef.current.get(message.file!.fileId)?.abort()} onDownload={() => void downloadFile(message.file!)} /> : <span>{message.body}</span>}</p>)}</div>
      {upload && <div className="uploadBar"><span className="fileName">{upload.name}</span><progress value={upload.sent} max={upload.total} /><span>{formatBytes(upload.sent)} / {formatBytes(upload.total)}</span><button className="secondary compact" type="button" onClick={() => uploadAbortRef.current?.abort()}>取消</button></div>}
      <form className="composer" onSubmit={(event) => void sendMessage(event)}>
        <input value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="输入消息，或粘贴 / 拖入图片或文件……" maxLength={4096} aria-label="消息" onPaste={(event) => { const item = Array.from(event.clipboardData.items).find((entry) => entry.kind === 'file' && entry.type.startsWith('image/')); const file = item?.getAsFile(); if (file) { event.preventDefault(); void sendImage(file) } }} />
        <input ref={fileInputRef} className="fileInput" type="file" accept="image/*" onChange={(event) => { const file = event.target.files?.[0]; if (file) void sendImage(file); event.target.value = '' }} />
        <input ref={fileSendInputRef} className="fileInput" type="file" onChange={(event) => { const file = event.target.files?.[0]; if (file) void sendFile(file); event.target.value = '' }} />
        <button className="secondary compact" type="button" onClick={() => fileInputRef.current?.click()}>图片</button>
        <button className="secondary compact" type="button" disabled={upload !== null} onClick={() => fileSendInputRef.current?.click()}>文件</button>
        <button type="submit">发送</button>
      </form>
      <p className="destroy">所有人离开后，房间立即销毁。文件仅保留 5 分钟，过期即从服务器删除。</p>
    </section>}
  </main>
}

// A file message: metadata, a self-driving countdown, and download controls.
// The countdown is display-only; the relay's file_expired broadcast (or a 410
// from the endpoint) is what authoritatively flips the card.
function FileCardView(props: { file: FileCardBody; progress: number | null; onDownload: () => void; onCancel: () => void }) {
  const { file, progress, onDownload, onCancel } = props
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (file.expired) return
    const timer = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [file.expired])
  const remaining = file.expiresAt - now
  const expired = file.expired || remaining <= 0
  const downloading = progress !== null
  return <span className={`fileCard${expired ? ' expired' : ''}`}>
    <span className="fileIcon" aria-hidden="true">📄</span>
    <span className="fileInfo">
      <strong className="fileName">{file.name}</strong>
      <span className="fileMeta">{formatBytes(file.size)}{expired ? ' · 已过期，服务器已删除' : ` · 剩余 ${formatCountdown(remaining)}`}</span>
    </span>
    {downloading
      ? <span className="fileActions"><span className="fileProgress">{formatBytes(progress ?? 0)}</span><button className="secondary compact" type="button" onClick={onCancel}>取消</button></span>
      : <span className="fileActions"><button type="button" disabled={expired} onClick={onDownload}>下载</button></span>}
  </span>
}

function errorText(code: string): string {
  const errors: Record<string, string> = { ROOM_NOT_FOUND: '房间不存在。', ROOM_FULL: '房间人数已满。', RATE_LIMITED: '操作过快，连接已关闭。', CAPACITY_REACHED: '服务器房间数量已达上限。', INVALID_MESSAGE: '连接违反了房间协议。', JOIN_REJECTED: '房主拒绝了你的入房申请。', OWNER_CHANGED: '房主已经离开，请重新申请加入。', FILE_TOKEN_LIMIT: '文件传输请求过多，请稍后再试。', FILE_DISABLED: '服务器未启用文件传输。' }
  return errors[code] ?? '房间请求失败。'
}
