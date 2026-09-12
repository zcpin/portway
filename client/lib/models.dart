import 'dart:convert';

/// 隧道配置与运行状态的合并视图，对应 daemon 的 /api/tunnels 返回项。
class Tunnel {
  final String name;
  final String group;
  final bool autoStart;
  final int localPort;
  final String remoteHost;
  final int remotePort;
  final String sshConnection;
  final String sshHost;
  final String sshUser;
  final String keyFile;
  final String hostKeyCheck;
  final String knownHostsFile;
  final String reconnectStrategy;
  final String reconnectInterval;
  final int maxReconnectAttempts;
  final bool isRunning;
  final String state;
  final String lastError;
  final int retryCount;
  final String connectedAt;

  const Tunnel({
    required this.name,
    this.group = '',
    this.autoStart = true,
    required this.localPort,
    required this.remoteHost,
    required this.remotePort,
    required this.sshConnection,
    required this.sshHost,
    required this.sshUser,
    this.keyFile = '',
    this.hostKeyCheck = '',
    this.knownHostsFile = '',
    required this.reconnectStrategy,
    required this.reconnectInterval,
    required this.maxReconnectAttempts,
    required this.isRunning,
    this.state = '',
    this.lastError = '',
    this.retryCount = 0,
    this.connectedAt = '',
  });

  factory Tunnel.fromJson(Map<String, dynamic> j) => Tunnel(
        name: j['name'] as String? ?? '',
        group: j['group'] as String? ?? '',
        autoStart: j['auto_start'] as bool? ?? true,
        localPort: _asInt(j['local_port']),
        remoteHost: j['remote_host'] as String? ?? '',
        remotePort: _asInt(j['remote_port']),
        sshConnection: j['ssh_connection'] as String? ?? '',
        sshHost: j['ssh_host'] as String? ?? '',
        sshUser: j['ssh_user'] as String? ?? '',
        keyFile: j['key_file'] as String? ?? '',
        hostKeyCheck: j['host_key_check'] as String? ?? '',
        knownHostsFile: j['known_hosts_file'] as String? ?? '',
        reconnectStrategy: j['reconnect_strategy'] as String? ?? '',
        reconnectInterval: j['reconnect_interval'] as String? ?? '',
        maxReconnectAttempts: _asInt(j['max_reconnect_attempts']),
        isRunning: j['is_running'] as bool? ?? false,
        state: j['state'] as String? ?? '',
        lastError: j['last_error'] as String? ?? '',
        retryCount: _asInt(j['retry_count']),
        connectedAt: j['connected_at'] as String? ?? '',
      );

  Map<String, dynamic> toJson() => {
        'name': name,
        if (group.isNotEmpty) 'group': group,
        if (!autoStart) 'auto_start': false,
        'local_port': localPort,
        'remote_host': remoteHost,
        'remote_port': remotePort,
        if (sshConnection.isNotEmpty) 'ssh_connection': sshConnection,
        if (sshHost.isNotEmpty) 'ssh_host': sshHost,
        if (sshUser.isNotEmpty) 'ssh_user': sshUser,
        if (keyFile.isNotEmpty) 'key_file': keyFile,
        if (hostKeyCheck.isNotEmpty) 'host_key_check': hostKeyCheck,
        if (knownHostsFile.isNotEmpty) 'known_hosts_file': knownHostsFile,
        'reconnect_strategy': reconnectStrategy,
        'reconnect_interval': reconnectInterval,
        'max_reconnect_attempts': maxReconnectAttempts,
      };

  String get stateLabel => switch (state) {
        'connecting' => '连接中',
        'connected' => '已连接',
        'reconnecting' => '重连中',
        'failed' => '连接失败',
        'stopped' => '已停止',
        _ => isRunning ? '运行中' : '已停止',
      };

  Tunnel duplicateAs(String name, {int? localPort}) => Tunnel.fromJson({
        ...toJson(), 'name': name, 'local_port': localPort ?? this.localPort,
      });

