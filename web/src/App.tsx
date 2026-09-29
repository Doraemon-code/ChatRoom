import { type FormEvent, useEffect, useRef, useState } from 'react'
import { createIdentity, FIXED_ROOM_IDS, normalizeNickname, type Identity } from './identity'
import { decryptMessage, deriveSenderKey, encryptMessage, exportAdmissionPublicKey, generateAdmissionKeyPair, generateRoomMaterial, NonceSequence, ReplayGuard, unwrapRoomKey, wrapRoomKey, type CiphertextEnvelope } from './crypto/roomCrypto'
import { decodePlaintext, encodePlaintext, imageDataURL } from './message'
import { encodeImageFile, MAX_IMAGE_BYTES } from './image'
import { forumPostsURL, forumRepliesURL, normalizeForumMessage, parseForumPosts, parseForumReply, type ForumPost } from './forum'
import { parseServerMessage, websocketURL, type JoinRequest, type PresenceMember, type ServerMessage } from './protocol'

type View = 'home' | 'forum' | 'connecting' | 'chat'
type MessageKind = 'text' | 'image'
interface ChatMessage { id: string; senderId: string; nickname: string; kind: MessageKind; body: string; own: boolean }

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
    setMessages([]); setMembers([]); setOwnerEphemeralId(''); setJoinRequests([]); setDraft(''); setRoomId(''); setView('home'); setNotice(reason)
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
      const body = plaintext.t === 'image' ? imageDataURL(plaintext.m, plaintext.b) : plaintext.b
      setMessages((current) => [...current, { id: envelope.messageId, senderId: envelope.senderEphemeralId, nickname, kind: plaintext.t, body, own: false }])
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
    {view === 'chat' && <section className="chat card" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); const file = event.dataTransfer.files?.[0]; if (file) void sendImage(file) }}>
      <div className="roomHeader"><div><span className="eyebrow">房间号</span><h2>{roomId}</h2></div><button className="secondary compact" onClick={() => void navigator.clipboard.writeText(roomId).then(() => setNotice('房间号已复制。'), () => setNotice('复制失败，请手动记录房间号。'))}>复制房间号</button><div className="online">在线：{members.length}</div>{isOwner && <span className="ownerBadge">房主</span>}<button className="secondary compact" onClick={() => reset()}>离开</button></div>
      {isOwner && joinRequests.length > 0 && <div className="joinRequests"><strong>入房申请</strong>{joinRequests.map((request) => <div className="joinRequest" key={request.requestId}><span>{request.nickname}</span><button className="compact" onClick={() => void decideJoin(request, true)}>同意</button><button className="secondary compact" onClick={() => void decideJoin(request, false)}>拒绝</button></div>)}</div>}
      <div className="memberLine">{members.map((member) => `${member.nickname}${member.ephemeralId === ownerEphemeralId ? '（房主）' : ''}`).join(' · ')}</div>
      <div className="messages" aria-live="polite">{messages.length === 0 && <p className="empty">没有历史消息，新消息会显示在这里。</p>}{messages.map((message) => <p className={`message ${message.own ? 'own' : ''}`} key={message.id}><strong>{message.nickname}</strong>{message.kind === 'image' ? <img className="messageImage" src={message.body} alt="图片消息" /> : <span>{message.body}</span>}</p>)}</div>
      <form className="composer" onSubmit={(event) => void sendMessage(event)}>
        <input value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="输入消息，或粘贴 / 拖入图片……" maxLength={4096} aria-label="消息" onPaste={(event) => { const item = Array.from(event.clipboardData.items).find((entry) => entry.kind === 'file' && entry.type.startsWith('image/')); const file = item?.getAsFile(); if (file) { event.preventDefault(); void sendImage(file) } }} />
        <input ref={fileInputRef} className="fileInput" type="file" accept="image/*" onChange={(event) => { const file = event.target.files?.[0]; if (file) void sendImage(file); event.target.value = '' }} />
        <button className="secondary compact" type="button" onClick={() => fileInputRef.current?.click()}>图片</button>
        <button type="submit">发送</button>
      </form>
      <p className="destroy">所有人离开后，房间立即销毁。</p>
    </section>}
  </main>
}

function errorText(code: string): string {
  const errors: Record<string, string> = { ROOM_NOT_FOUND: '房间不存在。', ROOM_FULL: '房间人数已满。', RATE_LIMITED: '操作过快，连接已关闭。', CAPACITY_REACHED: '服务器房间数量已达上限。', INVALID_MESSAGE: '连接违反了房间协议。', JOIN_REJECTED: '房主拒绝了你的入房申请。', OWNER_CHANGED: '房主已经离开，请重新申请加入。' }
  return errors[code] ?? '房间请求失败。'
}
