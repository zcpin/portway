import 'dart:io';

import 'package:flutter/material.dart';
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
      await trayManager.setIcon(
        Platform.isWindows ? 'assets/tray_icon.ico' : 'assets/tray_icon.png',
      );
      await trayManager.setToolTip('SSH 隧道管理器');
      await _setMenu(null);
      _initialized = true;
    } catch (e) {
      // 托盘在某些 Linux 桌面环境下不可用，忽略即可
      debugPrint('初始化系统托盘失败: $e');
    }
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
    // macOS 上右键才弹出菜单，Windows / Linux 由系统负责
    if (Platform.isMacOS) trayManager.popUpContextMenu();
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
