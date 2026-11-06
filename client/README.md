# TunnelX Client

TunnelX Client 运行在内网机器上，维护到一个或多个公网 TunnelX Server 的控制连接，并把服务端收到的流量转发到本机服务。客户端可以通过 Web Dashboard 申请 HTTPS、TCP 或 UDP 代理，也可以使用 YAML 预配置代理。

## 编译和启动

在项目根目录执行：

```bash
make build-go
./bin/tunnelx-client client/client.yaml
```

程序的第一个位置参数是配置文件路径；不传参数时会读取当前工作目录下的 `client.yaml`。指定文件不存在时，客户端会创建一份启用 Dashboard 的默认配置。

## Dashboard 申请流程

1. 启动客户端并访问 `http://客户端地址:7101`。
2. 首次访问设置至少 8 位的管理密码。
3. 填写完整服务地址、认证 Token、目标设备地址和端口，以及代理类型。例如 `https://tunnel.example.com:7100/` 会自动解析控制端口并启用 TLS。目标是本机时使用 `127.0.0.1`；目标是其他设备时，填写客户端能访问的 IP 或主机名，如 `192.168.1.20`。端口单独填写。
4. 提交申请后，在服务端 Dashboard 中审批并指定公网端口。
5. 审批成功后，代理会写入客户端 YAML；重启客户端后自动恢复。

客户端 Dashboard 支持同时申请多个目标的 HTTPS、TCP 和 UDP 代理。命令行版 Dashboard 默认监听 `0.0.0.0`，应使用主机防火墙限制访问来源；桌面客户端的管理界面只在本机监听。

UDP 保留每个数据报的边界，要求客户端和服务端都升级至 0.2.0 或更新版本。更新服务端可在对应版本的源码目录执行 `make build-go`，再用新生成的 `bin/tunnelx-server` 和原有配置启动。

## 配置

```yaml
client_id: ""
server_addr: 203.0.113.10
server_port: 7100
auth:
  token: replace-with-the-server-token
tls:
  enable: false
  ca_file: ""
log:
  level: info
  file: client.log
heartbeat:
  interval: 30
  timeout: 90
dashboard:
  enable: true
  port: 7101
  password_file: dashboard.password
proxies: []
```

| 配置项 | 说明 |
| --- | --- |
| `client_id` | 客户端唯一标识；为空时首次启动自动生成并保存 |
| `server_addr` / `server_port` | 默认服务端地址和共享控制端口 |
| `auth.token` | 默认服务端认证令牌 |
| `tls.enable` | 是否使用 TLS 连接服务端 |
| `tls.ca_file` | 用于验证服务端证书的 CA 文件 |
| `heartbeat.interval` | 心跳发送间隔秒数 |
| `heartbeat.timeout` | 控制连接无响应多久后重连（秒）；如小于等于心跳间隔，则使用三倍心跳间隔 |
| `dashboard.port` | 客户端 Dashboard 监听端口 |
| `dashboard.password_file` | Dashboard 密码哈希文件 |
| `proxies` | 已批准或预配置的代理列表 |

Dashboard 中显式填写 `https://` 或 `tls://` 会为该服务端会话启用 TLS，填写 `http://` 或 `tcp://` 会使用明文连接；未写协议时继承全局 `tls.enable`。获批代理会把该选择保存到 `server_tls`，重启后仍使用相同传输方式。

相对路径以客户端进程工作目录为准。运行时会以 `0600` 权限保存配置，但仍应避免把真实 Token、密码文件或日志提交到版本库。

## 代理示例

### TCP

```yaml
proxies:
  - name: ssh
    type: tcp
    local_ip: 127.0.0.1
    local_port: 22
    remote_port: 10022
```

外部用户连接 `服务器地址:10022`，流量会转发到客户端的 `127.0.0.1:22`。

### UDP

```yaml
proxies:
  - name: dns
    type: udp
    local_ip: 127.0.0.1
    local_port: 53
    remote_port: 10053
```

### HTTPS 公网端口转发到本地 HTTP

本地服务为 `http://127.0.0.1:8080/doc.html` 时，在 Dashboard 选择“HTTPS → 本地 HTTP”并申请本地端口 8080。服务端管理员将公网端口指定为 8080 后，从公网访问：

```text
https://tunnel.example.com:8080/doc.html
```

对应的客户端 YAML 代理配置为：

```yaml
proxies:
  - name: local-web
    type: https
    local_ip: 127.0.0.1
    local_port: 8080
    remote_port: 8080
```

HTTPS 在 TunnelX Server 上使用其 `tls.cert_file` / `tls.key_file` 完成 TLS 握手，解密后的 HTTP 请求再转发到本地 8080。本地服务不需要配置证书。这个公网端口只接受 HTTPS；服务端必须启用 TLS，证书必须覆盖访问域名，公网端口必须位于 `port_pool` 范围内并已在防火墙或安全组中开放。

### HTTP 虚拟主机

```yaml
proxies:
  - name: website
    type: http
    local_ip: 127.0.0.1
    local_port: 8080
    custom_domains:
      - tunnel.example.com
```

域名需要解析到服务端公网地址。服务端会在 80 端口读取 HTTP `Host` 并转发到对应代理。需要 HTTPS 时可使用上面的“HTTPS → 本地 HTTP”类型。

## 多服务端

每条代理都可以覆盖默认服务端信息：

```yaml
proxies:
  - name: office-ssh
    type: tcp
    server_addr: server-a.example.com
    server_port: 7100
    server_token: token-for-server-a
    local_ip: 127.0.0.1
    local_port: 22
    remote_port: 10022
  - name: lab-api
    type: tcp
    server_addr: server-b.example.com
    server_port: 7100
    server_token: token-for-server-b
    local_ip: 127.0.0.1
    local_port: 8080
    remote_port: 18080
```

客户端会为不同的 `server_addr:server_port` 建立独立会话。

## TLS

```yaml
tls:
  enable: true
  ca_file: ""
```

`ca_file` 为空时客户端使用操作系统的可信根证书，适用于 Let's Encrypt 等公共 CA 签发的证书。使用私有 CA 时，将 `ca_file` 设置为对应 CA 的 PEM 文件。客户端始终校验证书链和服务端域名。

## 桌面应用

从 [GitHub Releases](https://github.com/XY2006DATE/TunnelX/releases) 下载对应架构的 macOS DMG。开发者可在仓库根目录构建：

```bash
./build-macos.sh --client-only --arch native
```

点击黄色最小化按钮可把窗口收到 Dock；点击红色关闭按钮或按 `⌘W` 会把窗口收到菜单栏。隧道在两种情况下都会继续转发。要恢复窗口，点菜单栏图标并选 `Open TunnelX`；要停止隧道，选 `Quit TunnelX` 或在窗口打开时按 `⌘Q`。Mac 睡眠时无法持续转发，唤醒后客户端会重连。

Tauri 会随 App 启动 Go 客户端。配置保存在 `~/Library/Application Support/cn.xcloudy.tunnelx.client/`，升级时会沿用已有配置。首次转发到局域网设备时，如 macOS 提示访问本地网络，请允许；也可在“系统设置 → 隐私与安全性 → 本地网络”管理权限。

## 常见问题

- 无法连接服务端：确认公网地址、共享控制端口、Token 和防火墙规则。
- 本地服务连接失败：确认 `local_ip:local_port` 在客户端机器上可访问。
- 重启后代理消失：检查客户端是否有权限写入配置文件。
- TLS 握手失败：确认两端同时启用 TLS，并检查证书域名和 CA 文件。
- Dashboard 端口占用：修改 `dashboard.port` 后重启客户端。
