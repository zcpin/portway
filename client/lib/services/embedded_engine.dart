import 'dart:async';
import 'dart:convert';
import 'dart:ffi';
import 'dart:io';
import 'dart:isolate';

import 'package:dio/dio.dart';
import 'package:ffi/ffi.dart';
import 'package:flutter/foundation.dart' show visibleForTesting;

import '../models.dart';
import '../models/frp.dart';
import '../models/tunnel_diagnostic.dart';
import 'tunnel_engine.dart';

// ---------- C ABI 声明（对应 daemon/internal/embedded/export.go）----------

/// 事件回调：参数是一条事件 JSON，仅在回调期间有效。
///
/// 这是 C 侧的签名；Dart 侧由 `NativeCallable<_EventCallbackNative>.listener`
/// 包装，回调实现收到的同样是 [Pointer]<[Utf8]>。
typedef _EventCallbackNative = Void Function(Pointer<Utf8>);

typedef _CreateNative = Pointer<Utf8> Function(Pointer<Utf8>);
typedef _CreateDart = Pointer<Utf8> Function(Pointer<Utf8>);

typedef _HandleNative = Pointer<Utf8> Function(Int64);
typedef _HandleDart = Pointer<Utf8> Function(int);

typedef _HandleTextNative = Pointer<Utf8> Function(Int64, Pointer<Utf8>);
typedef _HandleTextDart = Pointer<Utf8> Function(int, Pointer<Utf8>);

typedef _HandleTextTextNative = Pointer<Utf8> Function(Int64, Pointer<Utf8>, Pointer<Utf8>);
typedef _HandleTextTextDart = Pointer<Utf8> Function(int, Pointer<Utf8>, Pointer<Utf8>);

typedef _HandleTextTextTextNative = Pointer<Utf8> Function(Int64, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>);
typedef _HandleTextTextTextDart = Pointer<Utf8> Function(int, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>);

typedef _SetHandlerNative = Pointer<Utf8> Function(Int64, Pointer<NativeFunction<_EventCallbackNative>>);
typedef _SetHandlerDart = Pointer<Utf8> Function(int, Pointer<NativeFunction<_EventCallbackNative>>);

typedef _FreeNative = Void Function(Pointer<Utf8>);
typedef _FreeDart = void Function(Pointer<Utf8>);

/// 引擎返回的错误。
///
/// [describeError] 对非 DioException 一律取 toString()，因此这里只把消息本身
/// 作为描述，界面提示与 daemon 模式保持一致。
class EngineException implements Exception {
  EngineException(this.message);

  final String message;

  @override
  String toString() => message;
}

/// 一次引擎调用的描述。
///
/// 之所以要这个可传递的数据结构：耗时调用会被放到临时 isolate 上执行，
/// 而 isolate 之间只能传值，因此不携带任何闭包或原生资源。
///
/// 参数按顺序给出，最多三个——目前只有 FRP 的「更新代理」用满三个
/// （客户端名、原代理名、新配置）。
class _EngineCall {
  const _EngineCall(this.libraryPath, this.handle, this.symbol, [this.arg1, this.arg2, this.arg3]);

  final String libraryPath;
  final int handle;
  final String symbol;
  final String? arg1;
  final String? arg2;
  final String? arg3;
}

