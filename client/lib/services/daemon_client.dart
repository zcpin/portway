import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:dio/dio.dart';

import '../models.dart';

/// WebSocket 断线后的重连间隔。
const _reconnectDelay = Duration(seconds: 2);

/// 与本地 daemon 通信的客户端：REST 走 Dio，事件流走 WebSocket。
///
/// 所有请求自动带上 token（daemon 每次启动会重新生成，通过服务发现文件获取）。
class DaemonClient {
  final DaemonInfo info;
  final Dio _dio;
  final Duration _connectTimeout;
  WebSocket? _socket;
  HttpClient? _connectingClient;
  bool _closed = false;

  /// [connectTimeout] 供服务发现阶段使用：遍历多个候选位置时，
  /// 指向已退出进程的陈旧条目应当很快失败，而不是每个都等满默认超时。
  DaemonClient(this.info, {Duration connectTimeout = const Duration(seconds: 5)})
      : _connectTimeout = connectTimeout,
        _dio = Dio(BaseOptions(
          baseUrl: info.httpBase,
          connectTimeout: connectTimeout,
          receiveTimeout: const Duration(seconds: 10),
          headers: {
            if (info.token.isNotEmpty) 'X-Auth-Token': info.token,
          },
        ));

  /// 健康检查，用于探测 daemon 是否就绪（该接口免认证）。
  Future<bool> ping() async {
    try {
      final resp = await _dio.get('/api/health');
      return resp.statusCode == 200;
    } catch (_) {
      return false;
    }
  }

  /// 探活并校验 token 是否仍然有效（访问需要认证的接口）。
  ///
  /// daemon 重启后会更换端口与 token，此时返回 false，
  /// 调用方应重新读取服务发现文件并重建客户端。
  Future<bool> checkAuth() async {
    try {
      final resp = await _dio.get('/api/status');
      return resp.statusCode == 200;
    } catch (_) {
      return false;
    }
  }

  Future<List<Tunnel>> getTunnels() async {
    final resp = await _dio.get('/api/tunnels');
    return (resp.data as List).map((e) => Tunnel.fromJson(e)).toList();
  }

  Future<List<SshConnection>> getSshConnections() async {
    final resp = await _dio.get('/api/ssh-connections');
    return (resp.data as List).map((e) => SshConnection.fromJson(e)).toList();
  }

  Future<ConnectionDiagnostic> testSshConnection(
    SshConnection connection, {CancelToken? cancelToken}
  ) async {
    final response = await _dio.post('/api/ssh-connections/test',
        data: connection.toJson(), cancelToken: cancelToken);
    return ConnectionDiagnostic.fromJson(response.data);
  }

  Future<List<KeyInfo>> getKeys() async {
    final resp = await _dio.get('/api/keys');
    return (resp.data as List).map((e) => KeyInfo.fromJson(e)).toList();
  }

  Future<List<LogEntry>> getLogs() async {
    final resp = await _dio.get('/api/logs');
    return (resp.data as List).map((e) => LogEntry.fromJson(e)).toList();
  }

  Future<void> addTunnel(Tunnel t) => _post('/api/tunnels', t.toJson());

  Future<void> updateTunnel(String name, Tunnel t) =>
      _put('/api/tunnels/${Uri.encodeComponent(name)}', t.toJson());

  Future<void> deleteTunnel(String name) =>
      _delete('/api/tunnels/${Uri.encodeComponent(name)}');

  Future<void> startTunnel(String name) =>
      _post('/api/tunnels/${Uri.encodeComponent(name)}/start', null);

  Future<List<BatchResult>> batchTunnels(String action, List<String> names) async {
    final response = await _dio.post('/api/tunnels/batch', data: {'action': action, 'names': names});
    return (response.data as List).map((value) => BatchResult.fromJson(value)).toList();
  }

  Future<void> stopTunnel(String name) =>
      _post('/api/tunnels/${Uri.encodeComponent(name)}/stop', null);

  Future<void> restartTunnel(String name) =>
      _post('/api/tunnels/${Uri.encodeComponent(name)}/restart', null);

  Future<void> addSshConnection(SshConnection c) =>
      _post('/api/ssh-connections', c.toJson());

  Future<void> updateSshConnection(String name, SshConnection c) =>
      _put('/api/ssh-connections/${Uri.encodeComponent(name)}', c.toJson());

  Future<void> deleteSshConnection(String name) =>
      _delete('/api/ssh-connections/${Uri.encodeComponent(name)}');

  /// 校验一个私钥路径对 daemon 是否可读。
  ///
  /// 私钥以本地路径的形式引用（不上传），但客户端与 daemon 可能处于不同的
  /// 文件系统语境（尤其是 daemon 以系统服务运行时），因此选完文件后交给
  /// daemon 确认一次，避免配置写入后才发现读不到。
  Future<KeyInfo> statKey(String path) async {
    final resp = await _dio.get(
      '/api/keys/stat',
      queryParameters: {'path': path},
    );
    return KeyInfo.fromJson(resp.data);
  }

