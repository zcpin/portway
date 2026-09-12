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

> 两种方式的运行身份不同，服务发现文件也会写到不同位置。客户端会**依次检查全部候选位置并校验 token**，取第一个通过认证的实例；地址无效、进程已退出或 token 已过期的条目都会被跳过。

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

端口默认由系统分配（`-addr 127.0.0.1:0`），避免多实例冲突；token 每次启动重新生成。客户端读取该文件完成自动连接，无需手工配置，也支持 IPv6 回环地址（如 `-addr [::1]:0`）。

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

客户端会自动发现并连接 daemon。若 daemon 未运行，且客户端旁带有
`ssh-tunnel-daemon.exe`（发布版已随程序分发），客户端会**自动拉起它**并等待就绪；
仅用 `flutter run` 调试时，会自动向上查找源码仓库里的 `daemon/bin/ssh-tunnel-daemon.exe`。
拉起后 daemon 独立常驻：关闭或退出客户端都不影响隧道。
WebSocket 连接恢复后，客户端会自动同步完整隧道列表与运行状态，包括离线期间的增删改。

顶部实例菜单可以选择自动发现的 daemon，或创建独立工作区。工作区配置与发现文件放在 `~/.ssh-tunnel/workspaces/<工作区标识>/`，名称和当前选择保存在 `~/.ssh-tunnel/workspaces.json`。新工作区从空配置启动，可再从「设置」导入配置。选定实例离线时保持该选择；需要切换时可手动选择其他实例或「自动选择实例」。切换会关闭旧客户端连接并重新加载列表、密钥、日志和设置，不停止原 daemon 的隧道。

### 3. 配置隧道

复制 `daemon/ssh-tunnel.example.toml` 为 `ssh-tunnel.toml`，或在客户端界面中添加 SSH 连接与隧道。

隧道可引用已有 SSH 连接，也可手动填写 SSH 主机、用户和私钥路径。编辑已有配置时会保留 `host_key_check`、`known_hosts_file` 和私钥路径；重连策略可选择「跟随全局设置」，重连间隔留空也会保留全局继承。

隧道卡片显示连接中、已连接、重连中、失败或停止，以及最近错误、重试次数和连接时间。SSH 连接编辑器的「测试连接」会测试当前填写的配置并显示结果，不需要先保存，也不会启动端口转发。

隧道支持分组、搜索和勾选后批量启停，结果会逐条展示。卡片菜单的「复制配置」会生成新名称，并选择一个尚未被配置占用的本地端口，确认后保存。`group` 为可选分组；`auto_start = false` 可关闭随 daemon 启动时自动连接，仍能手动启动，修改该开关不会中断当前连接。旧配置缺省时自动启动。

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

> 重连字段（`reconnect_strategy` / `reconnect_interval` / `max_reconnect_attempts`）可以省略：
> 此时使用**全局默认值**，见 `ssh-tunnel.example.toml` 顶部注释，或在客户端「设置」页里修改。

启动后即可连接本地端口：

```bash
mysql -h 127.0.0.1 -P 13306 -u your_user -p
```

**主机密钥校验**：默认按 `~/.ssh/known_hosts` 校验远端主机密钥（与 `ssh` 命令一致）。若目标主机还不在该文件里，先执行：

```bash
ssh-keyscan example.com >> ~/.ssh/known_hosts
```

也可以在 SSH 连接上设置 `known_hosts_file` 指定其它文件，或在可信网络中设置 `host_key_check = "insecure"` 关闭校验。

SSH 连接和直连隧道编辑器均可选择私钥文件或 **SSH agent** 认证（`auth_method = "agent"`，可选 `agent_socket`）。Windows 默认连接 OpenSSH 的 `\\.\pipe\openssh-ssh-agent`，Linux/macOS 使用 `SSH_AUTH_SOCK`。daemon 必须能访问其运行身份下的 agent。

带口令的私钥可在编辑器或「密钥」页解锁。口令只用于本次解锁，解析后的密钥保存在 daemon 内存；daemon 重启后需要再次解锁。锁定影响后续认证，已建立的连接继续运行，原私钥文件不会被改写。

