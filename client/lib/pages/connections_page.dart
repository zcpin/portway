import 'package:dio/dio.dart';
import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../services/daemon_client.dart';
import '../widgets.dart';

/// 打开 SSH 连接新建/编辑对话框。
Future<void> openConnectionEditor(
  BuildContext context,
  WidgetRef ref, {
  SshConnection? editing,
}) async {
  final result = await showDialog<SshConnection>(
    context: context,
    builder: (_) => ConnectionEditorDialog(editing: editing),
  );
  if (result == null || !context.mounted) return;

  try {
    await ref
        .read(sshConnectionsProvider.notifier)
        .save(result, editingName: editing?.name);
  } catch (e) {
    if (context.mounted) showErrorSnack(context, e);
  }
}

/// SSH 连接管理页。连接可被多个隧道引用，避免重复配置主机与密钥。
class ConnectionsPage extends ConsumerWidget {
  const ConnectionsPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final connections = ref.watch(sshConnectionsProvider);

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: 'SSH 连接',
            subtitle: '可被多个隧道引用，修改一次即对所有引用它的隧道生效',
            actions: [
              FilledButton.icon(
                onPressed: () => openConnectionEditor(context, ref),
                icon: const Icon(Icons.add),
                label: const Text('新建连接'),
              ),
              const SizedBox(width: 8),
              IconButton(
                onPressed: () =>
                    ref.read(sshConnectionsProvider.notifier).refresh(),
                icon: const Icon(Icons.refresh),
                tooltip: '刷新',
              ),
            ],
          ),
          Expanded(
            child: connections.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => ErrorView(
                message: describeError(e),
                onRetry: () =>
                    ref.read(sshConnectionsProvider.notifier).refresh(),
              ),
              data: (list) {
                if (list.isEmpty) {
                  return const EmptyView(
                    icon: Icons.dns_outlined,
                    message: '还没有 SSH 连接，点击右上角新建',
                  );
                }
                return ListView.builder(
                  padding: const EdgeInsets.fromLTRB(24, 8, 24, 24),
                  itemCount: list.length,
                  itemBuilder: (context, i) => Padding(
                    padding: const EdgeInsets.only(bottom: 12),
                    child: _ConnectionCard(connection: list[i]),
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

class _ConnectionCard extends ConsumerWidget {
  const _ConnectionCard({required this.connection});

  final SshConnection connection;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);

    return Card(
      child: ListTile(
        contentPadding:
            const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
        leading: const Icon(Icons.dns_outlined),
        title: Text(connection.name, style: theme.textTheme.titleMedium),
        subtitle: Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Wrap(
            spacing: 24,
            runSpacing: 6,
            children: [
              Text('${connection.user}@${connection.host}'),
              Text('密钥：${connection.keyFile}'),
            ],
          ),
        ),
        trailing: PopupMenuButton<String>(
          onSelected: (v) async {
            if (v == 'edit') {
              await openConnectionEditor(context, ref, editing: connection);
            } else if (v == 'delete') {
              final ok = await confirmDelete(context, connection.name);
              if (!ok || !context.mounted) return;
              try {
                await ref
                    .read(sshConnectionsProvider.notifier)
                    .remove(connection.name);
              } catch (e) {
                if (context.mounted) showErrorSnack(context, e);
              }
            }
          },
          itemBuilder: (_) => const [
            PopupMenuItem(value: 'edit', child: Text('编辑')),
            PopupMenuItem(value: 'delete', child: Text('删除')),
          ],
        ),
      ),
    );
  }
}

class ConnectionEditorDialog extends ConsumerStatefulWidget {
  const ConnectionEditorDialog({super.key, this.editing});

  final SshConnection? editing;

  @override
  ConsumerState<ConnectionEditorDialog> createState() =>
      _ConnectionEditorState();
}

/// 私钥路径的校验结果。
enum _KeyState { unknown, checking, ok, missing }

class _ConnectionEditorState extends ConsumerState<ConnectionEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _host;
  late final TextEditingController _user;
  late final TextEditingController _keyFile;

  _KeyState _keyState = _KeyState.unknown;
  String _keyMessage = '';
  bool _testing = false;
  CancelToken? _testCancel;

  @override
  void initState() {
    super.initState();
    final c = widget.editing;
    _name = TextEditingController(text: c?.name ?? '');
    _host = TextEditingController(text: c?.host ?? '');
    _user = TextEditingController(text: c?.user ?? '');
    _keyFile = TextEditingController(text: c?.keyFile ?? '');

    // 编辑已有连接时，先确认一次当前路径是否仍然有效
    if (c != null && c.keyFile.isNotEmpty) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _validateKey());
    }
  }

  @override
  void dispose() {
    _testCancel?.cancel();
    for (final c in [_name, _host, _user, _keyFile]) {
      c.dispose();
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final keys = ref.watch(keysProvider).valueOrNull ?? [];

    return AlertDialog(
      title: Text(widget.editing == null ? '新建 SSH 连接' : '编辑 SSH 连接'),
      content: SizedBox(
        width: 480,
        child: Form(
          key: _formKey,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              TextFormField(
                controller: _name,
                enabled: widget.editing == null,
                decoration: const InputDecoration(labelText: '连接名称 *'),
                validator: (v) =>
                    (v == null || v.trim().isEmpty) ? '请填写名称' : null,
              ),
              const SizedBox(height: 12),
              TextFormField(
                controller: _host,
                decoration:
                    const InputDecoration(labelText: '主机 *（host 或 host:port）'),
                validator: (v) =>
                    (v == null || v.trim().isEmpty) ? '请填写主机' : null,
              ),
              const SizedBox(height: 12),
              TextFormField(
                controller: _user,
                decoration: const InputDecoration(labelText: '用户名 *'),
                validator: (v) =>
                    (v == null || v.trim().isEmpty) ? '请填写用户名' : null,
              ),
              const SizedBox(height: 12),
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: TextFormField(
                      controller: _keyFile,
                      decoration: const InputDecoration(
                        labelText: '私钥路径 *',
                        hintText: r'C:\Users\me\.ssh\id_ed25519',
                      ),
                      validator: (v) =>
                          (v == null || v.trim().isEmpty) ? '请填写私钥路径' : null,
                      onChanged: (_) => setState(() {
                        _keyState = _KeyState.unknown;
                        _keyMessage = '';
                      }),
                    ),
                  ),
                  const SizedBox(width: 8),
                  OutlinedButton.icon(
                    onPressed: _pickKeyFile,
                    icon: const Icon(Icons.folder_open_outlined),
                    label: const Text('选择文件'),
                  ),
                ],
              ),
              if (_keyState != _KeyState.unknown) ...[
                const SizedBox(height: 6),
                Align(
                  alignment: Alignment.centerLeft,
                  child: Row(
                    children: [
                      _keyState == _KeyState.checking
                          ? const SizedBox(
                              width: 12,
                              height: 12,
                              child: CircularProgressIndicator(strokeWidth: 2),
                            )
                          : Icon(
                              _keyState == _KeyState.ok
                                  ? Icons.check_circle_outline
                                  : Icons.warning_amber_outlined,
                              size: 14,
                              color: _keyState == _KeyState.ok
                                  ? Colors.green
                                  : Theme.of(context).colorScheme.error,
                            ),
                      const SizedBox(width: 6),
                      Expanded(
                        child: Text(
                          _keyMessage,
                          style: Theme.of(context).textTheme.bodySmall,
                        ),
                      ),
                    ],
                  ),
                ),
              ],
              if (keys.isNotEmpty) ...[
                const SizedBox(height: 8),
                Align(
                  alignment: Alignment.centerLeft,
                  child: Wrap(
                    spacing: 6,
                    children: [
                      for (final k in keys)
                        ActionChip(
                          avatar: Icon(
                            k.exists
                                ? Icons.key_outlined
                                : Icons.warning_amber_outlined,
                            size: 16,
                            color: k.exists ? null : Colors.orange,
                          ),
                          label: Text(k.name),
                          onPressed: () {
                            _keyFile.text = k.path;
                            _validateKey();
                          },
                        ),
                    ],
                  ),
                ),
              ],
            ],
          ),
        ),
      ),
      actions: [
        OutlinedButton.icon(
          onPressed: _testing ? null : _testConnection,
          icon: const Icon(Icons.network_check),
          label: Text(_testing ? '测试中…' : '测试连接'),
        ),
        TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('取消')),
        FilledButton(onPressed: _submit, child: const Text('保存')),
      ],
    );
  }

  /// 打开文件选择器选取私钥，只记录路径，不复制文件。
  Future<void> _pickKeyFile() async {
    final file = await FilePicker.pickFile(dialogTitle: '选择 SSH 私钥');
    if (!mounted || file == null) return;

    final path = file.path;
    if (path == null) return;

    setState(() => _keyFile.text = path);
    await _validateKey();
  }

  /// 让 daemon 确认私钥路径可读。
  ///
  /// 客户端与 daemon 通常同机，但 daemon 也可能以系统服务身份运行，
  /// 此时能看到的文件范围不同，因此以 daemon 的校验结果为准。
  Future<void> _validateKey() async {
    final path = _keyFile.text.trim();
    if (path.isEmpty) return;

    setState(() {
      _keyState = _KeyState.checking;
      _keyMessage = '正在校验...';
    });

    try {
      final info = await ref.read(keysProvider.notifier).stat(path);
      if (!mounted) return;
      setState(() {
        _keyState = info.exists ? _KeyState.ok : _KeyState.missing;
        _keyMessage = info.exists
            ? 'daemon 可读：${info.resolved}'
            : 'daemon 无法读取该路径（${info.resolved}），请确认文件存在且权限正确';
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _keyState = _KeyState.unknown;
        _keyMessage = '校验失败：${describeError(e)}';
      });
    }
  }

  void _submit() {
    if (!_formKey.currentState!.validate()) return;
    Navigator.pop(context, _connection());
  }

  SshConnection _connection() => SshConnection(
        name: _name.text.trim(),
        host: _host.text.trim(),
        user: _user.text.trim(),
        keyFile: _keyFile.text.trim(),
        hostKeyCheck: widget.editing?.hostKeyCheck ?? '',
        knownHostsFile: widget.editing?.knownHostsFile ?? '',
      );

  Future<void> _testConnection() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _testing = true);
    final cancel = _testCancel = CancelToken();
    try {
      final client = await ref.read(clientProvider.future);
      if (!mounted || cancel.isCancelled) return;
      if (client == null) throw StateError('未连接到 daemon');
      final result = await client.testSshConnection(
          _connection(), cancelToken: cancel);
      if (!mounted) return;
      await showDialog<void>(
        context: context,
        builder: (context) => AlertDialog(
          title: Text(result.ok ? 'SSH 连接成功' : 'SSH 连接失败'),
          content: SelectableText('耗时 ${result.elapsedMs} ms\n'
              '${result.ok ? 'SSH 握手与认证已完成。' : result.error}'),
          actions: [
            TextButton(onPressed: () => Navigator.pop(context),
                child: const Text('关闭')),
          ],
        ),
      );
    } catch (error) {
      if (mounted && !cancel.isCancelled) showErrorSnack(context, error);
    } finally {
      if (mounted) setState(() => _testing = false);
    }
  }
}
