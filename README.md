# 匿名局域网聊天室

一个面向组织局域网、自托管的匿名会话临时聊天室，并附带公共吐槽板。聊天室浏览器生成一次性身份和房间密钥，使用 AES-256-GCM 加密消息；Go Relay 只维护当前 RAM 房间并转发密文。最后一名成员离开，房间立即消失。

准确的隐私承诺是：无注册、无永久身份；临时聊天室无聊天历史；公共吐槽板只持久化虚拟名字、留言和回复内容，不保存应用层 IP、MAC、发表时间、User-Agent 或其他请求元数据。项目**不声称绝对匿名、不可追踪或接收者无法保存内容**。

## 快速开始

要求 Docker Engine 及 Compose v2：

```bash
cp .env.example .env
docker compose up -d --build
```

服务默认绑定 `0.0.0.0:443`，可从局域网访问。将 `chat.local` 指向服务器 LAN 地址（内部 DNS，或各客户端 hosts 文件），打开 `https://chat.local`。默认会在容器 tmpfs 生成有效期 7 天的自签证书，浏览器不会信任它，因此仅适合本地验收。正式部署请配置受客户端信任的公司 CA 证书。

本机已将它接入 Hao123 门户：生产入口为 `https://clin-agent.iepose.cn/chat/`，外层 nginx 反代到本机 `127.0.0.1:9443`。门户场景使用根目录的 `.env`：`ALLOWED_ORIGINS` 必须包含门户的完整 origin，`BIND_ADDRESS=127.0.0.1`，并用 `VITE_BASE_PATH=/chat/` 构建前端。外层 nginx 片段见 `../Hao123/deploy/nginx/chatroom.conf`。

若测试机 443 端口被占用：

```bash
HTTPS_PORT=9443 ALLOWED_ORIGINS=https://localhost:9443 docker compose up -d --build
```

然后访问 `https://localhost:9443`。非 443 端口必须出现在 `ALLOWED_ORIGINS` 中。自签证书的 SAN 可用 `CERT_HOSTS` 调整；生产更推荐挂载证书。

## 生产 TLS / 局域网部署

1. 为 `chat.example.local` 配置内部 DNS。
2. 用公司 CA 或内部 ACME 签发匹配该名称的证书。公司 CA 是组织部署的推荐方案；mkcert 适合少量开发设备；单独自签需要逐设备导入信任且容易形成忽略证书警告的习惯。
3. 放置 `deploy/certs/tls.crt` 和 `deploy/certs/tls.key`。该目录被 git 与镜像构建排除，运行时只读挂载。
4. 设置 `.env`：

```dotenv
ALLOWED_ORIGINS=https://chat.example.local
BIND_ADDRESS=0.0.0.0
HTTPS_PORT=443
MAX_CONNECTIONS=2000
MAX_ROOMS=1000
MAX_MEMBERS_PER_ROOM=50
FORUM_DATA_PATH=/data/forum.json
MAX_FORUM_POSTS=10000
```

5. 运行 `docker compose up -d --build`，从电脑或手机浏览器打开站点。不要把 Relay 的 8080 端口发布到 LAN。

自签 fallback 可用 `CERT_HOSTS=chat.local,localhost,192.168.10.10` 添加目标 IP，但证书依旧不受信任。Web Crypto 需要 secure context，因此不提供 HTTP/明文 WebSocket 降级。

若直接通过 LAN IP 访问，必须同时匹配 Origin 和证书 SAN，例如：

```dotenv
ALLOWED_ORIGINS=https://192.168.10.10
BIND_ADDRESS=0.0.0.0
HTTPS_PORT=443
CERT_HOSTS=192.168.10.10
```

多个入口用逗号分隔 `ALLOWED_ORIGINS` 和 `CERT_HOSTS`。手机端必须信任签发该证书的内部 CA；仅能打开页面但证书不可信并不等于安全的 WSS 部署。

## 使用方式

- 页面每次装载从 3,544 项金庸小说、金瓶梅与四大名著人物表随机分配一个显示名，同时生成新的 UUID；刷新不是恢复。
- 用户可在页面中修改显示名。修改通过实时 presence 同步，但只在当前页面和 Room RAM 中存在，不是永久昵称。
- 房间固定为 0001–0008 共 8 个，无需创建；房间号就是唯一入口，**没有密码**。
- 进入空房间的第一人即成为房主并直接进入；之后进入同一房间的人，都要由房主在页面上确认（同意 / 拒绝）。
- RoomKey 由房主用基于 ECDH 的一次性临时密钥封装后交给新成员，不进入 URL，也不被服务器解出。
- 房主离开后，服务器在剩余成员中随机指定新房主，并拒绝当时的待审批申请。所有人离开后房间立即销毁并回到空置；服务器重启后所有房间消失。
- 支持发送图片：浏览器先把图片压缩到约 500KB 以内，再与文本一样端到端加密；图片同样只存在内存，不落盘。
- 公共吐槽板是独立的论坛页面，所有人可用当前虚拟名字发帖并回复；帖子在刷新和服务重启后仍可恢复。持久化记录只包含 `name`、`message` 和回复列表中的同样两类字段，不展示或写入发表时间、IP、MAC、User-Agent、连接 ID 等信息。

## 安全模型

