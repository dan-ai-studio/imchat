# imchat

一个**不依赖任何外部服务**的即时通信系统：一个可执行文件、一个 SQLite 文件、一个浏览器标签页。

```
./imchat
# open http://127.0.0.1:8080  →  alice / bob, password123
```

它要回答的不是"怎么画一个聊天框"，而是：

> 当连接会断、进程会崩、同一个请求会到达两次，一套 IM 凭什么说自己没有丢消息？

## 它承诺什么

本项目只承诺**至少一次**投递，并把每一条承诺钉在一个具体机制上（[ADR-0004](docs/adr/0004-at-least-once-delivery.md)）：

| 性质 | 机制 | 可验证的位置 |
|---|---|---|
| 不丢 | 消息与待投递记录同事务提交；只有客户端 ack 才消解；未 ack 的超时重投 | `outbox` 表 + `RepumpStale` |
| 不重 | 客户端生成 `client_msg_id`，服务端唯一索引兜底，重发返回既有消息 | `idx_messages_client` |
| 不乱序 | 会话序号由服务端在写事务内分配，客户端不得自造 | `UPDATE ... RETURNING next_seq` |
| 能补齐 | `hello` 帧携带 `last_seq`，服务端回补缺口 | `/messages?after=N` |

**不做恰好一次。** 跨进程不存在恰好一次；这里"看起来像恰好一次"，是幂等写入 + 客户端按
`(conversation_id, seq)` 去重 + 序号单调三者叠加出的假象。

## 架构

```
浏览器 ──WebSocket──┐
                    ├─► ws.Hub ──► Conn（每个终端一条）
浏览器 ──WebSocket──┘      ▲
                           │ subscribe user.<id>
                     ┌─────┴─────┐
                     │   NATS    │  进程内嵌入，不开 JetStream
                     └─────▲─────┘
                           │ publish
                     ┌─────┴──────┐
                     │ Dispatcher │  每 100ms 扫一次到期的待投递记录
                     └─────▲──────┘
                           │ due outbox
                     ┌─────┴──────┐
                     │   SQLite   │  单个写 goroutine，WAL，一个文件
                     └────────────┘
```

三条边界值得注意：

1. **SQLite 是唯一真相源。** broker 是内存的，重启即丢；未确认的待投递记录会在重启后重投，
   所以丢的是"缓存"，不是"事实"。
2. **写是单 goroutine。** 所有写事务排队通过一个 writer，取号与落库因此同事务，
   会话序号**不可能**出现空洞——这是设计，不是巧合。
3. **broker 划出的是替换边界，不是吞吐边界。** 换独立 NATS 集群只改连接配置。

## 快速开始

```
# 需要 Go 1.24+
go build -o bin/imchat.exe ./cmd/imchat
./bin/imchat.exe                      # 默认 127.0.0.1:8080，数据在 ./data/imchat.db
```

首次启动会种下演示数据：`alice` / `bob`，密码都是 `password123`，以及一个两人会话。
开两个浏览器（或一个普通窗口 + 一个隐身窗口）分别登录即可互发消息。

## 验证

自动化测试直接断言那四条承诺：

```
go test ./internal/store/ -v
```

- `TestReliability`：40 条消息，无丢失、无重复行、无乱序、无空洞；重发幂等
- `TestConcurrentSendsKeepSequenceDense`：8 个 goroutine 并发写 80 条，序号仍然稠密
- `TestOutboxResolvesOnlyOnAck`：投递只有 ack 才消解；未 ack 会被重投

端到端（需要 Node 22+）：

```
go build -o bin/imchat.exe ./cmd/imchat
./bin/imchat.exe -addr 127.0.0.1:18081 -db ./data/e2e.db &
node scripts/e2e.mjs
```

它会登录两个账号、各自建立 WebSocket、发一条消息、断言双方都收到、bob 回 ack，
最后检查 outbox 从 pending 变为 done。

## 目录

```
cmd/imchat/          进程入口：装配 store / bus / hub / api，优雅关闭
internal/api/        HTTP 路由与 WebSocket 升级，唯一写路径 SendMessage
internal/auth/       bcrypt + 服务端 session（无状态 JWT 在这里是负债）
internal/store/      SQLite：单写者、schema、消息序号、outbox
internal/bus/        进程内 NATS，仅做扇出
internal/ws/         连接、帧协议、Hub
internal/delivery/   outbox → bus 的投递器
docs/adr/            设计决策记录
GLOSSARY.md          领域词汇表（先读这个）
```

## 设计决策

- [ADR-0001 单机单文件：SQLite + Go 单二进制](docs/adr/0001-sqlite-single-file-deployment.md)
- [ADR-0002 实时通道：原生 WebSocket + 自定义帧](docs/adr/0002-native-websocket.md)
- [ADR-0003 嵌入 broker 作为扇出总线](docs/adr/0003-embedded-broker.md)
- [ADR-0004 投递承诺：至少一次](docs/adr/0004-at-least-once-delivery.md)

## 管理台

平台管理员（演示数据里的 `alice`）登录后，侧栏出现「管理台」入口，也可以直接访问 `/#/admin`。

它能做三件事：看（用户、会话、消息检索、投递队列）、管账号（封禁 / 解封）、管连接（踢下线）。
封禁会同时删掉该用户的 session 并断开其全部 WebSocket，所以下一次请求就是 401。

权限模型是**两套正交的主体**，代码里分别由两个不同的检查实现，互不推导：

| 主体 | 作用域 | 代码位置 |
|---|---|---|
| Platform Admin | 账号、连接、存储、指标 | `requireAdmin` 中间件 |
| Conversation Role | 禁言、成员、会话信息 | `RoleOf` 查询 |

平台管理员不能在后台点"禁言"，群主也不能在群里点"踢下线"——因为它们根本不是同一种权力。

## 协议

客户端 → 服务端：`hello` / `send` / `ack` / `read` / `ping`
服务端 → 客户端：`ready` / `msg` / `sent` / `pong` / `error`

```
// 客户端发送
{"type":"send","client_msg_id":"uuid","conversation_id":1,"kind":"text","body":"hi"}
// 客户端确认收到（这是唯一让待投递记录消解的帧）
{"type":"ack","conversation_id":1,"seq":7}
// 服务端下发
{"type":"msg","conversation_id":1,"seq":7,"id":"...","sender_id":2,"kind":"text","body":"hi","created_at":1763...}
```

## 状态

已完成：

- 存储层：单写者、会话序号、outbox
- 认证：bcrypt + 服务端 session
- HTTP API、WebSocket 传输、投递器
- 前端：登录、聊天、断线补齐、管理台
- 可靠性测试与端到端脚本

未开始：图片消息、撤回、@提醒、FTS5 搜索、群聊创建与群管理。
