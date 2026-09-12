import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'daemon_launcher.dart';

final updateServiceProvider = Provider<UpdateService>((ref) => UpdateService());

typedef UpdateCommand =
    Future<Map<String, dynamic>> Function(
      List<String> arguments, {
      Map<String, dynamic>? input,
      Duration timeout,
    });

class UpdateInfo {
  final String version, repository, platform, arch, root, reason;
  final bool portable;
  final Map<String, dynamic>? lastResult;
  UpdateInfo.fromJson(Map<String, dynamic> json)
    : version = json['version'] as String,
      repository = json['repository'] as String,
      platform = json['os'] as String,
      arch = json['arch'] as String,
      root = json['installation']['root'] as String,
      reason = json['installation']['reason'] as String,
      portable = json['installation']['portable'] as bool,
      lastResult = (json['last_result'] as Map?)?.cast<String, dynamic>();
}

class UpdateCheck {
  final bool available, currentKnown;
  final Map<String, dynamic>? release;
  UpdateCheck.fromJson(Map<String, dynamic> json)
    : available = json['available'] as bool,
      currentKnown = json['current_known'] as bool,
      release = (json['release'] as Map?)?.cast<String, dynamic>();
  String get version => release?['version'] as String? ?? '';
  bool get hasPortable => release?['portable'] != null;
  bool get hasInstaller => release?['installer'] != null;
}

class UpdateDownload {
  final String path, sha256, version, repository, kind;
  UpdateDownload.fromJson(Map<String, dynamic> json)
    : path = json['path'] as String,
      sha256 = json['sha256'] as String,
      version = json['version'] as String,
      repository = json['repository'] as String,
      kind = json['kind'] as String;
  Map<String, dynamic> toJson() => {
    'path': path,
    'sha256': sha256,
    'version': version,
    'repository': repository,
    'kind': kind,
  };
}

/// Uses the local bundled executable, independently of the selected daemon.
/// No shell is involved and no daemon token is sent to GitHub or a child process.
class UpdateService {
  UpdateService({UpdateCommand? command}) : _command = command ?? _run;
  final UpdateCommand _command;

  Future<UpdateInfo> info() async =>
      UpdateInfo.fromJson(await _command(['info']));

  Future<UpdateCheck> check(String channel) async =>
      UpdateCheck.fromJson(await _command(['check', '-channel', channel]));

  Future<UpdateDownload> download(String version, String kind) async =>
      UpdateDownload.fromJson(
        await _command([
          'download',
          '-tag',
          version,
          '-kind',
          kind,
        ], timeout: const Duration(minutes: 12)),
      );

  Future<void> install(
    UpdateDownload download,
    List<String> discoveryPaths,
    Future<void> Function() quit,
  ) async {
    Map<String, dynamic>? prepared;
    try {
      await DaemonLauncher.pauseForUpdate();
      prepared = await _command(
        ['prepare'],
        input: {
          'download': download.toJson(),
          'client_pid': pid,
          'client_executable': Platform.resolvedExecutable,
          'discovery_paths': discoveryPaths,
        },
      );
      await _command(['launch', '-plan', prepared['plan_path'] as String]);
      await quit();
    } catch (_) {
      if (prepared != null) {
        // The helper waits for this client to exit before stopping any daemon.
        try {
          await File(prepared['cancel_path'] as String).writeAsString('cancel');
        } catch (_) {
          // The helper still times out without changing the running installation.
        }
      }
      rethrow;
    } finally {
      // In production a successful quit terminates this process. On errors or
      // injected test exits, discovery must be able to launch daemons again.
      DaemonLauncher.resumeAfterUpdate();
    }
  }

  Future<void> showDownload(UpdateDownload download) async {
    final directory = File(download.path).parent.path;
    final command = Platform.isWindows
        ? 'explorer.exe'
        : Platform.isMacOS
        ? '/usr/bin/open'
        : 'xdg-open';
    await Process.start(command, [directory], mode: ProcessStartMode.detached);
  }

  static Future<Map<String, dynamic>> _run(
    List<String> arguments, {
    Map<String, dynamic>? input,
    Duration timeout = const Duration(minutes: 3),
  }) async {
    final executable = DaemonLauncher.bundledDaemonPath;
    if (executable == null) {
      throw StateError('未找到随客户端附带的更新工具，请使用完整发布包。');
    }
    final process = await Process.start(executable, ['update', ...arguments]);
    final stdout = process.stdout.transform(utf8.decoder).join();
    final stderr = process.stderr.transform(utf8.decoder).join();
    try {
      if (input != null) process.stdin.write(jsonEncode(input));
      await process.stdin.close();
      final values = await Future.wait<Object>([
        process.exitCode,
        stdout,
        stderr,
      ]).timeout(timeout);
      if (values[0] != 0) {
        final error = (values[2] as String).trim();
        throw StateError(error.isEmpty ? '更新工具执行失败' : error);
      }
      return (jsonDecode(values[1] as String) as Map).cast<String, dynamic>();
    } catch (_) {
      process.kill();
      rethrow;
    }
  }

  /// Called after the first frame. A random token acknowledges successful UI
  /// startup to the helper; absent variables make ordinary launches a no-op.
  static Future<void> signalReady({Map<String, String>? environment}) async {
    final env = environment ?? Platform.environment;
    final address = env['SSH_TUNNEL_UPDATE_ADDRESS'];
    final token = env['SSH_TUNNEL_UPDATE_TOKEN'];
    if (address == null ||
        token == null ||
        !RegExp(r'^[0-9a-f]{64}$').hasMatch(token)) {
      return;
    }
    final match = RegExp(r'^127\.0\.0\.1:([0-9]{1,5})$').firstMatch(address);
    final port = match == null ? 0 : int.parse(match[1]!);
    if (port <= 0 || port > 65535) return;
    Socket? socket;
    try {
      socket = await Socket.connect(
        InternetAddress.loopbackIPv4,
        port,
        timeout: const Duration(seconds: 3),
      );
      socket.write(token);
      await socket.flush();
      await socket.close();
    } catch (_) {
      // A stale helper must not prevent the application from opening.
    } finally {
      socket?.destroy();
    }
  }
}
