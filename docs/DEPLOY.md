# 部署

## 它是什么形状

一个可执行文件，加一个数据目录：

```
imchat            # 可执行文件（前端已编进去）
data/
├── imchat.db     # SQLite：用户、会话、消息、投递队列、全文索引
├── files/        # 图片原始字节
└── certs/        # 只有 -domain 模式才会生成
```

不需要 Postgres，不需要 Redis，不需要 Node 运行时，不需要 Caddy 或 Nginx。
这是 [ADR-0001](adr/0001-sqlite-single-file-deployment.md) 的意义。

---

## 三种模式，选一个

### 模式 A：本地或内网（零配置）

```bash
./imchat
```

它会依次做完：建库 → 建 `admin` 账号 → 生成随机密码 → 打印到终端 → 打开浏览器。

```
  ┌────────────────────────────────────────────────────────────┐
  │ imchat is running                                          │
  │                                                            │
  │ open    http://localhost:8080                              │
  │ data    data\imchat.db                                     │
  │                                                            │
  │ first-run account (shown once):                            │
  │   user  admin                                              │
  │   pass  mdXTgpEJVMUHNh                                     │
  └────────────────────────────────────────────────────────────┘
```

`http://localhost` 是浏览器认可的安全上下文，所以本地不需要 HTTPS，WebSocket 照常工作。

可选参数：

| 参数 | 作用 |
|---|---|
| `-addr 0.0.0.0:8080` | 让同局域网的其他设备也能访问（手机扫码演示很有用） |
| `-open=false` | 不自动开浏览器（装成服务时必须关） |
| `-seed` | 建演示账号 `alice`/`bob`（密码 `password123`），仅用于演示和测试 |
| `-db /path/imchat.db` | 换数据文件位置 |

### 模式 B：公网（一条命令带 HTTPS）

```bash
sudo ./imchat -domain im.example.com
```

它自己完成三件事：向 Let's Encrypt 申请证书、在 80 端口应答 HTTP-01 验证并把其余请求 301 到
HTTPS、用 443 提供服务。证书缓存在 `data/certs/`，到期自动续。

前提：

- 域名 A 记录已指向这台机器
- 80 和 443 可用（所以需要 root；也可以用 `setcap cap_net_bind_service=+ep ./imchat` 免 root）
- 端口被占用时可以改：`-acme-http :8080 -https :8443`

**不需要 Caddy，不需要 Nginx，不需要 certbot。**

### 模式 C：已经有反向代理

云负载均衡、K8s Ingress、已有的 Caddy/Nginx 里，用模式 A 启动，让它反代 `127.0.0.1:8080`。

Nginx 必须带这三行，少任何一行前端会一直显示"已断开"：

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```

完整示例见 [deploy/nginx.conf](../deploy/nginx.conf)，Caddy 见 [deploy/Caddyfile](../deploy/Caddyfile)。

---

## 编译

在你自己机器上交叉编译，目标机不需要装 Go：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o bin/imchat-linux-amd64 ./cmd/imchat
```

ARM 服务器换 `GOARCH=arm64`。三个平台一起出：`make release`。

**为什么能这样**：SQLite 驱动是 `modernc.org/sqlite`（纯 Go），关掉 CGO 照样跑。
换成 `mattn/go-sqlite3` 就得在目标机装 gcc，"一个文件"当场破功。

产物大小约 22 MB。

---

## 装成系统服务

```bash
sudo useradd -r -s /usr/sbin/nologin imchat
sudo mkdir -p /opt/imchat/data && sudo chown -R imchat:imchat /opt/imchat
sudo cp bin/imchat-linux-amd64 /opt/imchat/imchat && sudo chmod +x /opt/imchat/imchat
sudo cp deploy/imchat.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now imchat
sudo journalctl -u imchat -n 30 --no-pager
```

首次启动的随机密码只打印在 journal 里，记得马上去看，登录后改掉。

---

## 备份

