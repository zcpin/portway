import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:tray_manager/tray_manager.dart';

/// 系统托盘：关闭窗口时把程序收进托盘，保持后台常驻。
///
/// 隧道由 daemon 维持，客户端窗口本身不需要一直开着；
/// 收进托盘后仍可随时从托盘恢复窗口或真正退出。
class AppTray with TrayListener {
  AppTray({
    required this.onShowWindow,
    required this.onQuit,
  }) {
    // 只注册一次：init 失败后重试不应重复注册回调
    trayManager.addListener(this);
  }

  /// 请求显示主窗口。
  final VoidCallback onShowWindow;

  /// 请求退出程序。
  final VoidCallback onQuit;

  static const _keyShow = 'show_window';
  static const _keyQuit = 'exit_app';

  bool _initialized = false;

  /// 初始化托盘图标与菜单。失败不影响主窗口使用。
  Future<void> init() async {
    if (_initialized) return;

    try {
      await trayManager.setIcon(await _resolveIconPath());
      await trayManager.setToolTip('SSH 隧道管理器');
      await _setMenu(null);
      _initialized = true;
    } catch (e) {
      // 托盘在某些 Linux 桌面环境下不可用，忽略即可
      debugPrint('初始化系统托盘失败: $e');
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
    final assetName =
        Platform.isWindows ? 'assets/tray_icon.ico' : 'assets/tray_icon.png';
    final suffix = Platform.isWindows ? '.ico' : '.png';

    final data = await rootBundle.load(assetName);
    final bytes =
        data.buffer.asUint8List(data.offsetInBytes, data.lengthInBytes);
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

  /// 更新托盘菜单。runningCount 为运行中的隧道数量（null 表示未连接）。
  Future<void> updateStatus({int? runningCount}) async {
    if (!_initialized) return;
    try {
      await _setMenu(runningCount);
    } catch (_) {
      // 菜单更新失败无关紧要
    }
  }

  Future<void> _setMenu(int? runningCount) async {
    final statusLabel = switch (runningCount) {
      null => 'daemon 未连接',
      int n when n > 0 => '$n 条隧道运行中',
      _ => '没有运行中的隧道',
    };

    await trayManager.setContextMenu(
      Menu(
        items: [
          MenuItem(label: statusLabel, disabled: true),
          MenuItem.separator(),
          MenuItem(key: _keyShow, label: '显示窗口'),
          MenuItem.separator(),
          MenuItem(key: _keyQuit, label: '退出'),
        ],
      ),
    );
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
    }
  }

  Future<void> dispose() async {
    trayManager.removeListener(this);
    if (!_initialized) return;
    await trayManager.destroy();
    _initialized = false;
  }
}
