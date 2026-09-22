import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'models.dart';
import 'models/frp.dart';
import 'services/daemon_client.dart';
import 'services/daemon_discovery.dart';
import 'services/daemon_launcher.dart';
import 'services/embedded_engine.dart';
import 'services/tunnel_engine.dart';
import 'services/workspaces.dart';

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
final workspaceStoreProvider = Provider<WorkspaceStore>((ref) => WorkspaceStore());

class WorkspacesNotifier extends AsyncNotifier<WorkspacePreferences> {
  @override
  Future<WorkspacePreferences> build() => ref.watch(workspaceStoreProvider).load();

  Future<void> select(String path) async {
    final next = await ref.read(workspaceStoreProvider).select(path);
    if (ref.mounted) state = AsyncData(next);
  }

  Future<void> create(String name) async {
    final next = await ref.read(workspaceStoreProvider).create(name);
    if (ref.mounted) state = AsyncData(next);
  }
}

final workspacesProvider = AsyncNotifierProvider<WorkspacesNotifier, WorkspacePreferences>(WorkspacesNotifier.new);
typedef DaemonStarter = Future<bool> Function({String? configPath, String? dataDir});
final daemonStarterProvider = Provider<DaemonStarter>((ref) => DaemonLauncher.ensureRunning);

final discoveryProvider = FutureProvider<List<DaemonCandidate>>((ref) async {
  final preferences = await ref.watch(workspacesProvider.future);
  return DaemonDiscovery.loadAll(extraPaths: preferences.workspaces.map((w) => w.discoveryPath).toList());
});

final instanceAvailabilityProvider = FutureProvider<Map<String, bool>>((ref) async {
  final candidates = await ref.watch(discoveryProvider.future);
  final results = await Future.wait(candidates.map((candidate) async {
    DaemonClient? probe;
    try {
      probe = DaemonClient(candidate.info, connectTimeout: _probeTimeout, receiveTimeout: _probeTimeout);
      final owned = probe;
      ref.onDispose(owned.close);
      return MapEntry(candidate.path, await probe.checkAuth());
    } catch (_) {
      return MapEntry(candidate.path, false);
    } finally { probe?.close(); }
  }));
  return Map.fromEntries(results);
});

/// 已连通的引擎；不可用时为 null。
///
/// 有两种引擎形态，优先使用进程内引擎：
///
///   1. **进程内（FFI）**：引擎编译成动态库随客户端分发，加载进客户端进程。
///      没有端口、令牌与服务发现文件，因此不存在「发现文件陈旧 / 端口无人监听」
///      这类启动期竞态，冷启动不需要等待任何握手。
///   2. **独立 daemon**：候选位置按优先级逐个探活并鉴权，取第一个 token 有效的；
///      都没有时自动拉起随客户端打包的 daemon（见 [DaemonLauncher]）。
///
/// 动态库不存在或加载失败时会回退到 daemon 模式，这样 `flutter run` 等开发场景
/// 不必先编译引擎库。可用 `PORTWAY_ENGINE=daemon` 强制使用 daemon。
///
/// daemon 模式连接成功后定期探活：daemon 重启会更换端口与 token，旧客户端的所有
/// 认证请求都会失败，此时自动重新发现并重建客户端；未连接时定期重试。
final clientProvider = FutureProvider<TunnelEngine?>((ref) async {
  if (resolveEngineMode() == EngineMode.embedded) {
    final embedded = await _startEmbedded(ref);
    if (embedded != null) return embedded;
  }
  return _startDaemon(ref);
});

/// 引擎形态。
enum EngineMode { embedded, daemon }

/// 解析应当使用的引擎形态。
///
/// 默认在动态库可用时走进程内引擎；`PORTWAY_ENGINE=daemon` 可强制回退，
/// 便于对比两种模式或在动态库异常时绕过。
EngineMode resolveEngineMode() {
  final forced = Platform.environment['PORTWAY_ENGINE']?.trim().toLowerCase();
  if (forced == 'daemon') return EngineMode.daemon;
  if (forced == 'embedded') return EngineMode.embedded;
  return EmbeddedEngine.isAvailable ? EngineMode.embedded : EngineMode.daemon;
}

