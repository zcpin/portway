import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';

class _DirectConnections extends HttpOverrides {
  @override
  HttpClient createHttpClient(SecurityContext? context) =>
      super.createHttpClient(context)..findProxy = (_) => 'DIRECT';
}

DaemonInfo _info(int port) => DaemonInfo(
  host: '127.0.0.1',
  port: port,
  token: '',
  pid: 0,
  version: 'test',
  configPath: '',
);

void main() {
  HttpOverrides? previousOverrides;
  setUp(() {
    previousOverrides = HttpOverrides.current;
    HttpOverrides.global = _DirectConnections();
  });
  tearDown(() => HttpOverrides.global = previousOverrides);

  test('close 中断尚未响应的 WebSocket 握手', () async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final received = Completer<void>();
    final incoming = server.listen((request) {
      unawaited(request.response.done.catchError((_) {}));
      if (!received.isCompleted) received.complete();
    });
    final client = DaemonClient(_info(server.port));
    final done = Completer<void>();
    final subscription = client.events().listen((_) {}, onDone: done.complete);
    try {
      await received.future.timeout(const Duration(seconds: 2));
      client.close();
      await done.future.timeout(const Duration(seconds: 2));
    } finally {
      client.close();
      await server.close(force: true);
      await subscription.cancel();
      await incoming.cancel();
    }
  });

  test('升级刚完成时 close 也会关闭连接和事件流', () async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final upgraded = Completer<WebSocket>();
    final peerClosed = Completer<void>();
    final incoming = server.listen((request) async {
      final peer = await WebSocketTransformer.upgrade(request);
      peer.listen(
        (_) {},
        onDone: () {
          if (!peerClosed.isCompleted) peerClosed.complete();
        },
        onError: (_) {
          if (!peerClosed.isCompleted) peerClosed.complete();
        },
      );
      upgraded.complete(peer);
    });
    final client = DaemonClient(_info(server.port));
    final done = Completer<void>();
    final subscription = client.events().listen((_) {}, onDone: done.complete);
    WebSocket? peer;
    try {
      peer = await upgraded.future.timeout(const Duration(seconds: 2));
      client.close();
      await peerClosed.future.timeout(const Duration(seconds: 2));
      await done.future.timeout(const Duration(seconds: 2));
    } finally {
      client.close();
      await peer?.close();
      await server.close(force: true);
      await subscription.cancel();
      await incoming.cancel();
    }
  });
}
