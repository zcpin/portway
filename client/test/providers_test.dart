import 'dart:async';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/providers.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';
import 'package:ssh_tunnel_client/services/daemon_discovery.dart';

class _DirectConnections extends HttpOverrides {
  @override
  HttpClient createHttpClient(SecurityContext? context) =>
      super.createHttpClient(context)..findProxy = (_) => 'DIRECT';
}

class _SnapshotClient extends DaemonClient {
  _SnapshotClient()
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

  @override
  Future<List<Tunnel>> getTunnels() async => [
    Tunnel.fromJson({'name': 'removed', 'is_running': true}),
  ];
}

class _PendingSnapshotClient extends _SnapshotClient {
  final requested = Completer<void>();
  final response = Completer<List<Tunnel>>();

  @override
  Future<List<Tunnel>> getTunnels() {
    requested.complete();
    return response.future;
  }
}

void main() {
  test('服务发现跳过无效地址和过期 token', () async {
    final previousOverrides = HttpOverrides.current;
    HttpOverrides.global = _DirectConnections();
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final incoming = server.listen((request) async {
      request.response.statusCode =
          request.uri.path == '/api/health' ||
              request.headers.value('X-Auth-Token') == 'current-token'
          ? 200
          : 401;
      request.response.headers.contentType = ContentType.json;
      request.response.write('{}');
      await request.response.close();
    });
    DaemonInfo info(String host, String token) => DaemonInfo(
      host: host,
      port: server.port,
      token: token,
      pid: 0,
      version: 'test',
      configPath: '',
    );
    final container = ProviderContainer(
      overrides: [
        discoveryProvider.overrideWith(
          (ref) async => [
            DaemonCandidate(path: 'invalid', info: info('bad:host', 'bad')),
            DaemonCandidate(
              path: 'stale',
              info: info('127.0.0.1', 'stale-token'),
            ),
            DaemonCandidate(
              path: 'current',
              info: info('127.0.0.1', 'current-token'),
            ),
          ],
        ),
      ],
    );
    try {
      final client = await container.read(clientProvider.future);
      expect(client?.info.token, 'current-token');
    } finally {
      container.dispose();
      await server.close(force: true);
      await incoming.cancel();
      HttpOverrides.global = previousOverrides;
    }
  });

  test('完整快照替换离线期间的旧行和状态', () async {
    final client = _SnapshotClient();
    final container = ProviderContainer(
      overrides: [clientProvider.overrideWith((ref) async => client)],
    );
    addTearDown(() {
      container.dispose();
      client.close();
    });
    await container.read(tunnelsProvider.future);
    container.read(tunnelsProvider.notifier).applySnapshot([
      Tunnel.fromJson({'name': 'current', 'is_running': false}),
    ]);
    final rows = container.read(tunnelsProvider).valueOrNull!;
    expect(rows.map((row) => row.name), ['current']);
    expect(rows.single.isRunning, isFalse);
    container.read(tunnelsProvider.notifier).applySnapshot([]);
    expect(container.read(tunnelsProvider).valueOrNull, isEmpty);
  });

  for (final failRequest in [false, true]) {
    test('迟到的 HTTP ${failRequest ? '错误' : '列表'}不会覆盖最新快照和状态', () async {
      final client = _PendingSnapshotClient();
      final container = ProviderContainer(
        overrides: [clientProvider.overrideWith((ref) async => client)],
      );
      addTearDown(() {
        container.dispose();
        client.close();
      });
      final initial = container.read(tunnelsProvider.future);
      await client.requested.future;
      final notifier = container.read(tunnelsProvider.notifier);
      notifier.applySnapshot([
        Tunnel.fromJson({'name': 'current', 'is_running': false}),
      ]);
      notifier.applyStatus({'current': true});
      if (failRequest) {
        client.response.completeError(StateError('old request failed'));
      } else {
        client.response.complete([
          Tunnel.fromJson({'name': 'removed', 'is_running': false}),
        ]);
      }
      await initial;
      await Future<void>.delayed(Duration.zero);

      final rows = container.read(tunnelsProvider).valueOrNull!;
      expect(rows.map((row) => row.name), ['current']);
      expect(rows.single.isRunning, isTrue);
    });
  }
}
