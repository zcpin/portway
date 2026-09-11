# SSH Tunnel

本地 SSH 隧道管理工具。把一个远端端口映射到本地，常用于安全地访问内网数据库、缓存等不对外暴露的服务。

与上一版不同，本项目**不使用 WebView / Wails**：核心逻辑跑在一个无界面的 Go 守护进程里，界面是独立的 Flutter 桌面客户端（Windows / macOS / Linux 原生窗口），两者通过本机 HTTP + WebSocket 通信。

## 架构

```
Flutter 客户端（原生窗口）  ──HTTP / WebSocket──▶  Go daemon  ──SSH──▶  远端主机
      本地运行                    仅监听 127.0.0.1            隧道目标
```

这样切分的好处：

- **无 WebView 依赖**：不需要 WebView2 Runtime，也没有 alpha 版框架的风险
- **隧道不随界面退出**：关掉客户端窗口，daemon 仍在维持隧道
- **界面可独立演进**：换 UI 技术栈不影响隧道逻辑

## 目录结构

```
ssh-tunnel/
├── daemon/                    # Go 守护进程（无界面）
│   ├── cmd/ssh-tunnel/        # 入口
│   ├── internal/
│   │   ├── app/               # 业务门面：隧道、配置、密钥、日志缓冲
│   │   ├── server/            # HTTP API 与 WebSocket Hub
│   │   ├── config/            # 配置解析与读写
│   │   ├── manager/           # 多隧道管理
│   │   ├── tunnel/            # 单隧道：SSH 连接与端口转发
│   │   └── logger/            # 日志
│   ├── pkg/sshutil/
│   └── tools/wscheck/         # 调试工具：验证事件推送
└── client/                    # Flutter 桌面客户端
    └── lib/
        ├── models.dart
        ├── providers.dart     # 状态管理
        ├── services/          # 服务发现与 daemon 通信
        ├── pages/             # 隧道 / SSH 连接 / 密钥 / 日志
        └── widgets.dart
```

## 快速开始

### 1. 启动 daemon

```bash
cd daemon
go build -o bin/ssh-tunnel-daemon.exe ./cmd/ssh-tunnel
./bin/ssh-tunnel-daemon.exe -config ssh-tunnel.toml
```

首次运行会在配置路径不存在时自动创建一份最小配置。

### 1b. 开机自启（可选）

两种方式，按需要选一种即可：

```bash
# 用户级自启：随当前用户登录启动，无需管理员权限（桌面场景推荐）
ssh-tunnel-daemon autostart enable -config ~/.ssh-tunnel/config.toml
ssh-tunnel-daemon autostart status
ssh-tunnel-daemon autostart disable

# 系统服务：无需登录即可运行、崩溃自动重启，但需要管理员/root 权限
ssh-tunnel-daemon service install -config ~/.ssh-tunnel/config.toml
ssh-tunnel-daemon service start
ssh-tunnel-daemon service status
ssh-tunnel-daemon service stop
ssh-tunnel-daemon service uninstall
```

| | 用户级自启 | 系统服务 |
|---|---|---|
| 权限要求 | 无需管理员 | 需要管理员 / root |
| 运行身份 | 当前用户 | LocalSystem / root |
| 未登录时运行 | 否 | 是 |
| 崩溃自动重启 | 否 | 是 |
| 服务发现文件 | `~/.ssh-tunnel/daemon.json` | 系统公共目录（Windows 为 `%ProgramData%\ssh-tunnel`） |

> 两种方式的运行身份不同，服务发现文件也会写到不同位置。客户端会**依次检查全部候选位置并逐个探活**，取第一个真正有响应的，因此无论用哪种方式都能自动连上，也能跳过 daemon 被强制结束后残留的陈旧文件。

daemon 启动后会把连接信息写到发现文件：

