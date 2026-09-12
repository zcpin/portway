import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/pages/connections_page.dart';
import 'package:ssh_tunnel_client/pages/tunnels_page.dart';
import 'package:ssh_tunnel_client/providers.dart';

class _ReadableKeys extends KeysNotifier {
  @override
  Future<List<KeyInfo>> build() async => [];

  @override
  Future<KeyInfo> stat(String path) async =>
      KeyInfo.fromJson({'path': path, 'resolved': path, 'exists': true});
}

class _EmptyConnections extends SshConnectionsNotifier {
  @override
  Future<List<SshConnection>> build() async => [];
}

Future<void> _openEditor<T>(
  WidgetTester tester,
  Widget editor,
  ValueChanged<T?> onSaved,
) async {
  tester.view.physicalSize = const Size(1200, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        keysProvider.overrideWith(_ReadableKeys.new),
        sshConnectionsProvider.overrideWith(_EmptyConnections.new),
      ],
      child: MaterialApp(
        home: Builder(
          builder: (context) {
            return Scaffold(
              body: TextButton(
                onPressed: () async {
                  onSaved(
                    await showDialog<T>(
                      context: context,
                      builder: (_) => editor,
                    ),
                  );
                },
                child: const Text('打开编辑器'),
              ),
            );
          },
        ),
      ),
    ),
  );
  await tester.tap(find.text('打开编辑器'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('修改 SSH 主机后保存仍保留主机密钥校验配置', (tester) async {
    const original = SshConnection(
      name: 'bastion',
      host: 'old.example:22',
      user: 'alice',
      keyFile: 'keys/id_ed25519',
      hostKeyCheck: 'known_hosts',
      knownHostsFile: 'keys/known_hosts',
    );
    SshConnection? saved;
    await _openEditor<SshConnection>(
      tester,
      const ConnectionEditorDialog(editing: original),
      (value) => saved = value,
    );
    await tester.enterText(
      find.widgetWithText(TextFormField, '主机 *（host 或 host:port）'),
      'new.example:2222',
    );
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();

    expect(saved, isNotNull);
    expect(saved!.toJson(), {...original.toJson(), 'host': 'new.example:2222'});
  });

  testWidgets('修改直连隧道后保存仍保留私钥、安全配置和全局继承', (tester) async {
    final original = Tunnel.fromJson({
      'name': 'database',
      'local_port': 15432,
      'remote_host': '127.0.0.1',
      'remote_port': 5432,
      'ssh_host': 'bastion.example:22',
      'ssh_user': 'alice',
      'key_file': 'keys/id_ed25519',
      'host_key_check': 'known_hosts',
      'known_hosts_file': 'keys/known_hosts',
      'is_running': true,
    });
    Tunnel? saved;
    await _openEditor<Tunnel>(
      tester,
      TunnelEditorDialog(editing: original),
      (value) => saved = value,
    );
    await tester.enterText(
      find.widgetWithText(TextFormField, '远端端口 *'),
      '6432',
    );
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();

    expect(saved, isNotNull);
    expect(saved!.toJson(), {...original.toJson(), 'remote_port': 6432});
    expect(saved!.isRunning, isTrue);
  });

  testWidgets('新建直连隧道必须填写私钥并将路径传给 daemon', (tester) async {
    Tunnel? saved;
    await _openEditor<Tunnel>(
      tester,
      const TunnelEditorDialog(),
      (value) => saved = value,
    );
    for (final field in {
      '隧道名称 *': 'database',
      '本地端口 *': '15432',
      '远端端口 *': '5432',
      'SSH 主机（host:port）*': 'bastion.example:22',
      'SSH 用户 *': 'alice',
    }.entries) {
      await tester.enterText(
        find.widgetWithText(TextFormField, field.key),
        field.value,
      );
    }
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved, isNull);
    expect(find.text('请填写私钥路径'), findsOneWidget);

    await tester.enterText(
      find.widgetWithText(TextFormField, '私钥路径 *'),
      'keys/id_ed25519',
    );
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved?.toJson()['key_file'], 'keys/id_ed25519');
    expect(saved?.reconnectStrategy, isEmpty);
    expect(saved?.reconnectInterval, isEmpty);
  });
}