消息使用浏览器 Web Crypto：RoomKey 经 HKDF-SHA-256 派生每发送者 AES-256-GCM key，96-bit IV 由随机 64-bit prefix 与 32-bit 单调计数组成，envelope 路由字段作为 AAD。服务器永远不接收 RoomKey 或聊天明文。

房间没有密码，加入授权完全依赖当前房主的逐人批准：RoomKey 只在房主同意时经 ECDH 封装转交，任何未被批准的连接拿不到 RoomKey，也就无法解密消息。代价是没有口令作为第二道门槛——安全性取决于房主不误批，以及房主本人的可信度。读取服务器 RAM 的攻击者看不到 RoomKey 或明文，服务器只转发封装密文。

V1 的固定 RoomKey **没有 Forward Secrecy、post-compromise security、成员撤销 rekey 或强发送者认证**。任何成员都拥有群组 key；密钥泄漏可解密攻击者此前保存的同房间密文。路线图依次评估 X25519、ratchet/rekey 和 Group Sender Keys，不会把未实现属性写进宣传。

完整设计见 [PRD](docs/PRD.md)、[Architecture](docs/ARCHITECTURE.md)、[Security](docs/SECURITY.md) 和 [Privacy Architecture](PRIVACY_ARCHITECTURE.md)。

## 聊天零持久化与吐槽板存储

- 浏览器聊天数据只在 React/JavaScript runtime；不用 LocalStorage、SessionStorage、IndexedDB、Cookie、Service Worker 或 Cache API。
- 临时聊天室 Relay 只有 RAM Room/Member/待审批申请，不保存聊天历史；公共吐槽板是明确例外，使用 `/data/forum.json` 的命名 Docker volume 保存名字、留言和回复。
- 吐槽板 API 和文件记录严格没有 IP、MAC、发表时间、User-Agent、请求头、连接 ID、随机帖子 ID 等字段；应用代码也不读取 `RemoteAddr` 或转发 IP 头。
- Nginx access log 关闭，错误日志丢弃；Docker logging driver 为 `none`；应用只允许固定的启动/停止/fatal 类型码。
- 容器 root filesystem 只读；论坛数据单独挂载 `/data`，临时 TLS 与 `/tmp` 使用 tmpfs；Relay 只接入 internal network，只有 edge 接入可发布端口的 ingress bridge。

网络连接本身仍向 OS、路由器、AP、交换机、防火墙、DHCP 和 IDS/IPS 暴露实时 IP 与流量 metadata。应用不读取、关联或持久保存这些信息，但无法控制外部设备政策。

## 主机加固清单

Compose 已设置 `ulimits.core=0`。高保障专用主机还应先检查再由运维变更：

```bash
swapon --show
systemctl status systemd-coredump.socket
journalctl --disk-usage
docker inspect --format '{{.HostConfig.LogConfig.Type}}' chatroom-relay-1
```

评估禁用或加密 swap、禁用 hibernation/core/kdump、限制 journald、禁止 VM snapshot/EDR 内存采集。它们会降低诊断、休眠或灾难恢复能力，不应在共享主机上盲目修改。TLS 私钥是部署凭据，正常会持久保存；它不是 RoomKey 或聊天记录。

## 开发与验证

前端：

```bash
cd web
npm ci
npm run lint
npm run typecheck
npm test
npm run build
```

后端（要求 Go 1.24；依赖已 vendor 以便审计/离线构建）：

```bash
cd server
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
```

隐私与存储审计：

```bash
./scripts/zero-persistence-audit.sh
```

测试覆盖 Room 生命周期/房主离开随机转移、房主审批入房、拒绝与取消申请、空房销毁、后加入无历史、刷新身份工厂、3,544 人物名完整性、临时改名同步、服务新实例无恢复、公共板字段边界/持久化/逐条回复恢复、错/对密码与 challenge、AES-GCM/AAD、nonce 唯一与耗尽、replay、oversize、Origin 和连接限流。最终逐项证据见 [Security Audit](docs/SECURITY_AUDIT.md)。人物名的数据来源与生成方法见 [Character Names](docs/CHARACTER_NAMES.md)。

## 不包含的功能

没有账户、好友、私聊、永久昵称、音视频、除图片外的文件传输、永久房主、临时聊天室历史/离线消息、公共房间目录、搜索、审核数据库、管理后台、封禁、IP blacklist、analytics、Sentry、PWA、通知或 reconnect 恢复。公共吐槽板的帖子/回复持久保存，但没有删除、编辑或管理后台；邀请链接/QR 邀请、手动销房、每消息显示期限、WebRTC、语音仅保留在未来路线图。

## 运行限制

- 能修改服务器静态前端的管理员可向未来页面注入窃密代码；浏览器托管 E2EE 无法独自消除这个信任根。
- 被控浏览器、恶意扩展、键盘/屏幕采集和接收者保存均可得到明文。
- `Cache-Control: no-store`、删除 JS 引用和断电不能证明浏览器 GC、swap、hibernate、crash dump 或物理内存中没有残留。
- 完全不持久识别 IP/设备会限制长期反滥用能力；MVP 只做连接级限流、容量和大小限制，连接洪泛需要基础设施协助。
- 上线前应进行独立代码与密码学审计；本项目不面向生命安全或高对抗威胁场景。
