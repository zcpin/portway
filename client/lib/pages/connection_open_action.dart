import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../connection_preferences_provider.dart';
import '../models.dart';
import '../models/connection_preferences.dart';
import '../providers.dart';
import '../services/daemon_client.dart' show describeError;
import '../widgets.dart';

class _OpenActionEdit {
  final ConnectionOpenAction? action;
  const _OpenActionEdit(this.action);
}

Future<void> configureConnectionOpenAction(
  BuildContext context,
  WidgetRef ref,
  Tunnel tunnel,
) async {
  final connection = ref.read(clientProvider);
  final client = connection.valueOrNull;
  if (connection.isLoading || client == null) {
    showErrorSnack(context, StateError('当前实例未连接'));
    return;
  }
  final current = ref
      .read(connectionPreferencesProvider)
      .valueOrNull
      ?.openActions[tunnel.name];
  final result = await showDialog<_OpenActionEdit>(
    context: context,
    builder: (_) =>
        ConnectionOpenActionDialog(name: tunnel.name, initial: current),
  );
  if (result == null || !context.mounted) return;
  if (!identical(ref.read(clientProvider).valueOrNull, client)) {
    showErrorSnack(context, StateError('实例已切换，请重新配置'));
    return;
  }
  try {
    await ref
        .read(connectionPreferencesProvider.notifier)
        .setOpenAction(tunnel.name, result.action);
  } catch (error) {
    if (context.mounted) showErrorSnack(context, error);
  }
}

class ConnectionOpenActionDialog extends StatefulWidget {
  const ConnectionOpenActionDialog({
    super.key,
    required this.name,
    this.initial,
  });
  final String name;
  final ConnectionOpenAction? initial;
  @override
  State<ConnectionOpenActionDialog> createState() =>
      _ConnectionOpenActionState();
}

class _ConnectionOpenActionState extends State<ConnectionOpenActionDialog> {
  late String _kind;
  late final TextEditingController _url;
  late final TextEditingController _program;
  late final TextEditingController _arguments;
  String? _error;

  @override
  void initState() {
    super.initState();
    _kind = widget.initial?.kind ?? 'url';
    _url = TextEditingController(
      text: _kind == 'url'
          ? widget.initial?.target ?? 'http://{host}:{port}'
          : 'http://{host}:{port}',
    );
    _program = TextEditingController(
      text: _kind == 'program' ? widget.initial?.target : '',
    );
    _arguments = TextEditingController(
      text: widget.initial?.arguments.join('\n') ?? '',
    );
  }

  @override
  void dispose() {
    _url.dispose();
    _program.dispose();
    _arguments.dispose();
    super.dispose();
  }

  void _save() {
    try {
      final action = ConnectionOpenAction(
        kind: _kind,
        target: (_kind == 'url' ? _url.text : _program.text).trim(),
        arguments: _kind == 'program'
            ? _arguments.text
                  .split('\n')
                  .map((line) => line.trim())
                  .where((line) => line.isNotEmpty)
            : const [],
      );
      action.validate();
      Navigator.pop(context, _OpenActionEdit(action));
    } catch (error) {
      setState(() => _error = describeError(error));
    }
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text('配置打开方式 · ${widget.name}'),
    content: SizedBox(
      width: 560,
      child: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            DropdownButtonFormField<String>(
              initialValue: _kind,
              decoration: const InputDecoration(labelText: '打开方式'),
              items: const [
                DropdownMenuItem(value: 'url', child: Text('网页')),
                DropdownMenuItem(value: 'program', child: Text('本地程序')),
              ],
              onChanged: (value) => setState(() {
                _kind = value!;
                _error = null;
              }),
            ),
            const SizedBox(height: 16),
            if (_kind == 'url')
              TextField(
                controller: _url,
                decoration: const InputDecoration(labelText: '网址模板'),
              ),
            if (_kind == 'program') ...[
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      controller: _program,
                      decoration: const InputDecoration(labelText: '程序完整路径'),
                    ),
                  ),
                  IconButton(
                    tooltip: '选择程序',
                    icon: const Icon(Icons.folder_open),
                    onPressed: () async {
                      try {
                        final file = await FilePicker.pickFile(
                          type: Platform.isWindows
                              ? FileType.custom
                              : FileType.any,
                          allowedExtensions: Platform.isWindows
                              ? ['exe']
                              : null,
                        );
                        final path = file?.path;
                        if (mounted && path != null) _program.text = path;
                      } catch (error) {
                        if (mounted) {
                          setState(() => _error = describeError(error));
                        }
                      }
                    },
                  ),
                ],
              ),
              const SizedBox(height: 12),
              TextField(
                controller: _arguments,
                minLines: 3,
                maxLines: 6,
                decoration: const InputDecoration(
                  labelText: '参数（每行一个，无需额外引号）',
                  hintText: '--host\n{host}\n--port\n{port}',
                ),
              ),
            ],
            const SizedBox(height: 12),
            const Text('{host} 和 {port} 会替换为本地监听地址与端口。打开前会等待隧道连接就绪。'),
            if (_error != null) ...[
              const SizedBox(height: 12),
              Text(
                _error!,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ],
          ],
        ),
      ),
    ),
    actions: [
      if (widget.initial != null)
        TextButton(
          onPressed: () => Navigator.pop(context, const _OpenActionEdit(null)),
          child: const Text('清除打开方式'),
        ),
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('取消'),
      ),
      FilledButton(onPressed: _save, child: const Text('保存')),
    ],
  );
}

class ConnectionOpenButton extends ConsumerStatefulWidget {
  const ConnectionOpenButton({
    super.key,
    required this.tunnel,
    required this.action,
  });
  final Tunnel tunnel;
  final ConnectionOpenAction action;
  @override
  ConsumerState<ConnectionOpenButton> createState() =>
      _ConnectionOpenButtonState();
}

class _ConnectionOpenButtonState extends ConsumerState<ConnectionOpenButton> {
  bool _opening = false;
  Future<void> _open() async {
    final connection = ref.read(clientProvider);
    final client = connection.valueOrNull;
    if (connection.isLoading || client == null) return;
    setState(() => _opening = true);
    try {
      await ref
          .read(externalConnectionLauncherProvider)
          .open(
            client,
            widget.tunnel.name,
            widget.action,
            // The client is closed on workspace changes; scrolling a row out of
            // view should not cancel an explicitly requested external opening.
            stillCurrent: () => !client.isClosed,
          );
      if (mounted && identical(ref.read(clientProvider).valueOrNull, client)) {
        await ref.read(tunnelsProvider.notifier).refresh();
      }
    } catch (error) {
      if (mounted) showErrorSnack(context, error);
    } finally {
      if (mounted) setState(() => _opening = false);
    }
  }

  @override
  Widget build(BuildContext context) => IconButton(
    tooltip: _opening
        ? '正在等待连接…'
        : widget.tunnel.state == 'connected'
        ? '打开服务'
        : '启动并打开服务',
    onPressed: _opening ? null : _open,
    icon: _opening
        ? const SizedBox(
            width: 18,
            height: 18,
            child: CircularProgressIndicator(strokeWidth: 2),
          )
        : const Icon(Icons.open_in_new),
  );
}
