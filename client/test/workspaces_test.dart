import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/pages/workspace_switcher.dart';
import 'package:ssh_tunnel_client/providers.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';
import 'package:ssh_tunnel_client/services/daemon_discovery.dart';
import 'package:ssh_tunnel_client/services/tunnel_engine.dart';
import 'package:ssh_tunnel_client/services/workspaces.dart';

class _Preferences extends WorkspacesNotifier {
  _Preferences(this.initial);
  final WorkspacePreferences initial;
  String? created;
  @override
  Future<WorkspacePreferences> build() async => initial;
  @override
  Future<void> select(String path) async => state = AsyncData(
    WorkspacePreferences(
      selectedPath: path,
      workspaces: state.valueOrNull!.workspaces,
    ),
  );
  @override
  Future<void> create(String name) async {
    created = name;
  }
}

class _Direct extends HttpOverrides {
  @override
  HttpClient createHttpClient(SecurityContext? context) =>
      super.createHttpClient(context)..findProxy = (_) => 'DIRECT';
}

class _ClientSelection extends Notifier<DaemonClient> {
  _ClientSelection(this.initial);
  final DaemonClient initial;
  @override
  DaemonClient build() => initial;
  void select(DaemonClient client) => state = client;
}

class _ScopedClient extends DaemonClient {
  _ScopedClient(this.name)
    : super(
        DaemonInfo(
          host: '127.0.0.1',
          port: 1,
          token: name,
          pid: 0,
          version: 'test',
          configPath: '',
        ),
      );
  final String name;
  Completer<List<Tunnel>>? pending;
  Completer<List<LogEntry>>? history;
  @override
  Future<List<Tunnel>> getTunnels() async => pending == null
      ? [
          Tunnel.fromJson({'name': name}),
        ]
      : pending!.future;
  @override
  Future<List<LogEntry>> getLogs() async => history == null
      ? [
          LogEntry.fromJson({
            'timestamp': '2026-09-12T00:00:00Z',
            'message': name,
          }),
        ]
      : history!.future;
}

/// 只用于界面测试的假引擎：工作区切换器只读取 [info]，
/// 其余接口不会被调用，统一抛 [UnimplementedError]。
class _FakeEngine implements TunnelEngine {
  _FakeEngine(this.info);

  @override
  final DaemonInfo info;

  @override
  bool get isClosed => false;

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError();
}

