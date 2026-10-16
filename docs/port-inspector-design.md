# 端口占用查询与检测 — 功能设计

> 状态：**已确认，待实现**
> 关联：[扩展实施记录](extension-roadmap.md) 第 8 项
>
> 评审结论（2026-02，见第 13 节）：导航置于密钥与日志之间；UDP 纳入并标注；
> 本期即做隧道编辑器「检测占用」按钮；自动刷新默认关闭；提供「仅显示非系统进程」快捷筛选。

## 1. 背景与目标

Portway 已经能管理 SSH 隧道与 FRP 客户端，但用户遇到「本地端口被占用」时，只能看到隧道启动失败的一句错误，无法知道**是谁占了这个端口**。典型场景：

- 隧道配置 `local_port = 13306`，启动失败提示 `bind: address already in use`，但不知道占用者是谁、能不能停
- 想给新隧道挑一个空闲端口，只能凭经验猜
- 排查「某个服务到底有没有起来」时，需要确认端口是否真的在监听

因此新增一个**本机端口占用查询与检测**页面：以列表形式展示所有正在监听的端口及其所属进程，支持查询、结束进程、定位到进程目录。

### 与「隧道诊断」的区别

隧道诊断（`/api/tunnels/{name}/diagnose`）是**针对单条已配置隧道**的连通性检查，不涉及进程归属。本功能是**全局的端口视角**，与隧道配置无关，回答的是「这个端口现在被谁占着」。

两者互补：诊断发现「本地端口无法绑定」时，可以直接跳到本页面按端口号查占用者。

## 2. 范围

### 本期做

| 能力 | 说明 |
|---|---|
| 监听端口列表 | 所有处于 LISTEN 状态的 TCP 端口 + 所有处于绑定状态的 UDP 端口，**协议列明确标注 TCP / UDP** |
| 进程信息 | 进程名、PID、可执行文件完整路径、命令行、用户、启动时间 |
| 查询与筛选 | 按端口号、进程名、PID、地址模糊匹配；按协议 / 地址类型筛选 |
| 结束进程 | 终止占用端口的进程，带二次确认与结果反馈 |
| 定位到进程目录 | 在系统文件管理器中打开进程可执行文件所在目录（并选中该文件） |
| 占用检测 | 输入一个端口号，判断是否空闲；空闲时给出「可用于隧道」的结论 |
| 自动刷新 | 可开关的定时刷新，**默认关闭**；手动刷新始终可用 |
| 非系统进程筛选 | 一键隐藏系统关键进程，只留用户可安全结束的条目 |
| 隧道编辑器联动 | `local_port` 旁提供「检测占用」按钮，配置时即发现冲突 |
| 与隧道联动 | 列表中标出该端口是否正被某条隧道使用（反向转发标出远端端口） |

### 本期不做

- **不做跨主机查询**：只查本机（daemon 所在机器）。远端端口占用由隧道诊断的 SSH 侧探测覆盖
- **不做端口扫描**：不主动探测未监听的端口，只读取系统当前的监听表
- **不做连接级详情**：不展示 ESTABLISHED 连接的远端地址、不展示流量统计
- **不做进程树 / 服务归属**：不解析「这个进程属于哪个 Windows 服务」，只展示进程本身
- **不做历史趋势**：不记录端口占用的时间线
- **不做 UDP 的「监听」语义**：UDP 无连接、无 LISTEN 状态，只展示**已绑定**的 UDP socket，界面文案统一用「绑定」而非「监听」（见 8.3）

## 3. 架构落点

沿用现有四层结构，不引入新的通信方式：

```
Flutter 客户端
  ├── PortsPage（新页面，NavigationRail 第 5 项，位于密钥与日志之间）
  └── portsProvider（新 AsyncNotifier）
        │
        ├── 进程内引擎 ── dart:ffi ──▶ sshtunnel_ports / sshtunnel_kill_process ...
        └── 独立 daemon ── HTTP ─────▶ GET /api/ports / POST /api/ports/kill ...
                                            │
                                    app.App（业务门面，新增 ports.go）
                                            │
                                    internal/portscan（新包，平台实现）
                                      ├── portscan_windows.go
                                      ├── portscan_linux.go
                                      └── portscan_darwin.go
```

**关键决策：平台相关逻辑放在 Go 侧，不放 Dart 侧。**

理由：
1. 两种引擎形态共用同一份实现，避免 Dart 侧为「daemon 模式」和「进程内模式」各写一套
2. daemon 可能以**系统服务身份**（LocalSystem / root）运行，此时能看到普通用户进程看不到的端口归属；Dart 侧自己跑 `netstat` 只能看到当前用户视角
3. 结束进程需要权限提升，在 Go 侧统一处理错误语义（拒绝访问 / 进程不存在 / 系统关键进程）

**与 FRP 的取舍一致**：FRP 也是「Go 侧跑 frp 库，Dart 侧只做界面」，本功能同理。

## 4. 数据模型

### 4.1 端口条目 `PortEntry`

```go
// PortEntry 是一个正在占用（TCP 监听 / UDP 绑定）的端口及其占用进程。
type PortEntry struct {
    Protocol    string `json:"protocol"`      // "tcp" | "udp"
    State       string `json:"state"`         // "listen"（TCP）| "bound"（UDP）
    LocalAddr   string `json:"local_addr"`    // "0.0.0.0" / "127.0.0.1" / "::" / "::1"
    LocalPort   int    `json:"local_port"`
    Scope       string `json:"scope"`         // "all" | "loopback" | "specific"
    PID         int    `json:"pid"`           // 0 表示无法归属（见 4.3）
    ProcessName string `json:"process_name"`  // 不含扩展名，如 "svchost"
    Executable  string `json:"executable"`    // 可执行文件绝对路径，取不到时为空
    CommandLine string `json:"command_line"`  // 完整命令行，取不到时为空
    User        string `json:"user"`          // 进程所有者，取不到时为空
    StartedAt   string `json:"started_at"`    // RFC3339，取不到时为空
    System      bool   `json:"system"`        // 是否判定为系统关键进程（见 6.2）
    Tunnel      string `json:"tunnel,omitempty"` // 被哪条隧道占用（仅本地监听）
}
```