编辑器的「查看主机指纹」只探测目标主机公钥；经过跳板时先按配置认证跳板。确认信任后会再次核对指纹再写入指定 known_hosts，并启用校验。主机密钥变化时需要明确选择「替换并信任」，其他主机的信任记录会保留。

### 转发类型与跳板

`mode` 缺省为 `local`，保持原来的本地端口转发。`remote` 把远端监听端口转发到本机目标；`dynamic` 提供 SOCKS5 CONNECT 代理，支持 IPv4、IPv6 和由 SSH 服务端解析的域名，不支持 UDP。`local_host` 缺省为 `127.0.0.1`，本地和动态代理监听仅允许回环地址。

```toml
[[tunnels]]
name = "reverse-web"
mode = "remote"
ssh_connection = "prod-server"
local_host = "127.0.0.1"
local_port = 8080
remote_host = "127.0.0.1"
remote_port = 18080

[[tunnels]]
name = "socks-proxy"
mode = "dynamic"
ssh_connection = "prod-server"
local_port = 1080

[[ssh_connections]]
name = "internal-host"
host = "10.0.0.2:22"
user = "alice"
key_file = "~/.ssh/id_ed25519"
proxy_jump = ["prod-server"]
```

`proxy_jump` 可设置在 SSH 连接或直连隧道上，按顺序引用 SSH 连接名称；编辑器中每行填写一个名称。引用连接自身的跳板链会展开，重复、循环、缺失引用以及超过 8 跳的链会被拒绝。每个跳板使用自身的认证和主机校验配置；连接超时覆盖完整链，停止隧道时会关闭所有跳板。反向监听是否允许由远端 SSH 服务的转发策略决定。

## 客户端托盘

关闭窗口时会弹出确认（可在「设置」页改成默认直接收进托盘或直接退出），可选：

- **收进托盘** —— 程序继续在后台运行，隧道不受影响；点击托盘图标或菜单「显示窗口」恢复
- **退出程序** —— 完全关闭客户端（daemon 与隧道仍在运行）

托盘菜单会显示当前运行中的隧道数量。

## 客户端设置与关于

左侧导航除隧道管理外，还有「设置」与「关于」两个页面：

- **设置**：
  - 关闭窗口行为的默认动作（客户端本地偏好，存于 `~/.ssh-tunnel/client_settings.json`）
  - 隧道重连的**全局默认值**（重连策略 / 间隔 / 最大次数 / 日志级别），写入 daemon 配置，
    未单独配置这些字段的隧道回退使用；保存后受影响的运行中隧道会重启以应用新配置
  - 日志级别在保存或重新加载配置成功后立即生效；无效写入请求会在保存前被拒绝，写盘失败会保留原配置和运行状态
- **关于**：程序简介、快速上手步骤、GitHub 仓库地址

「设置 → 配置导入与备份」支持导出 TOML、读取文件或粘贴配置。导入上限为 256 KiB，可合并并覆盖同名项，或替换完整配置；必须先预览再确认。如果预览后配置被修改，需要重新预览。每次成功应用前都会在配置文件旁的 `<配置文件名>.backups` 目录保存原文件；恢复备份同样经过预览，并会再次备份当前配置。备份列表显示最近 100 项，文件不会自动清理。

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
| `-log-level` | 启动时覆盖日志级别：`debug/info/warn/error` |
| `-hide-console` | 隐藏控制台窗口（开机自启自动带上） |
| `-version` | 显示版本 |

## API

