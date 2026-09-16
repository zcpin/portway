import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/tunnel_diagnostic.dart';
import '../providers.dart';
import '../services/tunnel_engine.dart';
import '../widgets.dart';

Future<void> openTunnelDiagnostics(
  BuildContext context,
  WidgetRef ref,
  String name,
) async {
  final connection = ref.read(clientProvider);
  final client = connection.isLoading ? null : connection.valueOrNull;
  if (client == null) {
    showErrorSnack(context, StateError('当前实例未连接'));
    return;
  }
  await showDialog<void>(
    context: context,
    builder: (_) => TunnelDiagnosticsDialog(name: name, client: client),
  );
}

class TunnelDiagnosticsDialog extends ConsumerStatefulWidget {
  const TunnelDiagnosticsDialog({
    super.key,
    required this.name,
    required this.client,
  });

  final String name;
  final TunnelEngine client;

  @override
  ConsumerState<TunnelDiagnosticsDialog> createState() =>
      _TunnelDiagnosticsState();
}

class _TunnelDiagnosticsState extends ConsumerState<TunnelDiagnosticsDialog> {
  CancelToken? _cancel;
  TunnelDiagnostic? _result;
  String? _error;
  bool _loading = true;
  bool _instanceChanged = false;

  bool get _current {
    final connection = ref.read(clientProvider);
    return !connection.isLoading &&
        identical(connection.valueOrNull, widget.client);
  }

  @override
  void initState() {
    super.initState();
    _run();
  }

  @override
  void dispose() {
    _cancel?.cancel('诊断窗口已关闭');
    super.dispose();
  }

  Future<void> _run() async {
    final cancel = CancelToken();
    _cancel = cancel;
    setState(() {
      _loading = true;
      _result = null;
      _error = null;
    });
    try {
      final result = await widget.client.diagnoseTunnel(
        widget.name,
        cancelToken: cancel,
      );
      if (!mounted || cancel.isCancelled) return;
      if (!_current) {
        _changedInstance();
        return;
      }
      setState(() => _result = result);
    } catch (error) {
      if (mounted && !cancel.isCancelled) {
        if (!_current) {
          _changedInstance();
          return;
        }
        setState(() => _error = describeError(error));
      }
    } finally {
      if (mounted && identical(_cancel, cancel)) {
        setState(() => _loading = false);
      }
    }
  }

  void _changedInstance() {
    _cancel?.cancel('实例连接已变化');
    setState(() {
      _instanceChanged = true;
      _loading = false;
      _result = null;
      _error = '实例连接已变化，请关闭后重新诊断';
    });
  }

  @override
  Widget build(BuildContext context) {
    ref.listen(clientProvider, (_, next) {
      if (!_instanceChanged &&
          (next.isLoading || !identical(next.valueOrNull, widget.client))) {
        _changedInstance();
      }
    });
    final theme = Theme.of(context);
    final result = _result;
    return AlertDialog(
      title: Text('诊断连接 · ${widget.name}'),
      content: SizedBox(
        width: 620,
        child: ConstrainedBox(
          constraints: BoxConstraints(
            maxHeight: MediaQuery.sizeOf(context).height * 0.6,
          ),
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                if (_loading)
                  const Padding(
                    padding: EdgeInsets.symmetric(vertical: 24),
                    child: Row(
                      children: [
                        SizedBox(
                          width: 22,
                          height: 22,
                          child: CircularProgressIndicator(),
                        ),
                        SizedBox(width: 16),
                        Expanded(child: Text('正在检查监听端口、SSH 和目标服务…')),
                      ],
                    ),
                  ),
                if (_error != null)
                  SelectableText(
                    _error!,
                    style: TextStyle(color: theme.colorScheme.error),
                  ),
                if (result != null) ...[
                  Text(
                    '${result.summary} · ${result.elapsedMs} ms',
                    style: theme.textTheme.titleMedium,
                  ),
                  const SizedBox(height: 12),
                  for (final check in result.checks)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 16),
                      child: Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Icon(
                            switch (check.status) {
                              'ok' => Icons.check_circle_outline,
                              'failed' => Icons.error_outline,
                              _ => Icons.remove_circle_outline,
                            },
                            color: check.status == 'failed'
                                ? theme.colorScheme.error
                                : null,
                          ),
                          const SizedBox(width: 12),
                          Expanded(
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text(
                                  '${check.label} · ${check.statusLabel} · ${check.elapsedMs} ms',
                                  style: theme.textTheme.titleSmall,
                                ),
                                if (check.address.isNotEmpty)
                                  SelectableText(check.address),
                                Text(check.message),
                                if (check.error.isNotEmpty)
                                  SelectableText(
                                    check.error,
                                    style: theme.textTheme.bodySmall?.copyWith(
                                      color: theme.colorScheme.error,
                                    ),
                                  ),
                              ],
                            ),
                          ),
                        ],
                      ),
                    ),
                ],
                const SizedBox(height: 8),
                const Text('仅检查 TCP 可达性，不验证数据库账号或业务协议。诊断不会启动或停止隧道。'),
              ],
            ),
          ),
        ),
      ),
      actions: [
        if (result != null)
          TextButton.icon(
            onPressed: () async {
              try {
                await Clipboard.setData(ClipboardData(text: result.toReport()));
              } catch (error) {
                if (context.mounted) showErrorSnack(context, error);
              }
            },
            icon: const Icon(Icons.copy),
            label: const Text('复制报告'),
          ),
        TextButton(
          onPressed: _loading || _instanceChanged ? null : _run,
          child: const Text('重新诊断'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(context),
          child: Text(_loading ? '取消' : '关闭'),
        ),
      ],
    );
  }
}