**`State` 字段为什么必要**：TCP 的 LISTEN 与 UDP 的绑定是两种不同的事实，界面文案必须区分（见 8.3）。TCP 条目恒为 `listen`；UDP 条目恒为 `bound`。保留这个字段而不是让界面按 `Protocol` 推导，是因为将来若要纳入 TCP 的 `ESTABLISHED`（本期明确不做），结构不需要改。

`Scope` 由 `LocalAddr` 推导，用于界面分组与「这个端口是否对外可达」的判断：

| LocalAddr | Scope | 含义 |
|---|---|---|
| `0.0.0.0` / `::` | `all` | 绑定所有网卡，局域网可达 |
| `127.0.0.1` / `::1` | `loopback` | 仅本机可达 |
| 其它具体地址 | `specific` | 绑定到指定网卡 |

> IPv6 不单列 scope：`::` 归入 `all`，`::1` 归入 `loopback`，这样界面上的「仅本机」筛选对 IPv6 同样有效。IPv6 的具体网卡地址归入 `specific`。
>
> 对 UDP 而言「局域网可达」的措辞要更弱——UDP 绑定 `0.0.0.0` 意味着能收到任意来源的报文，但不存在「接受连接」这回事。界面在 UDP 行上用「所有网卡」而非「所有网卡监听中」。

### 4.2 列表响应 `PortSnapshot`

```go
type PortSnapshot struct {
    Ports       []PortEntry `json:"ports"`
    ScannedAt   string      `json:"scanned_at"`   // RFC3339Nano
    ElapsedMS   int64       `json:"elapsed_ms"`
    Partial     bool        `json:"partial"`      // 进程信息部分缺失时为 true
    Warnings    []string    `json:"warnings,omitempty"` // 如「无权限读取部分进程信息」
}
```

`Partial` / `Warnings` 的存在是必要的：非管理员运行时，Windows 上部分进程的路径与命令行读不到。**不能把「读不到」当成「没有」**——界面要明确区分「该进程没有命令行」和「没权限读」。

### 4.3 PID 为 0 的情形

某些平台/权限下无法把端口关联到 PID（Linux 上非 root 读 `/proc/net/tcp` 能看到 socket，但对应 `/proc/<pid>/fd` 不可读）。此时 `PID = 0`，界面显示「无法确定占用进程」并给出提示（如「以管理员身份运行可查看完整信息」），**不隐藏该条目**——端口确实被占着，这个事实本身有价值。

## 5. 平台实现

### 5.1 采集策略

各平台优先使用**原生 API**，失败时回退到命令行工具：

| 平台 | 首选 | 回退 |
|---|---|---|
| Windows | `GetExtendedTcpTable` / `GetExtendedUdpTable`（iphlpapi.dll，`golang.org/x/sys/windows`） | `netstat -ano` |
| Linux | 解析 `/proc/net/tcp`、`/proc/net/tcp6`、`/proc/net/udp`、`/proc/net/udp6` | `ss -tulpnH` |
| macOS | `sysctl net.inet.tcp.pcblist_n`（需 CGO，见下） | `lsof -nP -iTCP -sTCP:LISTEN` + `lsof -nP -iUDP` |

**Windows 走 iphlpapi 而非 `netstat`**：`netstat -ano` 输出需要按列宽解析，本地化环境下列名会变（中文 Windows 输出「本地地址」），且大量连接时慢。`GetExtendedTcpTable` 是结构化的，一次调用拿到全部条目，且能直接拿到 `OwningPid`。

**TCP 与 UDP 的枚举差异（各平台一致）**：

| | TCP | UDP |
|---|---|---|
| 过滤条件 | 只要 `MIB_TCP_STATE_LISTEN`（Windows）/ `st == 0A`（Linux）/ `-sTCP:LISTEN`（macOS） | **全部条目都算**——UDP 无状态机，绑定了就是绑定了 |
| 去重键 | `协议 + 地址 + 端口 + PID` | 同左 |
| 已知噪声 | 无 | Windows 上 `svchost`/`System` 会绑定大量 UDP 端口（DHCP、NetBIOS、SSDP 等），列表会明显变长 |

> **UDP 条目多不是 bug**。Windows 上单个 `svchost` 可能绑定十几个 UDP 端口，这是正常的。因此「仅显示非系统进程」筛选（见 8.3）对 UDP 尤其有用，且**默认不隐藏**——隐藏会让用户以为端口空闲，而实际上它被占了。

**macOS 的 CGO 约束**：`sysctl` 的 pcblist 结构体解析需要 CGO，而项目当前**不启用 CGO 也能构建**（进程内引擎的 c-shared 需要 CGO，但 daemon 不需要）。因此 macOS 默认走 `lsof` 回退路径，不引入 CGO 依赖。这与项目「daemon 不依赖 C 工具链」的现状一致。

> macOS 上 UDP 需要单独的 `lsof -nP -iUDP` 调用（`-sTCP:LISTEN` 只作用于 TCP）。两次调用串行执行，总耗时约 100–200ms。

### 5.2 进程信息补全

拿到 PID 集合后，逐个补全进程信息。**按 PID 去重**：一个进程可能监听多个端口，`GetProcessInfo` 只调用一次。

| 平台 | 进程名 | 可执行路径 | 命令行 | 用户 | 启动时间 |
|---|---|---|---|---|---|
| Windows | `QueryFullProcessImageName` | 同左 | `NtQueryInformationProcess` + PEB 读取（复杂）→ **改用 WMI `Win32_Process`** | `OpenProcessToken` + `GetTokenInformation` | `GetProcessTimes` |
| Linux | `/proc/<pid>/comm` | `/proc/<pid>/exe`（readlink） | `/proc/<pid>/cmdline`（NUL 分隔） | `/proc/<pid>/status` 的 `Uid` → 查 `/etc/passwd` | `/proc/<pid>/stat` 第 22 项 + `/proc/stat` btime |
| macOS | `proc_pidpath`（libproc） | 同左 | `sysctl KERN_PROCARGS2` | `proc_bsdinfo` | `proc_bsdinfo` |

