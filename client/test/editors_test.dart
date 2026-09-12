import 'package:flutter/material.dart';
import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/pages/connections_page.dart';
import 'package:ssh_tunnel_client/pages/tunnels_page.dart';
import 'package:ssh_tunnel_client/providers.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';

class _DiagnosticClient extends DaemonClient {
  _DiagnosticClient()
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
  SshConnection? tested;

  @override
  Future<ConnectionDiagnostic> testSshConnection(
    SshConnection connection, {
    CancelToken? cancelToken,
  }) async {
    tested = connection;
    return const ConnectionDiagnostic(ok: true, elapsedMs: 12);
  }
}

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
  ValueChanged<T?> onSaved, {
  DaemonClient? client,
}) async {
  tester.view.physicalSize = const Size(1200, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        keysProvider.overrideWith(_ReadableKeys.new),
        sshConnectionsProvider.overrideWith(_EmptyConnections.new),
        if (client != null) clientProvider.overrideWith((ref) async => client),
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
  testWidgets('SOCKS5 隧道无需固定远端目标并保留跳板链', (tester) async {
    final original = Tunnel.fromJson({
      'name': 'proxy',
      'mode': 'dynamic',
      'local_port': 1080,
      'ssh_host': 'example:22',
      'ssh_user': 'alice',
      'auth_method': 'agent',
      'proxy_jump': ['jump-a', 'jump-b'],
    });
    Tunnel? saved;
    await _openEditor<Tunnel>(
      tester,
      TunnelEditorDialog(editing: original),
      (value) => saved = value,
    );
    expect(find.widgetWithText(TextFormField, '远端端口 *'), findsNothing);
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved?.toJson(), original.toJson());
  });

  testWidgets('反向转发编辑器区分本机目标与远端监听', (tester) async {
    final original = Tunnel.fromJson({
      'name': 'reverse',
      'mode': 'remote',
      'local_host': 'localhost',
      'local_port': 5432,
      'remote_host': '127.0.0.1',
      'remote_port': 15432,
      'ssh_host': 'example:22',
      'ssh_user': 'alice',
      'auth_method': 'agent',
    });
    Tunnel? saved;
    await _openEditor<Tunnel>(
      tester,
      TunnelEditorDialog(editing: original),
      (value) => saved = value,
    );
    expect(find.widgetWithText(TextFormField, '本机目标端口 *'), findsOneWidget);
    expect(find.widgetWithText(TextFormField, '远端监听端口 *'), findsOneWidget);
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved?.toJson(), original.toJson());
  });
  testWidgets('agent 连接可不填写私钥并保留 agent 地址', (tester) async {
    SshConnection? saved;
    await _openEditor<SshConnection>(
      tester,
      const ConnectionEditorDialog(
        editing: SshConnection(
          name: 'agent',
          host: 'example:22',
          user: 'alice',
          keyFile: '',
          authMethod: 'agent',
          agentSocket: 'fixture-agent',
        ),
      ),
      (value) => saved = value,
    );
    expect(find.widgetWithText(TextFormField, '私钥路径 *'), findsNothing);
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved?.authMethod, 'agent');
    expect(saved?.agentSocket, 'fixture-agent');
  });

  testWidgets('agent 直连隧道可不填写私钥', (tester) async {
    Tunnel? saved;
    final original = Tunnel.fromJson({
      'name': 'agent',
      'local_port': 15432,
      'remote_host': '127.0.0.1',
      'remote_port': 5432,
      'ssh_host': 'example:22',
      'ssh_user': 'alice',
      'auth_method': 'agent',
      'agent_socket': 'fixture-agent',
    });
    await _openEditor<Tunnel>(
      tester,
      TunnelEditorDialog(editing: original),
      (value) => saved = value,
    );
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved?.toJson(), original.toJson());
  });
  testWidgets('测试连接使用未保存的编辑值并展示结果', (tester) async {
    final client = _DiagnosticClient();
    addTearDown(client.close);
    SshConnection? saved;
    await _openEditor<SshConnection>(
      tester,
      const ConnectionEditorDialog(
        editing: SshConnection(
          name: 'test',
          host: 'old.example',
          user: 'alice',
          keyFile: 'key',
          hostKeyCheck: 'known_hosts',
          knownHostsFile: 'custom_hosts',
        ),
      ),
      (value) => saved = value,
      client: client,
    );
    await tester.enterText(
      find.widgetWithText(TextFormField, '主机 *（host 或 host:port）'),
      'new.example',
    );
    await tester.tap(find.text('测试连接'));
    await tester.pumpAndSettle();
    expect(find.text('SSH 连接成功'), findsOneWidget);
    expect(client.tested?.host, 'new.example');
    expect(client.tested?.knownHostsFile, 'custom_hosts');
    expect(saved, isNull);
  });
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
