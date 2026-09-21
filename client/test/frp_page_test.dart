import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models/frp.dart';
import 'package:portway/pages/frp_page.dart';
import 'package:portway/providers.dart';

/// 用假数据驱动页面，只验证界面行为与引擎调用，不启动真实 frpc。
class _Clients extends FrpClientsNotifier {
  _Clients([List<FrpClient>? rows]) : rows = rows ?? defaultRows;

  static final defaultRows = [
    FrpClient.fromJson({
      'name': 'prod-frpc',
      'group': '生产',
      'server_addr': 'frps.example.com',
      'server_port': 7000,
      'running': true,
      'proxies': [
        {
          'name': 'mysql',
          'type': 'tcp',
          'enabled': true,
          'local_ip': '127.0.0.1',
          'local_port': 3306,
          'remote_port': 13306,
          'phase': 'start',
          'remote_addr': 'frps.example.com:13306',
        },
        {'name': 'redis', 'type': 'tcp', 'enabled': false, 'local_port': 6379, 'remote_port': 16379},
      ],
    }),
    FrpClient.fromJson({
      'name': 'dev-frpc',
      'server_addr': '10.0.0.1',
      'server_port': 7000,
      'last_error': '配置校验失败: localPort must be in range',
    }),
  ];

  final List<FrpClient> rows;
  final started = <String>[];
  final stopped = <String>[];
  final toggled = <String>[];
  final deletedClients = <String>[];
  final deletedProxies = <String>[];
  FrpProxyPayload? savedProxy;
  String? savedProxyEditingName;

  @override
  Future<List<FrpClient>> build() async => rows;

  @override
  Future<void> startClient(String name) async => started.add(name);

  @override
  Future<void> stopClient(String name) async => stopped.add(name);

  @override
  Future<void> removeClient(String name) async => deletedClients.add(name);

  @override
  Future<void> removeProxy(String client, String proxy) async => deletedProxies.add('$client/$proxy');

  @override
  Future<void> toggleProxy(String client, String proxy, bool enabled) async =>
      toggled.add('$client/$proxy=$enabled');

  @override
  Future<void> saveProxy(String client, FrpProxyPayload payload, {String? editingName}) async {
    savedProxy = payload;
    savedProxyEditingName = editingName;
  }
}

Future<_Clients> _showPage(WidgetTester tester, {_Clients? notifier}) async {
  tester.view.physicalSize = const Size(1400, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final clients = notifier ?? _Clients();
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        frpClientsProvider.overrideWith(() => clients),
        clientProvider.overrideWith((ref) async => null),
      ],
      child: const MaterialApp(home: FrpPage()),
    ),
  );
  await tester.pumpAndSettle();
  return clients;
}

