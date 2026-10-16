import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/port.dart';
import '../providers.dart';
import '../services/tunnel_engine.dart';
import '../widgets.dart';

/// 端口占用页。
///
/// 只显示正在监听 / 绑定端口的进程，类似任务管理器的进程列表，
/// 但过滤掉了不占端口的进程。支持按端口号、进程名、路径、命令行查询，
/// 可以结束进程，也可以定位到进程所在目录。
class PortsPage extends ConsumerStatefulWidget {
  const PortsPage({super.key});

  @override
  ConsumerState<PortsPage> createState() => _PortsPageState();
}

class _PortsPageState extends ConsumerState<PortsPage> {
  final _searchController = TextEditingController();

  /// 界面上的即时过滤词。
  ///
  /// 与「刷新时的服务端筛选」是两回事：用户敲字不该每次都打一次 HTTP，
  /// 因此本地先过滤已拿到的列表，只有点刷新或改筛选条件时才回服务端。
  String _localQuery = '';

  PortFilter _filter = const PortFilter();

  /// 展开的行，显示命令行等次要信息。
  final _expanded = <String>{};

  /// 自动刷新定时器；默认关闭。
  bool _autoRefresh = false;

  /// 用户正在操作（展开行、确认框），暂停自动刷新避免列表跳动。
  bool _interacting = false;

  Timer? _autoRefreshTimer;

  bool _firstLoadDone = false;

