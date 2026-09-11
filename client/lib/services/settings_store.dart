import 'dart:convert';
import 'dart:io';

import 'daemon_discovery.dart';

/// 关闭窗口时的默认动作。
enum CloseAction {
  /// 每次关闭都弹出确认对话框（默认）。
  ask('ask'),

  /// 直接收进系统托盘。
  tray('tray'),

  /// 直接退出程序。
  quit('quit');

  const CloseAction(this.storageValue);

  /// 写入设置文件的字符串值。
  final String storageValue;

  static CloseAction fromStorage(String? value) => switch (value) {
        'tray' => CloseAction.tray,
        'quit' => CloseAction.quit,
        _ => CloseAction.ask,
      };
}

/// 客户端本地偏好设置（与 daemon 配置分开存放）。
///
/// 这些设置只影响客户端自身行为（如关闭按钮的默认动作），不依赖 daemon，
/// 因此存到用户目录下独立的 JSON 文件，daemon 不在线也能读写。
class SettingsStore {
  /// 默认实例：写入 `<用户主目录>/.ssh-tunnel/client_settings.json`。
  static final SettingsStore instance = SettingsStore();

  SettingsStore() : _dataDir = null;

  /// 测试用：指定独立的数据目录，避免写入真实用户目录。
  SettingsStore.forDir(String this._dataDir);

  static const _fileName = 'client_settings.json';

  final String? _dataDir;
  bool _loaded = false;
  CloseAction _closeAction = CloseAction.ask;

  /// 关闭窗口时的默认动作。
  CloseAction get closeAction => _closeAction;

  /// 从磁盘加载设置（幂等，可重复调用）。
  Future<void> load() async {
    if (_loaded) return;
    _loaded = true;

    final path = _filePath();
    if (path == null || !File(path).existsSync()) return;

    try {
      final decoded = jsonDecode(await File(path).readAsString());
      if (decoded is Map<String, dynamic>) {
        _closeAction =
            CloseAction.fromStorage(decoded['close_action'] as String?);
      }
    } catch (_) {
      // 文件被写坏时退回默认值，不影响使用
    }
  }

  /// 更新关闭动作并立即持久化。
  Future<void> setCloseAction(CloseAction action) async {
    _closeAction = action;
    await _save();
  }

  Future<void> _save() async {
    final path = _filePath();
    if (path == null) return;

    try {
      final file = File(path);
      await file.parent.create(recursive: true);
      await file.writeAsString(jsonEncode({
        'close_action': _closeAction.storageValue,
      }));
    } catch (_) {
      // 写失败只影响下一次启动的默认值，忽略即可
    }
  }

  /// 设置文件路径；数据目录不可知（无主目录且未注入）时返回 null。
  String? _filePath() {
    final base = _dataDir?.isNotEmpty == true
        ? _dataDir
        : _defaultDir();
    if (base == null) return null;
    return '$base${Platform.pathSeparator}$_fileName';
  }

  /// 默认目录：`<用户主目录>/.ssh-tunnel`。
  static String? _defaultDir() {
    final home = DaemonDiscovery.homeDir;
    if (home == null || home.isEmpty) return null;
    return '$home${Platform.pathSeparator}.ssh-tunnel';
  }
}
