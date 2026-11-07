# TunnelX

TunnelX 是一个使用 Go 编写的内网穿透工具。它通过部署在公网的服务端，将外部 TCP、UDP、HTTP 或 HTTPS 流量转发到内网客户端上的服务，并提供服务端与客户端管理界面。

> 当前项目适合自托管和功能验证。上线到公网前，请务必更换认证令牌、启用 TLS、限制管理界面来源并配置防火墙。

## 功能

- TCP 端口映射与 UDP 端口映射
- 公网 HTTPS 端口的服务端 TLS 终止，解密后穿透到客户端本地 HTTP 服务
- 基于 `Host` 的 HTTP 虚拟主机转发
- Token 客户端认证和可选 TLS 传输
- 客户端断线重连、心跳检测和多服务端会话
- 一台客户端可同时转发本机和局域网内多台设备的服务
- 服务端审批代理申请、端口池分配和流量统计
- 服务端、客户端 Web Dashboard 及 Tauri 桌面壳
- Linux、macOS、Windows 的 amd64/arm64 CLI 发布包

客户端 Dashboard 支持申请 HTTPS、TCP 和 UDP 代理；基于域名的 HTTP 虚拟主机仍需在 YAML 中预先配置。

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

访问 `http://服务器地址:7100`，首次打开时设置至少 8 位的管理密码。可以在“设置”中配置 HTTPS 开关、证书链和私钥路径；保存后重启服务端生效。

### 3. 启动客户端

```bash
./bin/tunnelx-client client/client.yaml
```

访问 `http://客户端地址:7101` 并设置管理密码。在客户端页面填写完整服务地址、Token 和本地服务端口，提交 HTTPS、TCP 或 UDP 代理申请，然后到服务端 Dashboard 审批。

更完整的配置、端口与安全说明见：

- [服务端文档](server/README.md)
- [客户端文档](client/README.md)

## 通过 WebSocket 和 HTTPS 反向代理连接

客户端的“完整服务地址”现在支持 `https://tunnel.example.com/proxy` 或 `wss://tunnel.example.com/proxy`。HTTPS/HTTP 地址包含非根路径时自动使用 WSS/WS；显式 `ws://`、`wss://` 未填写路径时使用 `/proxy`。原有不带路径的 `http://主机:7100`、`https://主机:7100` 继续使用原生 TCP/TLS 直连。

服务端默认在控制端口 `7100` 上提供 `/proxy`，与管理界面共用端口；关闭 Dashboard 后 WebSocket 入口仍可使用。可在服务端 YAML 中修改路径：

```yaml
websocket:
  path: /proxy
```