**Windows 命令行走 WMI 而非 PEB 直读**：PEB 读取要跨进程读内存、处理 WoW64 差异、且对受保护进程会失败，代码量大且脆弱。WMI 的 `Win32_Process.CommandLine` 一次查询拿全部进程，代价是首次查询慢（约 200–500ms）。因此：

- **进程名与路径**走 `QueryFullProcessImageName`（快，微秒级）
- **命令行**走 WMI，且**按需加载**：列表默认不查命令行，用户展开某一行或使用搜索时才补

这样常规刷新的开销保持在毫秒级。

> WMI 查询通过 `wmic process get ProcessId,CommandLine /format:csv` 或 PowerShell `Get-CimInstance` 实现。优先用 PowerShell（`wmic` 在 Windows 11 已标记废弃）。

### 5.3 包结构

```
daemon/internal/portscan/
├── portscan.go          # 公共类型 PortEntry / PortSnapshot、Scanner 接口、进程信息缓存
├── ports_windows.go     # iphlpapi 枚举 + QueryFullProcessImageName + WMI 命令行
├── ports_linux.go       # /proc 解析
├── ports_darwin.go      # lsof 解析
├── process_windows.go   # 结束进程、定位目录
├── process_linux.go
├── process_darwin.go
└── portscan_test.go     # 跨平台的解析逻辑测试（用固定输入）
```

平台无关的部分（scope 推导、系统进程判定、排序、筛选）放在 `portscan.go`，可以纯逻辑测试，不需要真机。

## 6. 结束进程

### 6.1 语义

```go
// KillProcess 终止进程。force 为 true 时跳过优雅退出（Windows TerminateProcess /
// Unix SIGKILL），否则先请求优雅退出（Windows 先发 WM_CLOSE 不可行，直接
// TerminateProcess；Unix 发 SIGTERM 并等待宽限期）。
func KillProcess(pid int, force bool) error
```

**平台差异**：

| 平台 | 优雅（force=false） | 强制（force=true） |
|---|---|---|
| Windows | 无真正的优雅终止手段；`GenerateConsoleCtrlEvent` 只对同控制台进程有效。因此**统一走 `TerminateProcess`**，`force` 参数在 Windows 上不产生行为差异 | 同左 |
| Linux / macOS | 先发 `SIGTERM`，等待 3 秒 | 直接 `SIGKILL` |

Windows 上没有优雅终止是客观限制，界面文案要如实说明：**「结束进程」在 Windows 上等同于强制终止，进程没有机会保存数据。**

### 6.2 保护规则

以下情形**拒绝结束**，并返回可读原因：

1. **PID 0 / 4**（Windows 的 System Idle / System）—— 结束会导致系统崩溃
2. **PID 等于当前进程或 daemon 进程自身** —— 自杀式操作
3. **PID 等于父进程**（如果可判定）—— 避免误杀启动者
4. **判定为系统关键进程** —— `System`、`smss`、`csrss`、`wininit`、`winlogon`、`services`、`lsass`、`svchost`（Windows）；`systemd`、`init`、`kthreadd`（Linux）

`System` 字段由 Go 侧填充，界面据此把「结束进程」按钮置灰并给出 tooltip。**判定放在 Go 侧而不是 Dart 侧**，因为这是安全边界，不能依赖界面自觉。

> 规则 4 是**尽力而为**的白名单。真正的权限边界由操作系统决定：非管理员结束系统进程本来就会失败。白名单的价值是给出**清晰的提示**，而不是「防止」——防止靠 OS。

### 6.3 结果反馈

结束进程是异步的（进程退出需要时间），因此返回结构包含结果状态：

```go
type KillResult struct {
    PID     int    `json:"pid"`
    Name    string `json:"name"`
    OK      bool   `json:"ok"`
    Error   string `json:"error,omitempty"`
    Code    string `json:"code,omitempty"` // "denied" | "not_found" | "protected" | "failed"
}
```

界面在操作后**重新扫描一次**列表，用实际结果说话：如果 PID 还在，说明没杀掉，显示原因。

## 7. HTTP API

沿用现有约定：`/api` 前缀、`X-Auth-Token` 鉴权、写接口 1 MiB 上限、未知字段报 400。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/ports` | 端口占用列表。查询参数：`q`（模糊匹配端口/进程名/PID/地址）、`protocol`（`tcp`/`udp`/空=全部）、`scope`（`all`/`loopback`/`specific`）、`system`（`true`=仅系统进程 / `false`=仅非系统进程 / 空=全部）、`with_command_line`（`true` 时补全命令行） |
| GET | `/api/ports/check?port=<n>&protocol=<tcp\|udp>` | 检测端口是否被占用。`protocol` 省略时**同时检测 TCP 与 UDP**。返回 `{port, in_use, entries: [...]}`；空闲时 `entries` 为空数组 |
| POST | `/api/ports/kill` | 结束进程。请求体 `{pid, force}`，返回 `KillResult` |
| POST | `/api/ports/reveal` | 定位到进程目录。请求体 `{pid}`，返回 `{path}`（实际打开由客户端执行，见下） |

**`/api/ports/check` 的 `protocol` 语义**：隧道只占 TCP，但用户可能在排查 UDP 冲突。因此：

- **省略 `protocol`**：TCP 与 UDP 都查，任一侧被占即 `in_use = true`。返回的 `entries` 里每条都带 `protocol`，界面据此说明「TCP 3306 被 mysqld 占用」还是「UDP 3306 被占用」
- **隧道编辑器调用的场景固定传 `protocol=tcp`**——隧道不涉及 UDP，把 UDP 冲突报成「端口不可用」会误导用户

**`/api/ports/reveal` 为什么在 daemon 侧**：需要把 PID 解析成可执行文件路径，而这个能力已经在 Go 侧（进程信息补全）。但**真正打开文件管理器由客户端执行**——daemon 可能是系统服务，以 LocalSystem 身份 `explorer.exe /select,` 打开的窗口不属于用户桌面会话，用户看不到。因此接口只返回路径，客户端用 `Process.run` 打开（与 `keys_page.dart` 的 `_reveal` 现有做法一致）。

### 筛选放在服务端还是客户端

**放在服务端**。理由：端口条目数量在几百到几千量级，但每次刷新都要重新采集，服务端筛选可以避免把全量数据传到 Dart 侧再过滤。同时 `with_command_line` 是按需补全的开关，必须在服务端控制。

不过**界面上的即时输入过滤仍在客户端做**：用户敲字时不应每次都打一次 HTTP。因此：

- **服务端 `q`**：用于「刷新时带上当前筛选条件」，减少传输量
- **客户端过滤**：用于已加载列表的即时响应

两者用同一套匹配规则（见 8.2），避免行为不一致。

## 8. 客户端实现

### 8.1 引擎接口扩展

在 `client/lib/services/tunnel_engine.dart` 的 `TunnelEngine` 抽象类中新增：

```dart
// ---------- 端口 ----------

