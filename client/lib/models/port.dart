/// 本机端口占用的数据模型。
///
/// 字段与 Go 侧 `internal/portscan` 的 JSON 输出一一对应，两种引擎形态
/// （独立 daemon 与进程内引擎）返回的结构完全相同。
library;

/// 一条端口占用记录。
class PortEntry {
  const PortEntry({
    required this.protocol,
    required this.state,
    required this.localAddr,
    required this.localPort,
    required this.scope,
    required this.pid,
    required this.processName,
    this.executable = '',
    this.commandLine = '',
    this.user = '',
    this.startedAt = '',
    this.system = false,
    this.tunnel = '',
  });

  /// `tcp` 或 `udp`。
  final String protocol;

  /// `listen`（TCP 监听）或 `bound`（UDP 绑定）。
  ///
  /// UDP 没有连接状态机，全部条目都是 `bound`；界面据此显示不同文案，
  /// 避免用户把 UDP 的 `0.0.0.0` 误解成「在监听连接」。
  final String state;

  /// 绑定地址：`0.0.0.0` / `127.0.0.1` / `::` / `::1` / 具体网卡地址。
  final String localAddr;
  final int localPort;

  /// `all`（对外可达）/ `loopback`（仅本机）/ `specific`（绑定具体网卡）。
  final String scope;

  /// 占用进程号；`0` 表示无法归属到进程（权限不足或内核持有）。
  final int pid;

  /// 进程名，不含扩展名。
  final String processName;

  /// 可执行文件绝对路径，可能为空。
  final String executable;

  /// 完整命令行，默认不采集（Windows 上走 WMI，200–500ms）。
  final String commandLine;

  final String user;

  /// 进程启动时间，RFC3339；可能为空。
  final String startedAt;

  /// 系统关键进程，不允许结束。
  final bool system;

  /// 命中本机隧道时是隧道名，否则为空。
  final String tunnel;

  bool get isTCP => protocol == 'tcp';
  bool get isUDP => protocol == 'udp';

  /// 是否仅本机可达。
  bool get isLoopback => scope == 'loopback';

  /// 无法确定占用进程。
  bool get hasProcess => pid > 0;

  /// 是否可以结束：有进程号且不是系统关键进程。
  bool get canKill => hasProcess && !system;

  /// 监听地址的展示形式，IPv6 通配地址显示得更易读。
  String get displayAddr {
    switch (localAddr) {
      case '0.0.0.0':
        return '所有网卡';
      case '::':
        return '所有网卡 (IPv6)';
      case '127.0.0.1':
        return '仅本机';
      case '::1':
        return '仅本机 (IPv6)';
      default:
        return localAddr;
    }
  }

  /// `协议 地址:端口` 的紧凑形式，用于复制到剪贴板。
  String get endpoint => '${protocol.toUpperCase()} $localAddr:$localPort';

  factory PortEntry.fromJson(Map<String, dynamic> json) {
    return PortEntry(
      protocol: (json['protocol'] ?? 'tcp').toString(),
      state: (json['state'] ?? 'listen').toString(),
      localAddr: (json['local_addr'] ?? '').toString(),
      localPort: (json['local_port'] as num?)?.toInt() ?? 0,
      scope: (json['scope'] ?? 'all').toString(),
      pid: (json['pid'] as num?)?.toInt() ?? 0,
      processName: (json['process_name'] ?? '').toString(),
      executable: (json['executable'] ?? '').toString(),
      commandLine: (json['command_line'] ?? '').toString(),
      user: (json['user'] ?? '').toString(),
      startedAt: (json['started_at'] ?? '').toString(),
      system: json['system'] == true,
      tunnel: (json['tunnel'] ?? '').toString(),
    );
  }
}

/// 一次扫描的完整结果。
class PortSnapshot {
  const PortSnapshot({
    this.ports = const [],
    this.scannedAt = '',
    this.elapsedMs = 0,
    this.partial = false,
    this.warnings = const [],
  });

  final List<PortEntry> ports;
  final String scannedAt;
  final int elapsedMs;

  /// 采集不完整（通常是权限不足）。
  ///
  /// 为 true 时界面应提示「部分端口信息不可见」，但**不能隐藏条目**——
  /// 条目本身是准确的，只是缺少进程归属。
  final bool partial;

