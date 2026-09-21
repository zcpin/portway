import 'package:flutter_test/flutter_test.dart';
import 'package:portway/models.dart';

void main() {
  test('daemon 明确运行方式覆盖目录推断并保留可执行路径', () {
    final info = DaemonInfo.fromJson({
      'host': '127.0.0.1',
      'port': 1234,
      'service_mode': false,
      'executable_path': 'fixture-daemon',
    }).withDiscovery(path: 'custom/daemon.json', shared: true);
    expect(info.isShared, isFalse);
    expect(info.executablePath, 'fixture-daemon');
  });
  test('转发类型、本机目标与跳板链在状态更新和复制时保留', () {
    final tunnel = Tunnel.fromJson({
      'name': 'reverse',
      'mode': 'remote',
      'local_host': 'localhost',
      'local_port': 5432,
      'remote_host': '127.0.0.1',
      'remote_port': 15432,
      'proxy_jump': ['jump-a', 'jump-b'],
    });
    expect(tunnel.copyWith(isRunning: true).toJson(), tunnel.toJson());
    final copy = tunnel.duplicateAs('copy', remotePort: 15433);
    expect(copy.localPort, 5432);
    expect(copy.remotePort, 15433);
    expect(copy.modeLabel, '反向转发');
    expect(copy.proxyJump, ['jump-a', 'jump-b']);
  });
  test('agent 认证与密钥解锁状态保留', () {
    final json = {
      'name': 'agent',
      'host': 'example:22',
      'user': 'alice',
      'key_file': '',
      'auth_method': 'agent',
      'agent_socket': 'fixture-agent',
    };
    expect(SshConnection.fromJson(json).toJson(), json);
    final key = KeyInfo.fromJson({'encrypted': true, 'unlocked': true});
    expect(key.encrypted, isTrue);
    expect(key.unlocked, isTrue);
  });
  test('复制配置保留分组、自动启动和认证字段，重置运行状态', () {
    final original = Tunnel.fromJson({
      'name': 'db',
      'group': 'prod',
      'auto_start': false,
      'local_port': 15432,
      'key_file': 'key',
      'known_hosts_file': 'hosts',
      'is_running': true,
      'state': 'connected',
      'retry_count': 4,
    });
    final copy = original.duplicateAs('db-copy', localPort: 15433);
    expect(copy.toJson(), {
      ...original.toJson(),
      'name': 'db-copy',
      'local_port': 15433,
    });
    expect(copy.isRunning, isFalse);
    expect(copy.retryCount, 0);
    expect(copy.autoStart, isFalse);
    expect(Tunnel.fromJson({}).autoStart, isTrue);
  });
  test('运行状态独立于配置，更新后保留认证和错误字段', () {
    final tunnel = Tunnel.fromJson({
      'name': 'db',
      'key_file': 'key',
      'is_running': true,
      'state': 'reconnecting',
      'last_error': 'unreachable',
      'retry_count': 2,
    });
    expect(tunnel.stateLabel, '重连中');
    expect(tunnel.copyWith(isRunning: true).lastError, 'unreachable');
    final connected = tunnel.withRuntime({
      'state': 'connected',
      'last_error': '',
      'connected_at': '2026-09-12T00:00:00Z',
      'retry_count': 2,
      'is_running': true,
    });
    expect(connected.stateLabel, '已连接');
    expect(connected.keyFile, 'key');
    expect(connected.lastError, isEmpty);
    expect(connected.toJson().containsKey('state'), isFalse);
  });
  test('SSH 连接往返保留主机校验配置', () {
    final original = <String, dynamic>{
      'name': 'test',
      'host': 'example.com:22',
      'user': 'tester',
      'key_file': 'keys/id_ed25519',
      'host_key_check': 'insecure',
      'known_hosts_file': 'keys/custom_hosts',
    };
    expect(SshConnection.fromJson(original).toJson(), original);
  });

  test('直连隧道往返及状态更新保留认证字段和全局继承', () {
    final original = <String, dynamic>{
      'name': 'direct',
      'local_port': 13306,
      'remote_host': '127.0.0.1',
      'remote_port': 3306,
      'ssh_host': 'example.com:22',
      'ssh_user': 'tester',
      'key_file': 'keys/id_ed25519',
      'host_key_check': 'known_hosts',
      'known_hosts_file': 'keys/custom_hosts',
      'reconnect_strategy': '',
      'reconnect_interval': '',
      'max_reconnect_attempts': 0,
    };
    final tunnel = Tunnel.fromJson(original);
    expect(tunnel.toJson(), original);
    expect(tunnel.copyWith(isRunning: true).toJson(), original);
    expect(Tunnel.fromJson(const {}).reconnectInterval, isEmpty);
  });

  test('发现地址支持 IPv4、IPv6 和主机名', () {
    for (final host in ['127.0.0.1', '::1', 'localhost']) {
      final info = DaemonInfo.fromJson({'host': host, 'port': 54483});
      for (final url in [info.httpBase, info.wsBase]) {
        final uri = Uri.parse(url);
        expect(uri.host, host);
        expect(uri.port, 54483);
      }
      if (host == '::1') {
        expect(info.httpBase, 'http://[::1]:54483');
        expect(info.wsBase, 'ws://[::1]:54483');
      }
    }
  });

  test('GlobalSettings 序列化往返一致', () {
    final s = GlobalSettings.fromJson({
      'log_level': 'debug',
      'reconnect_strategy': 'exponential',
      'reconnect_interval': '30s',
      'max_reconnect_attempts': 4,
    });
    expect(s.logLevel, 'debug');
    expect(s.reconnectStrategy, 'exponential');
    expect(s.reconnectInterval, '30s');
    expect(s.maxReconnectAttempts, 4);

    final round = GlobalSettings.fromJson(s.toJson());
    expect(round.logLevel, 'debug');
    expect(round.reconnectStrategy, 'exponential');
    expect(round.reconnectInterval, '30s');
    expect(round.maxReconnectAttempts, 4);
  });

  test('GlobalSettings 缺省字段使用默认值', () {
    final s = GlobalSettings.fromJson(const {});
    expect(s.logLevel, '');
    expect(s.reconnectStrategy, 'fixed');
    expect(s.reconnectInterval, '5s');
    expect(s.maxReconnectAttempts, 0);
  });

  test('GlobalSettings.copyWith 只改指定字段', () {
    const s = GlobalSettings(
      logLevel: 'info',
      reconnectStrategy: 'fixed',
      reconnectInterval: '5s',
      maxReconnectAttempts: 0,
    );
    final changed = s.copyWith(reconnectStrategy: 'exponential');
    expect(changed.reconnectStrategy, 'exponential');
    expect(changed.logLevel, 'info');
    expect(changed.reconnectInterval, '5s');
    expect(changed.maxReconnectAttempts, 0);
  });
}
