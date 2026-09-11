import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../services/daemon_client.dart';
import '../services/daemon_discovery.dart';

/// riverpod 3 移除了 AsyncValue.valueOrNull，这里补一个等价实现，
/// 便于在 loading / error 状态下安全取值。
extension AsyncValueOrNull<T> on AsyncValue<T> {
  T? get valueOrNull => switch (this) {
        AsyncData(:final value) => value,
        _ => null,
      };
}

/// 服务发现的候选结果（仅读取与解析，未探活）。
///
/// daemon 以用户进程或系统服务运行时写入位置不同，因此这里可能返回多条。
final discoveryProvider = FutureProvider<List<DaemonCandidate>>((ref) async {
  return DaemonDiscovery.loadAll();
});

/// 已连通的 daemon 客户端；daemon 未运行时为 null。
///
/// 候选位置按优先级逐个探活，取第一个真正有响应的：
/// 陈旧文件（daemon 被强制结束后残留）指向的端口不会有响应，会被跳过，
/// 从而不会挡住后面真正在运行的那个 daemon。
///
/// 连接成功后定期探活：daemon 重启会更换端口与 token，旧客户端的所有
/// 认证请求都会失败，此时自动重新发现并重建客户端。
/// 未连接时定期重试，daemon 稍后启动即可自动连上。
final clientProvider = FutureProvider<DaemonClient?>((ref) async {
  final candidates = await ref.watch(discoveryProvider.future);

  DaemonClient? found;
  for (final candidate in candidates) {
    final probe = DaemonClient(candidate.info, connectTimeout: _probeTimeout);
    final alive = await probe.ping();
    probe.close();
    if (!alive) continue;

    // 探活用的客户端超时很短，确认存活后换成正常超时的客户端
    found = DaemonClient(candidate.info);
    break;
  }

  var disposed = false;
  Timer? timer;
  ref.onDispose(() {
    disposed = true;
    timer?.cancel();
    found?.close();
  });

  final client = found;
  if (client == null) {
    // 没找到 daemon：定期重新发现，让稍后启动的 daemon 自动被连上
    timer = Timer.periodic(_rediscoveryInterval, (_) {
      if (disposed) return;
      ref.invalidate(discoveryProvider);
      ref.invalidateSelf();
    });
    return null;
  }

  var checking = false;
  timer = Timer.periodic(_healthCheckInterval, (_) async {
    if (disposed || checking) return;
    checking = true;
    try {
      final ok = await client.checkAuth();
      if (!ok && !disposed) {
        ref.invalidate(discoveryProvider);
        ref.invalidateSelf();
      }
    } finally {
      checking = false;
    }
  });

  return client;
});

/// 探活超时：只连回环地址，失败必须很快，否则逐个候选尝试会拖慢启动。
const _probeTimeout = Duration(milliseconds: 800);

/// 已连接时的探活间隔（daemon 重启后 token 会失效）。
const _healthCheckInterval = Duration(seconds: 5);

/// 未发现 daemon 时的重新发现间隔。
const _rediscoveryInterval = Duration(seconds: 3);

/// 当前连接的 daemon 信息；未连接时为 null。
final daemonInfoProvider = FutureProvider<DaemonInfo?>((ref) async {
  final client = await ref.watch(clientProvider.future);
  return client?.info;
});

/// daemon 推送的事件流（状态变化、日志）。
final eventStreamProvider = StreamProvider<Map<String, dynamic>>((ref) async* {
  final client = await ref.watch(clientProvider.future);
  if (client == null) return;
  yield* client.events();
});

// ---------- 隧道 ----------

class TunnelsNotifier extends AsyncNotifier<List<Tunnel>> {
  @override
  Future<List<Tunnel>> build() async {
    final client = await ref.watch(clientProvider.future);
    if (client == null) return [];
    return client.getTunnels();
  }

