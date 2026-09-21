import 'dart:convert';

import 'package:dio/dio.dart';

import '../models.dart';
import '../models/frp.dart';
import '../models/tunnel_diagnostic.dart';

/// 客户端与隧道引擎之间的统一契约。
///
/// 两种实现可以互换：
///
///   - [DaemonClient]     —— 引擎是独立进程，通过回环 HTTP + WebSocket 通信
///   - [EmbeddedEngine]   —— 引擎编译成动态库加载进客户端进程，通过 dart:ffi 直接调用
///
/// provider 与页面只依赖这个接口，因此切换实现不需要改动业务代码。
/// 接口刻意保持与 [DaemonClient] 原有方法完全一致，避免出现两套调用习惯。
abstract class TunnelEngine {
  /// 引擎的运行信息。进程内引擎没有端口与令牌，但同样提供配置路径与版本，
  /// 界面据此显示来源与状态。
  DaemonInfo get info;

  /// 引擎是否已关闭（关闭后再调用方法属于编程错误）。
  bool get isClosed;

  /// 探活并确认引擎仍可服务。daemon 重启后 token 会失效，此方法返回 false。
  Future<bool> checkAuth();

  /// 引擎推送的事件流：`snapshot` / `status` / `runtime` / `log`。
  ///
  /// 与 daemon 的 WebSocket 事件流格式一致，订阅后无需再拉一次列表：
  /// 两种实现都会在订阅建立时先补发一份完整快照。
  Stream<Map<String, dynamic>> events();

  /// 释放连接或引擎资源。
  void close();

  // ---------- 隧道 ----------

  Future<List<Tunnel>> getTunnels();
  Future<void> addTunnel(Tunnel tunnel);
  Future<void> updateTunnel(String name, Tunnel tunnel);
  Future<void> deleteTunnel(String name);
  Future<void> startTunnel(String name);
  Future<void> stopTunnel(String name);
  Future<void> restartTunnel(String name);
  Future<List<BatchResult>> batchTunnels(String action, List<String> names);

  /// 检查一条隧道的监听、SSH 认证与目标可达性，不启停隧道。
  ///
  /// [cancelToken] 只在 daemon 模式下生效：进程内调用无法中途打断，
  /// 关闭诊断窗口后检查会在引擎里继续跑到超时为止。
  Future<TunnelDiagnostic> diagnoseTunnel(String name, {CancelToken? cancelToken});

  // ---------- SSH 连接 ----------

  Future<List<SshConnection>> getSshConnections();
  Future<void> addSshConnection(SshConnection connection);
  Future<void> updateSshConnection(String name, SshConnection connection);
  Future<void> deleteSshConnection(String name);

  /// 测试待保存的 SSH 配置，返回耗时与结果，不建立转发。
  Future<ConnectionDiagnostic> testSshConnection(SshConnection connection, {CancelToken? cancelToken});

  /// 探测目标主机公钥指纹，经过跳板时先按配置认证跳板。
  Future<HostKeyInfo> inspectHostKey(SshConnection connection);

  /// 写入信任的主机密钥；变更已有密钥需要 [replace] 为 true。
  Future<HostKeyInfo> trustHostKey(SshConnection connection, String fingerprint, bool replace);

  // ---------- FRP ----------

  /// 全部 FRP 客户端，每项含其代理与运行状态。
  Future<List<FrpClient>> getFrpClients();

  Future<void> addFrpClient(FrpClientPayload client);
  Future<void> updateFrpClient(String name, FrpClientPayload client);
  Future<void> deleteFrpClient(String name);
  Future<void> startFrpClient(String name);
  Future<void> stopFrpClient(String name);
  Future<void> restartFrpClient(String name);

  /// 在客户端下新增一条代理。
  Future<void> addFrpProxy(String client, FrpProxyPayload proxy);

  /// 更新一条代理；[proxy] 是原名称，改名也在这一次调用里完成。
  Future<void> updateFrpProxy(String client, String proxy, FrpProxyPayload payload);

  Future<void> deleteFrpProxy(String client, String proxy);

  /// 启用或停用一条代理。服务端热更新生效，不会断开与 frps 的连接。
  Future<void> toggleFrpProxy(String client, String proxy, bool enabled);

  // ---------- 私钥 ----------

  Future<List<KeyInfo>> getKeys();
  /// 校验一个私钥路径是否可读。
  Future<KeyInfo> statKey(String path);

  /// 用口令解锁私钥；口令不落盘，进程退出后需要重新解锁。
  Future<void> unlockKey(String path, String passphrase);

  /// 锁定已解锁的私钥。已建立的连接继续运行，原文件不会被改写。
  Future<void> lockKey(String path);

  // ---------- 日志与配置 ----------

  Future<List<LogEntry>> getLogs();

  Future<GlobalSettings> getGlobalSettings();
  Future<void> updateGlobalSettings(GlobalSettings settings);

  Future<ConfigExport> exportConfig();
  Future<ImportPreview> previewImport(String content, String mode);
  Future<ConfigBackup> importConfig(String content, String mode, String revision);
  Future<List<ConfigBackup>> listConfigBackups();
  Future<String> readConfigBackup(String name);
}

/// 把引擎抛出的错误转成可读提示。
///
/// 两种实现的错误形态不同：
///
///   - daemon 模式抛 [DioException]，错误信息在响应的 `error` 字段里；
///   - 进程内模式抛 [EngineException]，[toString] 就是消息本身。
///
/// 因此这里放在接口层：界面统一调用它，不必知道背后是哪种引擎。
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
