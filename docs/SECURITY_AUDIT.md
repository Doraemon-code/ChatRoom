# MVP Security and Privacy Audit

审计日期：2026-09-11。范围包括自有 production source、测试、Docker/Nginx 配置与文档；`server/vendor` 和 `web/node_modules` 作为第三方依赖单独处理，不把依赖源码中的通用能力误认为本应用的数据流。结果中的 PASS 表示 MVP 对该项已实现，或按明确要求正确排除并说明边界；不把未来能力伪装为已实现。

## Requirements matrix

| Requirement | Status | Evidence |
|---|---|---|
| No registration/login/account/profile/contact | PASS | UI/协议没有相关字段或 endpoint；`Room` 只有 ID、Auth、members |
| Fresh identity per page load | PASS | `App` 的 lazy state 调用 CSPRNG identity factory；identity test 验证连续实例不同 |
| 3,544 classic-novel aliases | PASS | 固化 CC0 数据恰好 3,544 个唯一、合法名称（金庸小说、金瓶梅、四大名著）；测试逐项校验 |
| User-controlled temporary rename | PASS | 2–24 可见 Unicode 字符前后端双重验证；WebSocket integration 验证 presence 广播；Room 删除后不保留 |
| Mobile browser layout | PASS | viewport meta、280px breakpoint、44px touch targets、dynamic viewport height、responsive composer/room-header/rename controls |
| LAN listener | PASS | Compose 明确发布 `${BIND_ADDRESS:-0.0.0.0}:443`；Relay 仍不发布端口且只在 internal network |
| No persistent identity/token/cookie | PASS | 无 Storage/Cookie/token 代码；离开 reset 清除 identity 以外的 room refs，刷新重建整个 App |
| Public forum virtual-name posting | PASS | `/api/board/posts` 只接受 name/message；UI 使用页面内虚拟名字，不需要账户 |
| Forum replies | PASS | 每条主留言通过数组位置提供 `/replies` 接口；回复只保存 name/message，重启后 Store 测试验证恢复 |
| Forum persistence field boundary | PASS | 严格 JSON decoder；线程只有 name/message/replies，回复只有 name/message；无 ID、IP、MAC、时间或 User-Agent 字段 |
| Fixed eight rooms | PASS | 房间号固定 `0001`–`0008`；协议 regex 只接受这 8 个；无创建流程 |
| First entrant becomes owner, transfer on leave | PASS | Room owner-transfer tests 验证空房首位进入者为房主、离开后随机转移给剩余成员并拒绝待审批申请 |
| Owner approves every later join | PASS | room + relay integration：占用的房间 `join_pending`，房主 `admission_decision` 同意才 `joined`，拒绝返回 `JOIN_REJECTED` |
| RoomKey delivered by ECDH wrap, never in URL | PASS | app 层 ECDH P-256 + AES-GCM 封装，Relay 只见封装密文；协议校验 65-byte 公钥与定长封装字段 |
| Image messages, still E2EE | PASS | 浏览器压缩到 ≤500KB 后按明文加密；relay test 转发 600KB 密文；protocol 拒绝超出 1MiB 明文 / 2MiB frame |
| Empty room immediately destroyed | PASS | `Manager.Leave` 同锁删除；unit 与 WebSocket integration 均验证 `Has == false` |
| No grace/reconnect/tombstone | PASS | 无对应状态/计时器/结构；刷新单人房间按断连删除语义处理 |
| Server restart clears temporary rooms | PASS | Manager 构造为空、无 room persistence adapter；fresh-manager test 验证临时房间无恢复 |
| Chat has no message/history/offline API | PASS | Room 无 message/history 字段；没有聊天 history route、DB、queue 或 replay store；论坛 API 是明确例外 |
| Late join receives no history | PASS | WebSocket integration：A 发 frame 后 B 加入，读取超时且未收到旧 frame |
| Browser messages RAM-only | PASS | 唯一消息集合是 React `useState`; reset/refresh 清除；禁用 API scan 通过 |
| Relay sees ciphertext, not plaintext | PASS | Web Crypto test 确认 serialized envelope 不含 `Hello`; protocol 只接受 ciphertext 字段；Relay 无 key |
| Independent random RoomKey | PASS | 房主浏览器 CSPRNG 32 bytes；只在房主浏览器 RAM 并经 ECDH 封装转交 |
| AES-256-GCM + 128-bit tag | PASS | Web Crypto deriveKey 256 bit、AES-GCM tagLength 128；round-trip/tamper tests |
| No AES-GCM IV reuse | PASS | 每 sender key 使用 64-bit random prefix + uint32 counter；10,000-IV uniqueness 与 exhaustion fail-closed tests |
| Envelope metadata authenticated | PASS | v/type/room/sender/message ID 以 length-prefix AAD；room mutation decrypt test fails |
| Message ID + replay mitigation | PASS | random UUID；browser bounded RAM ReplayGuard test；Relay 不持久保存 ID |
| No unnecessary timestamp | PASS | ciphertext schema 不含 timestamp；UI 按到达顺序 |
| No password/verifier on the wire or in RAM | PASS | 协议与 Room 结构不含任何口令、salt、verifier 或 challenge 字段 |
| Unapproved connection derives no key | PASS | integration：未获批连接只收到 `join_pending`，拿不到 `join_approved` 封装 |
| Strict protocol and max sizes | PASS | unknown-field/trailing/version/ID/base64 checks；2 MiB frame；1 MiB plaintext/ciphertext tests |
| Connection/message rate limiting | PASS | 全局 slots；per-connection token bucket 12/5s；integration verifies abusive connection closes |
| Max rooms/members/connections | PASS | 8 fixed rooms / 50 members / 2,000 connections；room capacity tests；fixed outbound queue 32 |
| Origin/CSWSH defense | PASS | complete Origin allowlist before upgrade；evil origin integration rejected；Nginx forwards only Origin |
| HTTPS/WSS required | PASS | edge exposes TLS 1.2/1.3 only；actual Compose smoke returned HTTPS 200 and WebSocket 101 |
| No invite links / RoomKey never in URL | PASS | 代码中无 fragment/invite 解析；RoomKey 仅经 ECDH 封装转发 |
| Referrer/cache/browser policy | PASS | `no-referrer`, `no-store`, CSP, frame/nosniff/permissions headers observed in running container |
| No application-level IP reading/mapping | PASS | source scan finds no RemoteAddr/IP headers/UA access；Room/Member have no IP field |
| Nginx does not log IP/access | PASS | `access_log off`, error only `/dev/null emerg` |
| Application safe logging | PASS | only exact `server_started`, `server_stopped`, `fatal:listen_failed`, `fatal:forum_store`; HTTP server error logger discarded |
| Docker log retention disabled | PASS | both services use `logging.driver: none`; inspect confirms edge type `none` |
| Errors do not dump request/socket/payload | PASS | fixed client error codes; no formatted error/request/socket logging; static scan passes |
| No chat database/cache/persistent queue | PASS | no DB dependencies or adapters；Compose 只有明确的 forum-data JSON volume，聊天状态不落盘 |
| Docker read-only/non-root/minimal | PASS | read-only roots, `cap_drop: ALL`, no-new-privileges, core=0；scratch Relay runs UID 65532 |
| TLS fallback does not persist chat data | PASS | certgen writes only key/cert to `/run/chat-tls` tmpfs；production cert mount is operator credential；forum-data 是唯一业务数据 volume |
| No public directory/admin/analytics/PWA | PASS | no routes/dependencies/code for excluded features；static scan covers telemetry/service worker tokens |
| Server disk/volume copy has only forum content fields | PASS | Relay 没有聊天写路径；forum-data JSON 只含 name/message/replies；host swap/core/snapshot caveat documented |
| LAN capture protection | PASS | TLS/WSS plus application E2EE；IP/timing/length visibility explicitly documented |
| Compromised Relay boundary | PASS | passive server cannot decrypt；active JS replacement attack explicitly not claimed away |
| Compromised browser/recipient saving | PASS | endpoint, screenshot, clipboard/copy and modified-client limitations documented |
| Network administrator boundary | PASS | Router/AP/Switch/DHCP/Firewall/IDS/OS metadata exclusions documented |
| Forward Secrecy honesty | PASS | V1 explicitly says no FS, no PCS/rekey/strong sender auth；X25519/ratchet/sender-key are roadmap only |
| No homemade crypto primitives | PASS | browser Web Crypto AES-GCM/HKDF/ECDH only；Go server performs no crypto |
| No TODO/FIXME/fake crypto | PASS | final repository scan has no TODO/FIXME; tests use deterministic inputs only, production uses CSPRNG |

