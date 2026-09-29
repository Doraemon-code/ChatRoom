# Privacy Architecture

本项目的可验证承诺是：**No application-level IP logging、No IP retention、No persistent identity；临时聊天室 No message persistence**。公共吐槽板是明确的持久化例外：应用文件只保存虚拟名字、留言和回复内容，不保存 IP、MAC、发表时间、User-Agent 或其他请求元数据。这不是“绝对匿名”或“无法追踪”的承诺。TCP/IP 连接的实时源地址仍对宿主内核、反向代理网络栈及局域网设备可见。

## Data inventory

| Data | Browser RAM | Relay RAM | Application disk | Lifetime / notes |
|---|---:|---:|---:|---|
| Message plaintext | Yes | No | No | React state 与加解密调用期间；刷新/关闭后引用消失 |
| Message ciphertext | Yes, transient | Yes, transient | No | Relay 验证后直接广播，不加入 Room 或队列历史；仅每连接有界发送通道短暂持有 |
| RoomKey | Yes | No | No | 32-byte 随机根密钥；房主批准入房时以 ECDH 封装转交给加入者，获批者本地副本不可撤回 |
| Derived SenderKey | Yes | No | No | non-extractable `CryptoKey`，页面生命周期 |
| Image bytes (plaintext) | Yes, transient | No | No | 压缩后的图片只在加密/解密调用期间存在，随后仅随消息列表留在浏览器 RAM |
| Image ciphertext | Yes, transient | Yes, transient | No | 作为大号 ciphertext frame 由 Relay 转发，不加入历史 |
| Room ID/config | Yes | Yes | No | 仅活动 Room；空房立即删除，无 tombstone |
| Ephemeral identity/nickname | Yes | Yes, current socket | No | 从离线人物表随机分配且可修改；每次页面装载重建，断线移除 |
| Message/replay ID | Yes | validation only | No | 客户端有界 RAM 去重；Relay 不建立集合 |
| Online members | Yes | Yes | No | presence 实时状态 |
| Public forum name/message/replies | Yes, transient | Yes, request-scoped | Yes, `/data/forum.json` | 跨刷新和服务重启保留；记录只有 `name`、`message`，回复列表中的元素也只有这两个字段 |
| Source IP | network stack | network stack only | No application retention | 应用不读取、不关联；外部基础设施可能另行记录 |
| TLS certificate/key | No | No | optional operator mount | 自动 fallback 只写 edge 容器 `/run/chat-tls` tmpfs；公司 CA 文件是部署凭据，不是聊天数据 |

## Browser RAM boundary

允许：临时身份、RoomKey、sender keys、当前收到/发送的明文消息、当前成员、nonce counter、当前会话 replay set。离开会清除 React state/ref 并关闭 socket；刷新创建新的 JavaScript realm 与身份。

禁止：Local/Session Storage、IndexedDB、Cookie、Cache API、WebSQL、OPFS、Service Worker、文件写入、analytics 与崩溃遥测。响应使用 `Cache-Control: no-store`，但 Web 应用不能对浏览器实现、操作系统 swap、崩溃恢复、扩展或内存取证作物理清除保证。

## Relay RAM boundary

临时聊天室唯一长于一次函数调用的内存业务结构是 `Map<roomId, Room>` 的 Go 等价结构。Room 只含 owner 连接、成员与待审批申请；Member 只含连接 ID、临时身份、发送通道和取消函数。聊天室不存在 message/history/account/IP/device/profile 字段。公共吐槽板由独立 Store 负责持久化线程；线程和回复只含名字与内容。

密文在 WebSocket read buffer、严格 decoder 输入与固定容量 outbound channel 中短暂存在。聊天室掉线或队列满不会转储到磁盘，也没有离线队列；论坛提交则同步写入单独的 `/data/forum.json`。最后成员离开时在同一清理路径同步删除 Room。

## Network and host boundary

TLS/WSS 保护 LAN 抓包者看不到应用帧内容，但不会隐藏 IP、端口、连接时间、长度和流量形态。路由器、AP、交换机、DHCP、DNS、防火墙、IDS/IPS、EDR、宿主 OS、虚拟化平台可能独立产生元数据；本应用不能替它们作承诺。

聊天室的“无应用落盘路径”也不等于 RAM 从未被主机写盘；论坛数据则按设计写入 `/data` volume。生产运维应评估 swap、hibernation、core/kdump、VM snapshot、journald 与容器日志。Compose 禁用 core dump 和 Docker logging、使用只读文件系统及 tmpfs；是否禁用宿主 swap/休眠必须由组织在可用性与取证政策下决定。应用 JSON 不写发表时间字段，但持久文件所在文件系统可能由宿主维护文件系统级时间属性；应用不读取或展示它们。

## External copies deliberately outside the guarantee

接收者截图、拍照、复制消息、修改客户端或保存网络密文无法被阻止。房间号经剪贴板、聊天软件、浏览器同步或截图传播后也可能被保留，产品不承诺撤回；被批准加入者解出的 RoomKey 同样无法远程收回。
