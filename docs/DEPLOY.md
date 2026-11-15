# 部署

## 0. 它是什么形状

一个可执行文件，加一个数据目录：

@@BT@@@@BT@@@@BT@@
/opt/imchat/
├── imchat              # 可执行文件（前端已经编进去了）
└── data/
    ├── imchat.db       # SQLite：用户、会话、消息、投递队列、索引
    └── files/          # 图片原始字节
@@BT@@@@BT@@@@BT@@

没有 Postgres，没有 Redis，没有 Node 运行时，没有反向依赖的服务。这是
[ADR-0001](adr/0001-sqlite-single-file-deployment.md) 的全部意义。

---

## 1. 交叉编译

在你自己的机器上编，目标机不需要装 Go：

@@BT@@@@BT@@@@BT@@bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o bin/imchat-linux-amd64 ./cmd/imchat
@@BT@@@@BT@@@@BT@@

ARM 服务器（Ampere、Graviton、树莓派）换 `GOARCH=arm64`。三条都要的话：

@@BT@@@@BT@@@@BT@@bash
make release        # 产出 linux/amd64、linux/arm64、windows/amd64
@@BT@@@@BT@@@@BT@@

**为什么能这样**：SQLite 驱动是 `modernc.org/sqlite`（纯 Go），关掉 CGO 照样跑。
换成 `mattn/go-sqlite3` 就得在目标机上装 gcc，整个"单文件"叙事当场破功。

---

## 2. 上传与目录

@@BT@@@@BT@@@@BT@@bash
ssh root@your-server 'mkdir -p /opt/imchat/data && useradd -r -s /usr/sbin/nologin imchat'
scp bin/imchat-linux-amd64 root@your-server:/opt/imchat/imchat
ssh root@your-server 'chmod +x /opt/imchat/imchat && chown -R imchat:imchat /opt/imchat'
@@BT@@@@BT@@@@BT@@

---

## 3. 创建管理员（**不要用 -seed**）

`-seed=true` 是本地演示用的，它会创建 `alice`/`bob`，密码都是 `password123`。
生产环境开着它等于把后台钥匙挂在门上。

@@BT@@@@BT@@@@BT@@bash
sudo -u imchat /opt/imchat/imchat \
  -db /opt/imchat/data/imchat.db \
  -create-user admin \
  -password '换成一个强密码' \
  -admin
@@BT@@@@BT@@@@BT@@

`-create-user` 建完账号就退出，不启动服务。重复执行会报 `UNIQUE constraint failed`，这是对的。

---

## 4. systemd

把 [deploy/imchat.service](../deploy/imchat.service) 放到 `/etc/systemd/system/`：

@@BT@@@@BT@@@@BT@@bash
sudo cp deploy/imchat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now imchat
sudo systemctl status imchat --no-pager
@@BT@@@@BT@@@@BT@@

服务只监听 `127.0.0.1:8080`，**不直接对外**。对外由反向代理负责。

---

## 5. 反向代理与 HTTPS

**这一步不是可选的。** 浏览器在 https 页面里禁止 `ws://` 连接；而且登录 cookie 需要
`Secure` 属性。把 http 直接暴露到公网，等于让每个用户的消息在链路上裸奔。

### Caddy（推荐，自动申请证书）

@@BT@@@@BT@@@@BT@@
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile   # 先改掉里面的域名
sudo systemctl reload caddy
@@BT@@@@BT@@@@BT@@

Caddy 默认就透传 WebSocket 的 `Upgrade` 头，不需要额外配置。

### Nginx（备选）

用 [deploy/nginx.conf](../deploy/nginx.conf) 里的 server 块。关键是这三行，
少了任何一行 WebSocket 都会握手失败、前端一直显示"已断开"：

@@BT@@@@BT@@@@BT@@nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
@@BT@@@@BT@@@@BT@@

---

## 6. 防火墙

@@BT@@@@BT@@@@BT@@bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
@@BT@@@@BT@@@@BT@@

8080 不需要对公网开放。

---

## 7. 备份

**不要直接 `cp imchat.db`。** WAL 模式下运行中的数据库主文件可能不含最新提交，
拷贝得到的是一份不一致的快照——恰恰在你最需要它的时候不可用。

用二进制自己提供的在线备份：

@@BT@@@@BT@@@@BT@@bash
sudo -u imchat /opt/imchat/imchat \
  -db /opt/imchat/data/imchat.db \
  -backup /var/backups/imchat/imchat-$(date +%F-%H%M).db
@@BT@@@@BT@@@@BT@@

