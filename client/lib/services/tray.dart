import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:tray_manager/tray_manager.dart';

import '../models.dart';
import '../models/connection_preferences.dart';
import 'tray_menu.dart';
import 'tunnel_engine.dart';

/// 系统托盘：关闭窗口时把程序收进托盘，保持后台常驻。
///
/// 收进托盘不会影响隧道，也不会关闭引擎；收进后仍可随时恢复窗口或真正退出。
///
/// 真正「退出程序」的后果取决于引擎形态：daemon 模式下隧道继续运行，
/// 进程内引擎模式下引擎与界面同进程，退出即隧道停止。
class AppTray with TrayListener {
  AppTray({
    required this.onShowWindow,
    required this.onQuit,
    required this.isCurrentClient,
    required this.onTunnelsChanged,
    required this.onError,
    this.onOpenConnection,
  }) {
    // 只注册一次：init 失败后重试不应重复注册回调
    trayManager.addListener(this);
  }

  /// 请求显示主窗口。
  final VoidCallback onShowWindow;

  /// 请求退出程序。
  final VoidCallback onQuit;

  final bool Function(TunnelEngine client) isCurrentClient;
  final Future<void> Function(TunnelEngine client) onTunnelsChanged;
  final Future<void> Function(Object error) onError;
  final Future<void> Function(
    TunnelEngine client,
    String name,
    ConnectionOpenAction action,
  )?
  onOpenConnection;

  static const _keyShow = 'show_window';
  static const _keyQuit = 'exit_app';

  bool _initialized = false;
  bool _disposed = false;
  Future<void>? _initializing;
  Future<void> _menuQueue = Future.value();
  int _revision = 0;
  TunnelEngine? _client;
  TunnelEngine? _busyClient;
  List<Tunnel>? _tunnels;
  String? _instanceLabel;
  ConnectionPreferences? _preferences;
  Map<String, TrayTunnelAction> _actions = const {};

  /// 初始化托盘图标与菜单。失败不影响主窗口使用。
  Future<void> init() {
    if (_initialized || _disposed) return Future.value();
    return _initializing ??= _initialize();
  }

  Future<void> _initialize() async {
    try {
      await trayManager.setIcon(await _resolveIconPath());
      _initialized = true;
      if (_disposed) return;
      await trayManager.setToolTip('端口通');
      await _refreshMenu();
    } catch (e) {
      // 托盘在某些 Linux 桌面环境下不可用，忽略即可
      debugPrint('初始化系统托盘失败: $e');
    } finally {
      _initializing = null;
    }
  }

  /// 把打包进 Flutter 资产的托盘图标解压到本机临时目录，返回绝对路径。
  ///
  /// tray_manager 在 Windows 上用 LoadImage 按路径读取图标，相对路径会相对
  /// 进程工作目录解析——开发时恰好落在项目目录能读到，发布后工作目录不固定、
  /// assets/ 也不会被复制到可执行文件旁边，图标会加载失败，托盘只剩一个
  /// 空白占位。这里统一先解压成绝对路径再交给插件。
  ///
  /// 临时文件名带内容指纹：图标换版后自动写入新文件，不会因为 %TEMP% 里
  /// 残留旧图而一直显示旧图标。
  Future<String> _resolveIconPath() async {
    final assetName = Platform.isWindows
        ? 'assets/tray_icon.ico'
        : 'assets/tray_icon.png';
    final suffix = Platform.isWindows ? '.ico' : '.png';

    final data = await rootBundle.load(assetName);
    final bytes = data.buffer.asUint8List(
      data.offsetInBytes,
      data.lengthInBytes,
    );
    final file = File(
      '${Directory.systemTemp.path}${Platform.pathSeparator}'
      'ssh_tunnel_tray_icon_${_fnv1a(bytes).toRadixString(16)}$suffix',
    );
    if (!await file.exists()) {
      await file.writeAsBytes(bytes);
    }
    return file.path;
  }