  Tunnel copyWith({
    bool? isRunning,
    String? state,
    String? lastError,
    int? retryCount,
    String? connectedAt,
  }) => Tunnel(
        name: name,
        group: group,
        autoStart: autoStart,
        localPort: localPort,
        remoteHost: remoteHost,
        remotePort: remotePort,
        sshConnection: sshConnection,
        sshHost: sshHost,
        sshUser: sshUser,
        keyFile: keyFile,
        hostKeyCheck: hostKeyCheck,
        knownHostsFile: knownHostsFile,
        reconnectStrategy: reconnectStrategy,
        reconnectInterval: reconnectInterval,
        maxReconnectAttempts: maxReconnectAttempts,
        isRunning: isRunning ?? this.isRunning,
        state: state ?? this.state,
        lastError: lastError ?? this.lastError,
        retryCount: retryCount ?? this.retryCount,
        connectedAt: connectedAt ?? this.connectedAt,
      );

  Tunnel withRuntime(Map<String, dynamic> runtime) => copyWith(
        isRunning: runtime['is_running'] as bool?,
        state: runtime['state'] as String?,
        lastError: runtime['last_error'] as String?,
        retryCount: runtime['retry_count'] as int?,
        connectedAt: runtime['connected_at'] as String?,
      );
}

class ConnectionDiagnostic {
  final bool ok;
  final int elapsedMs;
  final String error;

  const ConnectionDiagnostic({
    required this.ok,
    required this.elapsedMs,
    this.error = '',
  });

  factory ConnectionDiagnostic.fromJson(Map<String, dynamic> json) =>
      ConnectionDiagnostic(
        ok: json['ok'] == true,
        elapsedMs: _asInt(json['elapsed_ms']),
        error: json['error'] as String? ?? '',
      );
}

class BatchResult {
  final String name;
  final bool ok;
  final String error;
  const BatchResult({required this.name, required this.ok, this.error = ''});
  factory BatchResult.fromJson(Map<String, dynamic> json) => BatchResult(
    name: json['name'] as String, ok: json['ok'] == true,
    error: json['error'] as String? ?? '',
  );
}

/// 可复用的 SSH 连接配置。
class SshConnection {
  final String name;
  final String host;
  final String user;
  final String keyFile;
  final String hostKeyCheck;
  final String knownHostsFile;

  const SshConnection({
    required this.name,
    required this.host,
    required this.user,
    required this.keyFile,
    this.hostKeyCheck = '',
    this.knownHostsFile = '',
  });

  factory SshConnection.fromJson(Map<String, dynamic> j) => SshConnection(
        name: j['name'] as String? ?? '',
        host: j['host'] as String? ?? '',
        user: j['user'] as String? ?? '',
        keyFile: j['key_file'] as String? ?? '',
        hostKeyCheck: j['host_key_check'] as String? ?? '',
        knownHostsFile: j['known_hosts_file'] as String? ?? '',
      );

  Map<String, dynamic> toJson() => {
        'name': name,
        'host': host,
        'user': user,
        'key_file': keyFile,
        if (hostKeyCheck.isNotEmpty) 'host_key_check': hostKeyCheck,
        if (knownHostsFile.isNotEmpty) 'known_hosts_file': knownHostsFile,
      };
}

/// 一条日志。
class LogEntry {
  final String timestamp;
  final String level;
  final String message;
  final String tunnel;

  const LogEntry({
    required this.timestamp,
    required this.level,
    required this.message,
    required this.tunnel,
  });

  factory LogEntry.fromJson(Map<String, dynamic> j) => LogEntry(
        timestamp: j['timestamp'] as String? ?? '',
        level: j['level'] as String? ?? 'info',
        message: j['message'] as String? ?? '',
        tunnel: j['tunnel'] as String? ?? '',
      );
}

/// 配置中引用的一个私钥文件。
///
/// 私钥不复制到 daemon 目录，只记录本地路径，因此这里同时保留
/// 配置里填写的原始路径与 daemon 解析出的实际路径。
class KeyInfo {
  final String name;
  final String path;
  final String resolved;
  final bool exists;
  final int size;
  final String modified;
  final List<String> usedBy;

  const KeyInfo({
    required this.name,
    required this.path,
    required this.resolved,
    required this.exists,
    required this.size,
    required this.modified,
    required this.usedBy,
  });

