import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/frp.dart';
import '../providers.dart';
import '../services/tunnel_engine.dart';
import '../widgets.dart';
import 'frp_editors.dart';

/// FRP 页：按客户端分组展示代理，并提供启停、编辑与删除。
///
/// 一个客户端对应一份 frpc 配置（<配置目录>/frp/clients/<名称>.toml），
/// 可以独立启停；代理则挂在客户端下，增删改通过 frp 的热更新生效，不会断开连接。
class FrpPage extends ConsumerStatefulWidget {
  const FrpPage({super.key, this.active = true});

  final bool active;

  @override
  ConsumerState<FrpPage> createState() => _FrpPageState();
}

class _FrpPageState extends ConsumerState<FrpPage> {
  String _query = '';

  /// 正在执行操作的按钮，避免重复点击。
  final _busy = <String>{};

  @override
  Widget build(BuildContext context) {
    final clients = ref.watch(frpClientsProvider);
    final all = clients.valueOrNull ?? const <FrpClient>[];
    final visible = all
        .where((client) => '${client.name} ${client.group} ${client.serverLabel} '
                '${client.proxies.map((proxy) => proxy.name).join(' ')}'
            .toLowerCase()
            .contains(_query))
        .toList();

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: 'FRP',
            subtitle: '管理 frpc 客户端与它们转发的代理',
            actions: [
              FilledButton.icon(
                onPressed: () => _editClient(),
                icon: const Icon(Icons.add),
                label: const Text('新建客户端'),
              ),
              const SizedBox(width: 8),
              IconButton(
                onPressed: () => ref.read(frpClientsProvider.notifier).refresh(),
                icon: const Icon(Icons.refresh),
                tooltip: '刷新',
              ),
            ],
          ),
          if (all.isNotEmpty)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 8),
              child: SizedBox(
                width: 280,
                child: TextField(
                  key: const Key('frp-search'),
                  decoration: const InputDecoration(
                    labelText: '搜索客户端或代理',
                    prefixIcon: Icon(Icons.search),
                  ),
                  onChanged: (value) => setState(() => _query = value.trim().toLowerCase()),
                ),
              ),
            ),
          Expanded(
            child: switch (clients) {
              AsyncError(:final error) => ErrorView(
                  message: describeError(error),
                  onRetry: () => ref.read(frpClientsProvider.notifier).refresh(),
                ),
              AsyncData(:final value) when value.isEmpty => const EmptyView(
                  icon: Icons.swap_horiz_outlined,
                  message: '还没有 FRP 客户端。点「新建客户端」填写 frps 地址即可开始。',
                ),
              AsyncData() when visible.isEmpty => const EmptyView(
                  icon: Icons.search_off,
                  message: '没有匹配的客户端',
                ),
              AsyncData() => ListView(
                  padding: const EdgeInsets.fromLTRB(24, 8, 24, 24),
                  children: [
                    for (final client in visible) _buildClientCard(client),
                  ],
                ),
              _ => const Center(child: CircularProgressIndicator()),
            },
          ),
        ],
      ),
    );
  }

  Widget _buildClientCard(FrpClient client) {
    final theme = Theme.of(context);
    final busy = _busy.contains(client.name);

    return Card(
      margin: const EdgeInsets.only(bottom: 16),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 8, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(
                  client.isRunning ? Icons.cloud_done_outlined : Icons.cloud_off_outlined,
                  color: client.isRunning
                      ? theme.colorScheme.primary
                      : theme.colorScheme.onSurface.withValues(alpha: 0.4),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Text(client.name, style: theme.textTheme.titleMedium),
                          if (client.group.isNotEmpty) ...[
                            const SizedBox(width: 8),
                            Chip(
                              label: Text(client.group),
                              visualDensity: VisualDensity.compact,
                              materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                            ),
                          ],
                          const SizedBox(width: 8),
                          Text(client.stateLabel, style: theme.textTheme.bodySmall),
                        ],
                      ),
                      const SizedBox(height: 2),
                      Text(
                        '${client.serverLabel} · ${client.proxies.length} 条代理'
                        '（${client.enabledProxyCount} 条启用）'
                        '${client.autoStart ? '' : ' · 手动启动'}',
                        style: theme.textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
                if (busy)
                  const Padding(
                    padding: EdgeInsets.symmetric(horizontal: 12),
                    child: SizedBox(
                      width: 18,
                      height: 18,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    ),
                  )
                else
                  IconButton(
                    tooltip: client.isRunning ? '停止' : '启动',
                    icon: Icon(client.isRunning ? Icons.stop_circle_outlined : Icons.play_circle_outline),
                    onPressed: () => _run(client.name, () =>
                        client.isRunning
                            ? ref.read(frpClientsProvider.notifier).stopClient(client.name)
                            : ref.read(frpClientsProvider.notifier).startClient(client.name)),
                  ),
                PopupMenuButton<String>(
                  tooltip: '更多操作',
                  onSelected: (value) => switch (value) {
                    'restart' => _run(client.name,
                        () => ref.read(frpClientsProvider.notifier).restartClient(client.name)),
                    'edit' => _editClient(client: client),
                    'delete' => _deleteClient(client),
                    _ => null,
                  },
                  itemBuilder: (_) => const [
                    PopupMenuItem(value: 'restart', child: Text('重启')),
                    PopupMenuItem(value: 'edit', child: Text('编辑配置')),
                    PopupMenuItem(value: 'delete', child: Text('删除')),
                  ],
                ),
              ],
            ),
            if (client.lastError.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 8, left: 36),
                child: Text(
                  client.lastError,
                  style: theme.textTheme.bodySmall?.copyWith(color: theme.colorScheme.error),
                ),
              ),
            const Divider(height: 20),
            if (client.proxies.isEmpty)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 4),
                child: Text('还没有代理', style: theme.textTheme.bodySmall),
              )
            else
              for (final proxy in client.proxies) _buildProxyRow(client, proxy),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                onPressed: () => _editProxy(client.name),
                icon: const Icon(Icons.add, size: 18),
                label: const Text('添加代理'),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildProxyRow(FrpClient client, FrpProxy proxy) {
    final theme = Theme.of(context);
    final key = '${client.name}/${proxy.name}';
    final busy = _busy.contains(key);
    final error = proxy.lastError;

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        children: [
          Tooltip(
            message: proxy.enabled ? '停用该代理' : '启用该代理',
            child: Switch(
              value: proxy.enabled,
              onChanged: busy
                  ? null
                  : (value) => _run(
                        key,
                        () => ref.read(frpClientsProvider.notifier).toggleProxy(client.name, proxy.name, value),
                      ),
            ),
          ),
          const SizedBox(width: 8),
          SizedBox(
            width: 76,
            child: Text(proxy.typeLabel, style: theme.textTheme.labelMedium),
          ),
          Expanded(
            flex: 3,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(proxy.name, style: theme.textTheme.bodyMedium),
                if (proxy.localLabel.isNotEmpty)
                  Text(
                    proxy.remoteAddr.isEmpty
                        ? '本地 ${proxy.localLabel}'
                        : '本地 ${proxy.localLabel} → ${proxy.remoteAddr}',
                    style: theme.textTheme.bodySmall,
                  ),
              ],
            ),
          ),
          Expanded(
            flex: 2,
            child: Text(
              error.isEmpty ? proxy.stateLabel : error,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall?.copyWith(
                color: error.isEmpty ? null : theme.colorScheme.error,
              ),
            ),
          ),
          if (busy)
            const SizedBox(
              width: 18,
              height: 18,
              child: Padding(
                padding: EdgeInsets.symmetric(horizontal: 8),
                child: CircularProgressIndicator(strokeWidth: 2),
              ),
            )
          else
            PopupMenuButton<String>(
              tooltip: '代理操作',
              onSelected: (value) => switch (value) {
                'edit' => proxy.editable
                    ? _editProxy(client.name, editing: proxy)
                    : _showNotEditable(proxy),
                'delete' => _deleteProxy(client.name, proxy),
                _ => null,
              },
              itemBuilder: (_) => [
                PopupMenuItem(value: 'edit', enabled: proxy.editable, child: const Text('编辑')),
                const PopupMenuItem(value: 'delete', child: Text('删除')),
              ],
            ),
        ],
      ),
    );
  }

  // ---------- 操作 ----------

  /// 执行一个操作，失败时提示；成功后列表由 provider 刷新。
  Future<void> _run(String busyKey, Future<void> Function() action) async {
    setState(() => _busy.add(busyKey));
    try {
      await action();
    } catch (error) {
      if (mounted) showErrorSnack(context, error);
    } finally {
      if (mounted) setState(() => _busy.remove(busyKey));
    }
  }

  void _showNotEditable(FrpProxy proxy) {
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text('${proxy.typeLabel}类型的访问端暂不支持在界面上编辑，可以启停或删除后重建')),
    );
  }

  Future<void> _editClient({FrpClient? client}) async {
    final payload = await showDialog<FrpClientPayload>(
      context: context,
      builder: (_) => FrpClientEditorDialog(editing: client),
    );
    if (payload == null || !mounted) return;

    await _run(client?.name ?? payload.name, () async {
      await ref.read(frpClientsProvider.notifier).saveClient(payload, editingName: client?.name);
    });
  }

  Future<void> _deleteClient(FrpClient client) async {
    if (!await confirmDelete(context, client.name)) return;
    if (!mounted) return;
    await _run(client.name, () => ref.read(frpClientsProvider.notifier).removeClient(client.name));
  }

  Future<void> _editProxy(String clientName, {FrpProxy? editing}) async {
    final payload = await showDialog<FrpProxyPayload>(
      context: context,
      builder: (_) => FrpProxyEditorDialog(client: clientName, editing: editing),
    );
    if (payload == null || !mounted) return;

    final key = '$clientName/${editing?.name ?? payload.name}';
    await _run(key, () async {
      await ref
          .read(frpClientsProvider.notifier)
          .saveProxy(clientName, payload, editingName: editing?.name);
    });
  }

  Future<void> _deleteProxy(String clientName, FrpProxy proxy) async {
    if (!await confirmDelete(context, '代理 ${proxy.name}')) return;
    if (!mounted) return;
    await _run(
      '$clientName/${proxy.name}',
      () => ref.read(frpClientsProvider.notifier).removeProxy(clientName, proxy.name),
    );
  }
}