/// 执行一次引擎调用，返回原始 JSON 响应。
///
/// 顶层函数：既能在主 isolate 上直接调用，也作为 [Isolate.run] 的入口。
/// 动态库由系统按路径共享，重复 open 不会重复加载。
String _invokeEngine(_EngineCall call) {
  final library = DynamicLibrary.open(call.libraryPath);
  final freeString = library.lookupFunction<_FreeNative, _FreeDart>('sshtunnel_free_string');

  final args = <String>[
    if (call.arg1 != null) call.arg1!,
    if (call.arg2 != null) call.arg2!,
    if (call.arg3 != null) call.arg3!,
  ];

  final allocated = <Pointer<Utf8>>[];
  Pointer<Utf8> result;
  try {
    for (final arg in args) {
      allocated.add(arg.toNativeUtf8());
    }

    result = switch (allocated.length) {
      0 => library.lookupFunction<_HandleNative, _HandleDart>(call.symbol)(call.handle),
      1 => library.lookupFunction<_HandleTextNative, _HandleTextDart>(call.symbol)(call.handle, allocated[0]),
      2 => library
          .lookupFunction<_HandleTextTextNative, _HandleTextTextDart>(call.symbol)(call.handle, allocated[0], allocated[1]),
      3 => library.lookupFunction<_HandleTextTextTextNative, _HandleTextTextTextDart>(call.symbol)(
          call.handle, allocated[0], allocated[1], allocated[2]),
      _ => throw ArgumentError('引擎调用最多支持 3 个字符串参数，收到 ${allocated.length} 个'),
    };
  } finally {
    for (final pointer in allocated) {
      malloc.free(pointer);
    }
  }

  // 先在原生内存释放前把内容复制到 Dart 侧。
  try {
    return result.toDartString();
  } finally {
    // 必须用库自己的释放函数：字符串由 Go 侧的 C 运行时分发，
    // 直接用 Dart 侧的 free 会跨运行时释放堆内存。
    freeString(result);
  }
}

/// 注销事件回调（传空回调，引擎随即停止推送）。
///
/// 单独实现而不是复用 [_EngineCall]：这个函数第二个参数是函数指针，
/// 与其余「句柄 + 字符串」的签名不同，不能走同一条分派路径。
void _clearEngineEventHandler(String libraryPath, int handle) {
  final library = DynamicLibrary.open(libraryPath);
  final freeString = library.lookupFunction<_FreeNative, _FreeDart>('sshtunnel_free_string');
  final setHandler = library.lookupFunction<_SetHandlerNative, _SetHandlerDart>('sshtunnel_set_event_handler');
  final raw = setHandler(handle, nullptr);
  try {
    EmbeddedEngine._check(raw.toDartString());
  } finally {
    freeString(raw);
  }
}

/// 进程内引擎：把 Go 编译出的动态库加载进客户端进程，通过 dart:ffi 直接调用。
///
/// 与 [DaemonClient] 的区别：
///   - 没有端口、令牌与服务发现文件，不存在「读不到 / 读到陈旧发现文件」这一整类问题；
///   - 引擎与界面同进程，客户端退出即隧道停止（这也是选择单进程方案的前提）；
///   - 事件通过原生回调推回，格式与 daemon 的 WebSocket 事件流一致。
class EmbeddedEngine implements TunnelEngine {
  EmbeddedEngine._(this._libraryPath, this._handle, this.info);

  final String _libraryPath;
  final int _handle;

  @override
  final DaemonInfo info;

  bool _closed = false;
  NativeCallable<_EventCallbackNative>? _callable;

  late final StreamController<Map<String, dynamic>> _events =
      StreamController<Map<String, dynamic>>.broadcast(onListen: _registerEventHandler);

  /// 动态库文件名，各平台与 Go 的 `-buildmode=c-shared` 产物一致。
  static String get libraryFileName {
    if (Platform.isWindows) return 'portway.dll';
    if (Platform.isMacOS) return 'libportway.dylib';
    return 'libportway.so';
  }

  /// 显式指定动态库位置，便于开发时用 `flutter run` 直接指向构建产物。
  static const envLibraryPath = 'SSH_TUNNEL_EMBEDDED_LIB';

  /// 进程内指定动态库位置，优先级高于 [envLibraryPath]。
  ///
  /// 这是为 `flutter test` 准备的：测试进程里没有注入环境变量的入口，
  /// 而测试进程的可执行文件位于 Flutter 缓存中，向上查找也到不了本仓库。
  /// 正常运行时不需要设置。
  @visibleForTesting
  static String? libraryPathOverride;