  /// FNV-1a 32 位散列，仅用于区分图标文件内容。
  static int _fnv1a(List<int> bytes) {
    var hash = 0x811c9dc5;
    for (final b in bytes) {
      hash ^= b;
      hash = (hash * 0x01000193) & 0xFFFFFFFF;
    }
    return hash;
  }

  /// Keep the current snapshot even before the native icon finishes loading.
  Future<void> updateStatus({
    TunnelEngine? client,
    List<Tunnel>? tunnels,
    String? instanceLabel,
    ConnectionPreferences? preferences,
  }) {
    _client = client;
    _tunnels = client == null || tunnels == null
        ? null
        : List.unmodifiable(tunnels);
    _instanceLabel = client == null ? null : instanceLabel;
    _preferences = client == null ? null : preferences;
    return _refreshMenu();
  }

  Future<void> _refreshMenu() {
    _revision++;
    _actions = const {};
    if (!_initialized || _disposed) return Future.value();
    _menuQueue = _menuQueue.then((_) async {
      if (_disposed) return;
      final revision = _revision;
      final snapshot = TunnelTrayMenu.build(
        revision: revision,
        tunnels: _tunnels,
        instanceLabel: _instanceLabel,
        preferences: _preferences,
        busy: _busyClient != null && identical(_busyClient, _client),
      );
      try {
        await trayManager.setContextMenu(snapshot.menu);
        if (!_disposed && revision == _revision) _actions = snapshot.actions;
      } catch (error) {
        debugPrint('更新托盘菜单失败: $error');
      }
    });
    return _menuQueue;
  }

  bool _current(TunnelEngine client) =>
      !_disposed && identical(_client, client) && isCurrentClient(client);

  Future<void> _run(TrayTunnelAction action, TunnelEngine client) async {
    if (!_current(client) || identical(_busyClient, client)) return;
    _busyClient = client;
    _refreshMenu();
    try {
      if (action.command == 'copy') {
        await Clipboard.setData(ClipboardData(text: action.address!));
      } else if (action.command == 'open') {
        final open = onOpenConnection;
        if (open == null || action.openAction == null) {
          throw StateError('打开方式不可用');
        }
        await open(client, action.names.single, action.openAction!);
        if (_current(client)) await onTunnelsChanged(client);
      } else {
        final results = await client.batchTunnels(action.command, action.names);
        if (!_current(client)) return;
        await onTunnelsChanged(client);
        final failures = results.where((result) => !result.ok).toList();
        if (failures.isNotEmpty) {
          throw StateError(
            failures
                .map((result) => '${result.name}：${result.error}')
                .join('\n'),
          );
        }
      }
    } catch (error) {
      if (_current(client)) await onError(error);
    } finally {
      if (identical(_busyClient, client)) _busyClient = null;
      await _refreshMenu();
    }
  }

  @override
  void onTrayIconMouseDown() {
    // Windows 与 Linux 上左键点击直接恢复窗口
    if (!Platform.isMacOS) onShowWindow();
  }

  @override
  void onTrayIconRightMouseDown() {
    // Windows 的 tray_manager 插件在右键时只派发本回调、不会自动弹出菜单
    //（Windows 实现里 WM_RBUTTONUP 仅 InvokeMethod），必须手动调用
    // popUpContextMenu；macOS 同理。Linux 由桌面环境负责。
    if (Platform.isLinux) return;
    trayManager.popUpContextMenu();
  }

  @override
  void onTrayMenuItemClick(MenuItem menuItem) {
    switch (menuItem.key) {
      case _keyShow:
        onShowWindow();
      case _keyQuit:
        onQuit();
      default:
        final action = _actions[menuItem.key];
        final client = _client;
        if (action != null && client != null) _run(action, client);
    }
  }

  Future<void> dispose() async {
    _disposed = true;
    _actions = const {};
    trayManager.removeListener(this);
    await _initializing;
    await _menuQueue;
    if (!_initialized) return;
    await trayManager.destroy();
    _initialized = false;
  }
}
