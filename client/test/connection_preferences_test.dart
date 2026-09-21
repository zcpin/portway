import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models.dart';
import 'package:portway/models/connection_preferences.dart';
import 'package:portway/services/connection_preferences.dart';
import 'package:portway/services/tray_menu.dart';

void main() {
  test(
    'preferences persist and concurrent writers preserve separate workspaces',
    () async {
      final root = await Directory.systemTemp.createTemp(
        'ssh-connection-preferences-',
      );
      final first = ConnectionPreferencesStore(directory: root.path);
      final second = ConnectionPreferencesStore(directory: root.path);
      await Future.wait([
        first.toggleFavorite('development', 'database'),
        second.toggleFavorite('development', 'cache'),
        first.toggleFavorite('production', 'database'),
      ]);
      final reloaded = ConnectionPreferencesStore(directory: root.path);
      expect((await reloaded.load('development')).favorites, {
        'database',
        'cache',
      });
      expect((await reloaded.load('production')).favorites, {'database'});
      expect((await reloaded.load('other')).favorites, isEmpty);
    },
  );

  test(
    'favorites stay first, ordering persists, and rename carries the opening action',
    () async {
      final root = await Directory.systemTemp.createTemp(
        'ssh-connection-order-',
      );
      final store = ConnectionPreferencesStore(directory: root.path);
      final tunnels = [
        for (final name in ['alpha', 'beta', 'gamma'])
          Tunnel.fromJson({'name': name}),
      ];
      await store.toggleFavorite('workspace', 'beta');
      await store.setOpenAction(
        'workspace',
        'beta',
        ConnectionOpenAction(kind: 'url', target: 'http://{host}:{port}/admin'),
      );
      await store.move('workspace', 'gamma', 'alpha', true, tunnels);
      final current = await store.load('workspace');
      expect(current.sorted(tunnels).map((t) => t.name), [
        'beta',
        'gamma',
        'alpha',
      ]);
      await store.rename('workspace', 'beta', 'database');
      final renamed = await store.load('workspace');
      expect(renamed.favorites, {'database'});
      expect(renamed.order, ['database', 'gamma', 'alpha']);
      expect(
        renamed.openActions['database']?.target,
        'http://{host}:{port}/admin',
      );
      expect(renamed.openActions.containsKey('beta'), isFalse);
      await expectLater(
        store.move('workspace', 'alpha', 'database', true, [
          Tunnel.fromJson({'name': 'alpha'}),
          Tunnel.fromJson({'name': 'database'}),
        ]),
        throwsStateError,
      );
    },
  );

  test('renaming never inherits an unrelated orphan opening action', () async {
    final root = await Directory.systemTemp.createTemp(
      'ssh-connection-rename-',
    );
    final store = ConnectionPreferencesStore(directory: root.path);
    await store.toggleFavorite('workspace', 'old-deleted-name');
    await store.setOpenAction(
      'workspace',
      'old-deleted-name',
      ConnectionOpenAction(kind: 'url', target: 'https://example.test'),
    );
    final renamed = await store.rename(
      'workspace',
      'new-tunnel',
      'old-deleted-name',
    );
    expect(renamed.favorites, isEmpty);
    expect(renamed.openActions, isEmpty);
  });

  test(
    'corrupt preferences are reported without overwriting the original file',
    () async {
      final root = await Directory.systemTemp.createTemp(
        'ssh-connection-invalid-',
      );
      final store = ConnectionPreferencesStore(directory: root.path);
      final file = File(store.filePath);
      final original = jsonEncode({
        'version': 1,
        'instances': {
          'broken': {
            'favorites': [],
            'order': [],
            'open_actions': {'database': 'invalid'},
          },
        },
      });
      await file.writeAsString(original, encoding: utf8);
      await expectLater(store.load('workspace'), throwsFormatException);
      await expectLater(
        store.toggleFavorite('workspace', 'database'),
        throwsFormatException,
      );
      expect(await file.readAsString(encoding: utf8), original);
    },
  );

  test('the preference scope survives daemon token and port changes', () async {
    final root = await Directory.systemTemp.createTemp('ssh-connection-scope-');
    DaemonInfo info(int port, String token, String path) => DaemonInfo(
      host: '127.0.0.1',
      port: port,
      token: token,
      pid: port,
      version: 'test',
      configPath: path,
    );
    final first = connectionPreferenceScope(
      info(1234, 'old-token', '${root.path}/config.toml'),
    );
    final next = connectionPreferenceScope(
      info(4567, 'new-token', '${root.path}/unused/../config.toml'),
    );
    expect(first, next);
    expect(first, isNot(contains('old-token')));
    expect(
      connectionPreferenceScope(info(1234, '', '${root.path}/other.toml')),
      isNot(first),
    );
  });

  test(
    'tray favorites use saved order and carry the configured open action',
    () {
      final action = ConnectionOpenAction(
        kind: 'url',
        target: 'http://{host}:{port}',
      );
      final preferences = ConnectionPreferences(
        favorites: ['alpha', 'beta'],
        order: ['beta', 'alpha'],
        openActions: {'beta': action},
      );
      final snapshot = TunnelTrayMenu.build(
        revision: 4,
        preferences: preferences,
        tunnels: [
          Tunnel.fromJson({'name': 'alpha'}),
          Tunnel.fromJson({'name': 'beta'}),
        ],
      );
      final favorites = snapshot.menu.items!.firstWhere(
        (item) => item.label == '收藏（2）',
      );
      expect(favorites.submenu!.items!.map((item) => item.label), [
        'beta · 已停止',
        'alpha · 已停止',
      ]);
      final open = snapshot.actions.values.where(
        (action) => action.command == 'open',
      );
      expect(open, hasLength(2));
      expect(
        open.every(
          (entry) =>
              entry.names.single == 'beta' &&
              identical(entry.openAction, action),
        ),
        isTrue,
      );
    },
  );
}
