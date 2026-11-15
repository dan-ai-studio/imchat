# imchat

一个**不依赖任何外部服务**的即时通信系统：一个可执行文件、一个 SQLite 文件、一个浏览器标签页。

```bash
./imchat                                       # 本地：自动建库、建管理员、打印密码、打开浏览器
sudo ./imchat -domain im.example.com           # 公网：自己向 Let's Encrypt 申请证书，自带 HTTPS
```

不需要反向代理，不需要数据库，不需要运行时。

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

```bash
# 需要 Go 1.24+
go build -o bin/imchat.exe ./cmd/imchat
./bin/imchat.exe
```

首次启动会创建一个 `admin` 账号，密码随机生成、**只打印一次**，浏览器自动打开：

```
  │ first-run account (shown once):  │
  │   user  admin                    │
  │   pass  mdXTgpEJVMUHNh           │
```

要演示双人对话，加 `-seed` 会额外建 `alice` / `bob`（密码 `password123`）和一个两人会话。

## 功能

- **单聊与群聊**：群可建、可加人、可移出；群主不可被移出
- **会话内治理**：owner / admin / member 三种角色，禁言带过期时间
- **消息**：文本、图片（二进制落盘，消息表只存文件名）、撤回
- **@提醒**：服务端解析 `@username` 写入消息的 `meta.mentions`，前端高亮
- **已读位点**：按设备记录，用户级取各端最大值
- **断线补齐**：重连时按 `last_seq` 拉缺口，界面提示补了多少条
- **全文搜索**：FTS5 索引，中英文都能命中
- **管理台**：用户、会话、消息检索、投递队列、封禁与踢下线

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

```jsonc
// 客户端发送（kind 为 text 或 image；图片消息的 body 是上传返回的文件名）
{"type":"send","client_msg_id":"uuid","conversation_id":1,"kind":"text","body":"@Bob ping"}
// 客户端确认收到（这是唯一让待投递记录消解的帧）
{"type":"ack","conversation_id":1,"seq":7}
// 服务端下发
{"type":"msg","conversation_id":1,"seq":7,"id":"...","sender_id":2,"kind":"text",
 "body":"@Bob ping","meta":"{\"mentions\":[2]}","created_at":1763...}
```

撤回不是协议帧，而是一条普通消息：`kind=recall`，`recall_of` 指向被撤回的 seq
（[ADR-0005](docs/adr/0005-recall-as-tombstone.md)）。它因此复用同一套序号、投递与补齐。

## 验证

```bash
go test ./internal/store/ -v
```

- `TestReliability`：40 条消息，无丢失、无重复行、无乱序、无空洞；重发幂等
- `TestConcurrentSendsKeepSequenceDense`：8 个 goroutine 并发写 80 条，序号仍然稠密
- `TestOutboxResolvesOnlyOnAck`：投递只有 ack 才消解；未 ack 会被重投
- `TestRecallAppendsTombstone`：撤回追加墓碑，原消息不被修改；越权与重复撤回被拒
- `TestGroupLifecycleAndMute`：群创建、成员、移出、禁言、角色
- `TestCleanCloseLeavesSingleFile`：干净退出后目录里只有一个 `.db` 文件

端到端（需要 Node 22+，会真的走 WebSocket、上传、撤回、搜索）：

```bash
go build -o bin/imchat.exe ./cmd/imchat
./bin/imchat.exe -addr 127.0.0.1:18081 -db ./data/e2e.db -seed -open=false &
node scripts/e2e.mjs
```

## 压测

```bash
go build -o bin/loadtest.exe ./cmd/loadtest
./bin/loadtest.exe -base http://127.0.0.1:18095 -workers 16 -per 50
```

一次本机实测（Windows，SQLite 单写者，含 bcrypt session 校验）：

| 指标 | 值 |
|---|---|
| 并发写入 | 16 |
| 消息数 | 800 |
| 失败 | 0 |
| 吞吐 | 452 msg/s |
| p50 / p95 / p99 | 34 / 48 / 55 ms |
| 落库后序号密度 | 1000 条，first=1 last=1000，无空洞无重复 |

这组数字不是"我们很快"的声明，而是"并发压上去，序号仍然稠密"的证据。
单写者在这里不是瓶颈的辩解，而是这个性质的来源。

## 目录

```
cmd/imchat/          进程入口：装配 store / bus / hub / api，优雅关闭
internal/api/        HTTP 路由、WebSocket 升级、上传与文件服务，唯一写路径 SendMessage
internal/auth/       bcrypt + 服务端 session（无状态 JWT 在这里是负债）
internal/store/      SQLite：单写者、schema 迁移、消息序号、outbox、FTS5
internal/bus/        进程内 NATS，仅做扇出
internal/ws/         连接、帧协议、Hub
internal/delivery/   outbox → bus 的投递器
web/                 Vue 3 + Vite 前端，构建产物通过 go:embed 编进二进制
docs/adr/            设计决策记录
GLOSSARY.md          领域词汇表（先读这个）
```

## 部署

三种模式：

| 场景 | 命令 |
|---|---|
| 本地 / 内网 | `./imchat` —— 自动建库、建管理员、打印密码、开浏览器 |
| 公网 | `sudo ./imchat -domain im.example.com` —— 自己申请证书，自带 HTTPS |
| 已有反代 | `./imchat` + 反代 `127.0.0.1:8080` |

详细步骤（systemd、备份、升级、排障、Docker、Windows）见 [docs/DEPLOY.md](docs/DEPLOY.md)。

备份用 `-backup`，**不要直接 `cp` 那个 .db 文件**——WAL 模式下拷出来的快照可能不一致。

## 设计决策

- [ADR-0001 单机单文件：SQLite + Go 单二进制](docs/adr/0001-sqlite-single-file-deployment.md)
- [ADR-0002 实时通道：原生 WebSocket + 自定义帧](docs/adr/0002-native-websocket.md)
- [ADR-0003 嵌入 broker 作为扇出总线](docs/adr/0003-embedded-broker.md)
- [ADR-0004 投递承诺：至少一次](docs/adr/0004-at-least-once-delivery.md)
- [ADR-0005 撤回是追加，不是修改](docs/adr/0005-recall-as-tombstone.md)

## 已知限制

- 单写者决定了写并发上限；这是刻意的取舍，不是待修的问题
- `hello` 帧目前只用于握手，缺口补齐由客户端在 `open` 时主动调用 HTTP 接口完成
- FTS5 使用 unicode61 分词，中文按字切分，长句检索精度不如专门的 CJK 分词器
- 单机部署，没有多实例与横向扩展的运行时证据（只有设计论述）