/// 启动进程内引擎；失败时返回 null，由调用方回退到 daemon。
///
/// 工作区在这里直接映射为一份配置文件：进程内引擎没有发现文件，
/// 因此只有工作区（带独立 dataDir）能作为「实例」切换，
/// 选中的若是 daemon 实例则退回引擎自己的默认配置查找。
Future<TunnelEngine?> _startEmbedded(Ref ref) async {
  final preferences = await ref.watch(workspacesProvider.future);

  Workspace? workspace;
  for (final candidate in preferences.workspaces) {
    if (DaemonDiscovery.samePath(candidate.discoveryPath, preferences.selectedPath)) {
      workspace = candidate;
      break;
    }
  }

  EmbeddedEngine? engine;
  var disposed = false;
  ref.onDispose(() {
    disposed = true;
    engine?.close();
  });

  try {
    engine = await EmbeddedEngine.launch(configPath: workspace?.configPath);
  } catch (error) {
    debugPrint('进程内引擎启动失败，回退到 daemon 模式: $error');
    return null;
  }
  if (disposed) {
    engine.close();
    return null;
  }
  return engine;
}

/// 当前引擎是否就是某个工作区对应的实例。
///
/// 两种模式的实例标识不同：
///
///   - 进程内引擎没有发现文件，实例由工作区的**配置文件位置**唯一确定，
///     这也是 [_startEmbedded] 把工作区映射成 `configPath` 的逆运算；
///   - daemon 模式仍以**发现文件路径**为准。
///
/// 界面据此显示工作区名与在线状态，否则进程内模式下所有工作区都会显示「未连接」。
bool engineOwnsWorkspace(TunnelEngine engine, Workspace workspace) {
  final info = engine.info;
  return info.embedded
      ? DaemonDiscovery.samePath(workspace.configPath, info.configPath)
      : DaemonDiscovery.samePath(workspace.discoveryPath, info.discoveryPath);
}

/// 发现并连接独立 daemon；无可用的 daemon 时尝试拉起随客户端分发的可执行文件。
Future<TunnelEngine?> _startDaemon(Ref ref) async {
  var disposed = false;
  DaemonClient? found;
  Timer? timer;
  ref.onDispose(() { disposed = true; timer?.cancel(); found?.close(); });
  final preferencesFuture = ref.watch(workspacesProvider.future);
  final candidatesFuture = ref.watch(discoveryProvider.future);
  final starter = ref.watch(daemonStarterProvider);
  final preferences = await preferencesFuture;
  if (disposed) return null;
  var candidates = await candidatesFuture;
  if (disposed) return null;
  List<DaemonCandidate> selected(List<DaemonCandidate> all) => preferences.selectedPath.isEmpty ? all :
    all.where((candidate) => DaemonDiscovery.samePath(candidate.path, preferences.selectedPath)).toList();
  found = await _probeCandidates(selected(candidates), active: () => !disposed);
  if (disposed) { found?.close(); return null; }
  if (found == null) {
    Workspace? workspace;
    for (final candidate in preferences.workspaces) {
      if (DaemonDiscovery.samePath(candidate.discoveryPath, preferences.selectedPath)) { workspace = candidate; break; }
    }
    final canLaunch = preferences.selectedPath.isEmpty || workspace != null;
    if (canLaunch && await starter(configPath: workspace?.configPath, dataDir: workspace?.dataDir)) {
      if (disposed) return null;
      candidates = await DaemonDiscovery.loadAll(extraPaths: preferences.workspaces.map((w) => w.discoveryPath).toList());
      if (disposed) return null;
      found = await _probeCandidates(selected(candidates), active: () => !disposed);
    }
  }
  if (disposed) { found?.close(); return null; }
  final client = found;
  if (client == null) {
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
      if (!await client.checkAuth() && !disposed) {
        ref.invalidate(discoveryProvider);
        ref.invalidateSelf();
      }
    } finally { checking = false; }
  });
  return client;
}

