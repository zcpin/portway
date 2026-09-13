import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:window_manager/window_manager.dart';

import 'models.dart';
import 'connection_preferences_provider.dart';
import 'providers.dart';
import 'widgets.dart';
import 'services/daemon_client.dart';
import 'services/daemon_discovery.dart';
import 'services/desktop_notifications.dart';
import 'services/recovery_alerts.dart';
import 'services/settings_store.dart';
import 'services/tray.dart';
import 'services/updates.dart';
import 'pages/about_page.dart';
import 'pages/connections_page.dart';
import 'pages/keys_page.dart';
import 'pages/logs_page.dart';
import 'pages/settings_page.dart';
import 'pages/tunnels_page.dart';
import 'pages/workspace_switcher.dart';

/// 强制本机回环请求不走系统代理。
///
/// 部分环境配置了全局 HTTP 代理，会拦截发往 127.0.0.1 的请求：
/// REST 请求被转发到代理服务器，WebSocket 升级则会失败（HTTP 426）。
/// daemon 只监听回环地址，因此这里统一绕过代理。
class _DirectConnectionOverrides extends HttpOverrides {
  @override
  HttpClient createHttpClient(SecurityContext? context) {
    final client = super.createHttpClient(context);
    client.findProxy = (_) => 'DIRECT';
    return client;
  }
}

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  HttpOverrides.global = _DirectConnectionOverrides();
  await windowManager.ensureInitialized();

  const windowOptions = WindowOptions(
    size: Size(1180, 760),
    minimumSize: Size(960, 640),
    center: true,
    title: 'SSH 隧道管理器',
  );

  await windowManager.waitUntilReadyToShow(windowOptions, () async {
    await windowManager.show();
    await windowManager.focus();
  });

  // 关闭窗口时收进托盘而不是退出，由 WindowListener 拦截关闭事件
  await windowManager.setPreventClose(true);

  runApp(const ProviderScope(child: SshTunnelApp()));
  WidgetsBinding.instance.addPostFrameCallback((_) { UpdateService.signalReady(); });
}

class SshTunnelApp extends StatelessWidget {
  const SshTunnelApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'SSH 隧道管理器',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        useMaterial3: true,
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF3D7EFF),
          brightness: Brightness.light,
        ),
      ),
      darkTheme: ThemeData(
        useMaterial3: true,
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF3D7EFF),
          brightness: Brightness.dark,
        ),
      ),
      home: const HomeScreen(),
    );
  }
}

class HomeScreen extends ConsumerStatefulWidget {
  const HomeScreen({super.key});

