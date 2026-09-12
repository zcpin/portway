import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/pages/config_transfer_panel.dart';
import 'package:ssh_tunnel_client/providers.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';

class _TransferClient extends DaemonClient {
  _TransferClient()
    : super(
        const DaemonInfo(
          host: '127.0.0.1',
          port: 1,
          token: '',
          pid: 0,
          version: 'test',
          configPath: '',
        ),
      );
  int previews = 0;
  String? previewMode;
  (String, String, String)? imported;
  static const backup = ConfigBackup(
    name: '20260912T000000.000000000-012345abcdef.toml',
    createdAt: '2026-09-12T00:00:00Z',
    size: 23,
  );

  @override
  Future<ImportPreview> previewImport(String content, String mode) async {
    previewMode = mode;
    return ImportPreview(
      revision: 'revision-${++previews}',
      changes: const [
        ConfigChange(kind: 'global', name: 'defaults', action: 'replace'),
      ],
    );
  }

  @override
  Future<ConfigBackup> importConfig(
    String content,
    String mode,
    String revision,
  ) async {
    imported = (content, mode, revision);
    return backup;
  }

  @override
  Future<List<ConfigBackup>> listConfigBackups() async => [backup];
  @override
  Future<String> readConfigBackup(String name) async => "log_level = 'info'";
}

Future<_TransferClient> _showPanel(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1200, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final client = _TransferClient();
  addTearDown(client.close);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [clientProvider.overrideWith((ref) async => client)],
      child: const MaterialApp(
        home: Scaffold(
          body: SingleChildScrollView(
            child: SizedBox(width: 720, child: ConfigTransferPanel()),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return client;
}

void main() {
  testWidgets('导入要求最新预览和确认，发送对应修订号', (tester) async {
    final client = await _showPanel(tester);
    final input = find.byKey(const Key('config-import-content'));
    await tester.enterText(input, "log_level = 'debug'");
    await tester.tap(find.text('预览变更'));
    await tester.pumpAndSettle();
    expect(find.text('覆盖 全局设置'), findsOneWidget);
    await tester.enterText(input, "log_level = 'warn'");
    await tester.pumpAndSettle();
    expect(
      tester
          .widget<FilledButton>(find.widgetWithText(FilledButton, '应用导入'))
          .onPressed,
      isNull,
    );
    await tester.tap(find.text('预览变更'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('应用导入'));
    await tester.pumpAndSettle();
    expect(client.imported, isNull);
    await tester.tap(find.text('确认导入'));
    await tester.pumpAndSettle();
    expect(client.imported, ("log_level = 'warn'", 'merge', 'revision-2'));
  });

  testWidgets('恢复备份先载入替换预览，取消确认不会写入', (tester) async {
    final client = await _showPanel(tester);
    await tester.tap(find.text('查看备份'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('预览恢复'));
    await tester.pumpAndSettle();
    expect(client.previewMode, 'replace');
    expect(client.imported, isNull);
    await tester.tap(find.text('应用导入'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(client.imported, isNull);
  });
}
