/// 一个 FRP 客户端（一份 frpc 配置）的配置与运行状态。
///
/// 对应 daemon 的 /api/frp/clients 返回项，也对应进程内引擎的 frp_snapshot 事件。
class FrpClient {
  final String name;
  final String group;
  final bool autoStart;
  final String serverAddr;
  final int serverPort;
  final String authMethod;
  final String authToken;
  final bool tlsEnable;
  final String logLevel;
  final List<FrpProxy> proxies;
  final bool isRunning;
  final String startedAt;
  final String lastError;

  const FrpClient({
    required this.name,
    this.group = '',
    this.autoStart = true,
    this.serverAddr = '',
    this.serverPort = 7000,
    this.authMethod = 'token',
    this.authToken = '',
    this.tlsEnable = true,
    this.logLevel = '',
    this.proxies = const [],
    this.isRunning = false,
    this.startedAt = '',
    this.lastError = '',
  });

  factory FrpClient.fromJson(Map<String, dynamic> j) => FrpClient(
        name: j['name'] as String? ?? '',
        group: j['group'] as String? ?? '',
        autoStart: j['auto_start'] as bool? ?? true,
        serverAddr: j['server_addr'] as String? ?? '',
        serverPort: _asInt(j['server_port'], 7000),
        authMethod: j['auth_method'] as String? ?? 'token',
        authToken: j['auth_token'] as String? ?? '',
        tlsEnable: j['tls_enable'] as bool? ?? false,
        logLevel: j['log_level'] as String? ?? '',
        proxies: List<FrpProxy>.unmodifiable(
          (j['proxies'] as List? ?? []).map((row) => FrpProxy.fromJson((row as Map).cast<String, dynamic>())),
        ),
        isRunning: j['running'] as bool? ?? false,
        startedAt: j['started_at'] as String? ?? '',
        lastError: j['last_error'] as String? ?? '',
      );

  /// 运行中或有错误时给出可读状态，供列表直接显示。
  String get stateLabel {
    if (isRunning) return '运行中';
    if (lastError.isNotEmpty) return '配置有误';
    return '已停止';
  }

  /// 服务器地址的显示形式。
  String get serverLabel => serverAddr.isEmpty ? '' : '$serverAddr:$serverPort';

  int get enabledProxyCount => proxies.where((proxy) => proxy.enabled).length;
}

/// 一条 FRP 代理（或访问端）。
class FrpProxy {
  final String name;
  final String type;
  final bool enabled;

  /// 是否可以在界面上编辑。访问端（stcp / xtcp / sudp 的 visitor）暂不支持，
  /// 但可以启停与删除。
  final bool editable;

  final String localIp;
  final int localPort;
  final int remotePort;
  final List<String> customDomains;
  final String subdomain;

  /// frp 上报的运行阶段：new / waiting / start / closed 等，未运行时为空。
  final String phase;
  final String remoteAddr;
  final String lastError;

  const FrpProxy({
    required this.name,
    required this.type,
    this.enabled = true,
    this.editable = true,
    this.localIp = '',
    this.localPort = 0,
    this.remotePort = 0,
    this.customDomains = const [],
    this.subdomain = '',
    this.phase = '',
    this.remoteAddr = '',
    this.lastError = '',
  });

  factory FrpProxy.fromJson(Map<String, dynamic> j) => FrpProxy(
        name: j['name'] as String? ?? '',
        type: j['type'] as String? ?? 'tcp',
        enabled: j['enabled'] as bool? ?? true,
        editable: j['editable'] as bool? ?? true,
        localIp: j['local_ip'] as String? ?? '',
        localPort: _asInt(j['local_port']),
        remotePort: _asInt(j['remote_port']),
        customDomains: List<String>.unmodifiable((j['custom_domains'] as List? ?? []).cast<String>()),
        subdomain: j['subdomain'] as String? ?? '',
        phase: j['phase'] as String? ?? '',
        remoteAddr: j['remote_addr'] as String? ?? '',
        lastError: j['last_error'] as String? ?? '',
      );

  String get typeLabel => switch (type) {
        'tcp' => 'TCP',
        'udp' => 'UDP',
        'http' => 'HTTP',
        'https' => 'HTTPS',
        'tcpmux' => 'TCPMUX',
        'stcp' => 'STCP(访问端)',
        'xtcp' => 'XTCP(访问端)',
        'sudp' => 'SUDP(访问端)',
        _ => type.toUpperCase(),
      };

  /// 代理的运行状态。frp 的 phase 取值见其 proxy.WorkingStatus。
  String get stateLabel {
    if (!enabled) return '已停用';
    if (lastError.isNotEmpty) return '失败';
    return switch (phase) {
      'start' => '已连接',
      'new' || 'waiting' => '连接中',
      'closed' => '已断开',
      _ => '未运行',
    };
  }

  /// 本地后端地址，供列表展示。
  String get localLabel => localPort == 0 ? '' : '${localIp.isEmpty ? '127.0.0.1' : localIp}:$localPort';

  /// 本地编辑时提交的配置。
  Map<String, dynamic> toPayload() => {
        'name': name,
        'type': type,
        'enabled': enabled,
        'local_ip': localIp,
        'local_port': localPort,
        'remote_port': remotePort,
        if (customDomains.isNotEmpty) 'custom_domains': customDomains,
        if (subdomain.isNotEmpty) 'subdomain': subdomain,
      };
}

/// 新建 / 更新 FRP 客户端时提交的配置。
class FrpClientPayload {
  final String name;
  final String group;
  final bool autoStart;
  final String serverAddr;
  final int serverPort;
  final String authMethod;
  final String authToken;
  final bool tlsEnable;

  const FrpClientPayload({
    required this.name,
    this.group = '',
    this.autoStart = true,
    required this.serverAddr,
    this.serverPort = 7000,
    this.authMethod = 'token',
    this.authToken = '',
    this.tlsEnable = true,
  });

  Map<String, dynamic> toJson() => {
        'name': name,
        if (group.isNotEmpty) 'group': group,
        'auto_start': autoStart,
        'server_addr': serverAddr,
        'server_port': serverPort,
        'auth_method': authMethod,
        'auth_token': authToken,
        'tls_enable': tlsEnable,
      };
}

/// 新建 / 更新代理时提交的配置。
class FrpProxyPayload {
  final String name;
  final String type;
  final bool enabled;
  final String localIp;
  final int localPort;
  final int remotePort;
  final List<String> customDomains;
  final String subdomain;

  const FrpProxyPayload({
    required this.name,
    required this.type,
    this.enabled = true,
    this.localIp = '127.0.0.1',
    this.localPort = 0,
    this.remotePort = 0,
    this.customDomains = const [],
    this.subdomain = '',
  });

  Map<String, dynamic> toJson() => {
        'name': name,
        'type': type,
        'enabled': enabled,
        'local_ip': localIp,
        'local_port': localPort,
        'remote_port': remotePort,
        if (customDomains.isNotEmpty) 'custom_domains': customDomains,
        if (subdomain.isNotEmpty) 'subdomain': subdomain,
      };
}

/// 代理类型选项。阶段 1 只提供普通代理，访问端类型留到后续版本。
const frpProxyTypes = <String, String>{
  'tcp': 'TCP',
  'udp': 'UDP',
  'http': 'HTTP',
  'https': 'HTTPS',
  'tcpmux': 'TCPMUX',
};

int _asInt(Object? value, [int fallback = 0]) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  if (value is String) return int.tryParse(value) ?? fallback;
  return fallback;
}