/// 按优先级逐个探活候选位置，返回第一个通过鉴权的客户端；都不可用时返回 null。
Future<DaemonClient?> _probeCandidates(List<DaemonCandidate> candidates, {bool Function()? active}) async {
  for (final candidate in candidates) {
    if (active != null && !active()) return null;
    DaemonClient? probe;
    try {
      final info = candidate.info.withDiscovery(path: candidate.path, shared: candidate.info.isShared);
      probe = DaemonClient(info, connectTimeout: _probeTimeout, receiveTimeout: _probeTimeout);
      if (await probe.checkAuth() && (active == null || active())) return DaemonClient(info);
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

bool _clientIsCurrent(Ref ref, TunnelEngine client) =>
    ref.mounted && identical(ref.read(clientProvider).valueOrNull, client);

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
    if (!_clientIsCurrent(ref, client)) return;
    await client.updateGlobalSettings(settings);
    if (!_clientIsCurrent(ref, client)) return;
    // 重新读取回显（daemon 可能对输入做了归一化，如补齐默认值）
    state = const AsyncLoading();
    final next = await AsyncValue.guard(client.getGlobalSettings);
    if (!_clientIsCurrent(ref, client)) return;
    state = next;
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
    _pushRevision++;
    _lastPush = const [];
    final client = await ref.watch(clientProvider.future);
    if (client == null) return [];
    return _loadTunnels(client);
  }

  Future<List<Tunnel>> _loadTunnels(TunnelEngine client) async {
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
    if (!_clientIsCurrent(ref, client)) return;
    if (state.valueOrNull == null) state = const AsyncLoading();
    final next = await AsyncValue.guard(() => _loadTunnels(client));
    if (_clientIsCurrent(ref, client)) state = next;
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
  Future<void> _act(Future<void> Function(TunnelEngine) action) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到 daemon');
    }
    if (!_clientIsCurrent(ref, client)) return;
    await action(client);
    if (!_clientIsCurrent(ref, client)) return;
    await refresh();
  }

  Future<void> start(String name) => _act((c) => c.startTunnel(name));

  Future<List<BatchResult>> batch(String action, List<String> names) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到 daemon');
    if (!_clientIsCurrent(ref, client)) throw StateError('实例已切换，请重试');
    final results = await client.batchTunnels(action, names);
    if (!_clientIsCurrent(ref, client)) return results;
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
    if (!_clientIsCurrent(ref, client)) return;
    if (editingName == null || editingName.isEmpty) {
      await client.addTunnel(tunnel);
    } else {
      await client.updateTunnel(editingName, tunnel);
    }
    if (!_clientIsCurrent(ref, client)) return;
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
    if (!_clientIsCurrent(ref, client)) return;
    state = const AsyncLoading();
    final next = await AsyncValue.guard(client.getSshConnections);
    if (_clientIsCurrent(ref, client)) state = next;
  }

  Future<void> save(SshConnection conn, {String? editingName}) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) return;
    if (!_clientIsCurrent(ref, client)) return;
    if (editingName == null || editingName.isEmpty) {
      await client.addSshConnection(conn);
    } else {
      await client.updateSshConnection(editingName, conn);
    }
    if (!_clientIsCurrent(ref, client)) return;
    await refresh();
  }

  Future<void> remove(String name) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) return;
    if (!_clientIsCurrent(ref, client)) return;
    await client.deleteSshConnection(name);
    if (!_clientIsCurrent(ref, client)) return;
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
    if (!_clientIsCurrent(ref, client)) throw StateError('实例已切换，请重试');
    await client.unlockKey(path, passphrase);
    if (!_clientIsCurrent(ref, client)) return;
    await refresh();
  }

  Future<void> lock(String path) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到 daemon');
    if (!_clientIsCurrent(ref, client)) throw StateError('实例已切换，请重试');
    await client.lockKey(path);
    if (!_clientIsCurrent(ref, client)) return;
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
    if (!_clientIsCurrent(ref, client)) return;
    state = const AsyncLoading();
    final next = await AsyncValue.guard(client.getKeys);
    if (_clientIsCurrent(ref, client)) state = next;
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

// ---------- FRP ----------

/// FRP 客户端列表（含各自的代理与运行状态）。
///
/// 与隧道一样走「推送优先」：引擎每两秒兜底广播一次完整列表，
/// 操作后也会主动刷新，两者用同一个版本号避免迟到的响应覆盖新数据。
class FrpClientsNotifier extends AsyncNotifier<List<FrpClient>> {
  int _pushRevision = 0;
  List<FrpClient> _lastPush = const [];

  /// 连接恢复或引擎推送时接收完整列表。
  void applySnapshot(List<FrpClient> clients) {
    _pushRevision++;
    _lastPush = clients;
    state = AsyncData(clients);
  }