**不要直接 `cp imchat.db`。** WAL 模式下运行中的主文件可能不含最新提交，拷出来的是不一致的快照。

```bash
sudo -u imchat /opt/imchat/imchat -db /opt/imchat/data/imchat.db -backup /var/backups/imchat/$(date +%F).db
```

它执行 `VACUUM INTO`：不停服，得到一致的、不带 WAL 的完整快照。定时任务：

```cron
0 3 * * * imchat /opt/imchat/imchat -db /opt/imchat/data/imchat.db -backup /var/backups/imchat/imchat-$(date +\%F).db
```

图片不在数据库里，要单独同步：

```bash
rsync -a --delete /opt/imchat/data/files/ /var/backups/imchat/files/
```

### 恢复

```bash
sudo systemctl stop imchat
sudo -u imchat cp /var/backups/imchat/2026-11-15.db /opt/imchat/data/imchat.db
sudo rm -f /opt/imchat/data/imchat.db-wal /opt/imchat/data/imchat.db-shm
sudo systemctl start imchat
```

删掉 `-wal`/`-shm` 很重要：它们是旧库的伴生文件，留着会和恢复的主文件打架。

---

## 升级

```bash
sudo -u imchat /opt/imchat/imchat -db /opt/imchat/data/imchat.db -backup /var/backups/imchat/pre-upgrade.db
sudo systemctl stop imchat
sudo cp bin/imchat-linux-amd64 /opt/imchat/imchat.new && sudo chmod +x /opt/imchat/imchat.new
sudo mv /opt/imchat/imchat.new /opt/imchat/imchat
sudo systemctl start imchat
```

Schema 版本记在 `PRAGMA user_version`，启动时按版本逐步升级。当前 v3。

---

## 排障

| 现象 | 原因 | 处理 |
|---|---|---|
| 502 Bad Gateway | 后端没起来 | `systemctl status imchat`、`curl 127.0.0.1:8080/healthz` |
| 页面能开但一直"已断开" | 反代没透传 Upgrade | 模式 C 检查那三行；模式 B 检查防火墙 |
| 登录后立刻 401 | cookie 是 Secure 但站点是 http | 用 https 访问 |
| `database is locked` | 两个实例指向同一个 db | 只留一个进程 |
| 磁盘慢慢满 | `data/files/` 里的图片 | 没有自动清理，需要自己定策略 |
| 证书申请失败 | 80 端口不通，或域名没解析 | `curl -I http://域名/.well-known/acme-challenge/x` 应返回 403 而不是超时 |

健康检查 `GET /healthz` 返回 `outbox` 各状态计数与 `ws_conns`，可以直接接监控。

---

## Docker（可选）

```dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/imchat ./cmd/imchat

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/imchat /imchat
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/imchat", "-addr", "0.0.0.0:8080", "-db", "/data/imchat.db", "-open=false"]
```

容器里必须 `-addr 0.0.0.0:8080` 且 `-open=false`，否则容器外连不进来、还会尝试启动浏览器。

---

## Windows

双击就能用——不需要服务化：

1. 拷 `imchat-windows-amd64.exe` 到任意目录
2. 双击
3. 控制台打印账号密码，浏览器自动打开

要装成开机自启的服务，用 NSSM：

```powershell
nssm install imchat "C:\imchat\imchat.exe" "-addr 127.0.0.1:8080 -db C:\imchat\data\imchat.db -open=false"
nssm set imchat AppDirectory "C:\imchat"
nssm set imchat Start SERVICE_AUTO_START
nssm start imchat
```

---

## 上线后自检

1. `curl https://你的域名/healthz` → `"ok":true`
2. 两个浏览器窗口互发一条 → WebSocket 通
3. 点「模拟断网」，让对面发三条，重连后应显示"已补齐 3 条"
4. 管理台看 `outbox` 的 `done` 在增长 → ack 确实在消解

第 3 条过了，才说明整条可靠性链路在真实网络下成立。
