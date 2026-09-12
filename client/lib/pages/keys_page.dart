import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../services/daemon_client.dart';
import '../widgets.dart';
import 'ssh_security.dart';

/// 私钥页。私钥不上传到 daemon，配置里只记录本地文件路径，
/// 这里汇总展示「配置引用了哪些文件、文件是否还在、被谁引用」。
class KeysPage extends ConsumerWidget {
  const KeysPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final keys = ref.watch(keysProvider);

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: '密钥',
            subtitle: '私钥以本地文件路径引用，无需上传；在 SSH 连接配置中选择文件即可',
            actions: [
              IconButton(
                onPressed: () => ref.read(keysProvider.notifier).refresh(),
                icon: const Icon(Icons.refresh),
                tooltip: '刷新',
              ),
            ],
          ),
          Expanded(
            child: keys.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => ErrorView(
                message: describeError(e),
                onRetry: () => ref.read(keysProvider.notifier).refresh(),
              ),
              data: (list) {
                if (list.isEmpty) {
                  return const EmptyView(
                    icon: Icons.key_outlined,
                    message: '还没有引用任何私钥，在 SSH 连接配置中选择私钥文件',
                  );
                }
                return ListView.builder(
                  padding: const EdgeInsets.fromLTRB(24, 8, 24, 24),
                  itemCount: list.length,
                  itemBuilder: (context, i) => _KeyCard(keyInfo: list[i]),
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

class _KeyCard extends ConsumerWidget {
  final KeyInfo keyInfo;

  const _KeyCard({required this.keyInfo});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final missing = !keyInfo.exists;

    return Card(
      child: ListTile(
        contentPadding:
            const EdgeInsets.symmetric(horizontal: 20, vertical: 8),
        leading: Icon(
          Icons.key_outlined,
          color: missing ? theme.colorScheme.error : null,
        ),
        title: Row(
          children: [
            Expanded(child: Text(keyInfo.name)),
            if (keyInfo.encrypted) Text(keyInfo.unlocked ? '已解锁（本次会话）' : '口令保护'),
            if (missing)
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                decoration: BoxDecoration(
                  color: theme.colorScheme.errorContainer,
                  borderRadius: BorderRadius.circular(10),
                ),
                child: Text(
                  '文件缺失',
                  style: TextStyle(
                    fontSize: 11,
                    color: theme.colorScheme.onErrorContainer,
                  ),
                ),
              ),
          ],
        ),
        subtitle: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const SizedBox(height: 4),
            SelectableText(
              keyInfo.resolved.isNotEmpty ? keyInfo.resolved : keyInfo.path,
              style: theme.textTheme.bodySmall,
            ),
            const SizedBox(height: 2),
            Text(
              keyInfo.exists
                  ? '${_formatSize(keyInfo.size)} · ${_formatTime(keyInfo.modified)} · 被 ${keyInfo.usedBy.join('、')} 引用'
                  : '被 ${keyInfo.usedBy.join('、')} 引用',
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
        ),
        trailing: Row(mainAxisSize: MainAxisSize.min, children: [
          if (keyInfo.encrypted) TextButton(onPressed: () async {
            if (!keyInfo.unlocked) { await unlockPrivateKey(context, ref, keyInfo.path); return; }
            try { await ref.read(keysProvider.notifier).lock(keyInfo.path); }
            catch (error) { if (context.mounted) showErrorSnack(context, error); }
          }, child: Text(keyInfo.unlocked ? '锁定' : '解锁')),
          IconButton(
          icon: const Icon(Icons.folder_open_outlined),
          tooltip: '在文件管理器中显示',
          onPressed: keyInfo.exists ? () => _reveal(context) : null,
          ),
        ]),
      ),
    );
  }

  Future<void> _reveal(BuildContext context) async {
    final dir = File(keyInfo.resolved).parent.path;
    try {
      if (Platform.isWindows) {
        // /select, 参数会直接选中目标文件
        await Process.run('explorer', ['/select,', keyInfo.resolved]);
      } else if (Platform.isMacOS) {
        await Process.run('open', ['-R', keyInfo.resolved]);
      } else {
        await Process.run('xdg-open', [dir]);
      }
    } catch (e) {
      if (context.mounted) showErrorSnack(context, e);
    }
  }
}

String _formatSize(int bytes) {
  if (bytes < 1024) return '$bytes B';
  if (bytes < 1024 * 1024) return '${(bytes / 1024).toStringAsFixed(1)} KB';
  return '${(bytes / 1024 / 1024).toStringAsFixed(1)} MB';
}

String _formatTime(String iso) {
  if (iso.isEmpty) return '';
  final dt = DateTime.tryParse(iso);
  if (dt == null) return iso;
  return '${dt.year}-${_pad(dt.month)}-${_pad(dt.day)} ${_pad(dt.hour)}:${_pad(dt.minute)}';
}

String _pad(int n) => n.toString().padLeft(2, '0');