/// 扫描本机端口占用。返回的列表已按服务端筛选条件过滤。
Future<PortSnapshot> scanPorts({
  String? query,
  String? protocol,   // 'tcp' | 'udp' | null(全部)
  String? scope,      // 'all' | 'loopback' | 'specific' | null(全部)
  bool? systemOnly,   // null=全部, true=仅系统, false=仅非系统
  bool withCommandLine = false,
});

/// 检测单个端口是否被占用。protocol 为 null 时同时检查 TCP 与 UDP。
Future<PortCheck> checkPort(int port, {String? protocol});

/// 结束占用端口的进程。
Future<KillResult> killProcess(int pid, {bool force = false});

/// 解析进程可执行文件路径（用于定位目录）。
Future<String> resolveProcessPath(int pid);
```

两种实现分别对应：

- `DaemonClient` → `GET /api/ports` 等（Dio）
- `EmbeddedEngine` → `sshtunnel_ports` / `sshtunnel_check_port` / `sshtunnel_kill_process` / `sshtunnel_resolve_process_path`

FFI 侧新增 4 个导出函数，签名沿用现有约定（句柄 + 字符串参数，返回 `{"ok":..}` 信封）：

```go
//export sshtunnel_ports
func sshtunnel_ports(handle C.longlong, payload *C.char) *C.char

//export sshtunnel_check_port
func sshtunnel_check_port(handle C.longlong, port C.int) *C.char

//export sshtunnel_kill_process
func sshtunnel_kill_process(handle C.longlong, payload *C.char) *C.char

