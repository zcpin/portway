import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models.dart';
import 'package:portway/pages/ssh_security.dart';
import 'package:portway/providers.dart';
import 'package:portway/services/daemon_client.dart';

class _SecurityClient extends DaemonClient {
  _SecurityClient()
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
  (String, bool)? trusted;
  static const key = HostKeyInfo(
    host: 'example:22',
    fingerprint: 'SHA256:fixture',
    algorithm: 'ssh-ed25519',
    file: 'fixture/known_hosts',
    known: false,
    changed: true,
  );
  @override
  Future<HostKeyInfo> inspectHostKey(SshConnection connection) async => key;
  @override
  Future<HostKeyInfo> trustHostKey(
    SshConnection connection,
    String fingerprint,
    bool replace,
  ) async {
    trusted = (fingerprint, replace);
    return key;
  }
}

class _SecurityKeys extends KeysNotifier {
  (String, String)? unlocked;
  @override
  Future<List<KeyInfo>> build() async => [];
  @override
  Future<void> unlock(String path, String passphrase) async {
    unlocked = (path, passphrase);
  }
}

Future<void> _showSecurity(
  WidgetTester tester,
  _SecurityClient client,
  _SecurityKeys keys,
) async {
  tester.view.physicalSize = const Size(1200, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  addTearDown(client.close);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        clientProvider.overrideWith((ref) async => client),
        keysProvider.overrideWith(() => keys),
      ],
      child: MaterialApp(
        home: Scaffold(
          body: Consumer(
            builder: (context, ref, _) => Column(
              children: [
                TextButton(
                  onPressed: () => inspectAndTrustHostKey(
                    context,
                    ref,
                    const SshConnection(
                      name: 'example',
                      host: 'example:22',
                      user: 'alice',
                      keyFile: 'fixture.key',
                    ),
                  ),
                  child: const Text('检查指纹'),
                ),
                TextButton(
                  onPressed: () =>
                      unlockPrivateKey(context, ref, 'fixture.key'),
                  child: const Text('解锁文件'),
                ),
              ],
            ),
          ),
        ),
      ),
    ),
  );
}

void main() {
  testWidgets('变更指纹只有明确确认后才发送替换请求', (tester) async {
    final client = _SecurityClient();
    await _showSecurity(tester, client, _SecurityKeys());
    await tester.tap(find.text('检查指纹'));
    await tester.pumpAndSettle();
    expect(find.text('主机指纹已变化'), findsOneWidget);
    expect(client.trusted, isNull);
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(client.trusted, isNull);
    await tester.tap(find.text('检查指纹'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('替换并信任'));
    await tester.pumpAndSettle();
    expect(client.trusted, ('SHA256:fixture', true));
  });

  testWidgets('口令输入被遮蔽且原样提交到会话解锁', (tester) async {
    final keys = _SecurityKeys();
    await _showSecurity(tester, _SecurityClient(), keys);
    await tester.tap(find.text('解锁文件'));
    await tester.pumpAndSettle();
    final field = find.widgetWithText(TextField, '私钥口令');
    expect(tester.widget<TextField>(field).obscureText, isTrue);
    await tester.enterText(field, '  fixture secret  ');
    await tester.tap(find.byKey(const Key('unlock-submit')));
    await tester.pumpAndSettle();
    expect(keys.unlocked, ('fixture.key', '  fixture secret  '));
  });
}
