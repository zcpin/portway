import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models.dart';
import 'package:portway/models/frp.dart';
import 'package:portway/services/embedded_engine.dart';

/// 进程内引擎的端到端验证：真的把 Go 编译出的动态库加载进来，走一遍 FFI。
///
/// 这里覆盖的是「Dart 声明 ↔ Go 导出」这一层契约，单靠 Go 侧单测覆盖不到：
/// 符号名、参数类型（句柄是 64 位整数）、返回字符串的释放方式、
/// `{"ok":..}` 信封格式，任意一处对不上都会在这里暴露。
///
/// 动态库未编译时整个文件跳过，这样 CI 上只跑 Dart 逻辑也不会红。
void main() {
  // 测试用隧道：autoStart 关闭，agent 认证（不需要 key_file），
  // 因此既不会真去拨 SSH，也不依赖本机私钥。
  const demoTunnel = Tunnel(
    name: 'demo',
    autoStart: false,
    localPort: 18080,
    remoteHost: '127.0.0.1',
    remotePort: 8080,
    sshConnection: '',
    sshHost: '127.0.0.1',
    sshUser: 'root',
    authMethod: 'agent',
    reconnectStrategy: 'fixed',
    reconnectInterval: '5s',
    maxReconnectAttempts: 3,
    isRunning: false,
  );

  final separator = Platform.pathSeparator;
  final library = File(
    '..${separator}daemon${separator}bin$separator'
    '${EmbeddedEngine.libraryFileName}',
  ).absolute;

  if (!library.existsSync()) {
    test(
      '进程内引擎端到端',
      () {},
      skip: '未找到 ${library.path}，先运行 daemon 的 c-shared 构建',
    );
    return;
  }

  late Directory tempDir;

  String configPath() =>
      '${tempDir.path}${Platform.pathSeparator}config.toml';

  setUpAll(() {
    EmbeddedEngine.libraryPathOverride = library.path;
  });

  tearDownAll(() {
    EmbeddedEngine.libraryPathOverride = null;
  });

  setUp(() async {
    tempDir = await Directory.systemTemp.createTemp('portway-ffi');
  });

  tearDown(() async {
    if (tempDir.existsSync()) await tempDir.delete(recursive: true);
  });

  test('动态库能在当前平台被定位到', () {
    expect(EmbeddedEngine.isAvailable, isTrue);
    expect(EmbeddedEngine.resolveLibraryPath(), library.path);
  });

  test('创建引擎后能读到运行信息，且不带端口与令牌', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    expect(engine.info.embedded, isTrue);
    expect(engine.info.sourceLabel, '进程内引擎');
    expect(engine.info.version, isNotEmpty);
    expect(engine.info.configPath, contains('config.toml'));
    // 端口与令牌是 daemon 模式的概念，界面不应把它们显示出来。
    expect(engine.info.httpBase, isEmpty);
    expect(engine.info.port, 0);
    expect(engine.info.token, isEmpty);
  });

  test('查询类接口返回统一的信封数据', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    expect(await engine.getTunnels(), isEmpty);
    expect(await engine.getSshConnections(), isEmpty);
    expect(await engine.getKeys(), isEmpty);
    expect(await engine.getGlobalSettings(), isNotNull);
    expect(await engine.checkAuth(), isTrue);
  });

  test('订阅事件流会先补发一份完整快照', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    // 快照由 Go 在另一个 goroutine 上产生，经原生回调转到本 isolate。
    final snapshot = await engine
        .events()
        .firstWhere((event) => event['type'] == 'snapshot')
        .timeout(const Duration(seconds: 10));

    expect(snapshot['snapshot'], isA<List<dynamic>>());
  });

  // 回归测试：事件字符串的所有权在 Dart 侧，必须在这里释放。
  //
  // Go 侧不再 free，回调又是异步的，因此如果 Dart 漏掉释放会泄漏；
  // 反过来，如果 Go 侧"顺手"释放了，Dart 读到的就是已回收的内存，
  // 解析会失败 —— 表现为事件凭空变少，而不是报错。所以这里断言
  // 收到的事件数量级，并逐条校验能否正常解析。
  test('连续变更产生的事件都能完整送达', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    const cycles = 25;
    final received = <Map<String, dynamic>>[];

    // 先订阅，触发注册回调并补发快照；onListen 只在首个订阅者到来时触发，
    // 因此这个监听器必须最先建立。
    final subscription = engine.events().listen(received.add);
    addTearDown(subscription.cancel);

    final deadline = DateTime.now().add(const Duration(seconds: 10));
    while (received.isEmpty) {
      if (DateTime.now().isAfter(deadline)) {
        fail('订阅后 10 秒内没有收到补发的快照');
      }
      await Future<void>.delayed(const Duration(milliseconds: 20));
    }

    // 每次增删都会推送一次快照，因此期望约 2 × cycles 条事件。
    for (var i = 0; i < cycles; i++) {
      await engine.addTunnel(demoTunnel);
      await engine.deleteTunnel(demoTunnel.name);
    }

    final settleDeadline = DateTime.now().add(const Duration(seconds: 10));
    while (received.where((e) => e['type'] == 'snapshot').length < cycles * 2) {
      if (DateTime.now().isAfter(settleDeadline)) break;
      await Future<void>.delayed(const Duration(milliseconds: 20));
    }

    // 每条事件都必须是能解析的 JSON 对象（解析失败会被静默丢弃）。
    expect(received, isNotEmpty);
    for (final event in received) {
      expect(event['type'], isA<String>());
    }
    final snapshots =
        received.where((e) => e['type'] == 'snapshot').toList();
    expect(
      snapshots.length,
      greaterThanOrEqualTo(cycles),
      reason: '事件丢失通常意味着字符串生命周期没对齐',
    );
    expect(snapshots.last['snapshot'], isA<List<dynamic>>());
  });

  test('关闭后句柄立即失效，重复关闭是安全的', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    engine.close();

    expect(engine.isClosed, isTrue);
    expect(engine.events(), emitsDone);
    // 关闭后再调用应当报错，而不是静默返回或崩溃。
    await expectLater(engine.getTunnels(), throwsA(isA<StateError>()));
    engine.close(); // 幂等
  });

  test('写入的隧道能原样读回', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    await engine.addTunnel(demoTunnel);

    final stored = (await engine.getTunnels()).single;
    expect(stored.name, 'demo');
    expect(stored.localPort, 18080);
    expect(stored.remotePort, 8080);
    expect(stored.sshUser, 'root');
    expect(stored.autoStart, isFalse);

    await engine.deleteTunnel('demo');
    expect(await engine.getTunnels(), isEmpty);
  });

  test('FRP 客户端与代理的增删改查走通 FFI', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    expect(await engine.getFrpClients(), isEmpty);

    await engine.addFrpClient(const FrpClientPayload(
      name: 'demo-frpc',
      group: '测试',
      serverAddr: '127.0.0.1',
      serverPort: 1,
      authToken: 'secret',
      tlsEnable: true,
    ));

    var clients = await engine.getFrpClients();
    expect(clients, hasLength(1));
    expect(clients.single.name, 'demo-frpc');
    expect(clients.single.group, '测试');
    expect(clients.single.serverPort, 1);
    expect(clients.single.authToken, 'secret');
    expect(clients.single.proxies, isEmpty);

    await engine.addFrpProxy('demo-frpc', const FrpProxyPayload(
      name: 'mysql',
      type: 'tcp',
      localIp: '127.0.0.1',
      localPort: 3306,
      remotePort: 13306,
    ));

    clients = await engine.getFrpClients();
    expect(clients.single.proxies, hasLength(1));
    expect(clients.single.proxies.single.localPort, 3306);
    expect(clients.single.proxies.single.remotePort, 13306);
    expect(clients.single.proxies.single.enabled, isTrue);

    // 三参数调用（客户端名 + 原代理名 + 新配置）：改名与改端口
    await engine.updateFrpProxy(
      'demo-frpc',
      'mysql',
      const FrpProxyPayload(
        name: 'mysql-prod',
        type: 'tcp',
        localIp: '127.0.0.1',
        localPort: 3306,
        remotePort: 23306,
      ),
    );

    clients = await engine.getFrpClients();
    expect(clients.single.proxies.single.name, 'mysql-prod');
    expect(clients.single.proxies.single.remotePort, 23306);

    await engine.toggleFrpProxy('demo-frpc', 'mysql-prod', false);
    clients = await engine.getFrpClients();
    expect(clients.single.proxies.single.enabled, isFalse);

    await engine.deleteFrpProxy('demo-frpc', 'mysql-prod');
    expect((await engine.getFrpClients()).single.proxies, isEmpty);

    await engine.deleteFrpClient('demo-frpc');
    expect(await engine.getFrpClients(), isEmpty);
  });

  test('FRP 配置以 frp 原生 TOML 落在工作区目录下', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    await engine.addFrpClient(const FrpClientPayload(
      name: 'file-check',
      group: '文件检查',
      serverAddr: 'frps.example.com',
      serverPort: 7000,
      authToken: 'secret',
    ));
    await engine.addFrpProxy('file-check', const FrpProxyPayload(
      name: 'mysql',
      type: 'tcp',
      localIp: '127.0.0.1',
      localPort: 3306,
      remotePort: 13306,
    ));

    final separator = Platform.pathSeparator;
    final file = File('${tempDir.path}${separator}frp${separator}clients$separator'
        'file-check.toml');
    expect(file.existsSync(), isTrue, reason: '配置文件应位于工作区目录下');

    final content = file.readAsStringSync();
    // 键名必须是 frp 自己的小驼峰写法，这样文件可以直接交给官方 frpc 使用。
    expect(content, contains('serverAddr = '));
    expect(content, contains('[[proxies]]'));
    expect(content, contains('remotePort = 13306'));
    // 我们的元数据走 frp 原生字段，不新增私有段
    expect(content, contains('mgrGroup'));
    // 与 frp 默认值相同的字段会被省略，文件里只留用户改过的部分
    expect(content, isNot(contains('serverPort')));
    expect(content, isNot(contains('protocol = ')));
  });

  test('启动 FRP 客户端后状态可读，停止后回到未运行', () async {
    final engine = await EmbeddedEngine.launch(configPath: configPath());
    addTearDown(engine.close);

    // 指向一个必然连不上的地址：frp 应当保持重试而不是退出
    await engine.addFrpClient(const FrpClientPayload(
      name: 'offline',
      serverAddr: '127.0.0.1',
      serverPort: 1,
    ));

    await engine.startFrpClient('offline');
    await Future<void>.delayed(const Duration(milliseconds: 400));

    var client = (await engine.getFrpClients()).single;
    expect(client.isRunning, isTrue, reason: '服务器不可达时不应自行退出');

    await engine.stopFrpClient('offline');
    client = (await engine.getFrpClients()).single;
    expect(client.isRunning, isFalse);
  });
}