已有 Nginx HTTPS 网站时，将 [WebSocket 反向代理示例](examples/nginx-websocket.conf) 中的 `location` 放进现有 HTTPS `server` 块。示例由 Nginx 终止公网 TLS，再转发到同机 `http://127.0.0.1:7100/proxy`，对应 TunnelX 服务端 `tls.enable: false`。若 TunnelX 的 `7100` 本身启用了 TLS，则需配置带证书验证的 HTTPS 上游。Nginx 必须转发 WebSocket 的 `Upgrade` 和 `Connection` 头，详见 [Nginx 官方说明](https://nginx.org/en/docs/http/websocket.html)。外部路径不同也可以在反向代理中重写到服务端的 `/proxy`。

客户端申请代理时直接填写完整入口，例如 `https://tunnel.example.com/proxy`，再填写服务端 Token、内网目标地址、目标端口和业务代理类型。控制连接、TCP/UDP 数据连接均通过该 WebSocket 入口，完整路径会随审批配置和待同步删除记录保存，重启后自动恢复。WSS 始终验证服务器证书；使用私有 CA 时，可在客户端 YAML 的 `tls.ca_file` 中指定 CA 证书。

YAML 预配置也支持完整地址：

```yaml
proxies:
  - name: web
    server_addr: wss://tunnel.example.com/proxy
    server_token: replace-with-the-server-token
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8080
    remote_port: 18080
```

WebSocket 封装的是客户端与服务端之间的隧道连接，业务端口仍需在服务器上放行。外部使用者访问 TCP/UDP 业务时，应使用源站公网 IPv4 或直连域名及分配端口；这些业务端口不会自动变成 CDN 的网页路径。`HTTPS → HTTP` 业务代理仍按原有规则使用服务端 TLS 证书，与控制通道是否通过 WSS 无关。

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

## 安装包

在 [GitHub Releases](https://github.com/XY2006DATE/TunnelX/releases) 下载 macOS 客户端：Apple 芯片选 `arm64`，Intel 芯片选 `x86_64`。把 TunnelX 拖进“应用程序”后打开。关闭窗口会把 TunnelX 收到菜单栏，现有隧道会继续运行；从菜单栏选择 `Open TunnelX` 可恢复窗口，选择 `Quit TunnelX` 会停止隧道并退出。

申请代理时，目标设备填写本机的 `127.0.0.1`，或 NAS、另一台电脑等设备的局域网 IP/主机名。服务端审批后，Windows、Linux、macOS、iOS 和 Android 上的应用可通过分配到的公网地址使用对应的服务；访问端使用原有协议即可。

只构建 macOS 客户端：

```bash
./build-macos.sh --client-only --arch arm64
./build-macos.sh --client-only --arch x86_64
```

客户端的 `.app`、`.dmg` 和 SHA-256 校验文件在 `dist/macos-client-<架构>/`。

在 macOS 上可以一次构建 Client/Server 的 `.app` 和 `.dmg`，共四个产物：

```bash
./build-macos.sh
# 或者
make build-macos
```

默认构建当前 Mac 的原生架构，也可显式传入 `--arch arm64` 或 `--arch x86_64`。产物会写入 `dist/macos-<架构>/`。

客户端和服务端的当前发布版本统一为 `0.2.0`。

Windows 桌面版的客户端和服务端首次点击右上角关闭按钮时，会询问“隐藏到托盘”还是“退出程序”。勾选“记住选择”后，下次直接执行所选操作；不勾选则每次询问，也可以取消本次关闭。登录前同样可以使用提示。

在“设置 → 关闭窗口时”可随时切换“每次询问”“隐藏到系统托盘”或“退出程序”，修改立即生效并跨重启保留。隐藏到托盘时后台隧道继续运行，点击托盘图标或右键选择“打开”恢复窗口；选择“退出程序”或托盘菜单中的“退出”会停止对应后端。标题栏的最小化按钮仍会将窗口最小化到任务栏。

在 Windows 上构建客户端和服务端的 x64 桌面安装包及 CLI 便携包：

```powershell
./build-windows.ps1
```

如果原输出目录中的安装包正在使用，可指定另一个 `dist` 子目录，例如 `./build-windows.ps1 -OutputDirectory dist/windows-x64-new`。

需要 Go、Node.js/npm、Rust MSVC 工具链和 Visual Studio C++ 构建工具。产物位于 `dist/windows-x64/`，包括两个 NSIS `setup.exe`、两个 CLI ZIP 和 `SHA256SUMS`。CLI ZIP 解压后运行 `Run-Client.cmd` 或 `Run-Server.cmd`。可用 `-Arch arm64` 选择 ARM64（需要相应的 MSVC 交叉编译工具），已安装 npm 依赖时可传 `-SkipInstall`。

构建 Client/Server 的 Debian 安装包。每个包都包含后台服务、桌面入口和 Web Dashboard。

```bash
./build-deb.sh --arch amd64
# 或者
make build-deb
```

Debian 产物会写入 `dist/deb-<架构>/`，分别使用客户端和服务端各自的版本号，并附带 `SHA256SUMS`。构建需要 Linux 上的 Go、Node.js/npm 和 `dpkg-deb`；Windows 可在 WSL 中运行。脚本默认重新构建 Web Dashboard，已有最新静态资源时可传 `--skip-web`。

在存放安装包的目录使用 APT 安装并启动服务。以 amd64 服务端为例。

```bash
sudo apt install ./TunnelX-Server-0.2.0-linux-amd64.deb
sudo systemctl enable --now tunnelx-server
```

安装客户端时选择 Client 安装包并启动 `tunnelx-client`。服务启动后，从 Linux 应用菜单打开 **TunnelX Server** 或 **TunnelX Client**，也可以直接在浏览器里打开管理页。默认管理端口分别为 7100 和 7101。桌面窗口和浏览器使用同一个服务与管理密码，首次打开时设置密码。修改过端口或启用了 HTTPS 时，在桌面窗口地址栏填入对应的本机地址。关闭窗口后，后台服务继续运行。

APT 会为桌面入口安装 GTK 3 和 WebKitGTK 4.1 依赖。云服务器可以直接通过浏览器访问服务端管理页。配置和运行状态分别位于 `/var/lib/tunnelx-server/`、`/var/lib/tunnelx-client/`，日志通过 `journalctl -u tunnelx-server` 或 `journalctl -u tunnelx-client` 查看。重新安装保留配置和密码，`dpkg --purge` 清除对应组件的状态。

Tauri 的 `.app` 和 `.dmg` 必须在 macOS 上构建。

## 项目结构

```text
.
├── client/                 客户端、客户端 Dashboard 与桌面壳
├── common/                 配置、协议、认证、TLS 和通用组件
├── server/                 服务端、代理实现、Dashboard 与桌面壳
├── Makefile                日常构建入口
├── build-macos.sh          macOS App/DMG 打包脚本
└── build-deb.sh            Debian 安装包脚本
```

## 测试

```bash
make test
```

发布产物的端到端测试：

```powershell
python scripts/smoke-test.py --client <客户端可执行文件> --server <服务端可执行文件>
./scripts/test-windows.ps1
```

冒烟测试使用临时配置，验证密码设置和登录、申请及审批代理、TCP/UDP 实际转发和重启恢复。Windows 安装测试验证 NSIS 安装/卸载、桌面窗口及内嵌后端启动、关闭窗口后隐藏且后端继续响应，并恢复已有注册表项和配置文件。请先退出现有 TunnelX 进程。托盘图标恢复窗口和菜单主动退出需另外进行桌面交互验证。

`scripts/test-window-preferences.cjs` 使用 Playwright 在无界面浏览器中，以模拟 Tauri 桥验证首次关闭提示、取消/记住选择、设置即时保存、页面重载和写入失败处理；不会操作本机正在运行的 TunnelX。运行需要可用的 `playwright` 模块和 Edge（也可用 `TUNNELX_PLAYWRIGHT_PATH` 指定模块路径）。Rust 的 `cargo test --release --lib` 还会验证默认询问、偏好跨重启保留和保存失败时不更改当前行为。

在用于测试的 Ubuntu/Debian 环境中，以 root 运行 `bash scripts/test-deb.sh`，验证 amd64 deb 的安装、systemd 服务、重新安装保留状态及独立卸载。脚本会拒绝覆盖已有 TunnelX 安装，完成后移除测试安装。

## 许可证

[MIT](LICENSE)
