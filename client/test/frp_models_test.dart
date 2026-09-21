import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models/frp.dart';

void main() {
  test('客户端解析 JSON 并给出状态文案', () {
    final client = FrpClient.fromJson({
      'name': 'prod',
      'group': '生产',
      'auto_start': false,
      'server_addr': 'frps.example.com',
      'server_port': 7000,
      'auth_method': 'token',
      'auth_token': 'secret',
      'tls_enable': true,
      'proxies': [
        {'name': 'mysql', 'type': 'tcp', 'enabled': true, 'local_port': 3306, 'remote_port': 13306},
        {'name': 'redis', 'type': 'tcp', 'enabled': false, 'local_port': 6379},
      ],
      'running': true,
      'started_at': '2026-09-18T10:00:00Z',
    });

    expect(client.name, 'prod');
    expect(client.group, '生产');
    expect(client.autoStart, isFalse);
    expect(client.serverLabel, 'frps.example.com:7000');
    expect(client.tlsEnable, isTrue);
    expect(client.isRunning, isTrue);
    expect(client.stateLabel, '运行中');
    expect(client.proxies, hasLength(2));
    expect(client.enabledProxyCount, 1);
  });

  test('缺少字段时回落到默认值，不抛异常', () {
    final client = FrpClient.fromJson({'name': 'minimal'});

    expect(client.serverPort, 7000);
    expect(client.authMethod, 'token');
    expect(client.proxies, isEmpty);
    expect(client.isRunning, isFalse);
    expect(client.stateLabel, '已停止');
    expect(client.serverLabel, isEmpty);
  });

  test('配置有误但未运行时给出可读状态', () {
    final client = FrpClient.fromJson({'name': 'broken', 'last_error': '读取配置失败'});
    expect(client.stateLabel, '配置有误');
  });

  test('代理按 frp 的运行阶段给出中文状态', () {
    FrpProxy proxy(String phase, {bool enabled = true, String error = ''}) => FrpProxy.fromJson({
          'name': 'p',
          'type': 'tcp',
          'enabled': enabled,
          'phase': phase,
          'last_error': error,
        });

    expect(proxy('start').stateLabel, '已连接');
    expect(proxy('waiting').stateLabel, '连接中');
    expect(proxy('new').stateLabel, '连接中');
    expect(proxy('closed').stateLabel, '已断开');
    expect(proxy('').stateLabel, '未运行');
    expect(proxy('start', enabled: false).stateLabel, '已停用');
    expect(proxy('start', error: '端口被占用').stateLabel, '失败');
  });

  test('代理给出可读的本地与远端地址', () {
    final proxy = FrpProxy.fromJson({
      'name': 'mysql',
      'type': 'tcp',
      'local_ip': '127.0.0.1',
      'local_port': 3306,
      'remote_addr': 'frps.example.com:13306',
    });

    expect(proxy.localLabel, '127.0.0.1:3306');
    expect(proxy.typeLabel, 'TCP');
  });

  test('访问端类型不可编辑，但能显示为中文类型名', () {
    final visitor = FrpProxy.fromJson({'name': 'peer', 'type': 'stcp', 'editable': false});

    expect(visitor.editable, isFalse);
    expect(visitor.typeLabel, 'STCP(访问端)');
  });

  test('代理提交结构包含启用状态与端口', () {
    const payload = FrpProxyPayload(
      name: 'web',
      type: 'http',
      enabled: false,
      localIp: '127.0.0.1',
      localPort: 8080,
      customDomains: ['a.example.com'],
      subdomain: 'a',
    );

    expect(payload.toJson(), {
      'name': 'web',
      'type': 'http',
      'enabled': false,
      'local_ip': '127.0.0.1',
      'local_port': 8080,
      'remote_port': 0,
      'custom_domains': ['a.example.com'],
      'subdomain': 'a',
    });
  });

  test('客户端提交结构省略空分组', () {
    const payload = FrpClientPayload(
      name: 'prod',
      serverAddr: 'frps.example.com',
      serverPort: 7000,
      authToken: 'secret',
    );

    final json = payload.toJson();
    expect(json.containsKey('group'), isFalse);
    expect(json['auto_start'], isTrue);
    expect(json['server_addr'], 'frps.example.com');
    expect(json['tls_enable'], isTrue);
  });

  test('可创建的代理类型只包含阶段 1 支持的五种', () {
    expect(frpProxyTypes.keys, containsAll(['tcp', 'udp', 'http', 'https', 'tcpmux']));
    expect(frpProxyTypes.keys, isNot(contains('stcp')));
  });
}
