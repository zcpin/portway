import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';

void main() {
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
