import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../services/daemon_client.dart';
import '../widgets.dart';
import 'ssh_security.dart';
import 'tunnel_diagnostics.dart';

/// 打开隧道新建/编辑对话框，保存成功后刷新列表。
Future<void> openTunnelEditor(
  BuildContext context,
  WidgetRef ref, {
  Tunnel? editing,
  Tunnel? initial,
}) async {
  final result = await showDialog<Tunnel>(
    context: context,
    builder: (_) => TunnelEditorDialog(editing: editing, initial: initial),
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
class TunnelsPage extends ConsumerStatefulWidget {
  const TunnelsPage({super.key});

  @override
  ConsumerState<TunnelsPage> createState() => _TunnelsPageState();
}

class _TunnelsPageState extends ConsumerState<TunnelsPage> {
  String _query = '';
  String? _group;
  final _selected = <String>{};
  bool _busy = false;

  @override
  Widget build(BuildContext context) {
    final tunnels = ref.watch(tunnelsProvider);
    final all = tunnels.valueOrNull ?? <Tunnel>[];
    final groups = all.map((t) => t.group).toSet().toList()..sort();
    final group = groups.contains(_group) ? _group : null;
    final visible = all.where((t) => (group == null || t.group == group) &&
      '${t.name} ${t.group} ${t.sshHost} ${t.sshConnection} ${t.remoteHost} ${t.localHost} ${t.modeLabel} ${t.proxyJump.join(' ')}'.toLowerCase().contains(_query)).toList();
    final selected = _selected.intersection(visible.map((t) => t.name).toSet());

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: '隧道',
            subtitle: '管理本地转发、反向转发和 SOCKS5 代理',
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
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 8),
            child: Wrap(spacing: 12, runSpacing: 8, crossAxisAlignment: WrapCrossAlignment.center, children: [
              SizedBox(width: 250, child: TextField(
                key: const Key('tunnel-search'),
                decoration: const InputDecoration(labelText: '搜索名称、主机或分组', prefixIcon: Icon(Icons.search)),
                onChanged: (value) => setState(() { _query = value.trim().toLowerCase(); _selected.clear(); }),
              )),
              SizedBox(width: 150, child: DropdownButton<String?>(
                value: group, isExpanded: true, hint: const Text('全部分组'),
                items: [
                  const DropdownMenuItem<String?>(value: null, child: Text('全部分组')),
                  for (final value in groups) DropdownMenuItem<String?>(value: value, child: Text(value.isEmpty ? '未分组' : value)),
                ],
                onChanged: (value) => setState(() { _group = value; _selected.clear(); }),
              )),
              TextButton(onPressed: _busy ? null : () => setState(() {
                if (selected.length == visible.length) { _selected.clear(); }
                else { _selected.addAll(visible.map((t) => t.name)); }
              }), child: Text('选择全部（${selected.length}/${visible.length}）')),
              FilledButton.tonalIcon(onPressed: _busy || selected.isEmpty ? null : () => _batch('start', selected),
                icon: const Icon(Icons.play_arrow), label: const Text('批量启动')),
              OutlinedButton.icon(onPressed: _busy || selected.isEmpty ? null : () => _batch('stop', selected),
                icon: const Icon(Icons.stop), label: Text(_busy ? '处理中…' : '批量停止')),
            ]),
          ),
          Expanded(
            child: tunnels.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => ErrorView(
                message: describeError(e),
                onRetry: () => ref.read(tunnelsProvider.notifier).refresh(),
              ),
              data: (list) {
                if (visible.isEmpty) {
                  return EmptyView(
                    icon: Icons.swap_horiz_outlined,
                    message: list.isEmpty ? '还没有隧道，点击右上角新建' : '没有匹配的隧道',
                  );
                }
                return ListView.builder(
                  padding: const EdgeInsets.fromLTRB(24, 8, 24, 24),
                  itemCount: visible.length,
                  itemBuilder: (context, i) => Padding(
                    padding: const EdgeInsets.only(bottom: 12),
                    child: TunnelCard(tunnel: visible[i], selected: selected.contains(visible[i].name),
                      onSelected: _busy ? null : (value) => setState(() {
                        if (value == true) { _selected.add(visible[i].name); }
                        else { _selected.remove(visible[i].name); }
                      })),
                  ),
                );
              },
            ),
          ),
        ],
      ),
    );
  }

  Future<void> _batch(String action, Set<String> names) async {
    setState(() => _busy = true);
    try {
      final results = await ref.read(tunnelsProvider.notifier).batch(action, names.toList());
      if (!mounted) return;
      setState(_selected.clear);
      await showDialog<void>(context: context, builder: (context) => AlertDialog(
        title: const Text('批量操作结果'),
        content: SizedBox(width: 440, height: 300, child: ListView(children: [
          for (final result in results) ListTile(
            leading: Icon(result.ok ? Icons.check_circle_outline : Icons.error_outline),
            title: Text(result.name), subtitle: Text(result.ok ? '已完成' : result.error),
          ),
        ])),
        actions: [TextButton(onPressed: () => Navigator.pop(context), child: const Text('关闭'))],
      ));
    } catch (error) {
      if (mounted) showErrorSnack(context, error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }
}

class TunnelCard extends ConsumerWidget {
  const TunnelCard({super.key, required this.tunnel, this.selected = false, this.onSelected});

  final Tunnel tunnel;
  final bool selected;
  final ValueChanged<bool?>? onSelected;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final running = tunnel.isRunning;
    final statusColor = switch (tunnel.state) {
      'failed' => theme.colorScheme.error,
      'connecting' || 'reconnecting' => const Color(0xFFB57700),
      'connected' => const Color(0xFF2E9E5B),
      _ => theme.colorScheme.onSurface.withValues(alpha: 0.45),
    };

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(18),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                if (onSelected != null) Checkbox(value: selected, onChanged: onSelected),
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
                    tunnel.stateLabel,
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
                    if (v == 'diagnose') {
                      await openTunnelDiagnostics(context, ref, tunnel.name);
                    } else if (v == 'cancel_recovery') {
                      await _guard(context, () => ref.read(tunnelsProvider.notifier).stop(tunnel.name));
                    } else if (v == 'edit') {
                      await openTunnelEditor(context, ref, editing: tunnel);
                    } else if (v == 'copy') {
                      final all = ref.read(tunnelsProvider).valueOrNull ?? [];
                      final names = all.map((t) => t.name).toSet();
                      var name = '${tunnel.name}-copy';
                      for (var i = 2; names.contains(name); i++) { name = '${tunnel.name}-copy-$i'; }
                      final reverse = tunnel.mode == 'remote';
                      final ports = all.map((t) => reverse ? t.remotePort : t.localPort).toSet();
                      var port = reverse ? tunnel.remotePort : tunnel.localPort;
                      for (var i = 0; i < 65535; i++) {
                        port = port >= 65535 ? 1024 : port + 1;
                        if (!ports.contains(port)) break;
                      }
                      await openTunnelEditor(context, ref, initial: tunnel.duplicateAs(name,
                        localPort: reverse ? null : port, remotePort: reverse ? port : null));
                    } else if (v == 'delete') {
                      final ok = await confirmDelete(context, tunnel.name);
                      if (ok && context.mounted) {
                        await _guard(context,
                            () => ref.read(tunnelsProvider.notifier).remove(tunnel.name));
                      }
                    }
                  },
                  itemBuilder: (_) => [
                    const PopupMenuItem(value: 'diagnose', child: Text('诊断连接')),
                    if (!running && tunnel.desiredRunning)
                      const PopupMenuItem(value: 'cancel_recovery', child: Text('停止自动恢复')),
                    const PopupMenuItem(value: 'edit', child: Text('编辑')),
                    const PopupMenuItem(value: 'copy', child: Text('复制配置')),
                    const PopupMenuItem(value: 'delete', child: Text('删除')),
                  ],
                ),
              ],
            ),
            const SizedBox(height: 14),
            Wrap(
              spacing: 28,
              runSpacing: 10,
              children: [
                if (tunnel.group.isNotEmpty) InfoField(label: '分组', value: tunnel.group),
                InfoField(label: '自动启动', value: tunnel.autoStart ? '开启' : '关闭'),
                InfoField(label: '类型', value: tunnel.modeLabel),
                InfoField(label: tunnel.mode == 'remote' ? '本机目标' : '本地监听',
                  value: _endpoint(tunnel.localHost, tunnel.localPort)),
                InfoField(
                    label: tunnel.mode == 'remote' ? '远端监听' : '远端目标',
                    value: tunnel.mode == 'dynamic' ? '由 SOCKS5 请求指定' : _endpoint(tunnel.remoteHost, tunnel.remotePort)),
                InfoField(
                  label: 'SSH',
                  value: tunnel.sshConnection.isNotEmpty
                      ? '${tunnel.sshConnection}（引用连接）'
                      : '${tunnel.sshUser}@${tunnel.sshHost}',
                ),
                InfoField(
                  label: '重连',
                  value:
                      '${tunnel.reconnectStrategy.isEmpty ? '全局策略' : tunnel.reconnectStrategy == 'exponential' ? '指数退避' : '固定间隔'} / ${tunnel.reconnectInterval.isEmpty ? '全局间隔' : tunnel.reconnectInterval}',
                ),
                InfoField(label: '重试次数', value: '${tunnel.retryCount}'),
                if (tunnel.connectedAt.isNotEmpty)
                  InfoField(label: '连接时间', value: DateTime.tryParse(tunnel.connectedAt)?.toLocal().toString() ?? tunnel.connectedAt),
              ],
            ),
            if (tunnel.lastError.isNotEmpty) ...[
              const SizedBox(height: 12),
              SelectableText('最近错误：${tunnel.lastError}',
                style: theme.textTheme.bodySmall?.copyWith(color: theme.colorScheme.error)),
            ],
            if (!running && tunnel.desiredRunning) ...[
              const SizedBox(height: 8),
              const Text('网络变化后会再次尝试连接，也可手动启动或在菜单中停止自动恢复。'),
            ],
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

