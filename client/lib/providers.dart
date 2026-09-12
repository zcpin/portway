import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'models.dart';
import 'services/daemon_client.dart';
import 'services/daemon_discovery.dart';
import 'services/daemon_launcher.dart';

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
/// 候选位置按优先级逐个探活并鉴权，取第一个 token 有效的：
/// 陈旧文件指向的端口无响应或 token 已过期时会被跳过，
/// 从而不会挡住后面真正在运行的那个 daemon。
///
/// 如果没有任何候选可用，会尝试自动拉起随客户端打包的本地 daemon
/// （见 [DaemonLauncher]），成功后重新发现并连接——打开客户端即用，
/// 无需用户手动分两步启动。
///
/// 连接成功后定期探活：daemon 重启会更换端口与 token，旧客户端的所有
/// 认证请求都会失败，此时自动重新发现并重建客户端。
/// 未连接时定期重试，daemon 稍后启动即可自动连上。
final clientProvider = FutureProvider<DaemonClient?>((ref) async {
  var candidates = await ref.watch(discoveryProvider.future);
  var found = await _probeCandidates(candidates);

  // daemon 未运行：尝试自动拉起，成功后重新读取发现文件并探活
  if (found == null && await DaemonLauncher.ensureRunning()) {
    ref.invalidate(discoveryProvider);
    candidates = await ref.watch(discoveryProvider.future);
    found = await _probeCandidates(candidates);
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
    // 没找到 daemon：定期重新发现。DaemonLauncher 内部带退避地重试拉起，
    // 这里只需触发重新发现即可。
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

/// 按优先级逐个探活候选位置，返回第一个通过鉴权的客户端；都不可用时返回 null。
Future<DaemonClient?> _probeCandidates(List<DaemonCandidate> candidates) async {
  for (final candidate in candidates) {
    DaemonClient? probe;
    try {
      probe = DaemonClient(candidate.info, connectTimeout: _probeTimeout);
      if (await probe.checkAuth()) return DaemonClient(candidate.info);
    } catch (_) {
      // 单个发现文件的地址无效时，继续尝试后面的候选。
    } finally {
      probe?.close();
    }
  }
  return null;
}

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

/// daemon 的全局配置项（日志级别、重连默认值），供「设置」页回显与编辑。
class GlobalSettingsNotifier extends AsyncNotifier<GlobalSettings> {
  @override
  Future<GlobalSettings> build() async {
    final client = await ref.watch(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到 daemon');
    }
    return client.getGlobalSettings();
  }

  /// 保存全局配置；daemon 会重启套用新默认值的运行中隧道。
  Future<void> save(GlobalSettings settings) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到 daemon');
    }
    await client.updateGlobalSettings(settings);
    // 重新读取回显（daemon 可能对输入做了归一化，如补齐默认值）
    state = const AsyncLoading();
    state = await AsyncValue.guard(client.getGlobalSettings);
    // 隧道配置可能因全局默认值变化而重启，刷新列表让界面跟上
    await ref.read(tunnelsProvider.notifier).refresh();
  }
}

final globalSettingsProvider =
    AsyncNotifierProvider<GlobalSettingsNotifier, GlobalSettings>(
        GlobalSettingsNotifier.new);

/// daemon 推送的事件流（完整快照、状态变化、日志）。
final eventStreamProvider = StreamProvider<Map<String, dynamic>>((ref) async* {
  final client = await ref.watch(clientProvider.future);
  if (client == null) return;
  yield* client.events();
});

// ---------- 隧道 ----------

class TunnelsNotifier extends AsyncNotifier<List<Tunnel>> {
  int _pushRevision = 0;
  List<Tunnel> _lastPush = const [];

  /// 连接恢复时接收完整快照，补齐离线期间新增、移除及状态改变的隧道。
  void applySnapshot(List<Tunnel> tunnels) {
    _pushRevision++;
    _lastPush = tunnels;
    state = AsyncData(tunnels);
  }

  @override
  Future<List<Tunnel>> build() async {
    final client = await ref.watch(clientProvider.future);
    if (client == null) return [];
    return _loadTunnels(client);
  }

  Future<List<Tunnel>> _loadTunnels(DaemonClient client) async {
    final revision = _pushRevision;
    try {
      final tunnels = await client.getTunnels();
      // 请求期间收到的推送优先，避免迟到的 HTTP 结果恢复旧行或旧状态。
      return revision == _pushRevision ? tunnels : _lastPush;
    } catch (_) {
      if (revision != _pushRevision) return _lastPush;
      rethrow;
    }
  }

  Future<void> refresh() async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      state = const AsyncData([]);
      return;
    }
    if (state.valueOrNull == null) state = const AsyncLoading();
    state = await AsyncValue.guard(() => _loadTunnels(client));
  }

  /// 应用 WebSocket 推送的状态，避免整表刷新造成闪烁。
  void applyStatus(Map<String, dynamic> status) {
    final current = state.valueOrNull;
    if (current == null) return;
    applySnapshot([
      for (final t in current)
        t.copyWith(isRunning: status[t.name] as bool? ?? t.isRunning),
    ]);
  }

  void applyRuntime(Map<String, dynamic> runtime) {
    final current = state.valueOrNull;
    if (current == null) return;
    applySnapshot([
      for (final tunnel in current)
        if (runtime[tunnel.name] is Map)
          tunnel.withRuntime((runtime[tunnel.name] as Map).cast<String, dynamic>())
        else tunnel,
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

  Future<List<BatchResult>> batch(String action, List<String> names) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到 daemon');
    final results = await client.batchTunnels(action, names);
    await refresh();
    return results;
  }
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
  Future<void> unlock(String path, String passphrase) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到 daemon');
    await client.unlockKey(path, passphrase);
    await refresh();
  }

  Future<void> lock(String path) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到 daemon');
    await client.lockKey(path);
    await refresh();
  }
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
