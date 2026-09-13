import 'dart:async';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';
import 'package:ssh_tunnel_client/services/tray.dart';
import 'package:ssh_tunnel_client/services/tray_menu.dart';

Iterable<MenuItem> _items(Menu menu) sync* {
  for (final item in menu.items ?? <MenuItem>[]) {
    yield item;
    if (item.submenu != null) yield* _items(item.submenu!);
  }
}

Iterable<Map> _nativeItems(Map menu) sync* {
  for (final item in menu['items'] as List) {
    yield item as Map;
    if (item['submenu'] != null) yield* _nativeItems(item['submenu'] as Map);
  }
}

class _TrayClient extends DaemonClient {
  _TrayClient(int pid)
    : super(
        DaemonInfo(
          host: '127.0.0.1',
          port: 1,
          token: 'test',
          pid: pid,
          version: 'test',
          configPath: '',
        ),
      );

  final calls = <(String, List<String>)>[];
  List<BatchResult>? results;
  @override
  Future<List<BatchResult>> batchTunnels(
    String action,
    List<String> names,
  ) async {
    calls.add((action, names));
    return results ??
        [for (final name in names) BatchResult(name: name, ok: true)];
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final rows = [
    Tunnel.fromJson({'name': 'database', 'group': '开发', 'local_port': 15432}),
    Tunnel.fromJson({
      'name': 'cache',
      'group': '开发',
      'local_port': 16379,
      'is_running': true,
      'state': 'connected',
    }),
    Tunnel.fromJson({
      'name': 'reverse',
      'mode': 'remote',
      'remote_host': '::1',
      'remote_port': 18080,
    }),
  ];

  test(
    'tray menu groups tunnels, preserves command targets and formats IPv6',
    () {
      final menu = TunnelTrayMenu.build(
        revision: 1,
        tunnels: rows,
        instanceLabel: '工作区',
      );
      final items = _items(menu.menu).toList();
      expect(items.where((item) => item.label == '开发'), hasLength(1));
      expect(items.where((item) => item.label == '未分组'), hasLength(1));
      expect(items.any((item) => item.label == 'cache · 已连接'), isTrue);
      final group = items.firstWhere((item) => item.label == '开发').submenu!;
      final start = group.items!.firstWhere((item) => item.label == '启动本组全部');
      expect(menu.actions[start.key]?.names, ['database', 'cache']);
      final stop = items.firstWhere((item) => item.label == '停止');
      expect(menu.actions[stop.key]?.command, 'stop');
      expect(menu.actions[stop.key]?.names, ['cache']);
      final copy = items.firstWhere((item) => item.label == '复制远端监听地址');
      expect(menu.actions[copy.key]?.address, '[::1]:18080');
      final commandKeys = items
          .where((item) => item.key?.startsWith('tunnel_') == true)
          .map((item) => item.key)
          .toList();
      expect(commandKeys.toSet().length, commandKeys.length);
      final disconnected = TunnelTrayMenu.build(revision: 2);
      expect(disconnected.actions, isEmpty);
      expect(
        _items(disconnected.menu).any((item) => item.label == 'daemon 未连接'),
        isTrue,
      );
      expect(
        TunnelTrayMenu.build(revision: 3, tunnels: rows, busy: true).actions,
        isEmpty,
      );
    },
  );

  Map? nativeMenu;
  String? clipboard;
  setUp(() {
    nativeMenu = null;
    clipboard = null;
    final messenger =
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(const MethodChannel('tray_manager'), (
      call,
    ) async {
      if (call.method == 'setContextMenu') {
        nativeMenu = (call.arguments as Map)['menu'] as Map;
      }
      return null;
    });
    messenger.setMockMethodCallHandler(SystemChannels.platform, (call) async {
      if (call.method == 'Clipboard.setData') {
        clipboard = (call.arguments as Map)['text'] as String;
      }
      return null;
    });
  });
  tearDown(() {
    final messenger =
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(
      const MethodChannel('tray_manager'),
      null,
    );
    messenger.setMockMethodCallHandler(SystemChannels.platform, null);
  });

  test(
    'tray uses the snapshot received before initialization and performs actions',
    () async {
      final client = _TrayClient(1);
      addTearDown(client.close);
      final refreshed = Completer<void>();
      final errors = <Object>[];
      final tray = AppTray(
        onShowWindow: () {},
        onQuit: () {},
        isCurrentClient: (value) => identical(value, client),
        onTunnelsChanged: (_) async {
          refreshed.complete();
        },
        onError: (error) async {
          errors.add(error);
        },
      );
      addTearDown(tray.dispose);
      await tray.updateStatus(client: client, tunnels: rows.take(2).toList());
      await tray.init();
      final start = _nativeItems(
        nativeMenu!,
      ).firstWhere((item) => item['label'] == '启动');
      tray.onTrayMenuItemClick(MenuItem(key: start['key'] as String));
      await refreshed.future;
      await Future<void>.delayed(Duration.zero);
      expect(client.calls, hasLength(1));
      expect(client.calls.single.$1, 'start');
      expect(client.calls.single.$2, ['database']);
      expect(errors, isEmpty);
      final copy = _nativeItems(
        nativeMenu!,
      ).firstWhere((item) => item['label'] == '复制本地连接地址');
      tray.onTrayMenuItemClick(MenuItem(key: copy['key'] as String));
      await Future<void>.delayed(Duration.zero);
      expect(clipboard, '127.0.0.1:15432');
    },
  );

  test(
    'old menu clicks cannot operate on the same name in a different workspace',
    () async {
      final oldClient = _TrayClient(1);
      final nextClient = _TrayClient(2);
      addTearDown(oldClient.close);
      addTearDown(nextClient.close);
      var current = oldClient;
      final tray = AppTray(
        onShowWindow: () {},
        onQuit: () {},
        isCurrentClient: (value) => identical(value, current),
        onTunnelsChanged: (_) async {},
        onError: (_) async {},
      );
      addTearDown(tray.dispose);
      await tray.init();
      await tray.updateStatus(client: oldClient, tunnels: [rows.first]);
      final oldKey =
          _nativeItems(
                nativeMenu!,
              ).firstWhere((item) => item['label'] == '启动')['key']
              as String;
      current = nextClient;
      // The provider may have changed before a new native menu is installed.
      tray.onTrayMenuItemClick(MenuItem(key: oldKey));
      await tray.updateStatus(client: nextClient, tunnels: [rows.first]);
      tray.onTrayMenuItemClick(MenuItem(key: oldKey));
      await Future<void>.delayed(Duration.zero);
      expect(oldClient.calls, isEmpty);
      expect(nextClient.calls, isEmpty);
    },
  );

  test(
    'partial group failure is surfaced and the displayed state is refreshed',
    () async {
      final client = _TrayClient(1)
        ..results = const [
          BatchResult(name: 'database', ok: true),
          BatchResult(name: 'cache', ok: false, error: 'instance unavailable'),
        ];
      addTearDown(client.close);
      var refreshed = false;
      final error = Completer<Object>();
      final tray = AppTray(
        onShowWindow: () {},
        onQuit: () {},
        isCurrentClient: (value) => identical(value, client),
        onTunnelsChanged: (_) async {
          refreshed = true;
        },
        onError: (value) async {
          error.complete(value);
        },
      );
      addTearDown(tray.dispose);
      await tray.init();
      await tray.updateStatus(client: client, tunnels: rows.take(2).toList());
      final group = _nativeItems(
        nativeMenu!,
      ).firstWhere((item) => item['label'] == '启动本组全部');
      tray.onTrayMenuItemClick(MenuItem(key: group['key'] as String));
      expect(
        (await error.future).toString(),
        contains('cache：instance unavailable'),
      );
      expect(refreshed, isTrue);
      expect(client.calls, hasLength(1));
      expect(client.calls.single.$1, 'start');
      expect(client.calls.single.$2, ['database', 'cache']);
    },
  );
}