  factory KeyInfo.fromJson(Map<String, dynamic> j) => KeyInfo(
        name: j['name'] as String? ?? '',
        path: j['path'] as String? ?? '',
        resolved: j['resolved'] as String? ?? '',
        exists: j['exists'] as bool? ?? false,
        size: _asInt(j['size']),
        modified: j['modified'] as String? ?? '',
        usedBy: (j['used_by'] as List? ?? const [])
            .map((e) => e.toString())
            .toList(),
      );
}

/// daemon 的连接信息，来自服务发现文件。
///
/// 发现文件可能位于用户目录（daemon 以当前用户身份运行）或系统公共目录
/// （daemon 以系统服务身份运行），后两个字段由客户端在读取时补充。
class DaemonInfo {
  final String host;
  final int port;
  final String token;
  final int pid;
  final String version;
  final String configPath;

  /// 发现文件的来源路径。
  final String discoveryPath;

  /// 是否来自系统级公共目录，即 daemon 以系统服务方式运行。
  final bool isShared;

  const DaemonInfo({
    required this.host,
    required this.port,
    required this.token,
    required this.pid,
    required this.version,
    required this.configPath,
    this.discoveryPath = '',
    this.isShared = false,
  });

  factory DaemonInfo.fromJson(Map<String, dynamic> j) => DaemonInfo(
        host: j['host'] as String? ?? '127.0.0.1',
        port: _asInt(j['port']),
        token: j['token'] as String? ?? '',
        pid: _asInt(j['pid']),
        version: j['version'] as String? ?? '',
        configPath: j['config_path'] as String? ?? '',
      );

  /// 补充客户端侧的发现来源信息。
  DaemonInfo withDiscovery({required String path, required bool shared}) =>
      DaemonInfo(
        host: host,
        port: port,
        token: token,
        pid: pid,
        version: version,
        configPath: configPath,
        discoveryPath: path,
        isShared: shared,
      );

  /// 运行方式的中文描述，用于界面提示。
  String get sourceLabel => isShared ? '系统服务' : '用户进程';

  String get httpBase => Uri(scheme: 'http', host: host, port: port).toString();
  String get wsBase => Uri(scheme: 'ws', host: host, port: port).toString();
}

/// daemon 的全局配置项（日志级别、重连默认值），对应 GET/PUT /api/config。
///
/// 单个隧道未显式配置重连字段时，会回退使用这里的默认值。
class GlobalSettings {
  final String logLevel;
  final String reconnectStrategy;
  final String reconnectInterval;
  final int maxReconnectAttempts;

  const GlobalSettings({
    required this.logLevel,
    required this.reconnectStrategy,
    required this.reconnectInterval,
    required this.maxReconnectAttempts,
  });

  factory GlobalSettings.fromJson(Map<String, dynamic> j) => GlobalSettings(
        logLevel: j['log_level'] as String? ?? '',
        reconnectStrategy: j['reconnect_strategy'] as String? ?? 'fixed',
        reconnectInterval: j['reconnect_interval'] as String? ?? '5s',
        maxReconnectAttempts: _asInt(j['max_reconnect_attempts']),
      );

  Map<String, dynamic> toJson() => {
        'log_level': logLevel,
        'reconnect_strategy': reconnectStrategy,
        'reconnect_interval': reconnectInterval,
        'max_reconnect_attempts': maxReconnectAttempts,
      };

  GlobalSettings copyWith({
    String? logLevel,
    String? reconnectStrategy,
    String? reconnectInterval,
    int? maxReconnectAttempts,
  }) =>
      GlobalSettings(
        logLevel: logLevel ?? this.logLevel,
        reconnectStrategy: reconnectStrategy ?? this.reconnectStrategy,
        reconnectInterval: reconnectInterval ?? this.reconnectInterval,
        maxReconnectAttempts:
            maxReconnectAttempts ?? this.maxReconnectAttempts,
      );
}

int _asInt(dynamic v) {
  if (v is int) return v;
  if (v is num) return v.toInt();
  if (v is String) return int.tryParse(v) ?? 0;
  return 0;
}

/// 解析 JSON 字符串，失败返回 null。
Map<String, dynamic>? tryDecode(String source) {
  try {
    final decoded = jsonDecode(source);
    if (decoded is Map<String, dynamic>) return decoded;
  } catch (_) {}
  return null;
}