  /// 查找引擎动态库：显式指定 → 环境变量 → 客户端可执行文件同目录 → 向上查找源码仓库的 daemon/bin。
  ///
  /// 找不到时返回 null，调用方据此回退到 daemon 模式（例如未编译引擎库的开发环境）。
  static String? resolveLibraryPath() {
    final override = libraryPathOverride?.trim();
    if (override != null && override.isNotEmpty) {
      return File(override).existsSync() ? override : null;
    }

    final explicit = Platform.environment[envLibraryPath]?.trim();
    if (explicit != null && explicit.isNotEmpty) {
      return File(explicit).existsSync() ? explicit : null;
    }

    final name = libraryFileName;
    final separator = Platform.pathSeparator;
    final selfDir = File(Platform.resolvedExecutable).parent.path;
    final direct = '$selfDir$separator$name';
    if (File(direct).existsSync()) return direct;

    final parts = selfDir.split(separator);
    for (var i = parts.length - 1; i >= 0; i--) {
      final dir = parts.sublist(0, i + 1).join(separator);
      final candidate = '$dir${separator}daemon${separator}bin$separator$name';
      if (File(candidate).existsSync()) return candidate;
    }
    return null;
  }

  /// 引擎动态库是否可用。
  static bool get isAvailable => resolveLibraryPath() != null;

  /// 加载动态库、创建引擎并启动隧道。
  ///
  /// [configPath] 为空时由引擎按默认规则查找配置
  /// （`ssh-tunnel.toml` / `config.toml` / `~/.ssh-tunnel/config.toml`）。
  static Future<EmbeddedEngine> launch({String? configPath}) async {
    final libraryPath = resolveLibraryPath();
    if (libraryPath == null) {
      throw StateError('未找到引擎动态库 $libraryFileName');
    }

    final library = DynamicLibrary.open(libraryPath);
    final freeString = library.lookupFunction<_FreeNative, _FreeDart>('sshtunnel_free_string');
    final create = library.lookupFunction<_CreateNative, _CreateDart>('sshtunnel_new');

    final configArg = (configPath ?? '').toNativeUtf8();
    final Pointer<Utf8> raw;
    try {
      raw = create(configArg);
    } finally {
      malloc.free(configArg);
    }

    final String created;
    try {
      created = raw.toDartString();
    } finally {
      freeString(raw);
    }

    final envelope = jsonDecode(created);
    if (envelope is! Map || envelope['ok'] != true) {
      throw EngineException('${envelope is Map ? envelope['error'] : created}');
    }
    final handle = (envelope['data'] as Map)['handle'] as int;

    var engine = EmbeddedEngine._(libraryPath, handle, _placeholderInfo);
    try {
      final rawInfo = _invokeEngine(_EngineCall(libraryPath, handle, 'sshtunnel_info'));
      final info = _decodeInfo(rawInfo, configPath);
      engine = EmbeddedEngine._(libraryPath, handle, info);
      engine._invoke(_EngineCall(libraryPath, handle, 'sshtunnel_start'));
      return engine;
    } catch (_) {
      // 启动失败时不要留下无法回收的引擎（隧道可能已经跑起来）。
      _invokeEngine(_EngineCall(libraryPath, handle, 'sshtunnel_close'));
      rethrow;
    }
  }

  /// 创建过程中先用占位信息，读取到版本与配置路径后再替换。
  static const _placeholderInfo = DaemonInfo(
    host: 'embedded',
    port: 0,
    token: '',
    pid: 0,
    version: '',
    configPath: '',
    embedded: true,
  );

  static DaemonInfo _decodeInfo(String raw, String? requested) {
    final envelope = jsonDecode(raw) as Map<String, dynamic>;
    if (envelope['ok'] != true) {
      throw EngineException('${envelope['error']}');
    }
    final data = (envelope['data'] as Map).cast<String, dynamic>();
    return DaemonInfo(
      host: 'embedded',
      port: 0,
      token: '',
      pid: (data['pid'] as num?)?.toInt() ?? 0,
      version: data['version'] as String? ?? '',
      configPath: data['config_path'] as String? ?? (requested ?? ''),
      embedded: true,
    );
  }

