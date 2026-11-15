# 备份、健康检查与恢复演练

- 日期：2026-11-16
- 对应票：#34 产品级收尾

## 每日备份

进程内定时器：启动先做一次，之后每 24 小时一次，写到 `-backup-dir`（默认 `backups/`），
文件名 `imchat-YYYY-MM-DD.db`，保留最近七份。

```bash
./imchat -db data/imchat.db -backup-dir backups
```

一次性快照仍然可用：

```bash
./imchat -db data/imchat.db -backup ./snap.db   # 写完退出
```

备份用 SQLite 的 VACUUM INTO，得到的是一个自洽的单文件，不需要停服务。

## 健康检查

`GET /healthz` 反映依赖状态，不是永远 200：

| 情况 | 状态码 | 说明 |
|---|---|---|
| 数据库可读 | 200 | `ok: true`，附 outbox 深度、连接数、schema 版本 |
| 数据库不可读 | 503 | `ok: false`，只回错误与 schema 版本 |
| 媒体未配置 | 200 | `media: "disabled"`，可选依赖不算不健康 |

四组数：依赖状态（bus/delivery/media）、投递深度（outbox）、连接数（ws_conns）、
schema 版本（schema）。

## 恢复演练

2026-11-16 实测，开发机 Windows，SQLite：

1. 起服务、注册一个账号、发一条消息；
2. `-backup ./snap.db` 导出快照；
3. 停服务，用快照替换主库，重启；
4. 账号与消息都在，`/healthz` 回 200，schema 版本一致。

记录：**通过**。快照包含 schema v12 的全部表，恢复后无需迁移。

## 没做的

- 异地备份与加密（当前是单机部署，先把「有备份且能恢复」做实）。
- 自动化恢复演练（现在是人工按上面四步跑一遍）。