## Required keyword audit

`scripts/zero-persistence-audit.sh` scans production paths and fails on prohibited browser persistence, IP/header access, telemetry, databases, queues, production console calls, or non-allowlisted logs; it separately checks that Nginx clears request identity headers for the forum API.

| Search term group | Production result | Explanation |
|---|---|---|
| `localStorage`, `sessionStorage`, `indexedDB`, `document.cookie`, `Cookie` | Absent | No browser persistence/session mechanism |
| `Service Worker`, `Cache API` | Absent | No PWA/offline cache; documentation mentions the prohibition only |
| `fs.write`, `writeFile` | One reviewed exception outside Relay | Go certgen uses `os.WriteFile` only for TLS key/cert under edge tmpfs; accepts no chat input |
| database/sqlite/postgres/mysql/mongo/redis | Absent | No database/cache dependency; forum uses standard-library JSON file |
| Kafka/RabbitMQ | Absent | No persistent/offline queue |
| logger/`console.log`/`console.error` | No client console; fixed Go lifecycle events only | Production exception paths use generic UI/error codes and discard HTTP error log |
| `req.ip`, `remoteAddress`, forwarded-IP headers | Absent in application reads | Application never reads or maps source IP；forum proxy explicitly clears forwarded-IP headers |
| `User-Agent` | Not collected/forwarded to forum handler | Forum proxy explicitly clears it；application never reads or logs it |
| Sentry/Analytics/Google Analytics | Absent | No telemetry dependency or endpoint |
| `history` | Absent in production code | 无 fragment/invite 解析，也未使用 History API；聊天无 history API |