  @override
  Stream<Map<String, dynamic>> events() {
    if (_closed) return const Stream.empty();
    return _events.stream;
  }

  /// 首次订阅时注册原生回调，引擎随即补发一份完整快照。
  ///
  /// 与 daemon 的 WebSocket 行为一致：订阅之前产生的事件不重放，
  /// 但订阅后一定能拿到全量列表。
  void _registerEventHandler() {
    if (_closed || _callable != null) return;

    final callable = NativeCallable<_EventCallbackNative>.listener(_onEvent);
    _callable = callable;

    try {
      final library = DynamicLibrary.open(_libraryPath);
      final setHandler = library.lookupFunction<_SetHandlerNative, _SetHandlerDart>('sshtunnel_set_event_handler');
      final freeString = library.lookupFunction<_FreeNative, _FreeDart>('sshtunnel_free_string');
      final raw = setHandler(_handle, callable.nativeFunction);
      try {
        _check(raw.toDartString());
      } finally {
        freeString(raw);
      }
    } catch (error) {
      _callable = null;
      callable.close();
      _events.addError(error);
    }
  }

  /// 原生事件回调。
  ///
  /// **字符串所有权在这里**：`NativeCallable.listener` 是异步回调，Go 侧
  /// `sshtunnel_invoke_event` 返回后才执行本函数，所以引擎那边不能替我们释放。
  /// 读取完必须调用 [sshtunnel_free_string]，否则每来一条事件都会泄漏。
  /// 快照必然走这条路径——它在注册回调时同步派发，早于本函数执行。
  void _onEvent(Pointer<Utf8> event) {
    try {
      if (!_closed) {
        final decoded = jsonDecode(event.toDartString());
        if (decoded is Map && !_events.isClosed) {
          _events.add(decoded.cast<String, dynamic>());
        }
      }
    } catch (_) {
      // 单条事件解析失败不应该中断事件流。
    } finally {
      // 无论解析成功与否都要归还，因此放在 finally 里。
      _freeEvent(event);
    }
  }

  /// 延迟查找库自己的字符串释放函数。
  ///
  /// 必须用库导出的那个：字符串由 Go 侧的 C 运行时分发，
  /// 用 Dart 侧 `package:ffi` 的 free 会跨运行时释放堆内存。
  late final void Function(Pointer<Utf8>) _freeEvent = _lookupFreeString();

  void Function(Pointer<Utf8>) _lookupFreeString() {
    final library = DynamicLibrary.open(_libraryPath);
    return library.lookupFunction<_FreeNative, _FreeDart>(
      'sshtunnel_free_string',
    );
  }

  @override
  bool get isClosed => _closed;

  /// 进程内引擎始终可用：引擎与界面同进程，进程活着它就活着。
  ///
  /// 保留这个方法是因为 [clientProvider] 会周期性探活，daemon 模式下
  /// 它用于发现「daemon 重启后 token 失效」，进程内没有这个概念。
  @override
  Future<bool> checkAuth() async {
    if (_closed) return false;
    try {
      _invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_info'));
      return true;
    } catch (_) {
      return false;
    }
  }

  @override
  void close() {
    if (_closed) return;
    _closed = true;

    // 先注销回调再关闭引擎，避免关闭过程中还有事件打进来。
    final callable = _callable;
    _callable = null;
    if (callable != null) {
      try {
        _clearEngineEventHandler(_libraryPath, _handle);
      } catch (_) {
        // 引擎已不可用时忽略。
      }
      callable.close();
    }

    try {
      _invokeEngine(_EngineCall(_libraryPath, _handle, 'sshtunnel_close'));
    } catch (_) {
      // 关闭失败没有补救手段，进程退出即可释放。
    }
    unawaited(_events.close());
  }

  // ---------- 调用辅助 ----------