void main() {
  test('工作区独立存储且并发创建不丢失记录', () async {
    final root = await Directory.systemTemp.createTemp('ssh-workspaces-');
    final store = WorkspaceStore(directory: root.path);
    await Future.wait([store.create('开发 / A'), store.create('测试 B')]);
    final preferences = await store.load();
    expect(preferences.workspaces, hasLength(2));
    expect(preferences.workspaces.map((w) => w.dataDir).toSet(), hasLength(2));
    for (final workspace in preferences.workspaces) {
      expect(workspace.dataDir.startsWith(root.path), isTrue);
      expect(RegExp(r'^[0-9a-f]{32}$').hasMatch(workspace.id), isTrue);
      expect(
        await File(workspace.configPath).readAsString(),
        'log_level = "info"\n',
      );
    }
    await store.select(preferences.workspaces.first.discoveryPath);
    expect(
      (await WorkspaceStore(directory: root.path).load()).selectedPath,
      preferences.workspaces.first.discoveryPath,
    );
    await expectLater(store.create('开发 / A'), throwsArgumentError);
  });

  test('非法工作区标识与损坏偏好不会被覆盖', () async {
    final root = await Directory.systemTemp.createTemp(
      'ssh-workspaces-invalid-',
    );
    final file = File('${root.path}${Platform.pathSeparator}workspaces.json');
    final corrupt = jsonEncode({
      'selected_path': '',
      'workspaces': [
        {'id': '../escape', 'name': 'bad'},
      ],
    });
    await file.writeAsString(corrupt, encoding: utf8);
    final store = WorkspaceStore(directory: root.path);
    await expectLater(store.load(), throwsFormatException);
    await expectLater(store.create('new'), throwsFormatException);
    expect(await file.readAsString(), corrupt);
  });

  test('指定离线实例时不回退到其他健康实例或启动默认 daemon', () async {
    final previous = HttpOverrides.current;
    HttpOverrides.global = _Direct();
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((request) async {
      request.response.statusCode = 200;
      request.response.write('{}');
      await request.response.close();
    });
    final prefs = _Preferences(
      const WorkspacePreferences(selectedPath: 'offline'),
    );
    var launches = 0;
    final container = ProviderContainer(
      overrides: [
        workspacesProvider.overrideWith(() => prefs),
        discoveryProvider.overrideWith(
          (ref) async => [
            DaemonCandidate(
              path: 'healthy',
              info: DaemonInfo(
                host: '127.0.0.1',
                port: server.port,
                token: 'current',
                pid: 0,
                version: 'test',
                configPath: '',
              ),
            ),
          ],
        ),
        daemonStarterProvider.overrideWithValue(({configPath, dataDir}) async {
          launches++;
          return false;
        }),
      ],
    );
    try {
      expect(await container.read(clientProvider.future), isNull);
      expect(launches, 0);
      await prefs.select('healthy');
      await container.pump();
      final first = await container.read(clientProvider.future);
      expect(first?.info.discoveryPath, 'healthy');
      await prefs.select('offline');
      await container.pump();
      expect(await container.read(clientProvider.future), isNull);
      expect(await first!.checkAuth(), isFalse);
    } finally {
      container.dispose();
      await server.close(force: true);
      HttpOverrides.global = previous;
    }
  });

  test('工作区自动启动只传入所选配置和发现目录', () async {
    const workspace = Workspace(
      id: 'fixture',
      name: 'test',
      dataDir: 'fixture-data',
    );
    final preferences = _Preferences(
      WorkspacePreferences(
        selectedPath: workspace.discoveryPath,
        workspaces: [workspace],
      ),
    );
    (String?, String?)? launched;
    final container = ProviderContainer(
      overrides: [
        workspacesProvider.overrideWith(() => preferences),
        discoveryProvider.overrideWith((ref) async => []),
        daemonStarterProvider.overrideWithValue(({configPath, dataDir}) async {
          launched = (configPath, dataDir);
          return false;
        }),
      ],
    );
    addTearDown(container.dispose);
    expect(await container.read(clientProvider.future), isNull);
    expect(launched, (workspace.configPath, workspace.dataDir));
  });

  test('切换实例后迟到的列表和历史日志不覆盖新数据', () async {
    final old = _ScopedClient('old'), next = _ScopedClient('new');
    final selection = NotifierProvider<_ClientSelection, DaemonClient>(
      () => _ClientSelection(old),
    );
    final container = ProviderContainer(
      overrides: [
        clientProvider.overrideWith((ref) async => ref.watch(selection)),
      ],
    );
    addTearDown(() {
      container.dispose();
      old.close();
      next.close();
    });
    await container.read(tunnelsProvider.future);
    old.pending = Completer<List<Tunnel>>();
    old.history = Completer<List<LogEntry>>();
    final refresh = container.read(tunnelsProvider.notifier).refresh();
    final history = container.read(logsProvider.notifier).loadHistory();
    await Future<void>.delayed(Duration.zero);
    container.read(selection.notifier).select(next);
    await container.pump();
    await container.read(tunnelsProvider.future);
    container.read(logsProvider.notifier).clear();
    await container.read(logsProvider.notifier).loadHistory();
    old.pending!.complete([
      Tunnel.fromJson({'name': 'stale'}),
    ]);
    old.history!.complete([
      LogEntry.fromJson({'message': 'stale'}),
    ]);
    await Future.wait([refresh, history]);
    expect(container.read(tunnelsProvider).valueOrNull!.single.name, 'new');
    expect(container.read(logsProvider).single.message, 'new');
  });

  testWidgets('进程内引擎下不列出残留的 daemon 发现候选', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final engine = _FakeEngine(
      const DaemonInfo(
        host: 'embedded',
        port: 0,
        token: '',
        pid: 0,
        version: 'test',
        configPath: 'embedded-config',
        embedded: true,
      ),
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          workspacesProvider.overrideWith(
            () => _Preferences(const WorkspacePreferences()),
          ),
          discoveryProvider.overrideWith(
            (ref) async => [
              DaemonCandidate(
                path: 'stale.json',
                info: DaemonInfo(
                  host: '127.0.0.1',
                  port: 51437,
                  token: 'stale',
                  pid: 0,
                  version: 'test',
                  configPath: '',
                ),
              ),
            ],
          ),
          instanceAvailabilityProvider.overrideWith((ref) async => {}),
          clientProvider.overrideWith((ref) async => engine),
        ],
        child: const MaterialApp(home: Scaffold(body: WorkspaceSwitcher())),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('workspace-selector')));
    await tester.pumpAndSettle();
    // 下拉里只有「自动选择实例」，不应出现残留的 daemon 候选及其地址。
    expect(find.textContaining('51437'), findsNothing);
    expect(find.textContaining('用户进程'), findsNothing);
    // DropdownButton 会同时渲染按钮上的选中项与菜单项，故用 findsWidgets。
    expect(find.text('自动选择实例'), findsWidgets);
  });

  testWidgets('daemon 候选离线时不展示连接地址', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final engine = _FakeEngine(
      const DaemonInfo(
        host: '127.0.0.1',
        port: 1,
        token: 'live',
        pid: 0,
        version: 'test',
        configPath: '',
      ),
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          workspacesProvider.overrideWith(
            () => _Preferences(const WorkspacePreferences()),
          ),
          discoveryProvider.overrideWith(
            (ref) async => [
              DaemonCandidate(
                path: 'stale.json',
                info: DaemonInfo(
                  host: '127.0.0.1',
                  port: 51437,
                  token: 'stale',
                  pid: 0,
                  version: 'test',
                  configPath: '',
                ),
              ),
            ],
          ),
          instanceAvailabilityProvider.overrideWith((ref) async => {}),
          clientProvider.overrideWith((ref) async => engine),
        ],
        child: const MaterialApp(home: Scaffold(body: WorkspaceSwitcher())),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('workspace-selector')));
    await tester.pumpAndSettle();
    // daemon 模式下列出候选，但离线条目不携带假地址。
    expect(find.text('用户进程 · 离线'), findsOneWidget);
    expect(find.textContaining('51437'), findsNothing);
  });

  testWidgets('实例选择与创建工作区交互', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    const workspace = Workspace(
      id: 'fixture',
      name: 'development',
      dataDir: 'fixture-data',
    );
    final prefs = _Preferences(
      const WorkspacePreferences(workspaces: [workspace]),
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          workspacesProvider.overrideWith(() => prefs),
          discoveryProvider.overrideWith((ref) async => []),
          instanceAvailabilityProvider.overrideWith((ref) async => {}),
          clientProvider.overrideWith((ref) async => null),
        ],
        child: const MaterialApp(home: Scaffold(body: WorkspaceSwitcher())),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('workspace-selector')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('development · 未连接').last);
    await tester.pumpAndSettle();
    expect(prefs.state.valueOrNull?.selectedPath, workspace.discoveryPath);
    await tester.tap(find.text('新建工作区'));
    await tester.pumpAndSettle();
    await tester.enterText(find.widgetWithText(TextField, '工作区名称'), 'staging');
    await tester.tap(find.text('创建'));
    await tester.pumpAndSettle();
    expect(prefs.created, 'staging');
  });
}
