import '../models.dart';

class RecoveryAlert {
  final String name;
  final bool recovered;
  const RecoveryAlert(this.name, {this.recovered = false});
  String get title => recovered ? 'SSH 隧道已恢复' : 'SSH 隧道连接失败';
  String get body => recovered ? '$name 已重新连接。' : '$name 连续连接失败，点击查看详情。';
}

class _Outage {
  int lastRetries = 0;
  int failures = 0;
  bool notified = false;
}

/// Count a continuous outage, including retry counters reset by network recovery.
class RecoveryAlerts {
  final _instances = <String, Map<String, _Outage>>{};

  List<RecoveryAlert> update(String instance, List<Tunnel> tunnels) {
    final states = _instances.remove(instance) ?? <String, _Outage>{};
    _instances[instance] = states;
    if (_instances.length > 16) _instances.remove(_instances.keys.first);
    final names = tunnels.map((t) => t.name).toSet();
    states.removeWhere((name, _) => !names.contains(name));
    final alerts = <RecoveryAlert>[];
    for (final tunnel in tunnels) {
      if (!tunnel.desiredRunning || tunnel.state == 'stopped') {
        states.remove(tunnel.name);
        continue;
      }
      final outage = states.putIfAbsent(tunnel.name, _Outage.new);
      if (tunnel.state == 'connected') {
        if (outage.notified) {
          alerts.add(RecoveryAlert(tunnel.name, recovered: true));
        }
        outage.lastRetries = tunnel.retryCount;
        outage.failures = 0;
        outage.notified = false;
        continue;
      }
      if (tunnel.state != 'reconnecting' && tunnel.state != 'failed') continue;
      outage.failures += tunnel.retryCount >= outage.lastRetries
          ? tunnel.retryCount - outage.lastRetries
          : tunnel.retryCount;
      outage.lastRetries = tunnel.retryCount;
      if (!outage.notified &&
          (outage.failures >= 3 || tunnel.state == 'failed')) {
        outage.notified = true;
        alerts.add(RecoveryAlert(tunnel.name));
      }
    }
    return alerts;
  }
}
