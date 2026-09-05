# TunnelX Client

TunnelX Client 运行在内网机器上，维护到一个或多个公网 TunnelX Server 的控制连接，并把服务端收到的流量转发到本机服务。客户端可以通过 Web Dashboard 申请 TCP/UDP 代理，也可以使用 YAML 预配置 TCP、UDP 和 HTTP 代理。

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
3. 填写服务端地址、控制端口、认证 Token、本地端口和代理类型。
4. 提交申请后，在服务端 Dashboard 中审批并指定公网端口。
5. 审批成功后，代理会写入客户端 YAML；重启客户端后自动恢复。

客户端 Dashboard 当前支持申请 TCP 和 UDP 代理。它默认监听 `0.0.0.0`，应使用主机防火墙限制访问来源。

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
| `heartbeat.timeout` | 客户端侧保留的超时配置 |
| `dashboard.port` | 客户端 Dashboard 监听端口 |
| `dashboard.password_file` | Dashboard 密码哈希文件 |
| `proxies` | 已批准或预配置的代理列表 |

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

域名需要解析到服务端公网地址。服务端会在 80 端口读取 HTTP `Host` 并转发到对应代理；当前不提供 HTTPS 虚拟主机终止。

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
  ca_file: /etc/tunnelx/tls/ca.pem
```

生产环境应提供正确的 CA 文件。`ca_file` 为空时客户端会跳过服务端证书校验，无法防止中间人攻击。

## 桌面应用

客户端桌面壳位于 `client/clientdashboard`。构建当前操作系统的安装包：

```bash
./package.sh --desktop
```

也可以单独构建：

```bash
cd client/clientdashboard
npm ci
npm run tauri build
```

Tauri 会把客户端 Go 程序作为 sidecar 启动，并把首次使用的配置复制到系统应用配置目录。

## 常见问题

- 无法连接服务端：确认公网地址、共享控制端口、Token 和防火墙规则。
- 本地服务连接失败：确认 `local_ip:local_port` 在客户端机器上可访问。
- 重启后代理消失：检查客户端是否有权限写入配置文件。
- TLS 握手失败：确认两端同时启用 TLS，并检查证书域名和 CA 文件。
- Dashboard 端口占用：修改 `dashboard.port` 后重启客户端。
