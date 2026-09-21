import 'package:flutter/foundation.dart';
import 'package:local_notifier/local_notifier.dart';

import 'recovery_alerts.dart';

class DesktopNotifications {
  Future<void>? _setup;
  final _active = <LocalNotification>{};
  bool _disposed = false;

  Future<void> show(
    RecoveryAlert alert, {
    required bool Function() stillRelevant,
    required VoidCallback onClick,
  }) async {
    if (_disposed || !stillRelevant()) return;
    LocalNotification? notification;
    try {
      // Initialize only when there is an alert. Windows needs the app shortcut
      // registered by WinToast; other platforms use their notification service.
      await (_setup ??= localNotifier.setup(
        appName: 'Portway',
        shortcutPolicy: ShortcutPolicy.requireCreate,
      ));
      if (_disposed || !stillRelevant()) return;
      notification = LocalNotification(title: alert.title, body: alert.body);
      final owned = notification;
      _active.add(owned);
      owned.onClick = onClick;
      owned.onClose = (_) => _release(owned);
      await owned.show();
    } catch (error) {
      _setup = null;
      if (notification != null) await _release(notification);
      debugPrint('无法显示连接通知: $error');
    }
  }

  Future<void> _release(LocalNotification notification) async {
    if (!_active.remove(notification)) return;
    localNotifier.removeListener(notification);
    try {
      await notification.destroy();
    } catch (error) {
      debugPrint('无法关闭连接通知: $error');
    }
  }

  Future<void> dispose() async {
    _disposed = true;
    for (final notification in _active.toList()) {
      await _release(notification);
    }
  }
}
