import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../providers.dart';
import '../services/daemon_discovery.dart';
import '../widgets.dart';

class WorkspaceSwitcher extends ConsumerStatefulWidget {
  const WorkspaceSwitcher({super.key});
  @override
  ConsumerState<WorkspaceSwitcher> createState() => _WorkspaceSwitcherState();
}

class _WorkspaceSwitcherState extends ConsumerState<WorkspaceSwitcher> {
  bool _busy = false;

  Future<void> _run(Future<void> Function() action) async {
    setState(() => _busy = true);
    try {
      await action();
    } catch (error) {
      if (mounted) showErrorSnack(context, error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _create() async {
    final name = await showDialog<String>(
      context: context,
      builder: (_) => const _WorkspaceNameDialog(),
    );
    if (name == null || !mounted) return;
    await _run(() => ref.read(workspacesProvider.notifier).create(name));
  }

  @override
  Widget build(BuildContext context) {
    final preferences = ref.watch(workspacesProvider);
    final candidates = ref.watch(discoveryProvider).valueOrNull ?? [];
    final available = ref.watch(instanceAvailabilityProvider).valueOrNull ?? {};
    final engine = ref.watch(clientProvider).valueOrNull;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 6),
      child: preferences.when(
        loading: () => const LinearProgressIndicator(),
        error: (error, _) => Row(
          children: [
            Expanded(child: Text('无法读取工作区配置：$error')),
            TextButton(
              onPressed: () => ref.invalidate(workspacesProvider),
              child: const Text('重试'),
            ),
          ],
        ),
        data: (preferences) {
          final entries = <(String, String)>[('', '自动选择实例')];
          bool known(String path) => entries.any(
            (entry) =>
                entry.$1.isNotEmpty && DaemonDiscovery.samePath(entry.$1, path),
          );
          // 仅按发现文件路径判断在线状态：只对 daemon 实例有意义，
          // 进程内引擎没有发现文件，改由 engineOwnsWorkspace 判定。
          bool onlinePath(String path) =>
              available[path] == true ||
              (engine != null &&
                  !engine.info.embedded &&
                  DaemonDiscovery.samePath(engine.info.discoveryPath, path));
          for (final workspace in preferences.workspaces) {
            final isOnline = onlinePath(workspace.discoveryPath) ||
                (engine != null && engineOwnsWorkspace(engine, workspace));
            entries.add((
              workspace.discoveryPath,
              '${workspace.name} · ${isOnline ? '在线' : '未连接'}',
            ));
          }
          for (final candidate in candidates) {
            if (!known(candidate.path)) {
              entries.add((
                candidate.path,
                '${candidate.info.sourceLabel} · ${candidate.info.httpBase} · ${onlinePath(candidate.path) ? '在线' : '离线'}',
              ));
            }
          }
          if (preferences.selectedPath.isNotEmpty &&
              !known(preferences.selectedPath)) {
            entries.add((preferences.selectedPath, '已保存实例 · 离线'));
          }
          final selected = preferences.selectedPath.isEmpty
              ? ''
              : entries
                    .firstWhere(
                      (entry) =>
                          entry.$1.isNotEmpty &&
                          DaemonDiscovery.samePath(
                            entry.$1,
                            preferences.selectedPath,
                          ),
                    )
                    .$1;
          return Row(
            children: [
              const Icon(Icons.workspaces_outline, size: 18),
              const SizedBox(width: 10),
              Expanded(
                child: DropdownButton<String>(
                  key: const Key('workspace-selector'),
                  value: selected,
                  isExpanded: true,
                  items: [
                    for (final entry in entries)
                      DropdownMenuItem(
                        value: entry.$1,
                        child: Tooltip(
                          message: entry.$1.isEmpty ? entry.$2 : entry.$1,
                          child: Text(
                            entry.$2,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      ),
                  ],
                  onChanged: _busy
                      ? null
                      : (path) => _run(
                          () => ref
                              .read(workspacesProvider.notifier)
                              .select(path!),
                        ),
                ),
              ),
              const SizedBox(width: 10),
              TextButton.icon(
                onPressed: _busy ? null : _create,
                icon: const Icon(Icons.add),
                label: const Text('新建工作区'),
              ),
              IconButton(
                onPressed: _busy
                    ? null
                    : () => ref.invalidate(discoveryProvider),
                icon: const Icon(Icons.refresh),
                tooltip: '刷新实例',
              ),
            ],
          );
        },
      ),
    );
  }
}

class _WorkspaceNameDialog extends StatefulWidget {
  const _WorkspaceNameDialog();
  @override
  State<_WorkspaceNameDialog> createState() => _WorkspaceNameDialogState();
}

class _WorkspaceNameDialogState extends State<_WorkspaceNameDialog> {
  final _name = TextEditingController();
  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  void _submit() {
    if (_name.text.trim().isNotEmpty) Navigator.pop(context, _name.text.trim());
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: const Text('新建工作区'),
    content: SizedBox(
      width: 400,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          TextField(
            controller: _name,
            autofocus: true,
            maxLength: 80,
            decoration: const InputDecoration(labelText: '工作区名称'),
            onSubmitted: (_) => _submit(),
          ),
          const Text('创建独立的空配置并启动该工作区，之后可在设置页导入已有配置。'),
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('取消'),
      ),
      FilledButton(onPressed: _submit, child: const Text('创建')),
    ],
  );
}
