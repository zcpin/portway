import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/models.dart';
import 'package:ssh_tunnel_client/models/connection_preferences.dart';
import 'package:ssh_tunnel_client/services/daemon_client.dart';
import 'package:ssh_tunnel_client/services/external_connections.dart';

Tunnel _tunnel(
  String state, {
  int port = 15432,
  String host = '127.0.0.1',
  String mode = 'local',
}) => Tunnel.fromJson({
  'name': 'database',
  'mode': mode,
  'state': state,
  'local_host': host,
  'local_port': port,
  'is_running': state == 'connected' || state == 'connecting',
  'desired_running': state != 'stopped',
});

class _OpenClient extends DaemonClient {
  _OpenClient(this.snapshots)
    : super(
        const DaemonInfo(
          host: '127.0.0.1',
          port: 1,
          token: '',
          pid: 1,
          version: '',
          configPath: '',
        ),
      );
  final List<Tunnel> snapshots;
  int polls = 0;
  int starts = 0;
  Completer<List<Tunnel>>? pending;
  @override
  Future<List<Tunnel>> getTunnels() async {
    final index = polls++;
    if (pending != null) return pending!.future;
    return [snapshots[index < snapshots.length ? index : snapshots.length - 1]];
  }

  @override
  Future<List<BatchResult>> batchTunnels(
    String action,
    List<String> names,
  ) async {
    expect(action, 'start');
    expect(names, ['database']);
    starts++;
    return const [BatchResult(name: 'database', ok: true)];
  }
}

