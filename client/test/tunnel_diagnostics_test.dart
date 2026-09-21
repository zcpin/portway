import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:portway/connection_preferences_provider.dart';
import 'package:portway/models.dart';
import 'package:portway/models/connection_preferences.dart';
import 'package:portway/models/tunnel_diagnostic.dart';
import 'package:portway/pages/tunnel_diagnostics.dart';
import 'package:portway/pages/tunnels_page.dart';
import 'package:portway/providers.dart';
import 'package:portway/services/daemon_client.dart';

const _info = DaemonInfo(
  host: '127.0.0.1',
  port: 1,
  token: 'test',
  pid: 1,
  version: 'test',
  configPath: '',
);
const _report = TunnelDiagnostic(
  name: 'database',
  mode: 'local',
  ok: false,
  elapsedMs: 15,
  checks: [
    DiagnosticCheck(
      stage: 'local_listener',
      status: 'ok',
      address: '127.0.0.1:15432',
    ),
    DiagnosticCheck(stage: 'ssh', status: 'ok', message: 'SSH 认证成功'),
    DiagnosticCheck(
      stage: 'target',
      status: 'failed',
      address: '127.0.0.1:5432',
      message: '请检查目标服务是否启动',
      error: 'connection refused',
    ),
  ],
);

class _DiagnosticClient extends DaemonClient {
  _DiagnosticClient() : super(_info);
  String? name;
  CancelToken? cancelToken;
  Completer<TunnelDiagnostic>? pending;

  @override
  Future<TunnelDiagnostic> diagnoseTunnel(
    String name, {
    CancelToken? cancelToken,
  }) async {
    this.name = name;
    this.cancelToken = cancelToken;
    return pending == null ? _report : pending!.future;
  }
}

class _DirectHttpOverrides extends HttpOverrides {
  @override
  HttpClient createHttpClient(SecurityContext? context) =>
      super.createHttpClient(context)..findProxy = (_) => 'DIRECT';
}

class _EmptyConnectionPreferences extends ConnectionPreferencesNotifier {
  @override
  Future<ConnectionPreferences> build() async => ConnectionPreferences();
}

void main() {
  test(
    'diagnostic requests encode the tunnel name and use daemon authentication',
    () => HttpOverrides.runWithHttpOverrides(() async {
      final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
      addTearDown(() => server.close(force: true));
      final request = Completer<HttpRequest>();
      server.listen((incoming) async {
        request.complete(incoming);
        incoming.response.headers.contentType = ContentType.json;
        incoming.response.write(
          jsonEncode({
            'name': 'db / 开发',
            'mode': 'dynamic',
            'ok': true,
            'elapsed_ms': 8,
            'checks': [
              {
                'stage': 'target',
                'status': 'skipped',
                'elapsed_ms': 0,
                'message': '目标由 SOCKS5 请求指定',
              },
            ],
          }),
        );
        await incoming.response.close();
      });
      final client = DaemonClient(
        DaemonInfo(
          host: '127.0.0.1',
          port: server.port,
          token: 'diagnostic-test-token',
          pid: 1,
          version: 'test',
          configPath: '',
        ),
      );
      addTearDown(client.close);
      final report = await client.diagnoseTunnel('db / 开发');
      final incoming = await request.future;
      expect(incoming.method, 'POST');
      expect(incoming.uri.pathSegments, [
        'api',
        'tunnels',
        'db / 开发',
        'diagnose',
      ]);
      expect(incoming.headers.value('X-Auth-Token'), 'diagnostic-test-token');
      expect(report.summary, contains('未检查项'));
      expect(report.toReport(), contains('目标 TCP 服务：未检查'));
    }, _DirectHttpOverrides()),
  );

  testWidgets(
    'tunnel card opens diagnosis and allows copying the detailed report',
    (tester) async {
      final client = _DiagnosticClient();
      addTearDown(client.close);
      String? copied;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copied = (call.arguments as Map)['text'] as String;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );
      final container = ProviderContainer(
        overrides: [
          clientProvider.overrideWith((ref) async => client),
          connectionPreferencesProvider.overrideWith(
            _EmptyConnectionPreferences.new,
          ),
        ],
      );
      addTearDown(container.dispose);
      await container.read(clientProvider.future);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: MaterialApp(
            home: Scaffold(
              body: TunnelCard(tunnel: Tunnel.fromJson({'name': 'database'})),
            ),
          ),
        ),
      );
      await tester.tap(find.byType(PopupMenuButton<String>));
      await tester.pumpAndSettle();
      await tester.tap(find.text('诊断连接'));
      await tester.pumpAndSettle();
      expect(client.name, 'database');
      expect(find.textContaining('发现连接问题'), findsOneWidget);
      expect(find.textContaining('SSH 连接与认证 · 通过'), findsOneWidget);
      expect(find.text('connection refused'), findsOneWidget);
      await tester.tap(find.text('复制报告'));
      await tester.pump();
      expect(copied, contains('目标 TCP 服务：未通过'));
      expect(copied, contains('connection refused'));
      await tester.tap(find.text('关闭'));
      await tester.pumpAndSettle();
      expect(client.cancelToken?.isCancelled, isTrue);
    },
  );

  testWidgets(
    'closing a pending diagnosis cancels the request and ignores late results',
    (tester) async {
      final client = _DiagnosticClient()
        ..pending = Completer<TunnelDiagnostic>();
      addTearDown(client.close);
      final container = ProviderContainer(
        overrides: [clientProvider.overrideWith((ref) async => client)],
      );
      addTearDown(container.dispose);
      await container.read(clientProvider.future);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: MaterialApp(
            home: Builder(
              builder: (context) => Scaffold(
                body: TextButton(
                  onPressed: () => showDialog<void>(
                    context: context,
                    builder: (_) => TunnelDiagnosticsDialog(
                      name: 'database',
                      client: client,
                    ),
                  ),
                  child: const Text('打开诊断'),
                ),
              ),
            ),
          ),
        ),
      );
      await tester.tap(find.text('打开诊断'));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 250));
      await tester.tap(find.text('取消'));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 250));
      expect(client.cancelToken?.isCancelled, isTrue);
      client.pending!.complete(_report);
      await tester.pump();
      expect(tester.takeException(), isNull);
      expect(find.textContaining('发现连接问题'), findsNothing);
    },
  );

  testWidgets(
    'instance changes cancel diagnostics and discard the old result',
    (tester) async {
      final client = _DiagnosticClient()
        ..pending = Completer<TunnelDiagnostic>();
      final replacement = _DiagnosticClient();
      addTearDown(client.close);
      addTearDown(replacement.close);
      var current = client;
      final container = ProviderContainer(
        overrides: [clientProvider.overrideWith((ref) async => current)],
      );
      addTearDown(container.dispose);
      await container.read(clientProvider.future);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: MaterialApp(
            home: Scaffold(
              body: TunnelDiagnosticsDialog(name: 'database', client: client),
            ),
          ),
        ),
      );
      await tester.pump();
      current = replacement;
      container.invalidate(clientProvider);
      await tester.pump();
      expect(client.cancelToken?.isCancelled, isTrue);
      client.pending!.complete(_report);
      await tester.pump();
      expect(find.text('实例连接已变化，请关闭后重新诊断'), findsOneWidget);
      expect(find.text('connection refused'), findsNothing);
      expect(replacement.name, isNull);
      expect(tester.takeException(), isNull);
    },
  );
}
