import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ssh_tunnel_client/pages/update_panel.dart';
import 'package:ssh_tunnel_client/providers.dart';
import 'package:ssh_tunnel_client/services/updates.dart';
import 'package:ssh_tunnel_client/services/workspaces.dart';

class _Workspaces extends WorkspacesNotifier {
  @override
  Future<WorkspacePreferences> build() async => const WorkspacePreferences();
}

class _Updates extends UpdateService {
  bool portable = true;
  bool downloadFails = false;
  int installs = 0;
  final channels = <String>[];

  @override
  Future<UpdateInfo> info() async => UpdateInfo.fromJson({
    'version': 'v1.0.0',
    'repository': 'owner/repo',
    'os': 'windows',
    'arch': 'amd64',
    'installation': {
      'root': r'C:\portable',
      'portable': portable,
      'reason': '安装版请运行安装包。',
    },
  });

  @override
  Future<UpdateCheck> check(String channel) async {
    channels.add(channel);
    return UpdateCheck.fromJson({
      'available': true,
      'current_known': true,
      'release': {
        'version': 'v2.0.0',
        'url': 'https://github.com/owner/repo/releases/tag/v2.0.0',
        'portable': {'name': 'package.zip'},
        'installer': {'name': 'setup.exe'},
      },
    });
  }

  @override
  Future<UpdateDownload> download(String version, String kind) async {
    if (downloadFails) throw StateError('SHA256 mismatch');
    return UpdateDownload.fromJson({
      'path': r'C:\cache\package.zip',
      'sha256': 'a' * 64,
      'version': version,
      'repository': 'owner/repo',
      'kind': kind,
    });
  }

  @override
  Future<void> install(
    UpdateDownload download,
    List<String> paths,
    Future<void> Function() quit,
  ) async {
    installs++;
    await quit();
  }
}

Future<void> _show(
  WidgetTester tester,
  _Updates updates, {
  Future<void> Function()? quit,
}) async {
  tester.view.physicalSize = const Size(1200, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        updateServiceProvider.overrideWithValue(updates),
        workspacesProvider.overrideWith(_Workspaces.new),
        clientProvider.overrideWith(
          (ref) => throw StateError(
            'updates must not depend on the selected daemon',
          ),
        ),
      ],
      child: MaterialApp(
        home: Scaffold(
          body: SingleChildScrollView(
            child: SizedBox(
              width: 720,
              child: UpdatePanel(onQuit: quit ?? () async {}),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

Future<void> _download(WidgetTester tester) async {
  await tester.tap(find.text('检查更新'));
  await tester.pumpAndSettle();
  await tester.tap(find.text('下载并校验便携包'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('检查渠道独立于选中实例，切换渠道清除旧下载', (tester) async {
    final service = _Updates();
    await _show(tester, service);
    await _download(tester);
    expect(service.channels, ['stable']);
    expect(find.text('下载完成，SHA256 校验通过。'), findsOneWidget);
    await tester.tap(find.text('含预发布版'));
    await tester.pumpAndSettle();
    expect(find.text('升级便携版'), findsNothing);
    expect(find.text('下载完成，SHA256 校验通过。'), findsNothing);
    await tester.tap(find.text('检查更新'));
    await tester.pumpAndSettle();
    expect(service.channels, ['stable', 'prerelease']);
  });

  testWidgets('校验失败不允许安装，安装版不提供自动替换', (tester) async {
    final service = _Updates()..downloadFails = true;
    await _show(tester, service);
    await _download(tester);
    expect(find.textContaining('SHA256 mismatch'), findsOneWidget);
    expect(find.text('升级便携版'), findsNothing);
    service.downloadFails = false;
    service.portable = false;
    await _download(tester);
    expect(find.text('下载完成，SHA256 校验通过。'), findsOneWidget);
    expect(find.text('升级便携版'), findsNothing);
    expect(find.textContaining('请解压到新的程序目录'), findsOneWidget);
  });

  testWidgets('升级便携版需要明确确认，取消不退出', (tester) async {
    final service = _Updates();
    var quits = 0;
    await _show(
      tester,
      service,
      quit: () async {
        quits++;
      },
    );
    await _download(tester);
    await tester.tap(find.text('升级便携版'));
    await tester.pumpAndSettle();
    expect(find.textContaining('隧道会暂时中断'), findsOneWidget);
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(service.installs, 0);
    expect(quits, 0);
    await tester.tap(find.text('升级便携版'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('退出并升级'));
    await tester.pumpAndSettle();
    expect(service.installs, 1);
    expect(quits, 1);
  });

  test('退出失败会取消已准备的助手，并保留原始错误', () async {
    final dir = await Directory.systemTemp.createTemp(
      'ssh-tunnel-update-test-',
    );
    addTearDown(() => dir.delete(recursive: true));
    final cancelPath = '${dir.path}${Platform.pathSeparator}cancel';
    final calls = <String>[];
    final service = UpdateService(
      command: (args, {input, timeout = const Duration(minutes: 3)}) async {
        calls.add(args.first);
        if (args.first == 'prepare') {
          expect(input!['client_pid'], pid);
          expect(input['discovery_paths'], ['fixture-discovery.json']);
          expect(input.toString(), isNot(contains('token')));
          return {
            'plan_path': '${dir.path}/plan.json',
            'cancel_path': cancelPath,
          };
        }
        return {'ready': true};
      },
    );
    final download = await _Updates().download('v2.0.0', 'portable');
    await expectLater(
      service.install(download, ['fixture-discovery.json'], () async {
        throw StateError('window shutdown failed');
      }),
      throwsA(
        isA<StateError>().having(
          (e) => e.message,
          'message',
          'window shutdown failed',
        ),
      ),
    );
    expect(calls, ['prepare', 'launch']);
    expect(await File(cancelPath).readAsString(), 'cancel');
  });

  test('启动确认只连接回环地址并发送一次随机令牌', () async {
    final server = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(server.close);
    final received = Completer<String>();
    server.listen((socket) async {
      final data = await socket
          .cast<List<int>>()
          .transform(utf8.decoder)
          .join();
      received.complete(data);
      socket.destroy();
    });
    final token = '0123456789abcdef' * 4;
    await UpdateService.signalReady(
      environment: {
        'SSH_TUNNEL_UPDATE_ADDRESS': '127.0.0.1:${server.port}',
        'SSH_TUNNEL_UPDATE_TOKEN': token,
      },
    );
    expect(await received.future.timeout(const Duration(seconds: 3)), token);
    await UpdateService.signalReady(
      environment: {
        'SSH_TUNNEL_UPDATE_ADDRESS': 'example.com:443',
        'SSH_TUNNEL_UPDATE_TOKEN': token,
      },
    );
  });
}