  /// 同步调用：只用于读内存状态的方法（列表、日志、配置回显），耗时可忽略。
  String _invoke(_EngineCall call) {
    if (_closed) throw StateError('引擎已关闭');
    return _invokeEngine(call);
  }

  /// 异步调用：放到临时 isolate 上执行，避免 SSH 拨号、诊断这类秒级操作卡住界面线程。
  Future<String> _invokeInBackground(_EngineCall call) {
    if (_closed) {
      return Future.error(StateError('引擎已关闭'));
    }
    return Isolate.run(() => _invokeEngine(call));
  }

  /// 解析统一响应；失败时抛出 [EngineException]。
  static dynamic _check(String raw) {
    final envelope = jsonDecode(raw);
    if (envelope is! Map || envelope['ok'] != true) {
      final message = envelope is Map ? envelope['error'] : null;
      throw EngineException(message == null ? '引擎调用失败' : '$message');
    }
    return envelope['data'];
  }

  // ---------- 隧道 ----------

  @override
  Future<List<Tunnel>> getTunnels() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_tunnels')));
    return (data as List).map((row) => Tunnel.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<void> addTunnel(Tunnel tunnel) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_add_tunnel', jsonEncode(tunnel.toJson()))));
  }

  @override
  Future<void> updateTunnel(String name, Tunnel tunnel) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_update_tunnel', name, jsonEncode(tunnel.toJson()))));
  }

  @override
  Future<void> deleteTunnel(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_delete_tunnel', name)));
  }

  @override
  Future<void> startTunnel(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_start_tunnel', name)));
  }

  @override
  Future<void> stopTunnel(String name) async {
    // Stop 会等到监听与连接全部关闭，可能阻塞到 SSH 层超时，必须放到后台执行。
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_stop_tunnel', name)));
  }

  @override
  Future<void> restartTunnel(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_restart_tunnel', name)));
  }

  @override
  Future<List<BatchResult>> batchTunnels(String action, List<String> names) async {
    final payload = jsonEncode({'action': action, 'names': names});
    // 引擎逐条返回结果，与 HTTP 的 /api/tunnels/batch 一致。
    final raw = await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_batch_tunnels', payload));
    final data = _check(raw);
    return (data as List).map((row) => BatchResult.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<TunnelDiagnostic> diagnoseTunnel(String name, {CancelToken? cancelToken}) async {
    final raw = await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_diagnose_tunnel', name));
    return TunnelDiagnostic.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  // ---------- SSH 连接 ----------

  @override
  Future<List<SshConnection>> getSshConnections() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_ssh_connections')));
    return (data as List).map((row) => SshConnection.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<void> addSshConnection(SshConnection connection) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_add_ssh_connection', jsonEncode(connection.toJson()))));
  }

  @override
  Future<void> updateSshConnection(String name, SshConnection connection) async {
    _check(await _invokeInBackground(_EngineCall(
        _libraryPath, _handle, 'sshtunnel_update_ssh_connection', name, jsonEncode(connection.toJson()))));
  }

  @override
  Future<void> deleteSshConnection(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_delete_ssh_connection', name)));
  }

  @override
  Future<ConnectionDiagnostic> testSshConnection(SshConnection connection, {CancelToken? cancelToken}) async {
    final raw = await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_test_ssh_connection', jsonEncode(connection.toJson())));
    return ConnectionDiagnostic.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  @override
  Future<HostKeyInfo> inspectHostKey(SshConnection connection) async {
    final raw = await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_inspect_host_key', jsonEncode(connection.toJson())));
    return HostKeyInfo.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  @override
  Future<HostKeyInfo> trustHostKey(SshConnection connection, String fingerprint, bool replace) async {
    final payload = jsonEncode({
      'connection': connection.toJson(),
      'fingerprint': fingerprint,
      'replace': replace,
    });
    final raw = await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_trust_host_key', payload));
    return HostKeyInfo.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  // ---------- 私钥 ----------

  @override
  Future<List<KeyInfo>> getKeys() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_keys')));
    return (data as List).map((row) => KeyInfo.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<KeyInfo> statKey(String path) async {
    final raw = _invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_stat_key', path));
    return KeyInfo.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  @override
  Future<void> unlockKey(String path, String passphrase) async {
    final payload = jsonEncode({'path': path, 'passphrase': passphrase});
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_unlock_key', payload)));
  }

  @override
  Future<void> lockKey(String path) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_lock_key', path)));
  }

  // ---------- FRP ----------

  @override
  Future<List<FrpClient>> getFrpClients() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_frp_clients')));
    return (data as List).map((row) => FrpClient.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<void> addFrpClient(FrpClientPayload client) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_frp_add_client', jsonEncode(client.toJson()))));
  }

  @override
  Future<void> updateFrpClient(String name, FrpClientPayload client) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_frp_update_client', name, jsonEncode(client.toJson()))));
  }

  @override
  Future<void> deleteFrpClient(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_frp_delete_client', name)));
  }

  @override
  Future<void> startFrpClient(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_frp_start_client', name)));
  }

  @override
  Future<void> stopFrpClient(String name) async {
    // 停止会等待 frp 关闭连接，放到后台执行，避免卡住界面。
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_frp_stop_client', name)));
  }

  @override
  Future<void> restartFrpClient(String name) async {
    _check(await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_frp_restart_client', name)));
  }

  @override
  Future<void> addFrpProxy(String client, FrpProxyPayload proxy) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_frp_add_proxy', client, jsonEncode(proxy.toJson()))));
  }

  @override
  Future<void> updateFrpProxy(String client, String proxy, FrpProxyPayload payload) async {
    _check(await _invokeInBackground(_EngineCall(
        _libraryPath, _handle, 'sshtunnel_frp_update_proxy', client, proxy, jsonEncode(payload.toJson()))));
  }

  @override
  Future<void> deleteFrpProxy(String client, String proxy) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_frp_delete_proxy', client, proxy)));
  }

  @override
  Future<void> toggleFrpProxy(String client, String proxy, bool enabled) async {
    _check(await _invokeInBackground(_EngineCall(
        _libraryPath, _handle, 'sshtunnel_frp_toggle_proxy', client, proxy, jsonEncode({'enabled': enabled}))));
  }

  // ---------- 日志与配置 ----------

  @override
  Future<List<LogEntry>> getLogs() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_logs')));
    return (data as List).map((row) => LogEntry.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<GlobalSettings> getGlobalSettings() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_global_settings')));
    return GlobalSettings.fromJson((data as Map).cast<String, dynamic>());
  }

  @override
  Future<void> updateGlobalSettings(GlobalSettings settings) async {
    _check(await _invokeInBackground(
        _EngineCall(_libraryPath, _handle, 'sshtunnel_set_global_settings', jsonEncode(settings.toJson()))));
  }

  @override
  Future<ConfigExport> exportConfig() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_export_config')));
    return ConfigExport.fromJson((data as Map).cast<String, dynamic>());
  }

  @override
  Future<ImportPreview> previewImport(String content, String mode) async {
    final payload = jsonEncode({'content': content, 'mode': mode});
    final raw = await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_preview_import', payload));
    return ImportPreview.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  @override
  Future<ConfigBackup> importConfig(String content, String mode, String revision) async {
    final payload = jsonEncode({'content': content, 'mode': mode, 'revision': revision});
    final raw = await _invokeInBackground(_EngineCall(_libraryPath, _handle, 'sshtunnel_import_config', payload));
    return ConfigBackup.fromJson((_check(raw) as Map).cast<String, dynamic>());
  }

  @override
  Future<List<ConfigBackup>> listConfigBackups() async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_list_backups')));
    return (data as List).map((row) => ConfigBackup.fromJson((row as Map).cast<String, dynamic>())).toList();
  }

  @override
  Future<String> readConfigBackup(String name) async {
    final data = _check(_invoke(_EngineCall(_libraryPath, _handle, 'sshtunnel_read_backup', name)));
    return (data as Map)['content'] as String;
  }
}
