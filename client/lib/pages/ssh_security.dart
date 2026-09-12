import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../widgets.dart';

List<String> parseJumpNames(String value) => value.split(RegExp(r'\r?\n'))
    .map((name) => name.trim()).where((name) => name.isNotEmpty).toList();

Future<bool> unlockPrivateKey(
  BuildContext context,
  WidgetRef ref,
  String path,
) async {
  final passphrase = await showDialog<String>(
    context: context,
    builder: (_) => _UnlockDialog(path: path),
  );
  if (passphrase == null || !context.mounted) return false;
  try {
    await ref.read(keysProvider.notifier).unlock(path, passphrase);
    return true;
  } catch (error) {
    if (context.mounted) showErrorSnack(context, error);
    return false;
  }
}

class _UnlockDialog extends StatefulWidget {
  const _UnlockDialog({required this.path});
  final String path;
  @override
  State<_UnlockDialog> createState() => _UnlockDialogState();
}

class _UnlockDialogState extends State<_UnlockDialog> {
  final _passphrase = TextEditingController();
  @override
  void dispose() {
    _passphrase.dispose();
    super.dispose();
  }

  void _submit() {
    if (_passphrase.text.isEmpty) return;
    final value = _passphrase.text;
    _passphrase.clear();
    Navigator.pop(context, value);
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: const Text('解锁私钥'),
    content: SizedBox(
      width: 440,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          SelectableText(widget.path),
          const SizedBox(height: 12),
          TextField(
            controller: _passphrase,
            obscureText: true,
            enableSuggestions: false,
            autocorrect: false,
            autofocus: true,
            decoration: const InputDecoration(labelText: '私钥口令'),
            onSubmitted: (_) => _submit(),
          ),
          const SizedBox(height: 12),
          const Text('仅在当前 daemon 会话中解锁，口令不会保存到配置。'),
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('取消'),
      ),
      FilledButton(
        key: const Key('unlock-submit'),
        onPressed: _submit,
        child: const Text('解锁'),
      ),
    ],
  );
}

Future<bool> inspectAndTrustHostKey(
  BuildContext context,
  WidgetRef ref,
  SshConnection connection,
) async {
  try {
    final client = await ref.read(clientProvider.future);
    if (client == null) throw StateError('未连接到 daemon');
    final key = await client.inspectHostKey(connection);
    if (!context.mounted) return false;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(
          key.changed
              ? '主机指纹已变化'
              : key.known
              ? '已信任的主机指纹'
              : '确认主机指纹',
        ),
        content: SizedBox(
          width: 520,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text('${key.host} · ${key.algorithm}'),
              const SizedBox(height: 12),
              SelectableText(key.fingerprint),
              const SizedBox(height: 12),
              Text(key.changed ? '与已有记录不同，请确认服务器已更换密钥后再替换。' : '请与服务器提供的指纹核对。'),
              const SizedBox(height: 12),
              SelectableText('保存到：${key.file}'),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: Text(
              key.changed
                  ? '替换并信任'
                  : key.known
                  ? '启用校验'
                  : '信任此指纹',
            ),
          ),
        ],
      ),
    );
    if (confirmed != true || !context.mounted) return false;
    await client.trustHostKey(connection, key.fingerprint, key.changed);
    return true;
  } catch (error) {
    if (context.mounted) showErrorSnack(context, error);
    return false;
  }
}