  @override
  ConsumerState<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends ConsumerState<HomeScreen> with WindowListener {
  int _selected = 0;

  late final AppTray _tray;
  final _recoveryAlerts = RecoveryAlerts();
  final _notifications = DesktopNotifications();

  static const _destinations = [
    (icon: Icons.swap_horiz_outlined, label: '隧道'),
    (icon: Icons.dns_outlined, label: 'SSH 连接'),
    (icon: Icons.key_outlined, label: '密钥'),
    (icon: Icons.terminal_outlined, label: '日志'),
    (icon: Icons.settings_outlined, label: '设置'),
    (icon: Icons.info_outline, label: '关于'),
  ];

  /// 「设置」页在导航中的下标；其后的页面（设置、关于）不依赖 daemon，断开时也能访问。
  static const _settingsIndex = 4;

  /// 导航对应的页面。设置 / 关于无需连接 daemon 即可渲染。
  List<Widget> get _pages => [
    TunnelsPage(active: _selected == 0),
    const ConnectionsPage(),
    const KeysPage(),
    const LogsPage(),
    SettingsPage(onQuitForUpdate: _quit),
    const AboutPage(),
  ];

  @override
  void initState() {
    super.initState();
    windowManager.addListener(this);

    _tray = AppTray(
      onShowWindow: _showWindow,
      onQuit: _quit,
      onOpenConnection: (client, name, action) async {
        await ref.read(externalConnectionLauncherProvider).open(client, name, action,
          stillCurrent: () => mounted && !ref.read(clientProvider).isLoading &&
            identical(ref.read(clientProvider).valueOrNull, client));
      },
      isCurrentClient: (client) => mounted && !ref.read(clientProvider).isLoading &&
          identical(ref.read(clientProvider).valueOrNull, client),
      onTunnelsChanged: (client) async {
        if (mounted && identical(ref.read(clientProvider).valueOrNull, client)) {
          await ref.read(tunnelsProvider.notifier).refresh();
        }
      },
      onError: (error) async {
        if (!mounted) return;
        await _showWindow();
        if (mounted) showErrorSnack(context, error);
      },
    );
    _tray.init();

    // 提前加载本地偏好（关闭动作），首次关闭窗口前确保可用
    SettingsStore.instance.load();

    // 首帧后再拉历史日志，避免拖慢启动
    WidgetsBinding.instance.addPostFrameCallback((_) {
      ref.read(logsProvider.notifier).loadHistory();
    });
  }

  @override
  void dispose() {
    windowManager.removeListener(this);
    _notifications.dispose();
    _tray.dispose();
    super.dispose();
  }

  /// 关闭窗口时收进系统托盘，让程序继续在后台运行。
  ///
  /// 默认弹出确认对话框；用户可在「设置」页把默认动作改为直接收进托盘
  /// 或直接退出。隧道由 daemon 维持，客户端窗口关掉不影响隧道；
  /// 真正退出只能走关闭确认里的「退出程序」或托盘菜单的「退出」。
  @override
  void onWindowClose() async {
    await SettingsStore.instance.load();
    switch (SettingsStore.instance.closeAction) {
      case CloseAction.quit:
        await _quit();
      case CloseAction.tray:
        await windowManager.hide();
        await _tray.init(); // 确保托盘图标仍在（可能被系统清理）
      case CloseAction.ask:
        final minimize = await _confirmClose();
        if (!minimize) return;
        await windowManager.hide();
        await _tray.init();
    }
  }

  /// 询问用户是收进托盘还是退出，返回 true 表示收进托盘。
  Future<bool> _confirmClose() async {
    if (!mounted) return true;

    final result = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('关闭窗口'),
        content: const Text(
          '收进系统托盘后程序继续在后台运行，隧道不受影响。\n'
          '选择「退出程序」则完全关闭客户端。',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, 'cancel'),
            child: const Text('取消'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, 'quit'),
            child: const Text('退出程序'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, 'tray'),
            child: const Text('收进托盘'),
          ),
        ],
      ),
    );

    if (result == 'quit') {
      await _quit();
      return false;
    }
    return result == 'tray';
  }

  Future<void> _showWindow() async {
    await windowManager.show();
    await windowManager.focus();
  }

  /// 真正退出：先销毁托盘图标，再关闭窗口并结束进程。
  Future<void> _quit() async {
    await _notifications.dispose();
    await _tray.dispose();
    await windowManager.setPreventClose(false);
    await windowManager.destroy();
    exit(0);
  }

  @override
  Widget build(BuildContext context) {
    // 把 daemon 推送的事件分发到对应状态：状态变化更新隧道行，
    // 日志追加进日志缓冲。
    ref.listen(eventStreamProvider, (previous, next) {
      next.whenData((event) {
        switch (event['type']) {
          case 'snapshot':
            final snapshot = event['snapshot'];
            if (snapshot is List) {
              ref.read(tunnelsProvider.notifier).applySnapshot([
                for (final row in snapshot)
                  Tunnel.fromJson((row as Map).cast<String, dynamic>()),
              ]);
            }
          case 'status':
            final status = event['status'];
            if (status is Map) {
              ref
                  .read(tunnelsProvider.notifier)
                  .applyStatus(status.cast<String, dynamic>());
            }
          case 'runtime':
            final runtime = event['runtime'];
            if (runtime is Map) {
              ref.read(tunnelsProvider.notifier).applyRuntime(runtime.cast<String, dynamic>());
            }
          case 'log':
            final log = event['log'];
            if (log is Map) {
              ref
                  .read(logsProvider.notifier)
                  .add(LogEntry.fromJson(log.cast<String, dynamic>()));
            }
        }
      });
    });

    ref.listen(clientProvider, (previous, next) {
      _updateTray();
      ref.read(logsProvider.notifier).clear();
      next.whenData((client) {
        if (client != null) ref.read(logsProvider.notifier).loadHistory();
      });
    });

    ref.listen(tunnelsProvider, (_, _) { _updateTray(); _updateRecoveryAlerts(); });
    ref.listen(workspacesProvider, (_, _) => _updateTray());
    ref.listen(connectionPreferencesProvider, (_, _) => _updateTray());

    final connection = ref.watch(clientProvider);

    return Scaffold(
      body: Row(
        children: [
          NavigationRail(
            selectedIndex: _selected,
            onDestinationSelected: (i) => setState(() => _selected = i),
            labelType: NavigationRailLabelType.all,
            destinations: [
              for (final d in _destinations)
                NavigationRailDestination(
                  icon: Icon(d.icon),
                  label: Text(d.label),
                ),
            ],
          ),
          const VerticalDivider(width: 1),
          Expanded(
            child: Column(
              children: [
                _ConnectionBanner(state: connection),
                const WorkspaceSwitcher(),
                Expanded(
                  child: connection.when(
                    skipLoadingOnRefresh: false,
                    skipLoadingOnReload: false,
                    loading: () =>
                        const Center(child: CircularProgressIndicator()),
                    error: (e, _) => _DaemonMissing(message: e.toString()),
                    data: (client) {
                      // 设置 / 关于不依赖 daemon，断开时仍可访问；
                      // 其余页面需要连接，未连接时显示引导页。
                      if (client == null && _selected < _settingsIndex) {
                        return const _DaemonMissing();
                      }
                      return IndexedStack(
                        key: ValueKey('${client?.info.discoveryPath}|${client?.info.token}'),
                        index: _selected,
                        children: _pages,
                      );
                    },
                  ),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  void _updateTray() {
    final connection = ref.read(clientProvider);
    final client = connection.isLoading ? null : connection.valueOrNull;
    var label = client?.info.sourceLabel;
    final preferences = ref.read(workspacesProvider).valueOrNull;
    if (client != null && preferences != null) {
      for (final workspace in preferences.workspaces) {
        if (DaemonDiscovery.samePath(workspace.discoveryPath, client.info.discoveryPath)) {
          label = workspace.name;
          break;
        }
      }
    }
    final tunnels = ref.read(tunnelsProvider);
    _tray.updateStatus(client: client, tunnels: tunnels.isLoading ? null : tunnels.valueOrNull,
      instanceLabel: label, preferences: ref.read(connectionPreferencesProvider).valueOrNull);
  }

  void _updateRecoveryAlerts() {
    final connection = ref.read(clientProvider);
    final data = ref.read(tunnelsProvider);
    final client = connection.valueOrNull;
    final tunnels = data.valueOrNull;
    if (connection.isLoading || data.isLoading || client == null || tunnels == null) return;
    final instance = '${client.info.discoveryPath}|${client.info.pid}|${client.info.token}';
    for (final alert in _recoveryAlerts.update(instance, tunnels)) {
      _notifications.show(alert, onClick: _showWindow, stillRelevant: () {
        if (!mounted || !identical(ref.read(clientProvider).valueOrNull, client)) return false;
        final current = ref.read(tunnelsProvider).valueOrNull ?? <Tunnel>[];
        return current.any((t) => t.name == alert.name && t.desiredRunning &&
          (alert.recovered ? t.state == 'connected' : t.state == 'failed' || t.state == 'reconnecting'));
      });
    }
  }
}

/// 顶部连接状态条。
class _ConnectionBanner extends ConsumerWidget {
  const _ConnectionBanner({required this.state});

  final AsyncValue<DaemonClient?> state;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);

    late final String text;
    late final Color color;

    state.when(
      loading: () {
        text = '正在连接 daemon...';
        color = theme.colorScheme.tertiary;
      },
      error: (_, _) {
        text = '连接不可用';
        color = theme.colorScheme.error;
      },
      data: (client) {
        if (client == null) {
          text = 'daemon 未运行';
          color = theme.colorScheme.error;
        } else {
          final info = client.info;
          text =
              '已连接 ${info.httpBase} · ${info.sourceLabel}（版本 ${info.version}）';
          color = const Color(0xFF2E9E5B);
        }
      },
    );

    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
      color: color.withValues(alpha: 0.12),
      child: Row(
        children: [
          Icon(Icons.circle, size: 10, color: color),
          const SizedBox(width: 10),
          Text(text, style: theme.textTheme.bodyMedium?.copyWith(color: color)),
          const Spacer(),
          TextButton.icon(
            onPressed: () {
              // 重新读取发现文件并重新探活；其余数据状态随之级联刷新
              ref.invalidate(discoveryProvider);
              ref.invalidate(tunnelsProvider);
              ref.invalidate(sshConnectionsProvider);
              ref.invalidate(keysProvider);
            },
            icon: const Icon(Icons.refresh, size: 16),
            label: const Text('重新连接'),
          ),
        ],
      ),
    );
  }
}

/// daemon 未启动时的引导页。
class _DaemonMissing extends ConsumerWidget {
  const _DaemonMissing({this.message});

  final String? message;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final found =
        ref.watch(discoveryProvider).valueOrNull ?? const <DaemonCandidate>[];

    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 620),
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Icon(Icons.power_off,
                        color: theme.colorScheme.error, size: 28),
                    const SizedBox(width: 12),
                    Text('当前实例未连接',
                        style: theme.textTheme.titleLarge),
                  ],
                ),
                const SizedBox(height: 16),
                const Text(
                    '客户端会在启动时自动拉起随程序分发的 daemon。'
                    '若自动启动失败，也可手动启动本地守护进程：'),
                const SizedBox(height: 12),
                const _CodeBlock(
                  'cd daemon\n'
                  'go build -o bin/ssh-tunnel-daemon.exe ./cmd/ssh-tunnel\n'
                  'bin/ssh-tunnel-daemon.exe -config ssh-tunnel.toml',
                ),
                const SizedBox(height: 16),
                FilledButton.icon(
                  onPressed: () async {
                    ref.invalidate(clientProvider);
                    // 重新读取发现文件并重新探活；其余数据状态随之级联刷新
                    ref.invalidate(discoveryProvider);
                    ref.invalidate(tunnelsProvider);
                    ref.invalidate(sshConnectionsProvider);
                    ref.invalidate(keysProvider);
                  },
                  icon: const Icon(Icons.play_arrow),
                  label: const Text('重新检测 / 启动工作区'),
                ),
                const SizedBox(height: 16),
                Text('客户端会依次检查以下位置（按优先级）：',
                    style: theme.textTheme.bodySmall),
                const SizedBox(height: 6),
                for (final path in DaemonDiscovery.candidatePaths)
                  Padding(
                    padding: const EdgeInsets.only(bottom: 2),
                    child: Text(
                      '· $path',
                      style: theme.textTheme.bodySmall
                          ?.copyWith(fontFamily: 'monospace'),
                    ),
                  ),
                if (found.isNotEmpty) ...[
                  const SizedBox(height: 12),
                  Text(
                    '已找到 ${found.length} 个发现文件：'
                    '${found.map((c) => c.info.sourceLabel).join('、')}。'
                    '当前实例尚未通过连接验证，可在上方选择其他实例或重新检测。',
                    style: theme.textTheme.bodySmall
                        ?.copyWith(color: theme.colorScheme.error),
                  ),
                ],
                if (message != null) ...[
                  const SizedBox(height: 8),
                  Text(message!,
                      style: theme.textTheme.bodySmall
                          ?.copyWith(color: theme.colorScheme.error)),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}

class _CodeBlock extends StatelessWidget {
  const _CodeBlock(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: theme.colorScheme.surfaceContainerHighest,
        borderRadius: BorderRadius.circular(8),
      ),
      child: SelectableText(
        text,
        style: theme.textTheme.bodySmall?.copyWith(fontFamily: 'monospace'),
      ),
    );
  }
}
