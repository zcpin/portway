import 'dart:io';

import '../models.dart';

/// 一个候选的服务发现结果。
class DaemonCandidate {
  /// 发现文件路径。
  final String path;

  /// 从文件里解析出的连接信息。
  final DaemonInfo info;

  const DaemonCandidate({required this.path, required this.info});
}

/// 本地 daemon 的服务发现。
///
/// daemon 启动时会把 {host, port, token} 写到发现文件，客户端读取即可自动连接，
/// 无需用户手工填写地址和令牌。写入位置取决于 daemon 的运行方式：
///
///   - 用户级：`<用户主目录>/.ssh-tunnel/daemon.json` —— 前台运行或开机自启
///   - 系统级：平台公共目录 —— 安装为系统服务时以 LocalSystem / root 运行，
///     此时 `os.UserHomeDir()` 指向的是服务账户目录，客户端按用户目录读不到
///
/// 因此这里一次性给出**全部候选位置**，并由调用方逐个探活取第一个有响应的。
/// 这样做同时解决了另一个问题：daemon 被强制结束（或系统重启）时会残留
/// 陈旧文件，只按优先级取第一个会让客户端一直连一个已经不存在的端口。
class DaemonDiscovery {
  static const _subDir = '.ssh-tunnel';
  static const _fileName = 'daemon.json';

  /// 显式指定发现文件所在目录，需与 daemon 侧的 `SSH_TUNNEL_DATA_DIR` 同名。
  static const envDataDir = 'SSH_TUNNEL_DATA_DIR';

  /// 用户主目录路径。Windows 取 USERPROFILE，其他平台取 HOME。
  static String? get homeDir {
    if (Platform.isWindows) {
      return Platform.environment['USERPROFILE'] ?? Platform.environment['HOME'];
    }
    return Platform.environment['HOME'] ?? Platform.environment['USERPROFILE'];
  }

  /// 用户级发现文件路径；主目录不可知时返回 null。
  static String? get userPath {
    final home = homeDir;
    if (home == null || home.isEmpty) return null;
    return _join(home, _subDir, _fileName);
  }

  /// 系统级发现文件路径。
  static String get sharedPath {
    final override = _dataDirOverride;
    if (override != null) return _join(override, _fileName);

    if (Platform.isWindows) {
      final programData =
          Platform.environment['ProgramData'] ?? r'C:\ProgramData';
      return _join(programData, 'ssh-tunnel', _fileName);
    }
    // Linux / macOS 的系统服务均由 root 运行，daemon 侧同样写这里
    return _join('/var/lib', 'ssh-tunnel', _fileName);
  }

  /// 候选位置，顺序即优先级：显式覆盖 → 用户级 → 系统级。
  static List<String> get candidatePaths {
    final paths = <String>[];

    void add(String? path) {
      if (path == null || path.isEmpty) return;
      if (paths.any((existing) => _samePath(existing, path))) return;
      paths.add(path);
    }

    // 显式指定时优先，与 daemon 侧的覆盖行为一致
    final override = _dataDirOverride;
    if (override != null) add(_join(override, _fileName));

    add(userPath);
    add(sharedPath);
    return paths;
  }

  /// 读取全部可解析的候选文件，按优先级排序。
  ///
  /// 只做读取与解析，不探活——探活需要发 HTTP 请求，属于调用方的职责。
  static Future<List<DaemonCandidate>> loadAll() async {
    final shared = sharedPath;
    final candidates = <DaemonCandidate>[];

    for (final path in candidatePaths) {
      final info = await _read(path, isShared: _samePath(path, shared));
      if (info != null) {
        candidates.add(DaemonCandidate(path: path, info: info));
      }
    }
    return candidates;
  }

  /// 读取单个发现文件，文件不存在或内容不可解析时返回 null。
  static Future<DaemonInfo?> _read(String path, {required bool isShared}) async {
    try {
      final file = File(path);
      if (!await file.exists()) return null;

      final decoded = tryDecode(await file.readAsString());
      if (decoded == null) return null;

      final info = DaemonInfo.fromJson(decoded);
      if (info.port <= 0) return null;
      return info.withDiscovery(path: path, shared: isShared);
    } catch (_) {
      // 权限不足、文件被写坏等情况一律当作「该候选不可用」
      return null;
    }
  }

  /// `SSH_TUNNEL_DATA_DIR` 的值，未设置时返回 null。
  static String? get _dataDirOverride {
    final raw = Platform.environment[envDataDir]?.trim();
    return (raw == null || raw.isEmpty) ? null : raw;
  }

  static String _join(String a, String b, [String? c]) {
    final sep = Platform.pathSeparator;
    final parts = [a.replaceAll(RegExp(r'[\\/]+$'), ''), b, ?c];
    return parts.join(sep);
  }

  /// 路径比较：忽略分隔符差异与大小写（Windows 上同一路径可能多种写法）。
  static bool _samePath(String a, String b) =>
      a.replaceAll('\\', '/').toLowerCase() ==
      b.replaceAll('\\', '/').toLowerCase();
}
