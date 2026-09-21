import 'dart:async';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models.dart';
import 'package:portway/services/desktop_notifications.dart';
import 'package:portway/services/recovery_alerts.dart';
import 'package:portway/services/tray_menu.dart';

Tunnel _state(String state, int retries, {bool desired = true}) =>
    Tunnel.fromJson({
      'name': 'database',
      'state': state,
      'retry_count': retries,
      'is_running': state != 'failed' && state != 'stopped',
      'desired_running': desired,
    });

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  test('one failure and recovery alert per continuous outage', () {
    final alerts = RecoveryAlerts();
    expect(alerts.update('first', [_state('connected', 10)]), isEmpty);
    expect(alerts.update('first', [_state('reconnecting', 11)]), isEmpty);
    expect(alerts.update('first', [_state('reconnecting', 12)]), isEmpty);
    expect(
      alerts.update('first', [_state('reconnecting', 13)]).single.recovered,
      isFalse,
    );
    expect(alerts.update('first', [_state('reconnecting', 18)]), isEmpty);
    expect(alerts.update('first', [_state('failed', 18)]), isEmpty);
    expect(
      alerts.update('first', [_state('connected', 18)]).single.recovered,
      isTrue,
    );
    expect(alerts.update('first', [_state('connected', 18)]), isEmpty);
    expect(alerts.update('first', [_state('reconnecting', 19)]), isEmpty);
  });

  test(
    'network retry resets, manual stops, and workspace changes preserve deduplication',
    () {
      final alerts = RecoveryAlerts();
      expect(alerts.update('first', [_state('reconnecting', 2)]), isEmpty);
      expect(alerts.update('first', [_state('reconnecting', 0)]), isEmpty);
      expect(alerts.update('first', [_state('reconnecting', 1)]), hasLength(1));
      expect(alerts.update('second', [_state('failed', 0)]), hasLength(1));
      expect(alerts.update('first', [_state('failed', 1)]), isEmpty);
      expect(
        alerts.update('first', [_state('stopped', 1, desired: false)]),
        isEmpty,
      );
      expect(alerts.update('first', [_state('failed', 0)]), hasLength(1));
      expect(
        alerts.update('first', [_state('failed', 0, desired: false)]),
        isEmpty,
      );
    },
  );

  test(
    'stopped retry loop retains a tray action to cancel automatic recovery',
    () {
      final tunnel = _state('failed', 0);
      final snapshot = TunnelTrayMenu.build(revision: 1, tunnels: [tunnel]);
      expect(
        snapshot.actions.values.where((action) => action.command == 'stop'),
        isNotEmpty,
      );
      final stopped = tunnel.withRuntime({
        'desired_running': false,
        'is_running': false,
        'state': 'stopped',
      });
      expect(stopped.desiredRunning, isFalse);
      expect(
        TunnelTrayMenu.build(
          revision: 2,
          tunnels: [stopped],
        ).actions.values.where((action) => action.command == 'stop'),
        isEmpty,
      );
    },
  );

  test(
    'notification waiting for setup is discarded after switching instances',
    () async {
      final setup = Completer<bool>();
      var current = true;
      var notifications = 0;
      final messenger =
          TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
      messenger.setMockMethodCallHandler(
        const MethodChannel('local_notifier'),
        (call) async {
          if (call.method == 'setup') return setup.future;
          if (call.method == 'notify') notifications++;
          return null;
        },
      );
      addTearDown(
        () => messenger.setMockMethodCallHandler(
          const MethodChannel('local_notifier'),
          null,
        ),
      );
      final desktop = DesktopNotifications();
      addTearDown(desktop.dispose);
      final shown = desktop.show(
        const RecoveryAlert('database'),
        stillRelevant: () => current,
        onClick: () {},
      );
      current = false;
      setup.complete(true);
      await shown;
      expect(notifications, 0);
    },
  );
}
