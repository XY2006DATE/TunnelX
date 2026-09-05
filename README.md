# TunnelX

TunnelX 是一个使用 Go 编写的内网穿透工具。它通过部署在公网的服务端，将外部 TCP、UDP 或基于域名的 HTTP 流量转发到内网客户端上的服务，并提供服务端与客户端管理界面。

> 当前项目适合自托管和功能验证。上线到公网前，请务必更换认证令牌、启用 TLS、限制管理界面来源并配置防火墙。

## 功能

- TCP 端口映射与 UDP 端口映射
- 基于 `Host` 的 HTTP 虚拟主机转发
- Token 客户端认证和可选 TLS 传输
- 客户端断线重连、心跳检测和多服务端会话
- 服务端审批代理申请、端口池分配和流量统计
- 服务端、客户端 Web Dashboard 及 Tauri 桌面壳
- Linux、macOS、Windows 的 amd64/arm64 CLI 发布包

客户端 Dashboard 目前只支持申请 TCP/UDP 代理；HTTP 虚拟主机需要在 YAML 中预先配置。

## 工作方式

```text
外部访问者 -> TunnelX Server 公网监听端口
                         |
                         | 控制连接 / 工作连接
                         v
               TunnelX Client -> 内网本地服务
```

服务端的控制协议和 Dashboard 共用 `server.bind_port`。客户端向服务端申请代理后，管理员在服务端 Dashboard 中批准并分配公网端口；批准结果会写入客户端配置并在重启后恢复。

## 环境要求

- Go 1.21 或更高版本
- Node.js 与 npm（仅修改或重新构建 Dashboard 时需要）
- Rust、Tauri 2 及对应系统依赖（仅构建桌面安装包时需要）

## 快速开始

### 1. 获取并编译

```bash
git clone https://github.com/XY2006DATE/TunnelX.git
cd TunnelX
make build
```

`make build` 会先构建两个 React Dashboard，再生成：

```text
bin/tunnelx-server
bin/tunnelx-client
```

如果仓库内嵌的 Dashboard 静态资源已经是最新版本，也可以只编译 Go 程序：

```bash
make build-go
```

### 2. 配置服务端

编辑 `server/server.yaml`，至少替换 Token：

```yaml
server:
  bind_addr: 0.0.0.0
  bind_port: 7100
auth:
  method: token
  token: replace-with-a-long-random-token
dashboard:
  enable: true
  port: 7100
  password_file: dashboard.password
port_pool:
  start: 3001
  end: 20000
```

启动服务端：

```bash
./bin/tunnelx-server server/server.yaml
```

访问 `http://服务器地址:7100`，首次打开时设置至少 8 位的管理密码。

### 3. 启动客户端

```bash
./bin/tunnelx-client client/client.yaml
```

访问 `http://客户端地址:7101` 并设置管理密码。在客户端页面填写服务端地址、端口、Token 和本地服务端口，提交 TCP 或 UDP 代理申请，然后到服务端 Dashboard 审批。

更完整的配置、端口与安全说明见：

- [服务端文档](server/README.md)
- [客户端文档](client/README.md)

## 从 YAML 预配置代理

客户端也可以在启动前直接配置代理：

```yaml
server_addr: 203.0.113.10
server_port: 7100
auth:
  token: replace-with-the-server-token
proxies:
  - name: ssh
    type: tcp
    local_ip: 127.0.0.1
    local_port: 22
    remote_port: 10022
  - name: dns
    type: udp
    local_ip: 127.0.0.1
    local_port: 53
    remote_port: 10053
  - name: website
    type: http
    local_ip: 127.0.0.1
    local_port: 8080
    custom_domains:
      - tunnel.example.com
```

HTTP 虚拟主机固定监听服务端的 80 端口，因此需要相应权限、防火墙规则和指向服务端的 DNS 记录。

## 多平台打包

默认一次生成 Linux、macOS、Windows 的 amd64/arm64 服务端和客户端压缩包：

```bash
./package.sh --version 0.1.0
```

发布物位于 `dist/`，并生成 `SHA256SUMS`。可按需选择目标或重新构建内嵌 Dashboard：

```bash
./package.sh --version 0.1.0 --targets "linux/amd64 windows/amd64"
./package.sh --version 0.1.0 --build-web
```

构建当前操作系统的 Tauri 桌面安装包：

```bash
./package.sh --version 0.1.0 --desktop
```

Tauri 安装包必须在对应操作系统上原生构建；`--desktop` 不会跨系统生成安装器。

## 项目结构

```text
.
├── client/                 客户端、客户端 Dashboard 与桌面壳
├── common/                 配置、协议、认证、TLS 和通用组件
├── server/                 服务端、代理实现、Dashboard 与桌面壳
├── testdata/               集成测试配置和测试站点
├── Makefile                日常构建入口
└── package.sh              多平台发布脚本
```

## 测试

```bash
make test
```

## 许可证

[MIT](LICENSE)
