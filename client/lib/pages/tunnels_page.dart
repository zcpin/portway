import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../services/daemon_client.dart';
import '../widgets.dart';

/// 打开隧道新建/编辑对话框，保存成功后刷新列表。
Future<void> openTunnelEditor(
  BuildContext context,
  WidgetRef ref, {
  Tunnel? editing,
}) async {
  final result = await showDialog<Tunnel>(
    context: context,
    builder: (_) => TunnelEditorDialog(editing: editing),
  );
  if (result == null || !context.mounted) return;

  try {
    await ref
        .read(tunnelsProvider.notifier)
        .save(result, editingName: editing?.name);
  } catch (e) {
    if (context.mounted) showErrorSnack(context, e);
  }
}

/// 隧道列表页：展示状态并提供启动/停止/重启/编辑/删除。
class TunnelsPage extends ConsumerWidget {
  const TunnelsPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final tunnels = ref.watch(tunnelsProvider);

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: '隧道',
            subtitle: '把远端端口映射到本地，所有隧道由 daemon 统一维护',
            actions: [
              FilledButton.icon(
                onPressed: () => openTunnelEditor(context, ref),
                icon: const Icon(Icons.add),
                label: const Text('新建隧道'),
              ),
              const SizedBox(width: 8),
              IconButton(
                onPressed: () => ref.read(tunnelsProvider.notifier).refresh(),
                icon: const Icon(Icons.refresh),
                tooltip: '刷新',
              ),
            ],
          ),
          Expanded(
            child: tunnels.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => ErrorView(
                message: describeError(e),
                onRetry: () => ref.read(tunnelsProvider.notifier).refresh(),
              ),
              data: (list) {
                if (list.isEmpty) {
                  return const EmptyView(
                    icon: Icons.swap_horiz_outlined,
                    message: '还没有隧道，点击右上角新建',
                  );
                }
                return ListView.builder(
                  padding: const EdgeInsets.fromLTRB(24, 8, 24, 24),
                  itemCount: list.length,
                  itemBuilder: (context, i) => Padding(
                    padding: const EdgeInsets.only(bottom: 12),
                    child: TunnelCard(tunnel: list[i]),
                  ),
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

class TunnelCard extends ConsumerWidget {
  const TunnelCard({super.key, required this.tunnel});

  final Tunnel tunnel;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final running = tunnel.isRunning;
    final statusColor = running
        ? const Color(0xFF2E9E5B)
        : theme.colorScheme.onSurface.withValues(alpha: 0.45);

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(18),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(Icons.circle, size: 10, color: statusColor),
                const SizedBox(width: 10),
                Text(tunnel.name, style: theme.textTheme.titleMedium),
                const SizedBox(width: 10),
                Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                  decoration: BoxDecoration(
                    color: statusColor.withValues(alpha: 0.14),
                    borderRadius: BorderRadius.circular(10),
                  ),
                  child: Text(
                    running ? '运行中' : '已停止',
                    style: theme.textTheme.labelSmall?.copyWith(
                      color: running
                          ? statusColor
                          : theme.colorScheme.onSurface,
                    ),
                  ),
                ),
                const Spacer(),
                if (running)
                  TextButton.icon(
                    onPressed: () => _guard(context,
                        () => ref.read(tunnelsProvider.notifier).stop(tunnel.name)),
                    icon: const Icon(Icons.stop, size: 18),
                    label: const Text('停止'),
                  )
                else
                  FilledButton.tonalIcon(
                    onPressed: () => _guard(context,
                        () => ref.read(tunnelsProvider.notifier).start(tunnel.name)),
                    icon: const Icon(Icons.play_arrow, size: 18),
                    label: const Text('启动'),
                  ),
                const SizedBox(width: 8),
                OutlinedButton.icon(
                  onPressed: () => _guard(context,
                      () => ref.read(tunnelsProvider.notifier).restart(tunnel.name)),
                  icon: const Icon(Icons.restart_alt, size: 18),
                  label: const Text('重启'),
                ),
                const SizedBox(width: 8),
                PopupMenuButton<String>(
                  onSelected: (v) async {
                    if (v == 'edit') {
                      await openTunnelEditor(context, ref, editing: tunnel);
                    } else if (v == 'delete') {
                      final ok = await confirmDelete(context, tunnel.name);
                      if (ok && context.mounted) {
                        await _guard(context,
                            () => ref.read(tunnelsProvider.notifier).remove(tunnel.name));
                      }
                    }
                  },
                  itemBuilder: (_) => const [
                    PopupMenuItem(value: 'edit', child: Text('编辑')),
                    PopupMenuItem(value: 'delete', child: Text('删除')),
                  ],
                ),
              ],
            ),
            const SizedBox(height: 14),
            Wrap(
              spacing: 28,
              runSpacing: 10,
              children: [
                InfoField(label: '本地端口', value: '${tunnel.localPort}'),
                InfoField(
                    label: '远端',
                    value: '${tunnel.remoteHost}:${tunnel.remotePort}'),
                InfoField(
                  label: 'SSH',
                  value: tunnel.sshConnection.isNotEmpty
                      ? '${tunnel.sshConnection}（引用连接）'
                      : '${tunnel.sshUser}@${tunnel.sshHost}',
                ),
                InfoField(
                  label: '重连',
                  value:
                      '${tunnel.reconnectStrategy == 'exponential' ? '指数退避' : '固定间隔'} / ${tunnel.reconnectInterval}',
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  /// 操作失败时用 SnackBar 提示，列表数据保持不变。
  Future<void> _guard(
      BuildContext context, Future<void> Function() action) async {
    try {
      await action();
    } catch (e) {
      if (context.mounted) showErrorSnack(context, e);
    }
  }
}

class InfoField extends StatelessWidget {
  const InfoField({super.key, required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: theme.textTheme.labelSmall),
        const SizedBox(height: 3),
        Text(value, style: theme.textTheme.bodyMedium),
      ],
    );
  }
}

/// 隧道编辑/新建对话框。
class TunnelEditorDialog extends ConsumerStatefulWidget {
  const TunnelEditorDialog({super.key, this.editing});

  final Tunnel? editing;

  @override
  ConsumerState<TunnelEditorDialog> createState() => _TunnelEditorState();
}

class _TunnelEditorState extends ConsumerState<TunnelEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _localPort;
  late final TextEditingController _remoteHost;
  late final TextEditingController _remotePort;
  late final TextEditingController _sshHost;
  late final TextEditingController _sshUser;
  late final TextEditingController _interval;
  late final TextEditingController _maxAttempts;

  String? _sshConnection;
  String _strategy = 'fixed';

  bool get _isEditing => widget.editing != null;

  @override
  void initState() {
    super.initState();
    final t = widget.editing;
    _name = TextEditingController(text: t?.name ?? '');
    _localPort = TextEditingController(
        text: t != null && t.localPort > 0 ? '${t.localPort}' : '');
    _remoteHost = TextEditingController(text: t?.remoteHost ?? '127.0.0.1');
    _remotePort = TextEditingController(
        text: t != null && t.remotePort > 0 ? '${t.remotePort}' : '');
    _sshHost = TextEditingController(text: t?.sshHost ?? '');
    _sshUser = TextEditingController(text: t?.sshUser ?? '');
    _interval = TextEditingController(text: t?.reconnectInterval ?? '5s');
    _maxAttempts =
        TextEditingController(text: t != null ? '${t.maxReconnectAttempts}' : '0');
    _sshConnection =
        (t?.sshConnection.isNotEmpty ?? false) ? t!.sshConnection : null;
    _strategy = t?.reconnectStrategy ?? 'fixed';
  }

  @override
  void dispose() {
    for (final c in [
      _name,
      _localPort,
      _remoteHost,
      _remotePort,
      _sshHost,
      _sshUser,
      _interval,
      _maxAttempts,
    ]) {
      c.dispose();
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final connections = ref.watch(sshConnectionsProvider).valueOrNull ?? [];
    final useReference = _sshConnection != null;

    return AlertDialog(
      title: Text(_isEditing ? '编辑隧道' : '新建隧道'),
      content: SizedBox(
        width: 560,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                TextFormField(
                  controller: _name,
                  enabled: !_isEditing,
                  decoration: const InputDecoration(labelText: '隧道名称 *'),
                  validator: (v) =>
                      (v == null || v.trim().isEmpty) ? '请填写名称' : null,
                ),
                const SizedBox(height: 12),
                Row(
                  children: [
                    Expanded(
                      child: TextFormField(
                        controller: _localPort,
                        decoration:
                            const InputDecoration(labelText: '本地端口 *'),
                        keyboardType: TextInputType.number,
                        validator: (v) =>
                            int.tryParse(v ?? '') == null ? '请填写有效端口' : null,
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _remoteHost,
                        decoration:
                            const InputDecoration(labelText: '远端主机 *'),
                        validator: (v) =>
                            (v == null || v.trim().isEmpty) ? '请填写' : null,
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _remotePort,
                        decoration:
                            const InputDecoration(labelText: '远端端口 *'),
                        keyboardType: TextInputType.number,
                        validator: (v) =>
                            int.tryParse(v ?? '') == null ? '请填写有效端口' : null,
                      ),
                    ),
                  ],
                ),
                if (connections.isNotEmpty) ...[
                  const SizedBox(height: 12),
                  DropdownButtonFormField<String?>(
                    initialValue: _sshConnection,
                    decoration:
                        const InputDecoration(labelText: '引用 SSH 连接（可选）'),
                    items: [
                      const DropdownMenuItem<String?>(
                          value: null, child: Text('不引用，手动填写')),
                      for (final c in connections)
                        DropdownMenuItem<String?>(
                            value: c.name, child: Text(c.name)),
                    ],
                    onChanged: (v) => setState(() => _sshConnection = v),
                  ),
                ],
                if (!useReference) ...[
                  const SizedBox(height: 12),
                  Row(
                    children: [
                      Expanded(
                        flex: 2,
                        child: TextFormField(
                          controller: _sshHost,
                          decoration: const InputDecoration(
                              labelText: 'SSH 主机（host:port）*'),
                        ),
                      ),
                      const SizedBox(width: 12),
                      Expanded(
                        child: TextFormField(
                          controller: _sshUser,
                          decoration:
                              const InputDecoration(labelText: 'SSH 用户 *'),
                        ),
                      ),
                    ],
                  ),
                ],
                const SizedBox(height: 12),
                Row(
                  children: [
                    Expanded(
                      child: DropdownButtonFormField<String>(
                        initialValue: _strategy,
                        decoration: const InputDecoration(labelText: '重连策略'),
                        items: const [
                          DropdownMenuItem(
                              value: 'fixed', child: Text('固定间隔')),
                          DropdownMenuItem(
                              value: 'exponential', child: Text('指数退避')),
                        ],
                        onChanged: (v) =>
                            setState(() => _strategy = v ?? 'fixed'),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _interval,
                        decoration:
                            const InputDecoration(labelText: '重连间隔（如 5s）'),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _maxAttempts,
                        decoration:
                            const InputDecoration(labelText: '最大次数（0=无限）'),
                        keyboardType: TextInputType.number,
                      ),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('取消')),
        FilledButton(onPressed: _submit, child: const Text('保存')),
      ],
    );
  }

  void _submit() {
    if (!_formKey.currentState!.validate()) return;

    final tunnel = Tunnel(
      name: _name.text.trim(),
      localPort: int.parse(_localPort.text.trim()),
      remoteHost: _remoteHost.text.trim(),
      remotePort: int.parse(_remotePort.text.trim()),
      sshConnection: _sshConnection ?? '',
      sshHost: _sshConnection == null ? _sshHost.text.trim() : '',
      sshUser: _sshConnection == null ? _sshUser.text.trim() : '',
      reconnectStrategy: _strategy,
      reconnectInterval:
          _interval.text.trim().isEmpty ? '5s' : _interval.text.trim(),
      maxReconnectAttempts: int.tryParse(_maxAttempts.text.trim()) ?? 0,
      isRunning: widget.editing?.isRunning ?? false,
    );

    Navigator.pop(context, tunnel);
  }
}
