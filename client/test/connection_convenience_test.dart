import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:portway/connection_preferences_provider.dart';
import 'package:portway/models.dart';
import 'package:portway/models/connection_preferences.dart';
import 'package:portway/pages/tunnels_page.dart';
import 'package:portway/providers.dart';
import 'package:portway/services/daemon_client.dart';

final _rows = [
  for (final name in ['alpha', 'beta', 'gamma'])
    Tunnel.fromJson({'name': name, 'local_port': 15432}),
];

class _Tunnels extends TunnelsNotifier {
  List<String>? acted;
  @override
  Future<List<Tunnel>> build() async => _rows;
  @override
  Future<List<BatchResult>> batch(String action, List<String> names) async {
    acted = names;
    return [for (final name in names) BatchResult(name: name, ok: true)];
  }
}

class _Preferences extends ConnectionPreferencesNotifier {
  (String, String, bool)? moved;
  ConnectionOpenAction? action;
  @override
  Future<ConnectionPreferences> build() async => ConnectionPreferences(
    favorites: ['beta'],
    order: ['gamma', 'beta', 'alpha'],
  );
  @override
  Future<void> toggleFavorite(String name) async {
    final current = state.valueOrNull!;
    final names = {...current.favorites};
    if (!names.remove(name)) names.add(name);
    state = AsyncData(current.copyWith(favorites: names));
  }

  @override
  Future<void> move(String name, String neighbor, bool before) async {
    moved = (name, neighbor, before);
  }

  @override
  Future<void> setOpenAction(String name, ConnectionOpenAction? action) async {
    this.action = action;
    state = AsyncData(
      state.valueOrNull!.copyWith(openActions: {name: ?action}),
    );
  }
}

Future<(_Tunnels, _Preferences)> _show(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1500, 1150);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final tunnels = _Tunnels();
  final preferences = _Preferences();
  final client = DaemonClient(
    const DaemonInfo(
      host: '127.0.0.1',
      port: 1,
      token: '',
      pid: 1,
      version: '',
      configPath: '',
    ),
  );
  addTearDown(client.close);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        tunnelsProvider.overrideWith(() => tunnels),
        connectionPreferencesProvider.overrideWith(() => preferences),
        clientProvider.overrideWith((ref) async => client),
      ],
      child: const MaterialApp(home: TunnelsPage()),
    ),
  );
  await tester.pumpAndSettle();
  await ProviderScope.containerOf(
    tester.element(find.byType(TunnelsPage)),
  ).read(clientProvider.future);
  await tester.pump();
  return (tunnels, preferences);
}

Future<void> _shortcut(
  WidgetTester tester,
  LogicalKeyboardKey key, {
  bool alt = false,
}) async {
  final modifier = Platform.isMacOS
      ? LogicalKeyboardKey.metaLeft
      : LogicalKeyboardKey.controlLeft;
  await tester.sendKeyDownEvent(modifier);
  if (alt) await tester.sendKeyDownEvent(LogicalKeyboardKey.altLeft);
  await tester.sendKeyEvent(key);
  if (alt) await tester.sendKeyUpEvent(LogicalKeyboardKey.altLeft);
  await tester.sendKeyUpEvent(modifier);
  await tester.pumpAndSettle();
}

void main() {
  testWidgets(
    'favorites and fixed order are reflected in the list and filter',
    (tester) async {
      await _show(tester);
      List<String> names() => tester
          .widgetList<TunnelCard>(find.byType(TunnelCard))
          .map((card) => card.tunnel.name)
          .toList();
      expect(names(), ['beta', 'gamma', 'alpha']);
      await tester.tap(find.byTooltip('收藏隧道').last);
      await tester.pumpAndSettle();
      expect(names(), ['beta', 'alpha', 'gamma']);
      await tester.tap(find.text('仅看收藏'));
      await tester.pumpAndSettle();
      expect(names(), ['beta', 'alpha']);
    },
  );

  testWidgets('manual ordering targets the neighboring visible tunnel', (
    tester,
  ) async {
    final (_, preferences) = await _show(tester);
    await tester.tap(find.byType(PopupMenuButton<String>).last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('上移'));
    await tester.pumpAndSettle();
    expect(preferences.moved, ('alpha', 'gamma', true));
  });

  testWidgets(
    'window shortcuts focus search and operate the ordered favorite',
    (tester) async {
      final (tunnels, _) = await _show(tester);
      await _shortcut(tester, LogicalKeyboardKey.keyF);
      expect(
        tester
            .widget<TextField>(find.byKey(const Key('tunnel-search')))
            .focusNode!
            .hasFocus,
        isTrue,
      );
      await _shortcut(tester, LogicalKeyboardKey.digit1, alt: true);
      expect(tunnels.acted, ['beta']);
    },
  );

  testWidgets(
    'opening configuration rejects unsafe URLs and saves valid templates',
    (tester) async {
      final (_, preferences) = await _show(tester);
      await tester.tap(find.byType(PopupMenuButton<String>).first);
      await tester.pumpAndSettle();
      await tester.tap(find.text('配置打开方式'));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.widgetWithText(TextField, '网址模板'),
        'javascript:alert(1)',
      );
      await tester.tap(find.text('保存'));
      await tester.pumpAndSettle();
      expect(find.textContaining('http 或 https'), findsOneWidget);
      expect(preferences.action, isNull);
      await tester.enterText(
        find.widgetWithText(TextField, '网址模板'),
        'http://{host}:{port}/admin',
      );
      await tester.tap(find.text('保存'));
      await tester.pumpAndSettle();
      expect(preferences.action?.target, 'http://{host}:{port}/admin');
      expect(find.byTooltip('启动并打开服务'), findsOneWidget);
    },
  );
}