void main() {
  testWidgets('列出客户端及其代理，并显示状态与错误', (tester) async {
    await _showPage(tester);

    expect(find.text('prod-frpc'), findsOneWidget);
    expect(find.text('生产'), findsOneWidget);
    expect(find.text('frps.example.com:7000 · 2 条代理（1 条启用）'), findsOneWidget);
    expect(find.text('运行中'), findsOneWidget);

    expect(find.text('mysql'), findsOneWidget);
    expect(find.text('redis'), findsOneWidget);
    // 已连接的代理显示远端地址
    expect(find.text('本地 127.0.0.1:3306 → frps.example.com:13306'), findsOneWidget);

    // 配置损坏的客户端仍要列出来，并给出原因
    expect(find.text('dev-frpc'), findsOneWidget);
    expect(find.text('配置有误'), findsOneWidget);
    expect(find.textContaining('localPort must be in range'), findsOneWidget);
  });

  testWidgets('空列表给出引导文案', (tester) async {
    await _showPage(tester, notifier: _Clients([]));
    expect(find.textContaining('还没有 FRP 客户端'), findsOneWidget);
  });

  testWidgets('搜索按客户端名、地址或代理名过滤', (tester) async {
    await _showPage(tester);

    await tester.enterText(find.byKey(const Key('frp-search')), 'redis');
    await tester.pumpAndSettle();
    expect(find.text('prod-frpc'), findsOneWidget);
    expect(find.text('dev-frpc'), findsNothing);

    await tester.enterText(find.byKey(const Key('frp-search')), '10.0.0.1');
    await tester.pumpAndSettle();
    expect(find.text('dev-frpc'), findsOneWidget);
    expect(find.text('prod-frpc'), findsNothing);

    await tester.enterText(find.byKey(const Key('frp-search')), '不存在的名字');
    await tester.pumpAndSettle();
    expect(find.text('没有匹配的客户端'), findsOneWidget);
  });

  testWidgets('运行中的客户端可以停止，已停止的可以启动', (tester) async {
    final clients = await _showPage(tester);

    // 第一张卡片是运行中的，按钮为停止
    await tester.tap(find.byTooltip('停止').first);
    await tester.pumpAndSettle();
    expect(clients.stopped, ['prod-frpc']);

    await tester.tap(find.byTooltip('启动').first);
    await tester.pumpAndSettle();
    expect(clients.started, ['dev-frpc']);
  });

  testWidgets('切换代理启用状态时把新状态传给引擎', (tester) async {
    final clients = await _showPage(tester);

    // 第一个开关属于 mysql（已启用），切换后应停用
    await tester.tap(find.byType(Switch).first);
    await tester.pumpAndSettle();
    expect(clients.toggled, ['prod-frpc/mysql=false']);

    await tester.tap(find.byType(Switch).at(1));
    await tester.pumpAndSettle();
    expect(clients.toggled.last, 'prod-frpc/redis=true');
  });

  testWidgets('删除客户端需要确认，取消则不调用引擎', (tester) async {
    final clients = await _showPage(tester);

    await tester.tap(find.byTooltip('更多操作').first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('删除'));
    await tester.pumpAndSettle();

    expect(find.text('确认删除'), findsOneWidget);
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(clients.deletedClients, isEmpty);

    await tester.tap(find.byTooltip('更多操作').first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('删除'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, '删除'));
    await tester.pumpAndSettle();

    expect(clients.deletedClients, ['prod-frpc']);
  });

  testWidgets('新建代理时表单字段会提交给引擎', (tester) async {
    final clients = await _showPage(tester);

    await tester.tap(find.text('添加代理').first);
    await tester.pumpAndSettle();

    await tester.enterText(find.widgetWithText(TextFormField, '代理名称'), 'web');
    await tester.enterText(find.widgetWithText(TextFormField, '本地端口'), '8080');
    await tester.enterText(find.widgetWithText(TextFormField, '远端端口'), '18080');
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    final payload = clients.savedProxy;
    expect(payload, isNotNull);
    expect(payload!.name, 'web');
    expect(payload.type, 'tcp');
    expect(payload.localPort, 8080);
    expect(payload.remotePort, 18080);
    expect(payload.enabled, isTrue);
    expect(clients.savedProxyEditingName, isNull);
  });

  testWidgets('代理表单缺少本地端口时不提交', (tester) async {
    final clients = await _showPage(tester);

    await tester.tap(find.text('添加代理').first);
    await tester.pumpAndSettle();
    await tester.enterText(find.widgetWithText(TextFormField, '代理名称'), 'web');
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(clients.savedProxy, isNull);
    expect(find.text('请填写本地端口'), findsOneWidget);
  });

  testWidgets('编辑已有代理时提交原名称用于改名', (tester) async {
    final clients = await _showPage(tester);

    await tester.tap(find.byTooltip('代理操作').first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('编辑'));
    await tester.pumpAndSettle();

    await tester.enterText(find.widgetWithText(TextFormField, '代理名称'), 'mysql-renamed');
    await tester.tap(find.widgetWithText(FilledButton, '保存'));
    await tester.pumpAndSettle();

    expect(clients.savedProxyEditingName, 'mysql');
    expect(clients.savedProxy?.name, 'mysql-renamed');
  });

  testWidgets('访问端不支持编辑，给出提示而不是打开表单', (tester) async {
    final clients = await _showPage(
      tester,
      notifier: _Clients([
        FrpClient.fromJson({
          'name': 'peer',
          'server_addr': '10.0.0.2',
          'proxies': [
            {'name': 'peer-db', 'type': 'stcp', 'editable': false, 'enabled': true},
          ],
        }),
      ]),
    );

    await tester.tap(find.byTooltip('代理操作'));
    await tester.pumpAndSettle();

    // 菜单里的「编辑」对访问端是禁用的
    final editItem = tester.widget<PopupMenuItem<String>>(find.widgetWithText(PopupMenuItem<String>, '编辑'));
    expect(editItem.enabled, isFalse);
    expect(clients.savedProxy, isNull);
  });
}