  Future<void> unlockKey(String path, String passphrase) => _post('/api/keys/unlock', {'path': path, 'passphrase': passphrase});
  Future<void> lockKey(String path) => _post('/api/keys/lock', {'path': path});
  Future<HostKeyInfo> inspectHostKey(SshConnection connection) async =>
      HostKeyInfo.fromJson((await _dio.post('/api/ssh-connections/host-key', data: connection.toJson())).data);
  Future<HostKeyInfo> trustHostKey(SshConnection connection, String fingerprint, bool replace) async =>
      HostKeyInfo.fromJson((await _dio.post('/api/ssh-connections/trust',
        data: {'connection': connection.toJson(), 'fingerprint': fingerprint, 'replace': replace})).data);

  Future<void> reload() => _post('/api/reload', null);

  Future<ConfigExport> exportConfig() async => ConfigExport.fromJson((await _dio.get('/api/config/export')).data);

  Future<ImportPreview> previewImport(String content, String mode) async =>
      ImportPreview.fromJson((await _dio.post('/api/config/preview', data: {'content': content, 'mode': mode})).data);

  Future<ConfigBackup> importConfig(String content, String mode, String revision) async =>
      ConfigBackup.fromJson((await _dio.post('/api/config/import', data: {'content': content, 'mode': mode, 'revision': revision})).data);

  Future<List<ConfigBackup>> listConfigBackups() async =>
      ((await _dio.get('/api/config/backups')).data as List).map((value) => ConfigBackup.fromJson(value)).toList();

  Future<String> readConfigBackup(String name) async =>
      (await _dio.get('/api/config/backups/${Uri.encodeComponent(name)}')).data['content'] as String;

  /// 读取全局配置项（日志级别、重连默认值）。
  Future<GlobalSettings> getGlobalSettings() async {
    final resp = await _dio.get('/api/config');
    return GlobalSettings.fromJson(resp.data);
  }

  /// 更新全局配置项；套用了新默认值的运行中隧道会被重启。
  Future<void> updateGlobalSettings(GlobalSettings s) =>
      _put('/api/config', s.toJson());

  /// 连接 WebSocket 事件流，断线后自动重连，直到 [close] 被调用。
  ///
  /// 事件格式：{"type":"snapshot","snapshot":[...]}，以及 status / log 事件。
  Stream<Map<String, dynamic>> events() async* {
    while (!_closed) {
      final connector = HttpClient()..connectionTimeout = _connectTimeout;
      _connectingClient = connector;
      WebSocket? socket;
      var acceptingConnection = true;
      try {
        socket = await WebSocket.connect(
          '$wsBase/ws',
          headers: info.token.isNotEmpty ? {'X-Auth-Token': info.token} : null,
          customClient: connector,
        ).then((connected) {
          // 超时或 close 之后才完成的升级，也必须关闭得到的 socket。
          if (_closed || !acceptingConnection) {
            unawaited(_closeSocket(connected));
          }
          return connected;
        }).timeout(_connectTimeout, onTimeout: () {
          acceptingConnection = false;
          connector.close(force: true);
          throw TimeoutException('WebSocket handshake timed out');
        });
        if (_closed) return;
        _socket = socket;

        await for (final data in socket) {
          if (_closed) return;
          final event = tryDecode(data as String);
          if (event != null) yield event;
        }
      } catch (_) {
        // 连接失败或中途断开：等待后重试；token 过期由上层的探活触发重新发现
      } finally {
        acceptingConnection = false;
        if (identical(_connectingClient, connector)) _connectingClient = null;
        connector.close(force: true);
        if (identical(_socket, socket)) _socket = null;
        if (socket != null) unawaited(_closeSocket(socket));
      }

      if (_closed) return;
      await Future<void>.delayed(_reconnectDelay);
    }
  }

  String get wsBase => info.wsBase;

  void close() {
    if (_closed) return;
    _closed = true;
    _connectingClient?.close(force: true);
    _connectingClient = null;
    final socket = _socket;
    if (socket != null) unawaited(_closeSocket(socket));
    _socket = null;
    _dio.close(force: true);
  }

  static Future<void> _closeSocket(WebSocket socket) async {
    try {
      await socket.close();
    } catch (_) {
      // 对端已经断开时，清理连接仍视为完成。
    }
  }

  Future<void> _post(String path, Object? data) async {
    await _dio.post(path, data: data);
  }

  Future<void> _put(String path, Object? data) async {
    await _dio.put(path, data: data);
  }

  Future<void> _delete(String path) async {
    await _dio.delete(path);
  }
}

/// 从 daemon 错误响应中提取可读信息。
String describeError(Object error) {
  if (error is DioException) {
    final data = error.response?.data;
    if (data is Map && data['error'] != null) {
      return data['error'].toString();
    }
    if (data is String && data.contains('error')) {
      try {
        final decoded = jsonDecode(data);
        if (decoded is Map && decoded['error'] != null) {
          return decoded['error'].toString();
        }
      } catch (_) {}
    }
    return error.message ?? error.toString();
  }
  return error.toString();
}