  @override
  Future<List<FrpClient>> build() async {
    _pushRevision++;
    _lastPush = const [];
    final client = await ref.watch(clientProvider.future);
    if (client == null) return [];
    return _load(client);
  }

  Future<List<FrpClient>> _load(TunnelEngine client) async {
    final revision = _pushRevision;
    try {
      final clients = await client.getFrpClients();
      return revision == _pushRevision ? clients : _lastPush;
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
    if (!_clientIsCurrent(ref, client)) return;
    if (state.valueOrNull == null) state = const AsyncLoading();
    final next = await AsyncValue.guard(() => _load(client));
    if (_clientIsCurrent(ref, client)) state = next;
  }

  /// 执行一个动作并在成功后刷新；失败时把异常抛给页面提示。
  Future<void> _act(Future<void> Function(TunnelEngine) action) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) {
      throw StateError('未连接到引擎');
    }
    if (!_clientIsCurrent(ref, client)) return;
    await action(client);
    if (!_clientIsCurrent(ref, client)) return;
    await refresh();
  }

  Future<void> saveClient(FrpClientPayload payload, {String? editingName}) async {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到引擎');
    if (!_clientIsCurrent(ref, client)) return;
    if (editingName == null || editingName.isEmpty) {
      await client.addFrpClient(payload);
    } else {
      await client.updateFrpClient(editingName, payload);
    }
    if (!_clientIsCurrent(ref, client)) return;
    await refresh();
  }

  Future<void> removeClient(String name) => _act((c) => c.deleteFrpClient(name));
  Future<void> startClient(String name) => _act((c) => c.startFrpClient(name));
  Future<void> stopClient(String name) => _act((c) => c.stopFrpClient(name));
  Future<void> restartClient(String name) => _act((c) => c.restartFrpClient(name));

  Future<void> saveProxy(String client, FrpProxyPayload payload, {String? editingName}) async {
    final engine = await ref.read(clientProvider.future);
    if (engine == null) throw StateError('未连接到引擎');
    if (!_clientIsCurrent(ref, engine)) return;
    if (editingName == null || editingName.isEmpty) {
      await engine.addFrpProxy(client, payload);
    } else {
      await engine.updateFrpProxy(client, editingName, payload);
    }
    if (!_clientIsCurrent(ref, engine)) return;
    await refresh();
  }

  Future<void> removeProxy(String client, String proxy) =>
      _act((c) => c.deleteFrpProxy(client, proxy));

  /// 启用或停用一条代理。引擎侧走 frp 的热更新，不会断开与 frps 的连接。
  Future<void> toggleProxy(String client, String proxy, bool enabled) =>
      _act((c) => c.toggleFrpProxy(client, proxy, enabled));
}

final frpClientsProvider =
    AsyncNotifierProvider<FrpClientsNotifier, List<FrpClient>>(FrpClientsNotifier.new);

// ---------- 日志 ----------

/// 日志缓冲，WebSocket 推送与历史回放共用。
class LogsNotifier extends Notifier<List<LogEntry>> {
  static const _max = 500;
  int _historyGeneration = 0;

  @override
  List<LogEntry> build() => const [];

  void add(LogEntry entry) {
    final next = [...state, entry];
    state = next.length > _max ? next.sublist(next.length - _max) : next;
  }

  Future<void> loadHistory() async {
    final generation = ++_historyGeneration;
    try {
      final client = await ref.read(clientProvider.future);
      if (client == null) return;
      final history = await client.getLogs();
      if (generation != _historyGeneration || !_clientIsCurrent(ref, client)) return;
      final merged = <String, LogEntry>{};
      for (final entry in [...history, ...state]) {
        merged['${entry.timestamp}\u0000${entry.level}\u0000${entry.tunnel}\u0000${entry.message}'] = entry;
      }
      final entries = merged.values.toList()..sort((a, b) =>
        (DateTime.tryParse(a.timestamp)?.microsecondsSinceEpoch ?? 0).compareTo(DateTime.tryParse(b.timestamp)?.microsecondsSinceEpoch ?? 0));
      state = entries.length > _max ? entries.sublist(entries.length - _max) : entries;
    } catch (_) {
      // A disconnected instance must not restore its old history into the next one.
    }
  }

  void clear() { _historyGeneration++; state = const []; }
}

final logsProvider =
    NotifierProvider<LogsNotifier, List<LogEntry>>(LogsNotifier.new);