String _endpoint(String host, int port) => host.contains(':') && !host.startsWith('[')
    ? '[$host]:$port' : '$host:$port';

/// 隧道编辑/新建对话框。
class TunnelEditorDialog extends ConsumerStatefulWidget {
  const TunnelEditorDialog({super.key, this.editing, this.initial});

  final Tunnel? editing;
  final Tunnel? initial;

  @override
  ConsumerState<TunnelEditorDialog> createState() => _TunnelEditorState();
}

class _TunnelEditorState extends ConsumerState<TunnelEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _group;
  bool _autoStart = true;
  late final TextEditingController _localPort;
  late final TextEditingController _localHost;
  late final TextEditingController _proxyJump;
  String _mode = 'local';
  late final TextEditingController _remoteHost;
  late final TextEditingController _remotePort;
  late final TextEditingController _sshHost;
  late final TextEditingController _sshUser;
  late final TextEditingController _keyFile;
  late final TextEditingController _agentSocket;
  late final TextEditingController _knownHosts;
  String _authMethod = 'key';
  String _hostKeyCheck = '';
  bool _securityBusy = false;
  late final TextEditingController _interval;
  late final TextEditingController _maxAttempts;

  String? _sshConnection;
  String _strategy = '';

  bool get _isEditing => widget.editing != null;

  @override
  void initState() {
    super.initState();
    final t = widget.editing ?? widget.initial;
    _name = TextEditingController(text: t?.name ?? '');
    _group = TextEditingController(text: t?.group ?? '');
    _autoStart = t?.autoStart ?? true;
    _mode = t?.mode ?? 'local';
    _localHost = TextEditingController(text: t?.localHost ?? '127.0.0.1');
    _proxyJump = TextEditingController(text: t?.proxyJump.join('\n') ?? '');
    _localPort = TextEditingController(
        text: t != null && t.localPort > 0 ? '${t.localPort}' : '');
    _remoteHost = TextEditingController(text: t?.remoteHost ?? '127.0.0.1');
    _remotePort = TextEditingController(
        text: t != null && t.remotePort > 0 ? '${t.remotePort}' : '');
    _sshHost = TextEditingController(text: t?.sshHost ?? '');
    _sshUser = TextEditingController(text: t?.sshUser ?? '');
    _keyFile = TextEditingController(text: t?.keyFile ?? '');
    _agentSocket = TextEditingController(text: t?.agentSocket ?? '');
    _knownHosts = TextEditingController(text: t?.knownHostsFile ?? '');
    _authMethod = t?.authMethod == 'agent' ? 'agent' : 'key';
    _hostKeyCheck = t?.hostKeyCheck ?? '';
    _interval = TextEditingController(text: t?.reconnectInterval ?? '');
    _maxAttempts =
        TextEditingController(text: t != null ? '${t.maxReconnectAttempts}' : '0');
    _sshConnection =
        (t?.sshConnection.isNotEmpty ?? false) ? t!.sshConnection : null;
    _strategy = t?.reconnectStrategy ?? '';
  }

  @override
  void dispose() {
    for (final c in [
      _name,
      _group,
      _localPort,
      _localHost,
      _proxyJump,
      _remoteHost,
      _remotePort,
      _sshHost,
      _sshUser,
      _keyFile,
      _agentSocket,
      _knownHosts,
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
                TextFormField(controller: _group, decoration: const InputDecoration(labelText: '分组（可选）')),
                SwitchListTile(contentPadding: EdgeInsets.zero, title: const Text('随 daemon 自动启动'),
                  subtitle: const Text('关闭后可手动启动；修改此项不会中断当前连接'),
                  value: _autoStart, onChanged: (value) => setState(() => _autoStart = value)),
                DropdownButtonFormField<String>(initialValue: _mode,
                  decoration: const InputDecoration(labelText: '转发类型'),
                  items: const [DropdownMenuItem(value: 'local', child: Text('本地转发')),
                    DropdownMenuItem(value: 'remote', child: Text('反向转发')),
                    DropdownMenuItem(value: 'dynamic', child: Text('SOCKS5 代理'))],
                  onChanged: (mode) => setState(() => _mode = mode!)),
                const SizedBox(height: 12),
                TextFormField(controller: _localHost,
                  decoration: InputDecoration(labelText: _mode == 'remote' ? '本机目标主机 *' : '本地监听地址 *'),
                  validator: (value) => (value == null || value.trim().isEmpty) ? '请填写地址' : null),
                const SizedBox(height: 12),
                Row(
                  children: [
                    Expanded(
                      child: TextFormField(
                        controller: _localPort,
                        decoration:
                            InputDecoration(labelText: _mode == 'remote' ? '本机目标端口 *' : '本地端口 *'),
                        keyboardType: TextInputType.number,
                        validator: (v) =>
                            int.tryParse(v ?? '') == null ? '请填写有效端口' : null,
                      ),
                    ),
                    if (_mode != 'dynamic') ...[
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _remoteHost,
                        decoration:
                            InputDecoration(labelText: _mode == 'remote' ? '远端监听主机 *' : '远端主机 *'),
                        validator: (v) =>
                            (v == null || v.trim().isEmpty) ? '请填写' : null,
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _remotePort,
                        decoration:
                            InputDecoration(labelText: _mode == 'remote' ? '远端监听端口 *' : '远端端口 *'),
                        keyboardType: TextInputType.number,
                        validator: (v) =>
                            int.tryParse(v ?? '') == null ? '请填写有效端口' : null,
                      ),
                    ),
                    ],
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
                  TextFormField(controller: _proxyJump, minLines: 1, maxLines: 3,
                    decoration: const InputDecoration(labelText: '跳板链（每行一个 SSH 连接名）')),
                  const SizedBox(height: 12),
                  Row(
                    children: [
                      Expanded(
                        flex: 2,
                        child: TextFormField(
                          controller: _sshHost,
                          decoration: const InputDecoration(
                              labelText: 'SSH 主机（host:port）*'),
                          validator: (v) =>
                              (v == null || v.trim().isEmpty) ? '请填写 SSH 主机' : null,
                        ),
                      ),
                      const SizedBox(width: 12),
                      Expanded(
                        child: TextFormField(
                          controller: _sshUser,
                          decoration:
                              const InputDecoration(labelText: 'SSH 用户 *'),
                          validator: (v) =>
                              (v == null || v.trim().isEmpty) ? '请填写 SSH 用户' : null,
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 12),
                  DropdownButtonFormField<String>(initialValue: _authMethod,
                    decoration: const InputDecoration(labelText: '认证方式'),
                    items: const [DropdownMenuItem(value: 'key', child: Text('私钥文件')),
                      DropdownMenuItem(value: 'agent', child: Text('SSH agent'))],
                    onChanged: (value) => setState(() => _authMethod = value!)),
                  const SizedBox(height: 12),
                  if (_authMethod == 'key') ...[
                    TextFormField(controller: _keyFile,
                      decoration: const InputDecoration(labelText: '私钥路径 *'),
                      validator: (v) => (v == null || v.trim().isEmpty) ? '请填写私钥路径' : null),
                    TextButton.icon(onPressed: _securityBusy ? null : () async {
                      if (_keyFile.text.trim().isNotEmpty) await unlockPrivateKey(context, ref, _keyFile.text.trim());
                    }, icon: const Icon(Icons.lock_open), label: const Text('解锁私钥')),
                  ] else TextFormField(controller: _agentSocket,
                    decoration: const InputDecoration(labelText: 'Agent 地址（留空使用系统默认）')),
                  const SizedBox(height: 12),
                  DropdownButtonFormField<String>(key: ValueKey(_hostKeyCheck), initialValue: _hostKeyCheck,
                    decoration: const InputDecoration(labelText: '主机密钥校验'),
                    items: const [DropdownMenuItem(value: '', child: Text('known_hosts（默认）')),
                      DropdownMenuItem(value: 'known_hosts', child: Text('known_hosts')),
                      DropdownMenuItem(value: 'insecure', child: Text('不校验（insecure）'))],
                    onChanged: (value) => setState(() => _hostKeyCheck = value!)),
                  const SizedBox(height: 12),
                  TextFormField(controller: _knownHosts,
                    decoration: const InputDecoration(labelText: 'known_hosts 路径（留空使用默认）')),
                  TextButton.icon(onPressed: _securityBusy ? null : _inspectHost,
                    icon: const Icon(Icons.verified_user_outlined), label: Text(_securityBusy ? '检查中…' : '查看主机指纹')),
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
                              value: '', child: Text('跟随全局设置')),
                          DropdownMenuItem(
                              value: 'fixed', child: Text('固定间隔')),
                          DropdownMenuItem(
                              value: 'exponential', child: Text('指数退避')),
                        ],
                        onChanged: (v) =>
                            setState(() => _strategy = v ?? ''),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _interval,
                        decoration:
                            const InputDecoration(labelText: '重连间隔（留空跟随全局）'),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: TextFormField(
                        controller: _maxAttempts,
                        decoration:
                            const InputDecoration(labelText: '最大次数（0=跟随全局）'),
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
      group: _group.text.trim(),
      autoStart: _autoStart,
      mode: _mode,
      localHost: _localHost.text.trim(),
      proxyJump: parseJumpNames(_proxyJump.text),
      localPort: int.parse(_localPort.text.trim()),
      remoteHost: _mode == 'dynamic' ? '' : _remoteHost.text.trim(),
      remotePort: _mode == 'dynamic' ? 0 : int.parse(_remotePort.text.trim()),
      sshConnection: _sshConnection ?? '',
      sshHost: _sshConnection == null ? _sshHost.text.trim() : '',
      sshUser: _sshConnection == null ? _sshUser.text.trim() : '',
      keyFile: _sshConnection == null ? _keyFile.text.trim() : '',
      authMethod: _authMethod,
      agentSocket: _agentSocket.text.trim(),
      hostKeyCheck: _hostKeyCheck,
      knownHostsFile: _knownHosts.text.trim(),
      reconnectStrategy: _strategy,
      reconnectInterval: _interval.text.trim(),
      maxReconnectAttempts: int.tryParse(_maxAttempts.text.trim()) ?? 0,
      isRunning: widget.editing?.isRunning ?? false,
    );

    Navigator.pop(context, tunnel);
  }

  Future<void> _inspectHost() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _securityBusy = true);
    final trusted = await inspectAndTrustHostKey(context, ref, SshConnection(
      name: _name.text.trim(), host: _sshHost.text.trim(), user: _sshUser.text.trim(),
      keyFile: _keyFile.text.trim(), authMethod: _authMethod,
      agentSocket: _agentSocket.text.trim(), proxyJump: parseJumpNames(_proxyJump.text),
      hostKeyCheck: _hostKeyCheck, knownHostsFile: _knownHosts.text.trim()));
    if (mounted) setState(() { _securityBusy = false; if (trusted) _hostKeyCheck = 'known_hosts'; });
  }
}