void main() {
  test('duplicate open requests share one pending launch', () async {
    final client = _OpenClient([])..pending = Completer<List<Tunnel>>();
    addTearDown(client.close);
    var opened = 0;
    final launcher = ExternalConnectionLauncher(
      openUrl: (_) async {
        opened++;
        return true;
      },
    );
    final action = ConnectionOpenAction(
      kind: 'url',
      target: 'http://{host}:{port}',
    );
    final first = launcher.open(
      client,
      'database',
      action,
      stillCurrent: () => true,
    );
    final second = launcher.open(
      client,
      'database',
      action,
      stillCurrent: () => true,
    );
    expect(identical(first, second), isTrue);
    client.pending!.complete([_tunnel('connected')]);
    await Future.wait([first, second]);
    expect(opened, 1);
    expect(client.polls, 1);
  });

  test(
    'closing the owning client cancels a pending launch even after the row is gone',
    () async {
      final client = _OpenClient([])..pending = Completer<List<Tunnel>>();
      var opened = false;
      final future =
          ExternalConnectionLauncher(
            openUrl: (_) async {
              opened = true;
              return true;
            },
          ).open(
            client,
            'database',
            ConnectionOpenAction(kind: 'url', target: 'http://{host}:{port}'),
            stillCurrent: () => true,
          );
      client.close();
      client.pending!.complete([_tunnel('connected')]);
      await expectLater(future, throwsStateError);
      expect(opened, isFalse);
    },
  );

  test('waiting for readiness expires without opening the service', () async {
    final client = _OpenClient([_tunnel('connecting')]);
    addTearDown(client.close);
    var opened = false;
    final launcher = ExternalConnectionLauncher(
      connectionTimeout: const Duration(milliseconds: 50),
      openUrl: (_) async {
        opened = true;
        return true;
      },
    );
    await expectLater(
      launcher.open(
        client,
        'database',
        ConnectionOpenAction(kind: 'url', target: 'http://{host}:{port}'),
        stillCurrent: () => true,
      ),
      throwsStateError,
    );
    expect(opened, isFalse);
  });

  test(
    'opening starts a stopped tunnel and waits for the current endpoint',
    () async {
      final client = _OpenClient([
        _tunnel('stopped'),
        _tunnel('connecting'),
        _tunnel('connected', port: 25432),
      ]);
      addTearDown(client.close);
      final opened = <Uri>[];
      final launcher = ExternalConnectionLauncher(
        openUrl: (uri) async {
          opened.add(uri);
          return true;
        },
      );
      await launcher.open(
        client,
        'database',
        ConnectionOpenAction(kind: 'url', target: 'http://{host}:{port}/admin'),
        stillCurrent: () => true,
      );
      expect(client.starts, 1);
      expect(client.polls, 3);
      expect(opened.single.toString(), 'http://127.0.0.1:25432/admin');
    },
  );

  test(
    'program arguments retain spaces and shell syntax as literal arguments',
    () async {
      final root = await Directory.systemTemp.createTemp('ssh-open-program-');
      final program = File(
        '${root.path}${Platform.pathSeparator}database client.exe',
      );
      await program.writeAsString('test fixture');
      final client = _OpenClient([_tunnel('connected')]);
      addTearDown(client.close);
      String? executable;
      List<String>? arguments;
      final launcher = ExternalConnectionLauncher(
        openProgram: (path, args) async {
          executable = path;
          arguments = args;
        },
      );
      await launcher.open(
        client,
        'database',
        ConnectionOpenAction(
          kind: 'program',
          target: program.path,
          arguments: [
            '--host={host}',
            '--port={port}',
            'two words',
            r'$(echo literal)',
          ],
        ),
        stillCurrent: () => true,
      );
      expect(executable, program.path);
      expect(arguments, [
        '--host=127.0.0.1',
        '--port=15432',
        'two words',
        r'$(echo literal)',
      ]);
      expect(client.starts, 0);
    },
  );

  test(
    'IPv6 URL templates and invalid schemes are handled before any start',
    () async {
      final action = ConnectionOpenAction(
        kind: 'url',
        target: 'http://{host}:{port}/',
      );
      expect(
        action.urlFor(_tunnel('connected', host: '::1')).toString(),
        'http://[::1]:15432/',
      );
      for (final target in [
        'javascript:alert(1)',
        'file:///tmp/test',
        'https://user:password@example.test',
        'http://{unknown}:80',
      ]) {
        final client = _OpenClient([_tunnel('stopped')]);
        addTearDown(client.close);
        await expectLater(
          ExternalConnectionLauncher().open(
            client,
            'database',
            ConnectionOpenAction(kind: 'url', target: target),
            stillCurrent: () => true,
          ),
          throwsFormatException,
        );
        expect(client.polls, 0);
      }
    },
  );

  test(
    'switching instances during the wait prevents external launch',
    () async {
      final client = _OpenClient([])..pending = Completer<List<Tunnel>>();
      addTearDown(client.close);
      var current = true;
      var opened = false;
      final launcher = ExternalConnectionLauncher(
        openUrl: (_) async {
          opened = true;
          return true;
        },
      );
      final result = launcher.open(
        client,
        'database',
        ConnectionOpenAction(kind: 'url', target: 'http://{host}:{port}'),
        stillCurrent: () => current,
      );
      current = false;
      client.pending!.complete([_tunnel('connected')]);
      await expectLater(result, throwsStateError);
      expect(opened, isFalse);
      expect(client.starts, 0);
    },
  );

  test(
    'manual stop and unsupported modes never launch an external application',
    () async {
      for (final states in [
        [_tunnel('connecting'), _tunnel('stopped')],
        [_tunnel('connected', mode: 'remote')],
      ]) {
        final client = _OpenClient(states);
        addTearDown(client.close);
        var opened = false;
        await expectLater(
          ExternalConnectionLauncher(
            openUrl: (_) async {
              opened = true;
              return true;
            },
          ).open(
            client,
            'database',
            ConnectionOpenAction(kind: 'url', target: 'http://{host}:{port}'),
            stillCurrent: () => true,
          ),
          throwsStateError,
        );
        expect(opened, isFalse);
        expect(client.starts, 0);
      }
    },
  );

  test('a missing executable is rejected before starting the tunnel', () async {
    final root = await Directory.systemTemp.createTemp('ssh-missing-program-');
    final client = _OpenClient([_tunnel('stopped')]);
    addTearDown(client.close);
    await expectLater(
      ExternalConnectionLauncher().open(
        client,
        'database',
        ConnectionOpenAction(
          kind: 'program',
          target: '${root.path}${Platform.pathSeparator}missing.exe',
        ),
        stillCurrent: () => true,
      ),
      throwsStateError,
    );
    expect(client.polls, 0);
  });
}