Latest automated result:

```text
PASS: browser chat storage, IP access, telemetry, and sensitive log patterns absent
PASS: forum persistence is limited to names/messages/replies; no IP, MAC, time, or request metadata fields
PASS: Nginx access logs and Docker container logs disabled
NOTE: certgen writes only a TLS certificate and key to /run/chat-tls tmpfs; it has no chat data input
```

## Verification results

| Command / check | Result |
|---|---|
| `npm run lint` | PASS, zero warnings |
| `npm run typecheck` | PASS, TypeScript strict |
| `npm test` | PASS, 4 files / 15 tests |
| `npm run build` | PASS, Vite production bundle |
| `npm audit --omit=optional` | PASS, 0 vulnerabilities |
| `go test -mod=vendor -race ./...` | PASS, protocol/relay/room |
| `go vet -mod=vendor ./...` | PASS |
| `docker compose config --quiet` | PASS |
| Relay and edge image build | PASS |
| Compose runtime | PASS, Relay running / edge healthy |
| HTTPS static/health smoke | PASS, 200/204 + expected headers |
| WSS through Nginx | PASS, HTTP 101 with allowed Origin |

## Residual risks accepted for MVP

These are documented security boundaries, not hidden failures: shared fixed RoomKey has no Forward Secrecy; there is no password second factor, so admission security rests entirely on the owner approving correctly; a server administrator who changes delivered JavaScript can steal future secrets; traffic metadata remains visible; endpoint compromise/recipient saving wins; GC/swap/hibernation/core dump can defeat physical “RAM cleared” assumptions; connection-scoped anti-abuse cannot provide durable distributed-abuse control without identity or network metadata.

The most valuable next security phase is an independently reviewed ephemeral X25519/member-authenticated key establishment followed by membership-change rekey/ratchet. It must include transcript consistency and verification UX; merely adding X25519 without active-server MITM handling would create a misleading security claim.