  final List<String> warnings;

  factory PortSnapshot.fromJson(Map<String, dynamic> json) {
    final raw = json['ports'];
    return PortSnapshot(
      ports: raw is List
          ? raw
              .whereType<Map>()
              .map((e) => PortEntry.fromJson(Map<String, dynamic>.from(e)))
              .toList()
          : const [],
      scannedAt: (json['scanned_at'] ?? '').toString(),
      elapsedMs: (json['elapsed_ms'] as num?)?.toInt() ?? 0,
      partial: json['partial'] == true,
      warnings: (json['warnings'] as List?)?.map((e) => e.toString()).toList() ?? const [],
    );
  }
}

/// 单端口检测结果。
class PortCheck {
  const PortCheck({required this.port, required this.inUse, this.entries = const []});

  final int port;
  final bool inUse;
  final List<PortEntry> entries;

  factory PortCheck.fromJson(Map<String, dynamic> json) {
    final raw = json['entries'];
    return PortCheck(
      port: (json['port'] as num?)?.toInt() ?? 0,
      inUse: json['in_use'] == true,
      entries: raw is List
          ? raw
              .whereType<Map>()
              .map((e) => PortEntry.fromJson(Map<String, dynamic>.from(e)))
              .toList()
          : const [],
    );
  }
}

/// 结束进程的结果。
class KillResult {
  const KillResult({required this.pid, required this.name, required this.ok, this.error = '', this.code = ''});

  final int pid;
  final String name;
  final bool ok;
  final String error;

  /// 失败原因分类，界面据此给出不同文案。
  ///
  ///   - `protected` —— 命中保护规则（系统关键进程、自身进程等）
  ///   - `denied`    —— 权限不足，需要管理员身份
  ///   - `not_found` —— 进程已不存在（通常是自己先退出了）
  ///   - `failed`    —— 其它失败
  final String code;

  bool get isProtected => code == 'protected';
  bool get isDenied => code == 'denied';
  bool get isNotFound => code == 'not_found';

  factory KillResult.fromJson(Map<String, dynamic> json) {
    return KillResult(
      pid: (json['pid'] as num?)?.toInt() ?? 0,
      name: (json['name'] ?? '').toString(),
      ok: json['ok'] == true,
      error: (json['error'] ?? '').toString(),
      code: (json['code'] ?? '').toString(),
    );
  }
}

/// 端口列表的筛选条件。
///
/// 界面上的即时输入过滤在客户端做（用户敲字不该每次都打 HTTP），
/// 但刷新时的服务端筛选用同一套规则，两者结果一致。
class PortFilter {
  const PortFilter({
    this.text = '',
    this.protocol = '',
    this.scope = '',
    this.nonSystemOnly = false,
    this.withCommandLine = false,
  });

  /// 匹配端口号、进程名、可执行路径、命令行。
  final String text;

  /// `tcp` / `udp` / 空（全部）。
  final String protocol;

  /// `all` / `loopback` / `specific` / 空（全部）。
  final String scope;

  /// 仅显示非系统进程。默认关闭——默认隐藏会让用户误判端口空闲。
  final bool nonSystemOnly;

  /// 是否补全命令行。开销大，默认关闭。
  final bool withCommandLine;

  bool get isEmpty =>
      text.isEmpty && protocol.isEmpty && scope.isEmpty && !nonSystemOnly;

  PortFilter copyWith({
    String? text,
    String? protocol,
    String? scope,
    bool? nonSystemOnly,
    bool? withCommandLine,
  }) {
    return PortFilter(
      text: text ?? this.text,
      protocol: protocol ?? this.protocol,
      scope: scope ?? this.scope,
      nonSystemOnly: nonSystemOnly ?? this.nonSystemOnly,
      withCommandLine: withCommandLine ?? this.withCommandLine,
    );
  }

  Map<String, dynamic> toQuery() {
    return {
      if (text.trim().isNotEmpty) 'q': text.trim(),
      if (protocol.isNotEmpty) 'protocol': protocol,
      if (scope.isNotEmpty) 'scope': scope,
      if (nonSystemOnly) 'system': 'false',
      if (withCommandLine) 'with_command_line': 'true',
    };
  }
}
