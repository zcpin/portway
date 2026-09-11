import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../widgets.dart';

/// 日志页。daemon 通过 WebSocket 实时推送日志，进入页面时可回放历史缓冲。
class LogsPage extends ConsumerWidget {
  const LogsPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final logs = ref.watch(logsProvider);

    return Scaffold(
      body: Column(
        children: [
          PageHeader(
            title: '日志',
            subtitle: '实时接收 daemon 日志，最多保留最近 500 条',
            actions: [
              OutlinedButton.icon(
                onPressed: () =>
                    ref.read(logsProvider.notifier).loadHistory(),
                icon: const Icon(Icons.history),
                label: const Text('回放历史'),
              ),
              const SizedBox(width: 8),
              IconButton(
                onPressed: () => ref.read(logsProvider.notifier).clear(),
                icon: const Icon(Icons.clear_all),
                tooltip: '清空',
              ),
            ],
          ),
          Expanded(
            child: logs.isEmpty
                ? const EmptyView(
                    icon: Icons.terminal_outlined,
                    message: '暂无日志',
                  )
                : ListView.builder(
                    padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
                    itemCount: logs.length,
                    itemBuilder: (context, i) => _LogRow(entry: logs[i]),
                  ),
          ),
        ],
      ),
    );
  }
}

class _LogRow extends StatelessWidget {
  const _LogRow({required this.entry});

  final LogEntry entry;

  Color _color(BuildContext context) {
    switch (entry.level) {
      case 'error':
        return const Color(0xFFD0403A);
      case 'warn':
        return const Color(0xFFC77700);
      case 'debug':
        return Theme.of(context).colorScheme.onSurface.withValues(alpha: 0.5);
      default:
        return Theme.of(context).colorScheme.onSurface.withValues(alpha: 0.8);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final time = entry.timestamp.length >= 19
        ? entry.timestamp.substring(11, 19)
        : entry.timestamp;

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 70,
            child: Text(
              time,
              style: theme.textTheme.bodySmall
                  ?.copyWith(fontFamily: 'monospace'),
            ),
          ),
          const SizedBox(width: 10),
          SizedBox(
            width: 52,
            child: Text(
              entry.level.toUpperCase(),
              style: theme.textTheme.labelSmall?.copyWith(
                color: _color(context),
                fontFamily: 'monospace',
              ),
            ),
          ),
          const SizedBox(width: 10),
          Expanded(
            child: SelectableText(
              entry.message,
              style: theme.textTheme.bodySmall?.copyWith(
                color: _color(context),
                fontFamily: 'monospace',
              ),
            ),
          ),
        ],
      ),
    );
  }
}
