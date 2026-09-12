import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/pages/tunnels_page.dart';
import 'package:ssh_tunnel_client/providers.dart';

class _Rows extends TunnelsNotifier {
  final rows = [
    Tunnel.fromJson({
      'name': 'db-prod',
      'group': 'prod',
      'auto_start': false,
      'local_port': 15432,
      'remote_host': '127.0.0.1',
      'remote_port': 5432,
      'ssh_host': 'example:22',
      'ssh_user': 'alice',
      'key_file': 'key',
      'host_key_check': 'known_hosts',
      'known_hosts_file': 'custom_hosts',
    }),
    Tunnel.fromJson({'name': 'db-dev', 'group': 'dev', 'local_port': 15433}),
  ];
  List<String>? selectedNames;
  String? action;
  Tunnel? saved;
  String? editing;

  @override
  Future<List<Tunnel>> build() async => rows;

  @override
  Future<List<BatchResult>> batch(String action, List<String> names) async {
    this.action = action;
    selectedNames = names;
    return [for (final name in names) BatchResult(name: name, ok: true)];
  }

  @override
  Future<void> save(Tunnel tunnel, {String? editingName}) async {
    saved = tunnel;
    editing = editingName;
  }
}

class _NoConnections extends SshConnectionsNotifier {
  @override
  Future<List<SshConnection>> build() async => [];
}

Future<_Rows> _showPage(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1400, 1100);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final rows = _Rows();
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        tunnelsProvider.overrideWith(() => rows),
        sshConnectionsProvider.overrideWith(_NoConnections.new),
      ],
      child: const MaterialApp(home: TunnelsPage()),
    ),
  );
  await tester.pumpAndSettle();
  return rows;
}

void main() {
  testWidgets('搜索和分组筛选后只批量操作选中的可见隧道', (tester) async {
    final rows = await _showPage(tester);
    await tester.enterText(find.byKey(const Key('tunnel-search')), 'db-prod');
    await tester.pumpAndSettle();
    expect(find.text('db-dev'), findsNothing);
    await tester.tap(find.byType(Checkbox));
    await tester.enterText(find.byKey(const Key('tunnel-search')), '');
    await tester.pumpAndSettle();
    await tester.tap(find.byType(DropdownButton<String?>));
    await tester.pumpAndSettle();
    await tester.tap(find.text('dev').last);
    await tester.pumpAndSettle();
    expect(find.text('db-prod'), findsNothing);
    await tester.tap(find.byType(Checkbox));
    await tester.pumpAndSettle();
    await tester.tap(find.text('批量启动'));
    await tester.pumpAndSettle();
    expect(rows.action, 'start');
    expect(rows.selectedNames, ['db-dev']);
    expect(find.text('批量操作结果'), findsOneWidget);
    expect(find.text('已完成'), findsOneWidget);
  });

  testWidgets('复制配置使用新名称和端口并保留分组与认证选项', (tester) async {
    final rows = await _showPage(tester);
    await tester.tap(find.byType(PopupMenuButton<String>).first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('复制配置'));
    await tester.pumpAndSettle();
    expect(find.text('新建隧道'), findsWidgets);
    expect(
      tester
          .widget<TextFormField>(find.widgetWithText(TextFormField, '分组（可选）'))
          .controller!
          .text,
      'prod',
    );
    expect(
      tester.widget<SwitchListTile>(find.byType(SwitchListTile)).value,
      isFalse,
    );
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(rows.editing, isNull);
    expect(rows.saved?.name, 'db-prod-copy');
    expect(rows.saved?.localPort, 15434);
    expect(rows.saved?.knownHostsFile, 'custom_hosts');
    expect(rows.saved?.autoStart, isFalse);
  });
}
