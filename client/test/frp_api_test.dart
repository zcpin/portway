import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models.dart';
import 'package:portway/models/frp.dart';
import 'package:portway/services/daemon_client.dart';

/// 强制回环请求不走系统代理，否则请求会被转发到代理服务器。
class _DirectConnections extends HttpOverrides {
  @override
  HttpClient createHttpClient(SecurityContext? context) =>
      super.createHttpClient(context)..findProxy = (_) => 'DIRECT';
}

/// 校验 daemon 模式下 FRP 接口的路径、动词与请求体。
///
/// 进程内引擎走的是 FFI 符号，daemon 走的是 REST；两者容易只改一边，
/// 这里对着真实 HTTP 服务端跑一遍，把路径写错、动词用错的问题挡住。
void main() {
  HttpOverrides? previousOverrides;
  setUp(() {
    previousOverrides = HttpOverrides.current;
    HttpOverrides.global = _DirectConnections();
  });
  tearDown(() => HttpOverrides.global = previousOverrides);

  late HttpServer server;
  late DaemonClient client;
  late List<String> requests;
  Object? lastBody;

  setUp(() async {
    requests = [];
    lastBody = null;
    server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((request) async {
      requests.add('${request.method} ${request.uri.path}');
      final body = await utf8.decoder.bind(request).join();
      if (body.isNotEmpty) lastBody = jsonDecode(body);
      request.response.headers.contentType = ContentType.json;
      request.response.write(request.method == 'GET' && request.uri.path == '/api/frp/clients' ? '[]' : '{"ok":true}');
      await request.response.close();
    });
    client = DaemonClient(DaemonInfo(
      host: '127.0.0.1',
      port: server.port,
      token: 'test-token',
      pid: 0,
      version: 'test',
      configPath: '',
    ));
  });

  tearDown(() async {
    client.close();
    await server.close(force: true);
  });

  test('读取 FRP 客户端列表', () async {
    expect(await client.getFrpClients(), isEmpty);
    expect(requests, ['GET /api/frp/clients']);
  });

  test('客户端的新增、更新、删除与启停走 REST', () async {
    const payload = FrpClientPayload(
      name: 'prod',
      group: '生产',
      serverAddr: 'frps.example.com',
      serverPort: 7000,
      authToken: 'secret',
    );

    await client.addFrpClient(payload);
    expect(requests.last, 'POST /api/frp/clients');
    expect((lastBody as Map)['server_addr'], 'frps.example.com');
    expect((lastBody as Map)['auto_start'], isTrue);

    await client.updateFrpClient('prod', payload);
    expect(requests.last, 'PUT /api/frp/clients/prod');

    await client.startFrpClient('prod');
    expect(requests.last, 'POST /api/frp/clients/prod/start');

    await client.stopFrpClient('prod');
    expect(requests.last, 'POST /api/frp/clients/prod/stop');

    await client.restartFrpClient('prod');
    expect(requests.last, 'POST /api/frp/clients/prod/restart');

    await client.deleteFrpClient('prod');
    expect(requests.last, 'DELETE /api/frp/clients/prod');
  });

  test('名称里的特殊字符会被转义', () async {
    await client.startFrpClient('生产 环境/1');
    expect(requests.last, 'POST /api/frp/clients/%E7%94%9F%E4%BA%A7%20%E7%8E%AF%E5%A2%83%2F1/start');
  });

  test('代理的增删改与启用开关走 REST', () async {
    const payload = FrpProxyPayload(
      name: 'mysql',
      type: 'tcp',
      localIp: '127.0.0.1',
      localPort: 3306,
      remotePort: 13306,
    );

    await client.addFrpProxy('prod', payload);
    expect(requests.last, 'POST /api/frp/clients/prod/proxies');
    expect((lastBody as Map)['local_port'], 3306);
    expect((lastBody as Map)['remote_port'], 13306);

    await client.updateFrpProxy('prod', 'mysql', payload);
    expect(requests.last, 'PUT /api/frp/clients/prod/proxies/mysql');

    await client.toggleFrpProxy('prod', 'mysql', false);
    expect(requests.last, 'POST /api/frp/clients/prod/proxies/mysql/toggle');
    expect((lastBody as Map)['enabled'], isFalse);

    await client.deleteFrpProxy('prod', 'mysql');
    expect(requests.last, 'DELETE /api/frp/clients/prod/proxies/mysql');
  });
}