  @override
  void initState() {
    super.initState();
    // 首次可见时主动扫一次：provider 的 build 不自动扫描。
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) ref.read(portsProvider.notifier).refresh();
    });
  }

  @override
  void dispose() {
    _autoRefreshTimer?.cancel();
    _searchController.dispose();
    super.dispose();
  }

  /// 切换自动刷新。
  ///
  /// 默认关闭：端口扫描要枚举系统表并补全进程信息，常驻轮询的开销
  /// 与收益不成正比。开启后 5 秒一次，用户操作期间跳过。
  void _setAutoRefresh(bool enabled) {
    _autoRefreshTimer?.cancel();
    _autoRefreshTimer = null;
    setState(() => _autoRefresh = enabled);
    if (!enabled) return;
    _autoRefreshTimer = Timer.periodic(const Duration(seconds: 5), (_) {
      if (!mounted || _interacting) return;
      ref.read(portsProvider.notifier).refresh();
    });
  }

  Future<void> _refresh() async {
    await ref.read(portsProvider.notifier).refresh();
    if (mounted) setState(() => _firstLoadDone = true);
  }

  Future<void> _applyFilter(PortFilter next) async {
    setState(() => _filter = next);
    await ref.read(portsProvider.notifier).applyFilter(next);
  }

  /// 本地过滤：与服务端筛选规则保持一致。
  List<PortEntry> _visible(List<PortEntry> source) {
    final query = _localQuery.trim().toLowerCase();
    if (query.isEmpty) return source;
    return source.where((entry) {
      if (entry.localPort.toString().contains(query)) return true;
      if (entry.processName.toLowerCase().contains(query)) return true;
      if (entry.executable.toLowerCase().contains(query)) return true;
      if (entry.commandLine.toLowerCase().contains(query)) return true;
      if (entry.tunnel.toLowerCase().contains(query)) return true;
      return false;
    }).toList();
  }

  @override
  Widget build(BuildContext context) {
    final snapshot = ref.watch(portsProvider);

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: '端口',
            subtitle: '本机正在监听或绑定的端口及其占用进程',
            actions: [
              IconButton(
                onPressed: _refresh,
                icon: const Icon(Icons.refresh),
                tooltip: '重新扫描',
              ),
            ],
          ),
          _buildFilterBar(snapshot),
          Expanded(
            child: snapshot.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => ErrorView(message: describeError(e), onRetry: _refresh),
              data: (data) {
                if (data.ports.isEmpty && !_firstLoadDone) {
                  // provider 的 build 返回空快照，等首次扫描完成
                  return const Center(child: CircularProgressIndicator());
                }
                final visible = _visible(data.ports);
                return Column(
                  children: [
                    if (data.partial) _buildWarning(data.warnings),
                    Expanded(child: _buildList(data, visible)),
                  ],
                );
              },
            ),
          ),
        ],
      ),
    );
  }

  /// 采集不完整时的提示。
  ///
  /// 只提示、不隐藏条目：条目本身是准确的，只是缺少进程归属。
  Widget _buildWarning(List<String> warnings) {
    final theme = Theme.of(context);
    final text = warnings.isNotEmpty
        ? warnings.join('；')
        : '部分端口的占用进程无法确定，通常需要管理员权限才能看到。';
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.fromLTRB(24, 0, 24, 8),
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
      decoration: BoxDecoration(
        color: theme.colorScheme.errorContainer.withValues(alpha: 0.5),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Row(
        children: [
          Icon(Icons.warning_amber_outlined, size: 18, color: theme.colorScheme.onErrorContainer),
          const SizedBox(width: 8),
          Expanded(
            child: Text(text, style: TextStyle(fontSize: 12, color: theme.colorScheme.onErrorContainer)),
          ),
        ],
      ),
    );
  }

  Widget _buildFilterBar(AsyncValue<PortSnapshot> snapshot) {
    final data = snapshot.valueOrNull;
    final total = data?.ports.length ?? 0;
    final visible = data == null ? 0 : _visible(data.ports).length;

    return Padding(
      padding: const EdgeInsets.fromLTRB(24, 0, 24, 8),
      child: Column(
        children: [
          Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _searchController,
                  decoration: InputDecoration(
                    isDense: true,
                    prefixIcon: const Icon(Icons.search, size: 20),
                    hintText: '按端口号、进程名、路径或命令行筛选',
                    suffixIcon: _localQuery.isEmpty
                        ? null
                        : IconButton(
                            icon: const Icon(Icons.clear, size: 18),
                            onPressed: () {
                              _searchController.clear();
                              setState(() => _localQuery = '');
                            },
                          ),
                    border: const OutlineInputBorder(),
                  ),
                  onChanged: (value) => setState(() => _localQuery = value),
                ),
              ),
              const SizedBox(width: 12),
              SegmentedButton<String>(
                segments: const [
                  ButtonSegment(value: '', label: Text('全部')),
                  ButtonSegment(value: 'tcp', label: Text('TCP')),
                  ButtonSegment(value: 'udp', label: Text('UDP')),
                ],
                selected: {_filter.protocol},
                onSelectionChanged: (value) =>
                    _applyFilter(_filter.copyWith(protocol: value.first)),
              ),
              const SizedBox(width: 12),
              // 默认关闭：默认隐藏会让用户误判端口空闲，而端口确实被占着。
              FilterChip(
                label: const Text('仅非系统进程'),
                selected: _filter.nonSystemOnly,
                onSelected: (value) => _applyFilter(_filter.copyWith(nonSystemOnly: value)),
              ),
              const SizedBox(width: 8),
              FilterChip(
                label: const Text('仅本机'),
                selected: _filter.scope == 'loopback',
                onSelected: (value) =>
                    _applyFilter(_filter.copyWith(scope: value ? 'loopback' : '')),
              ),
              const SizedBox(width: 8),
              FilterChip(
                label: const Text('命令行'),
                tooltip: '补全完整命令行，采集较慢',
                selected: _filter.withCommandLine,
                onSelected: (value) => _applyFilter(_filter.copyWith(withCommandLine: value)),
              ),
              const SizedBox(width: 8),
              FilterChip(
                label: const Text('自动刷新'),
                selected: _autoRefresh,
                onSelected: _setAutoRefresh,
              ),
            ],
          ),
          const SizedBox(height: 6),
          Row(
            children: [
              Text(
                data == null
                    ? '正在扫描…'
                    : '共 $total 条${visible == total ? '' : '，显示 $visible 条'}'
                        '${data.elapsedMs > 0 ? ' · 耗时 ${data.elapsedMs}ms' : ''}',
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildList(PortSnapshot data, List<PortEntry> visible) {
    if (visible.isEmpty) {
      // 组合筛选后结果为空是合法结果，要提示可以关掉筛选。
      final filtered = _localQuery.isNotEmpty || !_filter.isEmpty;
      return EmptyView(
        icon: Icons.lan_outlined,
        message: filtered ? '当前筛选条件下没有匹配的端口，可以放宽筛选条件' : '没有检测到正在监听的端口',
      );
    }
    return ListView.builder(
      padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
      itemCount: visible.length,
      itemBuilder: (context, i) {
        final entry = visible[i];
        final key = '${entry.protocol}:${entry.localAddr}:${entry.localPort}:${entry.pid}';
        return _PortCard(
          entry: entry,
          expanded: _expanded.contains(key),
          onToggle: () => setState(() {
            if (!_expanded.remove(key)) _expanded.add(key);
          }),
          onInteract: (busy) => _interacting = busy,
        );
      },
    );
  }
}

/// 一条端口记录。
class _PortCard extends ConsumerWidget {
  const _PortCard({
    required this.entry,
    required this.expanded,
    required this.onToggle,
    required this.onInteract,
  });

  final PortEntry entry;
  final bool expanded;
  final VoidCallback onToggle;
  final ValueChanged<bool> onInteract;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final mono = theme.textTheme.bodyMedium?.copyWith(fontFamily: 'monospace');

    return Card(
      child: InkWell(
        onTap: entry.commandLine.isEmpty && entry.executable.isEmpty ? null : onToggle,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  _protocolChip(theme),
                  const SizedBox(width: 10),
                  // 端口号用等宽字体，列之间能对齐
                  Text('${entry.localPort}', style: mono?.copyWith(fontWeight: FontWeight.w600)),
                  const SizedBox(width: 12),
                  Expanded(
                    child: Text(
                      entry.displayAddr,
                      style: theme.textTheme.bodySmall,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  if (entry.tunnel.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.only(right: 8),
                      child: Chip(
                        visualDensity: VisualDensity.compact,
                        avatar: const Icon(Icons.swap_horiz, size: 14),
                        label: Text('隧道 ${entry.tunnel}', style: const TextStyle(fontSize: 11)),
                      ),
                    ),
                  Text(
                    entry.hasProcess ? '${entry.processName} (${entry.pid})' : '无法确定占用进程',
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: entry.hasProcess ? null : theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(width: 12),
                  _buildActions(context, ref),
                ],
              ),
              if (expanded) ...[
                const SizedBox(height: 10),
                if (entry.executable.isNotEmpty)
                  _detailRow(theme, '路径', entry.executable, selectable: true),
                if (entry.commandLine.isNotEmpty)
                  _detailRow(theme, '命令行', entry.commandLine, selectable: true),
                if (entry.user.isNotEmpty) _detailRow(theme, '用户', entry.user),
                if (entry.startedAt.isNotEmpty) _detailRow(theme, '启动于', entry.startedAt),
                if (entry.executable.isEmpty && entry.commandLine.isEmpty)
                  Text(
                    '没有可显示的进程详情（可能需要管理员权限）',
                    style: theme.textTheme.bodySmall,
                  ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  /// 协议与状态标记。
  ///
  /// UDP 用不同颜色并标注「绑定」：UDP 没有连接状态机，
  /// 把 0.0.0.0 说成「监听」会让用户误解成在等待连接。
  Widget _protocolChip(ThemeData theme) {
    final isUDP = entry.isUDP;
    final color = isUDP ? theme.colorScheme.tertiary : theme.colorScheme.primary;
    final label = isUDP ? 'UDP 绑定' : 'TCP 监听';
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(4),
      ),
      child: Text(
        label,
        style: TextStyle(fontSize: 11, color: color, fontWeight: FontWeight.w600),
      ),
    );
  }

  Widget _detailRow(ThemeData theme, String label, String value, {bool selectable = false}) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 4),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 60,
            child: Text(label, style: theme.textTheme.bodySmall),
          ),
          Expanded(
            child: selectable
                ? SelectableText(value, style: theme.textTheme.bodySmall)
                : Text(value, style: theme.textTheme.bodySmall),
          ),
        ],
      ),
    );
  }

  Widget _buildActions(BuildContext context, WidgetRef ref) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        IconButton(
          icon: const Icon(Icons.copy_outlined, size: 18),
          tooltip: '复制地址',
          visualDensity: VisualDensity.compact,
          onPressed: () async {
            await Clipboard.setData(ClipboardData(text: entry.endpoint));
            if (context.mounted) {
              ScaffoldMessenger.of(context)
                  .showSnackBar(SnackBar(content: Text('已复制 ${entry.endpoint}')));
            }
          },
        ),
        IconButton(
          icon: const Icon(Icons.folder_open_outlined, size: 18),
          tooltip: '定位到进程目录',
          visualDensity: VisualDensity.compact,
          onPressed: entry.hasProcess ? () => _reveal(context, ref) : null,
        ),
        IconButton(
          icon: const Icon(Icons.close_outlined, size: 18),
          tooltip: entry.canKill ? '结束进程' : (entry.system ? '系统关键进程，不可结束' : '无法确定进程'),
          visualDensity: VisualDensity.compact,
          color: entry.canKill ? Theme.of(context).colorScheme.error : null,
          onPressed: entry.canKill ? () => _kill(context, ref) : null,
        ),
      ],
    );
  }

  /// 定位到进程目录。
  ///
  /// 路径由引擎解析，打开文件管理器在客户端做：daemon 可能是系统服务，
  /// 以 LocalSystem 身份 explorer 打开的窗口不属于用户桌面会话。
  Future<void> _reveal(BuildContext context, WidgetRef ref) async {
    try {
      final path = await ref.read(portsProvider.notifier).resolvePath(entry.pid);
      if (path.isEmpty) {
        if (context.mounted) showErrorSnack(context, '无法获取该进程的可执行文件路径');
        return;
      }
      if (Platform.isWindows) {
        await Process.run('explorer', ['/select,', path]);
      } else if (Platform.isMacOS) {
        await Process.run('open', ['-R', path]);
      } else {
        await Process.run('xdg-open', [File(path).parent.path]);
      }
    } catch (e) {
      if (context.mounted) showErrorSnack(context, e);
    }
  }

  /// 结束进程。
  ///
  /// Windows 上没有优雅终止：统一走 TerminateProcess，因此文案必须如实
  /// 说明「等同强制终止，进程没有机会保存数据」，不能让用户以为是一次
  /// 正常的退出请求。
  Future<void> _kill(BuildContext context, WidgetRef ref) async {
    onInteract(true);
    try {
      final confirmed = await showDialog<bool>(
        context: context,
        builder: (_) => AlertDialog(
          title: const Text('结束进程'),
          content: Text(
            '将结束 ${entry.processName}（PID ${entry.pid}）。\n\n'
            '这会强制终止进程，它没有机会保存数据或做清理。'
            '如果它正在提供服务，其它程序会立即断开。',
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('取消')),
            FilledButton(
              style: FilledButton.styleFrom(backgroundColor: Theme.of(context).colorScheme.error),
              onPressed: () => Navigator.pop(context, true),
              child: const Text('结束进程'),
            ),
          ],
        ),
      );
      if (confirmed != true) return;
      if (!context.mounted) return;

      final result = await ref.read(portsProvider.notifier).kill(entry.pid);
      if (!context.mounted) return;
      if (result.ok) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('已结束 ${result.name}（PID ${result.pid}）')));
      } else if (result.isProtected) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('${result.name} 是系统关键进程，不能从这里结束')),
        );
      } else if (result.isDenied) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('权限不足，需要以管理员身份运行才能结束该进程')),
        );
      } else if (result.isNotFound) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('${result.name} 已经退出')));
      } else {
        showErrorSnack(context, result.error.isEmpty ? '结束进程失败' : result.error);
      }
    } catch (e) {
      if (context.mounted) showErrorSnack(context, e);
    } finally {
      onInteract(false);
    }
  }
}