```json
{
  "host": "127.0.0.1",
  "port": 54483,
  "token": "c480cb03...",
  "pid": 43224,
  "version": "dev",
  "config_path": "/home/me/.ssh-tunnel/config.toml"
}
```

端口默认由系统分配（`-addr 127.0.0.1:0`），避免多实例冲突；token 每次启动重新生成。客户端读取该文件完成自动连接，无需手工配置。

**服务发现的候选位置**（按优先级）：

| 顺序 | 位置 | 何时写入 |
|---|---|---|
| 1 | `$SSH_TUNNEL_DATA_DIR/daemon.json` | 显式指定时（见下） |
| 2 | `<用户主目录>/.ssh-tunnel/daemon.json` | 前台运行 / 用户级自启 |
| 3 | Windows：`%ProgramData%\ssh-tunnel\daemon.json`<br>Linux / macOS：`/var/lib/ssh-tunnel/daemon.json` | 系统服务模式 |

设置 `SSH_TUNNEL_DATA_DIR` 可显式指定目录，daemon 与客户端都会以它为准（无需是管理员权限，便于自定义部署）：

```bash
SSH_TUNNEL_DATA_DIR=/opt/ssh-tunnel-data ssh-tunnel-daemon
SSH_TUNNEL_DATA_DIR=/opt/ssh-tunnel-data ./ssh_tunnel_client
```

> 系统公共服务目录下的发现文件以 `0755` / `0644` 权限写入，因为服务进程以 LocalSystem / root 运行，而客户端以普通用户身份读取。该文件包含访问令牌，因此同机其他用户也能读到它——在单用户机器上无碍，多用户机器上若不希望其他用户控制隧道，请改用用户级自启。

### 2. 启动客户端

```bash
cd client
flutter run -d windows        # 或 macos / linux
```

客户端会自动发现并连接 daemon。若 daemon 未运行，界面会提示启动命令。

### 3. 配置隧道

复制 `daemon/ssh-tunnel.example.toml` 为 `ssh-tunnel.toml`，或在客户端界面中添加 SSH 连接与隧道。

## 典型场景：本地连远端 MySQL

私钥**不上传、不复制**，配置里只记录本地文件路径（支持绝对路径、`~` 开头、相对路径）：

```toml
[[ssh_connections]]
name = "prod-server"
host = "example.com:22"
user = "root"
key_file = "~/.ssh/id_ed25519"

[[tunnels]]
name = "mysql-prod"
ssh_connection = "prod-server"
local_port = 13306
remote_host = "127.0.0.1"
remote_port = 3306
```

启动后即可连接本地端口：

```bash
mysql -h 127.0.0.1 -P 13306 -u your_user -p
```

**主机密钥校验**：默认按 `~/.ssh/known_hosts` 校验远端主机密钥（与 `ssh` 命令一致）。若目标主机还不在该文件里，先执行：

```bash
ssh-keyscan example.com >> ~/.ssh/known_hosts
```

也可以在 SSH 连接上设置 `known_hosts_file` 指定其它文件，或在可信网络中设置 `host_key_check = "insecure"` 关闭校验。

## 客户端托盘

关闭窗口时会弹出确认，可选：

- **收进托盘** —— 程序继续在后台运行，隧道不受影响；点击托盘图标或菜单「显示窗口」恢复
- **退出程序** —— 完全关闭客户端（daemon 与隧道仍在运行）

托盘菜单会显示当前运行中的隧道数量。

## 命令行

```
ssh-tunnel-daemon                      前台运行
ssh-tunnel-daemon autostart <动作>     用户级开机自启
ssh-tunnel-daemon service <动作>       系统服务托管
```

`autostart` 动作：`enable` / `disable` / `status`
`service` 动作：`install` / `uninstall` / `start` / `stop` / `restart` / `status`

