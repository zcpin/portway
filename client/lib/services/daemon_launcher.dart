import 'dart:io';

import 'package:flutter/foundation.dart';

import 'daemon_client.dart';
import 'daemon_discovery.dart';

/// 自动拉起本地 daemon 并等待其就绪。
///
/// 设计目标：打开客户端时自动把 daemon 跑起来，用户无需先手动启动。
/// daemon 可执行文件随客户端一起分发：
///
///   - 发布形态：与客户端可执行文件位于同一目录
///   - 开发形态：向上查找源码仓库里的 `daemon/bin/`（`flutter run` 时用）
///
/// 拉起后 daemon 独立运行：关闭客户端窗口或退出客户端都不影响它，
/// 这正是「隧道不随界面退出」这一架构设计的前提。
class DaemonLauncher {
  DaemonLauncher._();

  /// daemon 可执行文件名。
  static final String _exeName =
      Platform.isWindows ? 'ssh-tunnel-daemon.exe' : 'ssh-tunnel-daemon';

  /// 本会话内是否已经发起过拉起，避免创建重复进程。
  static bool _launchAttempted = false;

  /// 最近一次拉起的时间，用于失败后的退避重试。
  static DateTime _lastLaunchAt = DateTime.fromMillisecondsSinceEpoch(0);

  /// 拉起后等待 daemon 就绪的轮询间隔。
  static const _pollInterval = Duration(milliseconds: 250);

  /// 拉起后等待就绪的最长时间。
  static const _waitTimeout = Duration(seconds: 10);

  /// 一次拉起失败/崩溃后，允许再次拉起的最小间隔。
  static const _retryBackoff = Duration(seconds: 15);

  /// 探活超时：只连回环地址，失败必须很快。
  static const _probeTimeout = Duration(milliseconds: 800);

  /// 返回本地 daemon 可执行文件的绝对路径；找不到时返回 null。
  ///
  /// 优先取客户端可执行文件同目录（发布形态），其次从客户端可执行文件
  /// 所在目录向上逐级查找 `daemon/bin/`（`flutter run` 的 Debug 目录在
  /// 源码仓库深处，向上能找到仓库根）。
  static String? get bundledDaemonPath {
    final candidates = <String>[];
    final selfDir = File(Platform.resolvedExecutable).parent.path;
    candidates.add('$selfDir${Platform.pathSeparator}$_exeName');

    final parts = selfDir.split(Platform.pathSeparator);
    for (var i = parts.length - 1; i >= 0; i--) {
      final dir = parts.sublist(0, i + 1).join(Platform.pathSeparator);
      candidates.add(_join(dir, 'daemon', 'bin', _exeName));
    }

    for (final path in candidates) {
      if (File(path).existsSync()) return path;
    }
    return null;
  }

  /// 确保本地 daemon 正在运行并已可访问；返回 true 表示可用。
  ///
  /// 调用方应在返回 true 后重新读取服务发现文件（daemon 每次启动会
  /// 更换端口与 token）。本方法自带防重：已在冷却期内不会重复拉起。
  static Future<bool> ensureRunning() async {
    if (await _anyDaemonHealthy()) return true;

    final path = bundledDaemonPath;
    if (path == null) return false; // 未随客户端分发，无法自动拉起

    final now = DateTime.now();
    final inBackoff = _launchAttempted &&
        now.difference(_lastLaunchAt) < _retryBackoff;
    if (inBackoff) {
      // 刚拉过还没就绪：不重复创建进程，交给调用方的周期重试再探
      return false;
    }

    _launchAttempted = true;
    _lastLaunchAt = now;

    try {
      // detached 不创建需要客户端消费的 stdout/stderr 管道，
      // daemon 的日志输出与生命周期都不再依赖客户端。
      await Process.start(
        path,
        const ['-hide-console'],
        mode: ProcessStartMode.detached,
      );
    } catch (e) {
      debugPrint('拉起本地 daemon 失败: $e');
      return false;
    }

    return _waitUntilHealthy();
  }

  /// 轮询发现文件并逐个探活，直到有一个 daemon 可访问或超时。
  static Future<bool> _waitUntilHealthy() async {
    final deadline = DateTime.now().add(_waitTimeout);
    while (DateTime.now().isBefore(deadline)) {
      if (await _anyDaemonHealthy()) return true;
      await Future<void>.delayed(_pollInterval);
    }
    return false;
  }

  /// 读取全部候选发现文件并探活，存在任意一个可访问的 daemon 即返回 true。
  static Future<bool> _anyDaemonHealthy() async {
    final candidates = await DaemonDiscovery.loadAll();
    for (final candidate in candidates) {
      DaemonClient? probe;
      try {
        probe = DaemonClient(candidate.info, connectTimeout: _probeTimeout);
        if (await probe.checkAuth()) return true;
      } catch (_) {
        // 跳过不可用的候选地址。
      } finally {
        probe?.close();
      }
    }
    return false;
  }

  static String _join(String a, String b, [String? c, String? d]) {
    final sep = Platform.pathSeparator;
    final parts = [
      a.replaceAll(RegExp(r'[\\/]+$'), ''),
      b,
      ?c,
      ?d,
    ];
    return parts.join(sep);
  }
}