  Future<void> refresh() async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      state = const AsyncData([]);
      return;
    }
    state = const AsyncLoading();
    state = await AsyncValue.guard(client.getTunnels);
  }

  /// 应用 WebSocket 推送的状态，避免整表刷新造成闪烁。
  void applyStatus(Map<String, dynamic> status) {
    final current = state.valueOrNull;
    if (current == null) return;
    state = AsyncData([
      for (final t in current)
        t.copyWith(isRunning: status[t.name] as bool? ?? t.isRunning),
    ]);
  }

  /// 执行一个动作并在成功后刷新列表。
  ///
  /// 失败时把异常抛给调用方（由页面用 SnackBar 提示），不改动列表状态：
  /// 一次操作失败（如端口被占用）不应该让整个隧道列表变成错误页。
  Future<void> _act(Future<void> Function(DaemonClient) action) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到 daemon');
    }
    await action(client);
    await refresh();
  }

  Future<void> start(String name) => _act((c) => c.startTunnel(name));
  Future<void> stop(String name) => _act((c) => c.stopTunnel(name));
  Future<void> restart(String name) => _act((c) => c.restartTunnel(name));
  Future<void> remove(String name) => _act((c) => c.deleteTunnel(name));

  Future<void> save(Tunnel tunnel, {String? editingName}) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到 daemon');
    }
    if (editingName == null || editingName.isEmpty) {
      await client.addTunnel(tunnel);
    } else {
      await client.updateTunnel(editingName, tunnel);
    }
    await refresh();
  }
}

final tunnelsProvider =
    AsyncNotifierProvider<TunnelsNotifier, List<Tunnel>>(TunnelsNotifier.new);

// ---------- SSH 连接 ----------

class SshConnectionsNotifier extends AsyncNotifier<List<SshConnection>> {
  @override
  Future<List<SshConnection>> build() async {
    final client = await ref.watch(clientProvider.future);
    if (client == null) return [];
    return client.getSshConnections();
  }

  Future<void> refresh() async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      state = const AsyncData([]);
      return;
    }
    state = const AsyncLoading();
    state = await AsyncValue.guard(client.getSshConnections);
  }

  Future<void> save(SshConnection conn, {String? editingName}) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) return;
    if (editingName == null || editingName.isEmpty) {
      await client.addSshConnection(conn);
    } else {
      await client.updateSshConnection(editingName, conn);
    }
    await refresh();
  }

  Future<void> remove(String name) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) return;
    await client.deleteSshConnection(name);
    await refresh();
  }
}

final sshConnectionsProvider =
    AsyncNotifierProvider<SshConnectionsNotifier, List<SshConnection>>(
        SshConnectionsNotifier.new);

// ---------- 密钥 ----------

class KeysNotifier extends AsyncNotifier<List<KeyInfo>> {
  @override
  Future<List<KeyInfo>> build() async {
    final client = await ref.watch(clientProvider.future);
    if (client == null) return [];
    return client.getKeys();
  }

  Future<void> refresh() async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      state = const AsyncData([]);
      return;
    }
    state = const AsyncLoading();
    state = await AsyncValue.guard(client.getKeys);
  }

  /// 让 daemon 校验一个私钥路径是否可读，返回校验结果（路径不存在时 exists 为 false）。
  Future<KeyInfo> stat(String path) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到 daemon');
    }
    return client.statKey(path);
  }
}

final keysProvider =
    AsyncNotifierProvider<KeysNotifier, List<KeyInfo>>(KeysNotifier.new);

// ---------- 日志 ----------

/// 日志缓冲，WebSocket 推送与历史回放共用。
class LogsNotifier extends Notifier<List<LogEntry>> {
  static const _max = 500;

  @override
  List<LogEntry> build() => const [];

  void add(LogEntry entry) {
    final next = [...state, entry];
    state = next.length > _max ? next.sublist(next.length - _max) : next;
  }

  Future<void> loadHistory() async {
    final client = await ref.read(clientProvider.future);
    if (client == null) return;
    final history = await client.getLogs();
    state = history.length > _max
        ? history.sublist(history.length - _max)
        : history;
  }

  void clear() => state = const [];
}

final logsProvider =
    NotifierProvider<LogsNotifier, List<LogEntry>>(LogsNotifier.new);