所有接口前缀 `/api`，除 `/api/health` 外都需要在请求头携带 `X-Auth-Token: <token>`（或 `Authorization: Bearer <token>`）。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查（免认证） |
| GET | `/api/tunnels` | 隧道列表（含运行状态） |
| POST | `/api/tunnels` | 新增隧道 |
| POST | `/api/tunnels/batch` | `action=start/stop`、`names`；返回每条隧道的成功/错误，重复操作幂等 |
| PUT | `/api/tunnels/{name}` | 更新隧道 |
| DELETE | `/api/tunnels/{name}` | 删除隧道 |
| POST | `/api/tunnels/{name}/start` | 启动 |
| POST | `/api/tunnels/{name}/stop` | 停止 |
| POST | `/api/tunnels/{name}/restart` | 重启 |
| GET/POST/PUT/DELETE | `/api/ssh-connections[/{name}]` | SSH 连接增删改查 |
| POST | `/api/ssh-connections/test` | 测试待保存 SSH 配置，返回 `ok`、`elapsed_ms` 和错误 |
| POST | `/api/ssh-connections/host-key` | 查看目标主机指纹，跳板按配置认证，目标不发送认证凭据 |
| POST | `/api/ssh-connections/trust` | 确认 `connection`、`fingerprint`，变更密钥需 `replace=true` |
| GET | `/api/keys` | 配置中引用的私钥列表（含是否存在） |
| GET | `/api/keys/stat?path=<路径>` | 校验私钥路径对 daemon 是否可读 |
| POST | `/api/keys/unlock` | 使用 `path`、`passphrase` 解锁私钥，口令不保存 |
| POST | `/api/keys/lock` | 锁定 `path` 对应的内存私钥 |
| GET | `/api/logs` | 日志缓冲 |
| GET | `/api/status` | 所有隧道的运行状态（`{名称: 是否运行}`） |
| POST | `/api/reload` | 重新加载配置 |
| GET | `/api/config` | 全局配置（日志级别、重连默认值） |
| PUT | `/api/config` | 更新全局配置（未单独配置的隧道会套用新默认值并重启） |
| GET | `/api/config/export` | 导出 TOML 和当前修订号 |
| POST | `/api/config/preview` | 校验 `content`、`mode=merge/replace` 并预览变更 |
| POST | `/api/config/import` | 使用预览返回的 `revision` 导入并先备份，版本冲突返回 409 |
| GET | `/api/config/backups[/{name}]` | 列出备份或读取指定备份，读取后可按替换模式预览/导入 |
| GET | `/ws` | WebSocket 事件流 |

写接口的请求体上限为 1 MiB，字段名写错会直接返回 400。

WebSocket 事件格式：

```json
{"type": "status", "status": {"mysql-prod": true}}
{"type": "log", "log": {"timestamp": "...", "level": "info", "message": "...", "tunnel": "mysql-prod"}}
```

每次连接或重连 `/ws` 时，daemon 会主动发送 `snapshot` 事件，其 `snapshot` 字段与 `/api/tunnels` 的完整列表格式相同（没有隧道时为 `[]`），随后发送兼容旧客户端的 `status` 事件。配置增删改及重新加载也会推送快照；周期状态广播仍只在状态变化时发送。

`runtime` 事件以隧道名为键，包含 `is_running`、`state`、`last_error`、`retry_count`、`connected_at`；这些字段也包含在列表快照中。`is_running` 表示运行任务仍存活，`state=connected` 表示 SSH 连接和转发监听均已建立。详细状态变化最多在下一次 2 秒轮询时推送。

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
- TCP 拨号与 SSH 握手共用 30 秒超时；隧道建立后每 30 秒发送一次 SSH keepalive，单次响应最多等待 30 秒，超时后关闭连接并按 `reconnect_strategy` 自动重连

## 开发

扩展计划及逐项实施进度见 [扩展实施记录](docs/extension-roadmap.md)。

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

每次 push / PR 会由 `.github/workflows/ci.yml` 执行以上检查（Go 侧含 `gofmt` 校验与 `-race` 测试），以及发布版本解析和 Release 发布逻辑的测试。脚本测试也可在仓库根目录运行：

```powershell
pwsh -NoProfile -File scripts/test_release_version.ps1
```

```bash
bash scripts/test_release_publish.sh
```

Windows 发布任务还会编译安装器并测试自启动路径匹配。安装 Inno Setup 6 后，可运行 `pwsh -NoProfile -File scripts/test_autostart_cleanup.ps1`；该测试使用临时载荷，不执行产品安装或卸载，也不修改自启动注册表。

## 打包安装包（Windows）

一键脚本会同时产出**安装包**与**便携 zip** 两种形式：

```bash
scripts/build_windows.bat [版本号]
```

产物在 `dist/`：

