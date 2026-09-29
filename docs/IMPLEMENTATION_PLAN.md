# Implementation Plan

每个阶段必须保持可运行且无 placeholder security code。Phase gate 失败时先修复再继续。

## Phase 1 — Backend Room Engine

Deliverables：Go module；并发安全 Room/Manager；原子 create-first-member、join、leave/delete；容量上限；Room 无 message/history 字段；公共论坛 Store 独立实现。  
Tests：create/join/leave/last-leave、creator-first-leave、collision、capacity、并发 race、process-new-manager 无恢复。  
Gate：`go test -race ./...`、`go vet ./...`。

## Phase 2 — WebSocket Transport

Deliverables：严格协议 decoder；connection state machine；origin allowlist；frame/read/handshake limits；bounded writer；heartbeat；固定错误码。  
Tests：create/join integration、late join no history、malformed/unknown fields、oversize、wrong sender/room、origin rejection、slow queue behavior。  
Gate：backend tests/race/vet。

## Phase 3 — Frontend Create/Join

Deliverables：React 极简 UI；RAM identity；3,544 项离线人物名（金庸小说、金瓶梅与四大名著）与临时改名；手机响应式布局；固定 8 房间列表 + 房主审批 + ECDH RoomKey 交付 + 图片压缩发送；WebSocket client；presence/chat state only in React。  
Tests：identity factory produces new values、name-pool integrity、rename validation/presence、room list、admission approval flow、owner transfer、image codec、state reset、protocol guards。  
Gate：`npm run lint`、`npm run typecheck`、`npm test`、`npm run build`。

## Phase 4 — Browser E2EE

Deliverables：RoomKey、HKDF sender key、AES-256-GCM、AAD codec、structured nonce, replay set；plaintext never enters wire object。  
Tests：round-trip、tamper/AAD failure、wrong key、nonce no reuse across many messages、counter exhaustion、server fixture only sees ciphertext。  
Gate：frontend checks + backend integration。

## Phase 5 — Password Rooms

Deliverables：固定 8 房间、空房首位进入者即房主；房主审批入房申请（同意/拒绝/离开作废）、房主随机转移、ECDH RoomKey 交付；图片压缩与图片消息。  
Tests：first entrant becomes owner、admission approve/reject/cancel、owner transfer、unapproved connection gets no key、no password/verifier fields、image size limits。  
Gate：all checks。

## Phase 6 — Lifecycle and Failure Semantics

Deliverables：disconnect cleanup once、refresh/sole-member behavior、owner approval and owner-transfer behavior、shutdown cancels all sockets。  
Tests：last disconnect immediate deletion、late join, restart/new process state, reconnect has new identity/no history。  
Gate：race test plus integration suite。

## Phase 7 — Security Hardening

Deliverables：CSP/no-store headers、origin config、global/per-room/per-connection limits、safe errors/log allowlist、read-only runtime assumptions。  
Tests：oversize/rate/global capacity/origin/header tests；static sensitive logger scan。  
Gate：lint/typecheck/test/build/vet/race。

## Phase 8 — Docker and LAN Deployment

Deliverables：multi-stage Dockerfiles；Nginx TLS/WSS/static config；tmpfs fallback certificate；Compose 使用独立 forum-data volume、其余 root read-only、无日志；`.env.example`。  
Tests：`docker compose config`；if daemon available, build/up/health/WSS smoke/down。  
Gate：container health + documented company CA path。

## Phase 9 — Zero-Persistence Audit

Deliverables：`PRIVACY_ARCHITECTURE.md`；automated ripgrep audit for all forbidden APIs/keywords；论坛字段边界与聊天零持久化的 manual data-flow review；`SECURITY_AUDIT.md` evidence table。  
Tests：script fails on prohibited production code and permits documented test/docs references only。  
Gate：all implementable requirements PASS; architectural limitations marked PASS with accurate limitation evidence, not falsely upgraded claims。

## Phase 10 — README and Release Verification

Deliverables：product/non-product definition、quick start、LAN DNS/hosts、certificate choices、host hardening checklist、threat/limitations、operations。  
Final gate：clean install reproducibility, complete checks, no TODO/FIXME, repository inventory and final acceptance walkthrough。

## Verification Matrix

| Requirement | Primary automated evidence |
|---|---|
| Room lifecycle | Go room + WS integration tests |
| No history / offline | WS late-join test |
| Refresh identity | frontend identity/state tests + browser behavior design |
| Restart clears | fresh Manager/process test; no storage paths |
| Password | cross-language vector + WS auth integration |
| E2EE | Web Crypto tests + relay captured envelope assertion |
| Nonce | generator uniqueness/exhaustion tests |
| Oversize/rate | Go protocol/relay tests |
| Zero persistence | source audit script + deployment config inspection |

## Definition of Done

- 所有 phase gate 通过，或环境缺少 Docker/browser 时明确列出未能执行的命令与静态替代证据。
- 无 TODO、FIXME、mock/fake crypto、临时聊天室历史 endpoint、聊天持久 storage 和敏感日志；论坛只保留明确允许的 name/message/replies。
- 文档与实际常量/协议一致；最终 audit 对所有用户要求给出证据。
