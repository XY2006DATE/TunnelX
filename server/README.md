# TunnelX Server

TunnelX Server 部署在具有公网可达地址的机器上，负责客户端认证、代理审批、公网端口监听、流量转发和管理界面。控制协议与服务端 Dashboard 共用一个 TCP 端口。

## 编译和启动

在项目根目录执行：

```bash
make build-go
./bin/tunnelx-server server/server.yaml
```

程序的第一个位置参数是配置文件路径；不传参数时会读取当前工作目录下的 `server.yaml`。

## 配置

默认配置文件为 `server/server.yaml`：

```yaml
server:
  bind_addr: 0.0.0.0
  bind_port: 7100
auth:
  method: token
  token: replace-with-a-long-random-token
tls:
  enable: false
  cert_file: ""
  key_file: ""
log:
  level: info
  file: server.log
  max_size: 100
  max_backups: 3
dashboard:
  enable: true
  port: 7100
  password_file: dashboard.password
port_pool:
  start: 3001
  end: 20000
heartbeat_timeout: 90
```

| 配置项 | 说明 |
| --- | --- |
| `server.bind_addr` | 控制端口和代理端口的监听地址 |
| `server.bind_port` | 控制协议与 Dashboard 的共享 TCP 端口 |
| `auth.token` | 客户端认证令牌；为空会允许任意客户端连接，不建议公网使用 |
| `tls.enable` | 是否对共享端口启用 TLS |
| `tls.cert_file` / `tls.key_file` | TLS 证书链和私钥路径 |
| `log.level` | `debug`、`info`、`warn` 或 `error` |
| `log.file` | 日志文件；相对路径以进程工作目录为准 |
| `dashboard.enable` | 是否启用服务端 Dashboard |
| `dashboard.password_file` | Dashboard 密码哈希文件 |
| `port_pool.start` / `port_pool.end` | 动态审批时可分配的公网端口范围 |
| `heartbeat_timeout` | 客户端心跳超时秒数 |

`dashboard.port` 是兼容字段。当前运行时 Dashboard 始终与 `server.bind_port` 共用端口，页面修改端口后需要重启服务端才能切换监听端口。

## Dashboard

启用后访问：

```text
http://服务器地址:7100
```

启用 TLS 后改用 `https://`。首次访问需要设置至少 8 位的管理员密码，哈希会写入 `dashboard.password_file`。Dashboard 可用于：

- 查看连接、代理和流量统计
- 审批或拒绝 TCP/UDP 代理申请
- 修改代理公网端口
- 删除代理或客户端配置
- 轮换连接 Token 和管理密码

管理界面与控制端口默认监听所有网卡。公网部署时应通过防火墙、反向代理或安全组限制管理访问来源。

## 需要开放的端口

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| `server.bind_port` | TCP | 客户端控制/工作连接和 Dashboard |
| `port_pool` 范围 | TCP/UDP | 动态分配的公网代理端口 |
| `80` | TCP | HTTP 虚拟主机，仅配置 HTTP 代理时使用 |

只开放实际使用的代理端口。HTTP 虚拟主机监听 80 端口，在 Linux 上通常需要 root、`CAP_NET_BIND_SERVICE` 或端口转发。

## TLS

配置证书后启用：

```yaml
tls:
  enable: true
  cert_file: /etc/tunnelx/tls/fullchain.pem
  key_file: /etc/tunnelx/tls/private.key
```

客户端可以在 Dashboard 中直接填写 `https://域名:端口/`，或设置 `tls.enable: true`。使用公共 CA 证书时客户端可将 `tls.ca_file` 留空并使用操作系统可信根；使用私有 CA 时则应填写对应 CA 文件。客户端会校验证书链和服务端域名。

## systemd 示例

```ini
[Unit]
Description=TunnelX Server
After=network-online.target
Wants=network-online.target

[Service]
User=tunnelx
Group=tunnelx
WorkingDirectory=/etc/tunnelx
ExecStart=/usr/local/bin/tunnelx-server /etc/tunnelx/server.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

建议让配置文件、私钥和密码文件只对运行用户可读。

## 常见问题

- 客户端认证失败：确认两端 Token 完全一致，并检查是否连接到了正确端口。
- Dashboard 无法访问：确认 `dashboard.enable` 为 `true`，并检查共享端口、防火墙及 HTTP/HTTPS 协议。
- 代理审批失败：确认目标公网端口未占用，并位于端口池范围内。
- HTTP 代理启动失败：检查 80 端口占用情况以及进程绑定低端口的权限。
- 客户端频繁离线：检查网络质量，并让 `heartbeat_timeout` 大于客户端心跳间隔。