- `ssh-tunnel-setup-<版本>.exe` —— Inno Setup 安装程序
- `ssh-tunnel-portable-<版本>.zip` —— 便携版，解压即用

步骤：`flutter build windows --release` → `go build` 并把 daemon 拷入客户端发布目录（同目录是客户端自动拉起 daemon 的前提）→ 复制示例配置 → Inno Setup 打包 → 压缩便携 zip。

版本参数同时写入客户端、daemon 和安装包。本地构建默认使用构建号 `1`；CI 通过额外的 `[build-name] [build-number]` 参数传入数字版本及运行序号。缺少 Inno Setup 时只跳过安装包，仍然生成便携 ZIP。

前置条件：

- Flutter / Go / VS Build Tools 在 PATH
- 打包安装程序需要 Inno Setup 6（未安装时脚本会跳过安装包、仍产出便携 zip）：
  `winget install JRSoftware.InnoSetup`

### GitHub Actions 发布

推 `v*` 标签（或手动触发 `Actions → Release → Run workflow`）会在各平台 runner 上构建并上传产物，推送标签时还会统一发布到对应的 GitHub Release：

```bash
git tag v1.2.3
git push origin v1.2.3
```

发布构建先复用 CI，通过 Go、Flutter 和发布脚本检查后再打包。CI 与发布使用固定的 Flutter `3.44.9`。

标签必须是 `v` 开头的语义版本（例如 `v1.2.3` 或 `v1.2.3-rc.1`）。手动从分支构建时，产物版本为 `ci-<运行序号>`，客户端数字版本取自 `client/pubspec.yaml`；标签构建则取标签中的数字版本。客户端与安装包的构建号统一使用 GitHub Actions 运行序号。

带预发布段的标签（如 `-rc.1`、`-beta`）会标记为 GitHub Pre-release，并且不会设为 Latest；重跑已有版本时也会同步该标记。仅构建元数据包含连字符（如 `v1.2.3+build-rc.1`）的版本仍按正式版处理。

已有草稿 Release 会在全部附件上传成功后正式发布，并检查其已退出草稿状态；附件上传失败时保留草稿，便于修复后重跑。

各平台产物：

| 平台 | 产物 |
|---|---|
| Windows | `ssh-tunnel-setup-<版本>.exe`（安装包）+ `ssh-tunnel-portable-<版本>.zip`（便携版） |
| Linux | `ssh-tunnel-portable-<版本>-linux-x64.tar.gz` |
| macOS | `ssh-tunnel-portable-<版本>-macos-<arch>.zip`（跟随 runner 架构，arm64 / x64） |

> macOS 为 ad-hoc 签名、未公证，分发给其它 Mac 首次需右键「打开」绕过 Gatekeeper；
> 正式分发建议补上 Developer ID 签名与 notarization。

Linux 便携版需要系统提供 GTK 3 与 Ayatana AppIndicator（或 AppIndicator）运行库；发布工作流会显式安装对应的开发依赖。

安装程序为**按用户安装**（无需管理员权限），安装到
`%LOCALAPPDATA%\Programs\SSH Tunnel Manager`，创建开始菜单与桌面快捷方式。
卸载时会移除指向本安装目录的用户级自启动项；指向其他安装目录的条目会保留。用户目录 `~/.ssh-tunnel/` 中的配置与服务发现文件会保留。

需要管理员级（Program Files）安装时，把 `installer.iss` 里的
`PrivilegesRequired=lowest` 改为 `admin`、`DefaultDirName` 改为 `{autopf}\SSH Tunnel Manager` 即可。

## 已知限制

- 同一发现位置只允许一个 daemon 实例（`daemon.json` 为单文件，后启动的会覆盖）；需要并行多实例时用 `SSH_TUNNEL_DATA_DIR` 各自指定目录
- 系统服务模式下发现文件需对所有本地用户可读，令牌因此对本机其他用户可见（详见「服务发现的候选位置」）
- 私钥以明文路径记录，不做复制；daemon 以运行身份读取该文件，因此权限需对运行身份开放（系统服务模式下是 LocalSystem / root）
- Windows 构建需要带 `PROGRAMFILES` 系列环境变量的终端环境（VS Build Tools）