它执行 `VACUUM INTO`：不停服、拿到一致的、不带 WAL 的完整快照。

定时任务：

@@BT@@@@BT@@@@BT@@cron
0 3 * * * imchat /opt/imchat/imchat -db /opt/imchat/data/imchat.db -backup /var/backups/imchat/imchat-$(date +%F).db
@@BT@@@@BT@@@@BT@@

图片目录另外同步（它不在数据库里）：

@@BT@@@@BT@@@@BT@@bash
rsync -a --delete /opt/imchat/data/files/ /var/backups/imchat/files/
@@BT@@@@BT@@@@BT@@

### 恢复

@@BT@@@@BT@@@@BT@@bash
sudo systemctl stop imchat
sudo -u imchat cp /var/backups/imchat/imchat-2026-11-15.db /opt/imchat/data/imchat.db
sudo rm -f /opt/imchat/data/imchat.db-wal /opt/imchat/data/imchat.db-shm
sudo systemctl start imchat
@@BT@@@@BT@@@@BT@@

删掉 `-wal`/`-shm` 很重要：它们是旧库的伴生文件，留着会和恢复出来的主文件打架。

---

## 8. 升级

@@BT@@@@BT@@@@BT@@bash
# 1. 先备份
sudo -u imchat /opt/imchat/imchat -db /opt/imchat/data/imchat.db -backup /var/backups/imchat/pre-upgrade.db
# 2. 替换二进制
sudo systemctl stop imchat
sudo cp bin/imchat-linux-amd64 /opt/imchat/imchat.new && sudo chmod +x /opt/imchat/imchat.new
sudo mv /opt/imchat/imchat.new /opt/imchat/imchat
# 3. 启动（schema 迁移自动执行）
sudo systemctl start imchat
sudo journalctl -u imchat -n 30 --no-pager
@@BT@@@@BT@@@@BT@@

Schema 版本记在 SQLite 的 `PRAGMA user_version` 里，启动时按版本号逐步升级。
当前是 v3（v1 基线 → v2 加 `recall_of` → v3 加 FTS5 索引）。

---

## 9. 排障

| 现象 | 原因 | 处理 |
|---|---|---|
| 502 Bad Gateway | 后端没起来或没监听 | `systemctl status imchat`、`curl 127.0.0.1:8080/healthz` |
| 页面能开，但一直"已断开" | 反代没透传 Upgrade 头 | 检查 Nginx 那三行；用 Caddy 则确认没开 `http2` 强制 |
| 登录成功后立刻 401 | cookie 是 `Secure` 但访问的是 http | 用 https 访问，或在反代上强制跳转 |
| `database is locked` | 有别的进程在写同一个 db | 确认只有一个实例在跑 |
| 磁盘慢慢变满 | `data/files/` 里的图片 | 没有自动清理，需要自己加策略 |

健康检查是 `GET /healthz`，返回里有 `outbox` 各状态计数和 `ws_conns`，可以直接接监控。

---

## 10. Docker（可选）

单文件哲学下 Docker 不是必需的，但如果你要统一编排：

@@BT@@@@BT@@@@BT@@dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/imchat ./cmd/imchat

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/imchat /imchat
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/imchat", "-addr", "0.0.0.0:8080", "-db", "/data/imchat.db", "-seed=false"]
@@BT@@@@BT@@@@BT@@

容器里必须用 `-addr 0.0.0.0:8080`，否则容器外连不进来。

---

## 11. Windows Server

Windows 上没有 systemd，用 NSSM 把二进制注册成服务：

@@BT@@@@BT@@@@BT@@powershell
nssm install imchat "C:\imchat\imchat.exe" "-addr 127.0.0.1:8080 -db C:\imchat\data\imchat.db -seed=false"
nssm set imchat AppDirectory "C:\imchat"
nssm set imchat Start SERVICE_AUTO_START
nssm start imchat
@@BT@@@@BT@@@@BT@@

反向代理同样可以放 IIS（需要装 ARR + WebSocket Protocol）或前置一台 Caddy。

---

## 12. 上线后第一件事

1. `curl https://你的域名/healthz` —— 确认返回 `"ok":true`
2. 用浏览器登录，开两个窗口互发一条 —— 确认 WebSocket 通
3. 点「模拟断网」，让对面发三条，重连后看是否显示"已补齐 3 条" —— 这一条过了，说明整条可靠性链路在真实网络下成立
4. 在管理台确认 `outbox` 里的 `done` 在增长 —— 说明 ack 真的在消解
