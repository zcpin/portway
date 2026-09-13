import 'dart:io';

import 'package:url_launcher/url_launcher.dart';

import '../models/connection_preferences.dart';
import 'daemon_client.dart';

typedef ProgramOpener =
    Future<void> Function(String executable, List<String> arguments);

class ExternalConnectionLauncher {
  ExternalConnectionLauncher({
    Future<bool> Function(Uri)? openUrl,
    ProgramOpener? openProgram,
    this.connectionTimeout = const Duration(seconds: 15),
  }) : _openUrl =
           openUrl ??
           ((uri) => launchUrl(uri, mode: LaunchMode.externalApplication)),
       _openProgram = openProgram ?? _startProgram;

  final Future<bool> Function(Uri) _openUrl;
  final ProgramOpener _openProgram;
  final Duration connectionTimeout;
  final _pending = <(DaemonClient, String), Future<void>>{};

  static Future<void> _startProgram(
    String executable,
    List<String> arguments,
  ) async {
    if (Platform.isMacOS && executable.toLowerCase().endsWith('.app')) {
      await Process.start(
        '/usr/bin/open',
        ['-a', executable, '--args', ...arguments],
        mode: ProcessStartMode.detached,
        runInShell: false,
      );
    } else {
      await Process.start(
        executable,
        arguments,
        mode: ProcessStartMode.detached,
        runInShell: false,
      );
    }
  }

  Future<void> open(
    DaemonClient client,
    String name,
    ConnectionOpenAction action, {
    required bool Function() stillCurrent,
  }) {
    if (client.isClosed || !stillCurrent()) {
      return Future.error(StateError('实例已切换，已取消打开'));
    }
    final key = (client, name);
    final current = _pending[key];
    if (current != null) return current;
    final future = _open(client, name, action, stillCurrent: stillCurrent);
    _pending[key] = future;
    void finished() {
      if (identical(_pending[key], future)) _pending.remove(key);
    }

    future.then<void>(
      (_) => finished(),
      onError: (Object _, StackTrace _) => finished(),
    );
    return future;
  }

  Future<void> _open(
    DaemonClient client,
    String name,
    ConnectionOpenAction action, {
    required bool Function() stillCurrent,
  }) async {
    action.validate();
    if (action.kind == 'program') {
      final exists =
          Platform.isMacOS && action.target.toLowerCase().endsWith('.app')
          ? await Directory(action.target).exists()
          : await File(action.target).exists();
      if (!exists) throw StateError('程序不存在，请重新配置打开方式');
    }
    void checkCurrent() {
      if (client.isClosed || !stillCurrent()) throw StateError('实例已切换，已取消打开');
    }

    final deadline = DateTime.now().add(connectionTimeout);
    var started = false;
    while (true) {
      checkCurrent();
      final rows = await client.getTunnels();
      checkCurrent();
      if (DateTime.now().isAfter(deadline)) {
        throw StateError('等待连接就绪超时，请稍后重试打开');
      }
      final found = rows.where((t) => t.name == name);
      if (found.isEmpty) throw StateError('隧道已不存在');
      final tunnel = found.first;
      if (tunnel.mode.isNotEmpty && tunnel.mode != 'local') {
        throw StateError('外部打开仅支持本地端口转发');
      }
      if (tunnel.state == 'connected') {
        checkCurrent();
        if (action.kind == 'url') {
          if (!await _openUrl(action.urlFor(tunnel))) {
            throw StateError('系统无法打开此网址');
          }
        } else {
          await _openProgram(action.target, action.argumentsFor(tunnel));
        }
        return;
      }
      if (!started && !tunnel.isRunning) {
        final results = await client.batchTunnels('start', [name]);
        checkCurrent();
        if (results.length != 1 || !results.single.ok) {
          throw StateError(results.isEmpty ? '启动隧道失败' : results.first.error);
        }
        started = true;
      } else {
        started = true;
        if (!tunnel.desiredRunning || tunnel.state == 'failed') {
          throw StateError(
            tunnel.lastError.isNotEmpty ? tunnel.lastError : '隧道已停止，已取消打开',
          );
        }
      }
      await Future<void>.delayed(const Duration(milliseconds: 250));
    }
  }
}