//export sshtunnel_resolve_process_path
func sshtunnel_resolve_process_path(handle C.longlong, pid C.int) *C.char
```

> 注意 `sshtunnel_ports` 的筛选参数走 JSON payload 而不是多个字符串参数——现有 `_EngineCall` 最多支持 3 个字符串参数，而筛选有 5 个（`q`/`protocol`/`scope`/`system`/`with_command_line`）。用 JSON 更稳，也和 `sshtunnel_batch_tunnels` 的既有做法一致。

### 8.2 匹配规则

`q` 的匹配规则（服务端与客户端共用同一套语义）：

- 纯数字 → 优先按**端口号精确匹配**，同时按 **PID 精确匹配**，两者都命中时都返回
- 其它 → 对 `process_name`、`executable`、`command_line`、`local_addr` 做**不区分大小写的子串匹配**
- 端口号也参与子串匹配（输入 `330` 能匹配到 `3306`），但精确命中的排在前面

排序：**端口号升序**为默认；有 `q` 时按「精确匹配 → 前缀匹配 → 子串匹配」分组，组内仍按端口号升序。这样输入 `3306` 时它一定排第一。

**同一端口同时被 TCP 和 UDP 占用时的排序**：端口号相同时 TCP 排在 UDP 前面（TCP 是隧道场景的主角）。同一端口同一协议被多个进程占用（`SO_REUSEADDR` / 多播）时按 PID 升序。

**筛选条件的组合语义**：`q`、`protocol`、`scope`、`system` 之间是 **AND** 关系，全部满足才保留。`system=false`（仅非系统进程）与 `q` 同时生效时，先按 `q` 匹配再排除系统进程——**结果为空是合法结果**，界面显示「没有匹配的非系统进程」，并提示可以关闭该筛选。

### 8.3 页面结构

新增 `client/lib/pages/ports_page.dart`，**导航位置在「密钥」与「日志」之间**（已确认）：

```dart
static const _destinations = [
  (icon: Icons.swap_horiz_outlined, label: '隧道'),
  (icon: Icons.dns_outlined, label: 'SSH 连接'),
  (icon: Icons.cloud_outlined, label: 'FRP'),
  (icon: Icons.key_outlined, label: '密钥'),
  (icon: Icons.lan_outlined, label: '端口'),      // 新增，索引 4
  (icon: Icons.terminal_outlined, label: '日志'),  // 索引 5
  (icon: Icons.settings_outlined, label: '设置'),  // 索引 6
  (icon: Icons.info_outline, label: '关于'),       // 索引 7
];
```

> **`_settingsIndex` 必须从 5 改为 6**。这个常量决定「哪些页面在未连接时不显示引导页」，改错会让设置页在断连时不可访问。同时检查所有硬编码的页面索引（`_aboutIndex` 之类）是否受影响。

页面布局：

```
┌───────────────────────────────────────────────────────────────────┐
│ 端口占用                                        [刷新] [自动刷新 ⚪] │
│ 本机正在占用端口的进程（TCP 监听 / UDP 绑定）                       │
├───────────────────────────────────────────────────────────────────┤
│ 🔍 [搜索端口 / 进程名 / PID...]  [协议▾] [地址▾] [仅非系统进程 ⚪]   │
├───────────────────────────────────────────────────────────────────┤
│ 端口   协议  绑定地址        进程          PID    操作              │
│ ───────────────────────────────────────────────────────────────── │
│ 13306  TCP   127.0.0.1      portway      12345  ⋯                 │
│              仅本机 · 隧道 mysql-prod                               │
│ ───────────────────────────────────────────────────────────────── │
│ 3306   TCP   0.0.0.0        mysqld       6789   ⋯                 │
│              所有网卡                                               │
│ ───────────────────────────────────────────────────────────────── │
│ 5353   UDP   0.0.0.0        svchost      1024   ⋯  [系统]          │
│              所有网卡 · UDP 绑定                                    │
└───────────────────────────────────────────────────────────────────┘
```

**协议列的呈现**：TCP 与 UDP 用**不同颜色的 chip**区分（TCP 用主题色，UDP 用中性色），而不是只靠文字。理由是 UDP 条目在 Windows 上数量多、且用户通常不关心，颜色能让视线快速跳过。副标题里 TCP 行写「仅本机 / 所有网卡」，UDP 行额外补一句「UDP 绑定」，避免用户把 UDP 的 `0.0.0.0` 误解成「在监听连接」。

**「仅非系统进程」快捷筛选**（已确认本期做）：放在筛选栏最右侧，是一个 `FilterChip`。开启后调用 `scanPorts(systemOnly: false)`，服务端过滤掉 `System == true` 的条目。

> 这个筛选**默认关闭**。默认隐藏系统进程会让用户误以为端口空闲——而端口确实被占着。筛选是「帮我看得清」，不是「帮我忽略」。

行内操作（`⋯` 菜单）：

| 操作 | 行为 |
|---|---|
| 定位到进程目录 | 打开文件管理器并选中可执行文件；路径为空时置灰 |
| 复制进程路径 | 复制到剪贴板 |
| 复制 PID | 复制到剪贴板 |
| 结束进程 | 二次确认后调用；系统进程置灰并带 tooltip |

**为什么用列表 + 行内菜单而不是任务管理器那样的多列可排序表格**：Flutter 桌面端做可排序多列表格需要 `DataTable` 或第三方库，而现有界面全部是 `Card` + `ListTile` 风格（见 `keys_page.dart`、`tunnels_page.dart`）。保持风格一致比模仿任务管理器的外观更重要。端口号、进程名、PID 这些关键列在行内用等宽字体对齐，扫描效率不差。

### 8.4 端口检测入口

除了列表，还需要一个**单端口检测**的轻量入口。放在两处：

1. **端口页顶部**：一个「检测端口」按钮，弹出输入框，输入端口号后给出结果（同时查 TCP 与 UDP）
2. **隧道编辑器**：`local_port` 输入框旁加一个「检测占用」按钮，**本期就做**（已确认）

第 2 点是与现有功能的关键联动：用户在配置隧道时就能发现端口冲突，而不是等保存后启动失败。

**隧道编辑器的调用固定传 `protocol: 'tcp'`**——隧道不涉及 UDP，把 UDP 冲突报成「端口不可用」会误导用户。检测结果分三种：

| 结果 | 界面呈现 |
|---|---|
| TCP 空闲 | 绿色对勾「端口 13306 可用」 |
| TCP 被占 | 橙色警告「端口 13306 已被 `mysqld`（PID 6789）占用」，并给一个「查看详情」链接跳到端口页并带上搜索词 |
| 检测失败 | 灰色「无法检测」（daemon 未连接 / 无权限），**不阻断保存** |

> 检测失败**不阻断保存**是刻意的：端口检测是辅助信息，不是校验规则。用户可能就是要先存配置、稍后启动。把它做成硬校验会让离线编辑配置变得不可能。

### 8.5 自动刷新

**默认关闭**（已确认）。开启后每 5 秒刷新一次。理由：

- 端口扫描在 Windows 上走 iphlpapi，本身很快（几毫秒），但**进程信息补全**要遍历 PID，开销更大
- 无脑定时刷新会让界面上的操作（比如展开菜单）被打断

开启自动刷新时，如果用户正在操作（菜单打开、确认框弹出），**暂停刷新**，操作结束后恢复。

## 9. 隧道联动

`PortEntry.Tunnel` 字段：扫描完成后，把结果与当前隧道配置对照：

- `mode = local` / `dynamic`：用 `local_host:local_port` 匹配 `LocalAddr:LocalPort`
- `mode = remote`：本地不监听，**不参与匹配**（该字段留空）

匹配是**展示性**的，不影响任何行为。价值在于：用户看到 `13306` 被占用时，能立刻知道「这是我自己的 mysql-prod 隧道占的，不用管」，而不是去杀自己的隧道。

> 反向转发的端口在远端，本机列表里看不到，因此不匹配。这是符合预期的。

## 10. 安全与权限

### 10.1 威胁模型

daemon 只监听回环 + token 鉴权（现有安全模型）。本功能新增的攻击面：

- **结束进程**是一个**破坏性操作**。任何能访问 daemon API 的人都能杀本机任意进程
- 现有 token 保护已经覆盖这个风险：能拿到 token 的只有本机同用户进程（或系统服务模式下同机其他用户，见 README 的已知限制）

**结论：不额外增加鉴权层，沿用现有 token 模型。** 但要在界面上明确这是破坏性操作（二次确认 + 红色按钮）。

### 10.2 权限差异

| 运行身份 | 能看到的 | 能结束的 |
|---|---|---|
| 普通用户 | 自己 + 部分系统进程的信息（路径可能读不到） | 自己启动的进程 |
| 管理员 / root | 全部 | 全部（除受保护进程） |

非管理员运行时，列表带 `Partial = true` 和 warning，界面顶部显示一条提示：「部分进程信息需要管理员权限才能查看」。

### 10.3 不做的防护

**不限制「只能结束隧道相关进程」**。虽然这是 Portway 的功能，但用户的需求就是「端口被别的程序占了，我要停掉它」——限制范围会让功能失去意义。保护靠 6.2 的系统进程白名单 + 二次确认。

## 11. 测试计划

### Go 侧

| 测试 | 覆盖 |
|---|---|
| `portscan_test.go` | scope 推导、系统进程判定、排序、`q` 匹配规则、端口号/PID 双命中、TCP 优先于 UDP、筛选条件 AND 组合 |
| `ports_parse_test.go` | 各平台解析函数用**固定输入**测试：Linux `/proc/net/tcp` 与 `/proc/net/udp` 文本、`ss -tulpnH` 输出、`lsof` 的 TCP 与 UDP 两种输出、`netstat -ano` 输出 |
| `kill_test.go` | 保护规则（PID 0/4、自身、系统进程白名单）全部拒绝；不存在 PID 返回 `not_found` |
| `server/ports_test.go` | API 鉴权、5 个筛选参数、未知字段 400、`kill` 的请求体校验、`check` 的 protocol 省略/指定两种语义 |
| `embedded/engine_test.go` | 新增 4 个 FFI 方法的信封格式 |

**平台相关代码的测试边界**：枚举逻辑（iphlpapi 调用、`/proc` 读取）在 CI 上无法跨平台验证，因此**解析逻辑与系统调用分离**——解析函数接受 `[]byte` 或 `string`，可以喂固定输入测试。这与现有 `tunnel/diagnostics_test.go` 用本地 SSH 夹具、`update/archive_test.go` 用临时夹具的做法一致。

### Dart 侧

| 测试 | 覆盖 |
|---|---|
| `ports_page_test.dart` | 搜索过滤、协议/地址筛选、「仅非系统进程」开关、空状态、错误状态、系统进程按钮置灰、UDP 行文案 |
| `ports_model_test.dart` | `PortEntry.fromJson` 容错（字段缺失、PID 为 0、`state` 缺失时按 `protocol` 推导） |
| `engine_contract_test.dart` | 两种引擎实现同一套接口行为（现有契约测试的扩展） |
| `tunnel_editor_test.dart` | 「检测占用」按钮三态（空闲/被占/检测失败）、失败时不阻断保存 |

### 真机验证

- Windows：管理员与非管理员各跑一次，对比 `Partial` 与可见端口差异
- Linux：`/proc` 路径，非 root 用户下 PID 归属的降级行为
- 结束进程：用一个自建的长驻进程验证，**不用系统进程测试**

## 12. 实施顺序

按依赖关系分 5 步，每步可独立验收：

| 步 | 内容 | 验收 |
|---|---|---|
| 1 | `internal/portscan` 包：类型、scope 推导、系统进程判定、排序筛选（纯逻辑） | 单元测试通过，不涉及平台调用 |
| 2 | Windows 平台实现：iphlpapi 枚举（TCP+UDP）+ 进程信息补全 | 真机跑通，与 `netstat -ano` 结果对照一致 |
| 3 | Linux / macOS 平台实现 | 解析函数用固定输入测试；Linux 真机验证 |
| 4 | `app.App` 门面 + HTTP API + FFI 导出 | API 测试、FFI 信封测试 |
| 5 | Dart 侧：模型、引擎接口、两种实现、`portsProvider`、`PortsPage`（含「仅非系统进程」筛选）、隧道编辑器「检测占用」按钮 | Widget 测试、真机交互 |

**第 1 步先做纯逻辑**是刻意的：平台调用无法在 CI 上验证，把可测的部分先固化下来，平台实现就只剩「把数据喂给解析函数」这一层薄封装。

## 13. 评审结论与风险

### 已确认的决策（2026-02 评审）

| # | 问题 | 结论 |
|---|---|---|
| 1 | 导航位置 | **放在「密钥」与「日志」之间**（索引 4）。`_settingsIndex` 随之从 5 改为 6 |
| 2 | UDP 是否纳入 | **纳入**，并在协议列明确标注（不同颜色 chip + 副标题「UDP 绑定」）。`State` 字段区分 `listen`/`bound` |
| 3 | 隧道编辑器联动 | **本期就做**「检测占用」按钮，固定查 TCP；检测失败不阻断保存 |
| 4 | 自动刷新默认值 | **默认关闭**，开启后 5 秒间隔，用户操作期间暂停 |
| 5 | 「仅显示非系统进程」筛选 | **本期做**，默认关闭（默认隐藏会让用户误判端口空闲） |

### 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| Windows WMI 查询慢（200–500ms） | 列表首次加载卡顿 | 命令行按需加载，默认不查 |
| 非管理员看不到部分进程路径 | 界面显示不完整 | `Partial` 标记 + 顶部提示，明确区分「没有」和「读不到」 |
| 结束进程误杀 | 用户数据丢失 | 二次确认 + 系统进程白名单；文案明确「Windows 上等同强制终止」 |
| macOS 无 CGO 时 `lsof` 可能未安装 | 该平台功能不可用 | 检测 `lsof` 存在性，缺失时返回明确错误而不是空列表 |
| 端口数量大时列表卡顿 | 界面性能 | 服务端筛选 + 客户端虚拟滚动（`ListView.builder`） |
| **UDP 条目多导致列表噪声**（Windows 上 svchost 常绑十几个） | 用户找不到关心的 TCP 条目 | 协议列用颜色区分 + 默认按端口号排序让同端口 TCP 在前；提供协议筛选与「仅非系统进程」 |
| **「仅非系统进程」开启后结果为空** | 用户以为没有占用 | 空状态文案明确提示「当前筛选下无结果，可关闭『仅非系统进程』查看」 |

### 实现中需要注意的坑

1. **`_settingsIndex` 改错会让设置页在断连时不可访问**——这是硬编码索引的既有脆弱点，改动时必须同步检查所有索引常量
2. **UDP 的 `0.0.0.0` 不等于「在监听」**——文案必须区分，否则用户会误以为某个 UDP 端口在等连接
3. **`check` 接口在隧道编辑器的场景必须传 `protocol=tcp`**——否则 UDP 冲突会被误报为「端口不可用」
4. **`Partial = true` 时不要隐藏条目**——读不到进程信息不等于端口空闲
5. **`GetExtendedTcpTable` 的 `TCP_TABLE_CLASS` 常量极易写错，且写错不报错**（见下方 12.1）
6. **`size` 参数是 in/out 的**：第二次调用会把实际填充字节数写回，通常小于首次探测的容量；解析必须用回写的值，不能用 `len(buf)`
7. **`System` 字段必须在补全进程信息之前无条件判定**：PID 0/4 是内核伪进程，读不到进程信息，只在 lookup 成功分支赋值会让它们漏进「仅非系统进程」筛选

## 12.1 实现记录

### 已完成

| 步 | 状态 | 说明 |
|---|---|---|
| 1 | 完成 | `daemon/internal/portscan/portscan.go`（纯逻辑）+ `portscan_test.go`，22 个测试 |
| 2 | 完成（Windows） | `ports_windows.go` + `proc_windows.go` + `ports_windows_test.go`，真机验证通过 |
| 3 | 完成（Linux/macOS） | `ports_linux.go` + `ports_linux_test.go`、`ports_darwin.go` + `ports_darwin_test.go`；五个目标平台交叉编译通过 |
| 4 | 完成 | `app/ports.go` 门面 + `server/ports.go` HTTP API + `embedded` FFI 导出；`server/ports_test.go` 覆盖协议层 |
| 5 | 完成 | Dart 侧模型、引擎接口、两种实现、`portsProvider`、`PortsPage`、导航接入、隧道编辑器「检测占用」 |

### 交付清单

**Go 侧**

| 文件 | 内容 |
|---|---|
| `daemon/internal/portscan/portscan.go` | 公共类型、`Filter`/`CheckPort`/`Annotate`/`Scan`/`Kill`/`ResolvePath`/`ReadProcessInfo`、排序与筛选 |
| `daemon/internal/portscan/ports_windows.go` | iphlpapi 枚举 + 进程信息补全 + WMI 命令行 |
| `daemon/internal/portscan/proc_windows.go` | `hiddenCommand`（隐藏控制台窗口） |
| `daemon/internal/portscan/ports_linux.go` | `/proc/net/*` 枚举 + inode 反查 PID + `/proc/<pid>` 进程信息 |
| `daemon/internal/portscan/ports_darwin.go` | `lsof` 枚举 + `ps` 进程信息 |
| `daemon/internal/app/ports.go` | 业务门面：快照、检测、结束进程、解析路径、隧道标注 |
| `daemon/internal/server/ports.go` | 4 个 HTTP 接口 |
| `daemon/internal/embedded/engine.go` | `Ports`/`CheckPort`/`KillProcess`/`ResolveProcessPath` 导出 |

**Dart 侧**

| 文件 | 内容 |
|---|---|
| `client/lib/models/port.dart` | `PortEntry`/`PortSnapshot`/`PortCheck`/`KillResult`/`PortFilter` |
| `client/lib/services/tunnel_engine.dart` | 4 个抽象方法 |
| `client/lib/services/daemon_client.dart` | HTTP 实现 |
| `client/lib/services/embedded_engine.dart` | FFI 实现（筛选参数走 JSON payload） |
| `client/lib/providers.dart` | `portsProvider` + `PortsNotifier` |
| `client/lib/pages/ports_page.dart` | 列表、筛选、结束进程、定位目录 |
| `client/lib/pages/tunnels_page.dart` | `_PortCheckButton`（隧道编辑器「检测占用」） |

### 测试覆盖

| 层 | 文件 | 数量 |
|---|---|---|
| 纯逻辑 | `daemon/internal/portscan/portscan_test.go` | 22 |
| Windows 平台 | `daemon/internal/portscan/ports_windows_test.go` | 常量断言 + 行宽校验 |
| Linux 平台 | `daemon/internal/portscan/ports_linux_test.go` | 13（交叉编译验证，Windows 上不执行） |
| macOS 平台 | `daemon/internal/portscan/ports_darwin_test.go` | 8（同上） |
| HTTP 协议层 | `daemon/internal/server/ports_test.go` | 参数校验、鉴权、响应结构 |
| Dart 编辑器 | `client/test/editors_test.dart` | 4 个「检测占用」用例 |

HTTP 层测试**不断言具体扫到哪些端口**——那取决于跑测试的机器上恰好开着什么，
断言它会让测试在别人机器上随机失败。平台采集逻辑的测试全部走「解析函数接受
`[]byte`/`string`」的形式，喂固定输入。

### 三平台实现差异小结

| | Windows | Linux | macOS |
|---|---|---|---|
| 枚举 | iphlpapi `GetExtendedTcpTable`/`GetExtendedUdpTable` | `/proc/net/{tcp,tcp6,udp,udp6}` | `lsof -nP -iTCP -sTCP:LISTEN` + `lsof -nP -iUDP` |
| PID 来源 | 表结构自带 `OwningPID` | socket inode 反查 `/proc/<pid>/fd` | lsof 输出直接给 PID |
| 进程名/路径 | `QueryFullProcessImageName` | `/proc/<pid>/comm` + `readlink exe` | `ps -p <pid> -o comm=,args=,lstart=,user=` |
| 命令行 | WMI（按需加载） | `/proc/<pid>/cmdline`（随进程信息一起读） | `ps` 的 `args=`（随进程信息一起读） |
| 优雅终止 | 无（一律 `TerminateProcess`） | SIGTERM + 3s 宽限 → SIGKILL | 同 Linux |
| 回退路径 | `netstat -ano` | `ss -tulpnH` | 无（lsof 缺失时明确报错） |

**macOS 与 Linux 的 `fillCommandLines` 是空实现**：这两个平台读命令行没有额外成本，已在
`readProcessInfo` 里一并读回；只有 Windows 因为 WMI 查询慢（200–500ms）才需要按需加载。
保留这个空函数是为了让 `portscan.go` 的平台无关代码不必写 `if runtime.GOOS == "windows"`。

### 跨平台验证方式

Windows 上无法直接运行 Linux/macOS 的测试，采用两种手段：

1. **纯函数抽取验证**：把 `parseProcNet`/`parseProcAddr`/`parseLsof` 等纯解析逻辑临时复制到
   一个 Windows 可运行的程序里，用真实格式的固定输入跑一遍。这发现了 IPv6 每 4 字节分组
   小端的编码问题（见下）。
2. **交叉编译 + vet**：`GOOS=linux/darwin go vet ./internal/portscan/` 覆盖类型检查与构建标签，
   五个目标平台（windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64）全部通过。

### 踩过的坑（Step 3）

**IPv6 地址是「每 4 字节一组」各自小端，不是整体反转**

`/proc/net/tcp6` 里的 `::1` 是 `00000000000000000000000001000000`。正确解法是按 4 个
32 位字分组，每组内部反转字节序：

```go
for g := 0; g < 4; g++ {
    ip[g*4+0] = raw[g*4+3]
    ip[g*4+1] = raw[g*4+2]
    ip[g*4+2] = raw[g*4+1]
    ip[g*4+3] = raw[g*4+0]
}
```

一开始按「整体反转 16 字节」写，`::1` 恰好也能得到正确结果（因为只有最后一个字节非零，
整体反转和分组反转对它等价），所以**单看 `::1` 的测试会通过**。是 `fe80::1` 暴露了问题：
整体反转得到 `1:fe80::`，分组反转才是 `fe80::1`。

**lsof 的协议列位置不固定**：DEVICE 列宽度随设备号变化，不能按固定索引取协议。
实现里从第 7 列开始向后找第一个 `TCP`/`UDP` 标记，比硬编码索引稳。

**lsof 无匹配时退出码为 1**：这不是错误，必须当成「空结果」处理，否则没有监听端口时
会报错而不是显示空列表。

### 真机验证结果（Windows 11，非管理员）

```
total=215 elapsed=12ms partial=false
tcp=96 udp=119 pid0=0 system=29 withUser=67 withPath=192
```

- 与 `Get-NetTCPConnection` 对照一致：`svchost` 占 135、PID 4 占 445/139/5357/5985、`node` 占 3000/5173、`adb` 占 5037
- 端口查询 `3307` → 命中 `portway`（自身）；进程名查询 `node` → 7 条命中，含 `editor_sdk`（路径含 `node_modules`，属预期）
- `ResolvePath(21628)` → `C:\Users\Administrator\.version-fox\cache\nodejs\v-22.22.2\nodejs-22.22.2\node.exe`
- 保护规则：PID 0 / PID 4 / 自身进程均被拒绝
- 命令行补全（WMI）批量查询耗时约 7ms，195/224 条拿到命令行

### 踩过的坑（已修，均有回归测试）

**`TCP_TABLE_CLASS` 常量写错导致全表 PID 错位**（最严重）

`TCP_TABLE_OWNER_PID_LISTENER` 的正确值是 **3**，代码里误写成 2。2 是 `TCP_TABLE_BASIC_ALL`，
其行结构是 20 字节且**不含 `OwningPID`**。用 24 字节的结构体按 20 字节步长解析整张表，
读出来的「PID」实际是邻行的 `RemotePort`，表现为 2、5、8、11 这样的小整数——
**看起来像进程号，实际全是垃圾，而且 API 不返回任何错误**。

修正后补充了两道防线：
1. `parseTCPTable` 校验 `(len(buf)-4)/count` 是否等于结构体大小，不符则返回空
2. `ports_windows_test.go` 用常量断言 + 行宽不符用例把正确取值钉死

**`size` 参数是 in/out 的**：首次探测返回容量（如 7572），第二次调用回写实际填充量（如 6732）。
解析若用 `len(buf)` 算行宽，会得到偏大的步长，把正确数据误判成「class 用错」而全部丢弃——
这是修完第一个坑后立刻踩到的第二个坑，两者症状相反但根因相邻。

**`System` 判定位置错误**：PID 4（System）读不到进程信息，`cache.lookup(4)` 返回 false，
而 `entry.System` 原本只在 lookup 成功分支里赋值，导致内核占用（445、135 等）被标成非系统进程，
恰好出现在「仅显示非系统进程」的结果里。改为在补全前无条件判定，拿到进程名后再判一次
（白名单里多数条目如 `smss`、`lsass` 只能靠名字识别）。

**`partial` 误报**：内核伪进程本来就没有用户态路径，被当成「权限不足读不到路径」，
导致每次扫描都报 `partial=true`。现按 `entry.System` 排除。

**`User` 字段一度为空**：根因是 `windows.OpenProcessToken` 的签名是
`OpenProcessToken(handle, access, *Token)`（3 参数、返回 error），不是 2 参数返回 `(Token, error)`。
写错时编译不过，但更早的一版把错误吞在 `if err == nil` 里，表现为静默空值。

**结束进程的响应里进程名是空的**：`app.KillProcess` 原本传 `""` 给 `portscan.Kill`，
界面拿不到名字，只能显示「已结束 PID 1234」。修法是**先取名字再 kill**——kill 之后进程就没了，
名字查不到。为此导出了 `portscan.ReadProcessInfo(pid)`（只查一个 PID，Windows 上走
`QueryFullProcessImageName`，微秒级）；取不到名字也不影响结束操作本身。

**「检测占用」的转圈动画让 `pumpAndSettle` 永远等不到静止**：`_check()` 里 `_busy = true` 之后
`await _showOccupied(...)`，弹窗期间转圈（无限动画）一直在跑，widget 测试卡死。
修法是把「收起转圈」提到弹窗之前——先把结果收进局部变量，`setState(_busy = false)`，
再决定是弹窗还是提示。这同时修掉了界面本身一直闪的问题。

## 14. 与现有文档的衔接

实现完成后需要更新：

- `README.md`：新增「端口占用查询」章节，API 表格补充 4 个接口，已知限制补充平台差异与 UDP 语义说明
- `docs/extension-roadmap.md`：新增「第 8 项：端口占用查询与检测」，记录实现、测试结果与提交
