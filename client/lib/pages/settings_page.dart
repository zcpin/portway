import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../services/settings_store.dart';
import 'config_transfer_panel.dart';

/// 左侧导航「设置」页。
///
/// 包含两类配置：
///   - 关闭窗口行为的默认动作（客户端本地，存于 ~/.ssh-tunnel/client_settings.json）
///   - 隧道重连的全局默认值（daemon 配置，未单独设置的隧道回退使用）
class SettingsPage extends ConsumerStatefulWidget {
  const SettingsPage({super.key});

  @override
  ConsumerState<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends ConsumerState<SettingsPage> {
  static const _logLevels = ['debug', 'info', 'warn', 'error'];

  late final TextEditingController _intervalCtrl;
  late final TextEditingController _maxAttemptsCtrl;

  String _strategy = 'fixed';
  String _logLevel = 'info';
  CloseAction _closeAction = CloseAction.ask;
  bool _dirty = false;
  bool _seeding = false;
  bool _saving = false;

  @override
  void initState() {
    super.initState();
    _intervalCtrl = TextEditingController();
    _maxAttemptsCtrl = TextEditingController();

    // 关闭动作是本地设置，先加载出来再渲染
    SettingsStore.instance.load().then((_) {
      if (!mounted) return;
      setState(() => _closeAction = SettingsStore.instance.closeAction);
    });

    _intervalCtrl.addListener(_markDirty);
    _maxAttemptsCtrl.addListener(_markDirty);
  }

  @override
  void dispose() {
    _intervalCtrl.dispose();
    _maxAttemptsCtrl.dispose();
    super.dispose();
  }

  void _markDirty() {
    // 回填表单时也会触发 listener，此时不算用户编辑
    if (_seeding || _dirty) return;
    setState(() => _dirty = true);
  }

  /// 从 daemon 回填表单；仅在用户尚未编辑时执行（build 期间调用，不应触发 setState）。
  void _seedFrom(GlobalSettings s) {
    if (_dirty) return;
    _seeding = true;
    _strategy = s.reconnectStrategy == 'exponential' ? 'exponential' : 'fixed';
    _logLevel = _logLevels.contains(s.logLevel) ? s.logLevel : 'info';
    _intervalCtrl.text = s.reconnectInterval;
    _maxAttemptsCtrl.text = s.maxReconnectAttempts.toString();
    _seeding = false;
  }

  Future<void> _save() async {
    if (_saving) return;
    setState(() => _saving = true);
    try {
      final maxAttempts = int.tryParse(_maxAttemptsCtrl.text.trim()) ?? 0;
      await ref.read(globalSettingsProvider.notifier).save(GlobalSettings(
            logLevel: _logLevel,
            reconnectStrategy: _strategy,
            reconnectInterval: _intervalCtrl.text.trim().isEmpty
                ? '5s'
                : _intervalCtrl.text.trim(),
            maxReconnectAttempts: maxAttempts < 0 ? 0 : maxAttempts,
          ));
      if (!mounted) return;
      setState(() => _dirty = false);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('设置已保存')),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('保存失败：$e')),
        );
      }
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final globals = ref.watch(globalSettingsProvider);

    return SingleChildScrollView(
      padding: const EdgeInsets.all(24),
      child: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 720),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text('设置', style: theme.textTheme.headlineSmall),
              const SizedBox(height: 20),

              _Card(
                title: '关闭窗口行为',
                subtitle: '点击窗口关闭按钮时的默认动作，不依赖 daemon。',
                child: RadioGroup<CloseAction>(
                  groupValue: _closeAction,
                  onChanged: (v) {
                    if (v == null) return;
                    setState(() => _closeAction = v);
                    SettingsStore.instance.setCloseAction(v);
                  },
                  child: Column(
                    children: [
                      for (final (action, label, desc) in [
                        (CloseAction.ask, '总是询问', '每次关闭都弹出确认对话框'),
                        (CloseAction.tray, '收进托盘', '直接最小化到系统托盘，隧道不受影响'),
                        (CloseAction.quit, '退出程序', '直接完全退出客户端（daemon 仍继续运行）'),
                      ])
                        RadioListTile<CloseAction>(
                          value: action,
                          title: Text(label),
                          subtitle: Text(desc),
                        ),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: 16),

              _Card(
                title: '隧道重连（全局默认值）',
                subtitle:
                    '单个隧道未单独设置重连字段时回退使用这些值。'
                    '保存后，套用了新默认值的运行中隧道会被重启以应用新配置。',
                child: globals.when(
                  loading: () =>
                      const Center(child: Padding(
                        padding: EdgeInsets.all(16),
                        child: CircularProgressIndicator(),
                      )),
                  error: (e, _) => Row(
                    children: [
                      Icon(Icons.cloud_off,
                          color: theme.colorScheme.error, size: 20),
                      const SizedBox(width: 8),
                      const Expanded(child: Text('daemon 未连接，重连配置不可用')),
                    ],
                  ),
                  data: (s) {
                    _seedFrom(s);
                    return Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        _Label('重连策略'),
                        SegmentedButton<String>(
                          segments: const [
                            ButtonSegment(
                                value: 'fixed',
                                label: Text('固定间隔'),
                                icon: Icon(Icons.timer_outlined)),
                            ButtonSegment(
                                value: 'exponential',
                                label: Text('指数退避'),
                                icon: Icon(Icons.trending_up)),
                          ],
                          selected: {_strategy},
                          onSelectionChanged: (v) {
                            setState(() {
                              _strategy = v.first;
                              _dirty = true;
                            });
                          },
                        ),
                        const SizedBox(height: 16),
                        _Label('重连间隔'),
                        TextField(
                          controller: _intervalCtrl,
                          decoration: const InputDecoration(
                            hintText: '如 5s / 30s / 2m',
                            suffixText: '间隔需大于 0',
                          ),
                        ),
                        const SizedBox(height: 16),
                        _Label('最大重连次数'),
                        TextField(
                          controller: _maxAttemptsCtrl,
                          keyboardType: TextInputType.number,
                          decoration: const InputDecoration(
                            hintText: '0',
                            suffixText: '0 = 无限重试',
                          ),
                        ),
                        const SizedBox(height: 16),
                        _Label('日志级别'),
                        DropdownButton<String>(
                          value: _logLevel,
                          isExpanded: true,
                          items: [
                            for (final level in _logLevels)
                              DropdownMenuItem(
                                  value: level, child: Text(level)),
                          ],
                          onChanged: (v) {
                            if (v == null) return;
                            setState(() {
                              _logLevel = v;
                              _dirty = true;
                            });
                          },
                        ),
                        const SizedBox(height: 20),
                        Row(
                          children: [
                            FilledButton.icon(
                              onPressed: _saving || !_dirty ? null : _save,
                              icon: _saving
                                  ? const SizedBox(
                                      width: 16,
                                      height: 16,
                                      child:
                                          CircularProgressIndicator(strokeWidth: 2),
                                    )
                                  : const Icon(Icons.save_outlined),
                              label: Text(_saving ? '保存中…' : '保存'),
                            ),
                            const SizedBox(width: 12),
                            if (_dirty)
                              TextButton(
                                onPressed: () {
                                  setState(() => _dirty = false);
                                  final current =
                                      ref.read(globalSettingsProvider).valueOrNull;
                                  if (current != null) _seedFrom(current);
                                },
                                child: const Text('撤销修改'),
                              ),
                          ],
                        ),
                      ],
                    );
                  },
                ),
              ),
              const SizedBox(height: 16),
              _Card(
                title: '配置导入与备份',
                subtitle: '导入前校验并展示变更，应用时备份原配置。',
                child: ConfigTransferPanel(onApplied: () => setState(() => _dirty = false)),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _Card extends StatelessWidget {
  const _Card({required this.title, required this.subtitle, required this.child});

  final String title;
  final String subtitle;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title, style: theme.textTheme.titleMedium),
            const SizedBox(height: 4),
            Text(subtitle, style: theme.textTheme.bodySmall),
            const SizedBox(height: 16),
            child,
          ],
        ),
      ),
    );
  }
}

class _Label extends StatelessWidget {
  const _Label(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: Text(text, style: Theme.of(context).textTheme.bodyMedium),
    );
  }
}