| 参数 | 说明 |
|---|---|
| `-config` | 配置文件路径 |
| `-addr` | 监听地址，默认 `127.0.0.1:0`（端口由系统分配） |
| `-no-auth` | 关闭 token 认证（仅限可信本机环境） |
| `-log-level` | 覆盖日志级别：`debug/info/warn/error` |
| `-hide-console` | 隐藏控制台窗口（开机自启自动带上） |
| `-version` | 显示版本 |

## API

所有接口前缀 `/api`，除 `/api/health` 外都需要在请求头携带 `X-Auth-Token: <token>`（或 `Authorization: Bearer <token>`）。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查（免认证） |
| GET | `/api/tunnels` | 隧道列表（含运行状态） |
| POST | `/api/tunnels` | 新增隧道 |
| PUT | `/api/tunnels/{name}` | 更新隧道 |
| DELETE | `/api/tunnels/{name}` | 删除隧道 |
| POST | `/api/tunnels/{name}/start` | 启动 |
| POST | `/api/tunnels/{name}/stop` | 停止 |
| POST | `/api/tunnels/{name}/restart` | 重启 |
| GET/POST/PUT/DELETE | `/api/ssh-connections[/{name}]` | SSH 连接增删改查 |
| GET | `/api/keys` | 配置中引用的私钥列表（含是否存在） |
| GET | `/api/keys/stat?path=<路径>` | 校验私钥路径对 daemon 是否可读 |
| GET | `/api/logs` | 日志缓冲 |
| GET | `/api/status` | 所有隧道的运行状态（`{名称: 是否运行}`） |
| POST | `/api/reload` | 重新加载配置 |
| GET | `/ws` | WebSocket 事件流 |

写接口的请求体上限为 1 MiB，字段名写错会直接返回 400。

WebSocket 事件格式：

```json
{"type": "status", "status": {"mysql-prod": true}}
{"type": "log", "log": {"timestamp": "...", "level": "info", "message": "...", "tunnel": "mysql-prod"}}
```

调试事件推送可用：

```bash
cd daemon
go build -o bin/wscheck.exe ./tools/wscheck
./bin/wscheck.exe ws://127.0.0.1:<port>/ws <token>
```

## 安全模型

daemon **只监听回环地址**，并且：

- 每次启动生成随机 token，客户端从发现文件读取
- 刻意**不设置 CORS 响应头**——浏览器中的恶意页面无法跨域访问本服务，而原生客户端不受同源策略限制
- 私钥只记录路径、不复制文件，daemon 按运行身份读取，因此私钥权限需对 daemon 的运行身份开放
- SSH 主机密钥默认按 `~/.ssh/known_hosts` 校验；可在 `ssh_connections`（或直连配置的 `tunnels`）上设置 `host_key_check = "insecure"` 关闭校验，或用 `known_hosts_file` 指定其它文件
- 隧道建立后每 30 秒发送一次 SSH keepalive，探测 NAT 超时、对端崩溃等僵死连接；掉线后按 `reconnect_strategy` 自动重连

## 开发

```bash
# daemon：构建、静态检查、测试（CI 中带 -race 运行）
cd daemon
go build ./...
go vet ./...
go test ./...

# client：依赖、静态检查、测试
cd client
flutter pub get
dart analyze
flutter test
```

每次 push / PR 会由 `.github/workflows/ci.yml` 执行以上检查（Go 侧含 `gofmt` 校验与 `-race` 测试）。

## 已知限制

- 同一发现位置只允许一个 daemon 实例（`daemon.json` 为单文件，后启动的会覆盖）；需要并行多实例时用 `SSH_TUNNEL_DATA_DIR` 各自指定目录
- 系统服务模式下发现文件需对所有本地用户可读，令牌因此对本机其他用户可见（详见「服务发现的候选位置」）
- 私钥以明文路径记录，不做复制；daemon 以运行身份读取该文件，因此权限需对运行身份开放（系统服务模式下是 LocalSystem / root）
- Windows 构建需要带 `PROGRAMFILES` 系列环境变量的终端环境（VS Build Tools）
