import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../providers.dart';
import '../services/daemon_discovery.dart';
import '../services/updates.dart';

class UpdatePanel extends ConsumerStatefulWidget {
  const UpdatePanel({super.key, this.onQuit});
  final Future<void> Function()? onQuit;

  @override
  ConsumerState<UpdatePanel> createState() => _UpdatePanelState();
}

class _UpdatePanelState extends ConsumerState<UpdatePanel> {
  UpdateInfo? _info;
  UpdateCheck? _check;
  UpdateDownload? _download;
  String _channel = 'stable';
  String? _error;
  String? _busy;

  @override
  void initState() {
    super.initState();
    _perform('读取版本信息', () async {
      final info = await ref.read(updateServiceProvider).info();
      if (mounted) setState(() => _info = info);
    });
  }

  Future<void> _perform(String label, Future<void> Function() action) async {
    if (_busy != null) return;
    setState(() {
      _busy = label;
      _error = null;
    });
    try {
      await action();
    } catch (error) {
      if (mounted) setState(() => _error = error.toString());
    } finally {
      if (mounted) setState(() => _busy = null);
    }
  }

  Future<void> _checkUpdates() => _perform('检查更新', () async {
    final service = ref.read(updateServiceProvider);
    final info = await service.info();
    final check = await service.check(_channel);
    if (!mounted) return;
    setState(() {
      _info = info;
      _check = check;
      _download = null;
    });
  });

  Future<void> _downloadPackage(String kind) => _perform('下载并校验', () async {
    setState(() => _download = null);
    final download = await ref
        .read(updateServiceProvider)
        .download(_check!.version, kind);
    if (mounted) setState(() => _download = download);
  });

  Future<void> _install() async {
    final download = _download;
    if (download == null || widget.onQuit == null) return;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('升级至 ${download.version}？'),
        content: const Text(
          '程序将退出并重启，此安装运行的隧道会暂时中断。\n\n'
          '用户配置保持原位，旧程序会完整备份；新版本启动失败时自动恢复。'
          '请先关闭同一安装的其他客户端窗口。',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('退出并升级'),
          ),
        ],
      ),
    );
    if (confirmed != true || !mounted) return;
    await _perform('准备升级并退出', () async {
      final preferences = await ref.read(workspacesProvider.future);
      final paths = <String>{
        ...DaemonDiscovery.candidatePaths,
        if (preferences.selectedPath.isNotEmpty) preferences.selectedPath,
        ...preferences.workspaces.map((workspace) => workspace.discoveryPath),
      }.toList();
      await ref
          .read(updateServiceProvider)
          .install(download, paths, widget.onQuit!);
    });
  }

  @override
  Widget build(BuildContext context) {
    final info = _info;
    final check = _check;
    final download = _download;
    final disabled = _busy != null;
    final last = info?.lastResult;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (info != null) ...[
          Text('当前版本 ${info.version} · ${info.platform} ${info.arch}'),
          const SizedBox(height: 4),
          Text('发布来源：${info.repository}'),
          if (!info.portable)
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: Text(info.reason),
            ),
          if (last != null)
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: SelectableText(switch (last['status']) {
                'updated' =>
                  '上次升级成功：${last['version']}\n原程序备份：${last['backup']}',
                'rolled_back' => '上次升级未完成，已恢复原程序。${last['error'] ?? ''}',
                'recovery_required' =>
                  '需要恢复原程序备份：${last['backup']}\n${last['error'] ?? ''}',
                _ => '上次升级未修改程序。${last['error'] ?? ''}',
              }),
            ),
          const SizedBox(height: 16),
        ],
        Wrap(
          spacing: 12,
          runSpacing: 12,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            SegmentedButton<String>(
              segments: const [
                ButtonSegment(value: 'stable', label: Text('稳定版')),
                ButtonSegment(value: 'prerelease', label: Text('含预发布版')),
              ],
              selected: {_channel},
              onSelectionChanged: disabled
                  ? null
                  : (value) => setState(() {
                      _channel = value.single;
                      _check = null;
                      _download = null;
                      _error = null;
                    }),
            ),
            OutlinedButton.icon(
              onPressed: disabled ? null : _checkUpdates,
              icon: const Icon(Icons.refresh),
              label: const Text('检查更新'),
            ),
          ],
        ),
        if (_busy != null)
          Padding(
            padding: const EdgeInsets.only(top: 12),
            child: Row(
              children: [
                const SizedBox(
                  width: 18,
                  height: 18,
                  child: CircularProgressIndicator(strokeWidth: 2),
                ),
                const SizedBox(width: 12),
                Text('$_busy…'),
              ],
            ),
          ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.only(top: 12),
            child: SelectableText(
              _error!,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        if (check != null) ...[
          const SizedBox(height: 16),
          Text(
            check.release == null
                ? '该渠道暂无可用版本。'
                : !check.currentKnown
                ? '最新版本 ${check.version}；当前构建无法比较版本。'
                : check.available
                ? '发现新版本 ${check.version}'
                : '当前版本已是该渠道的最新版本。',
          ),
          if (check.release != null)
            TextButton.icon(
              onPressed: () async {
                await Clipboard.setData(
                  ClipboardData(text: check.release!['url'] as String),
                );
                if (context.mounted) {
                  ScaffoldMessenger.of(
                    context,
                  ).showSnackBar(const SnackBar(content: Text('已复制发布页面链接')));
                }
              },
              icon: const Icon(Icons.link),
              label: const Text('复制发布页面链接'),
            ),
          if (check.release != null &&
              !check.hasPortable &&
              !check.hasInstaller)
            const Text('该版本没有适合当前平台且附带校验文件的安装包。'),
          Wrap(
            spacing: 12,
            runSpacing: 12,
            children: [
              if (check.hasPortable)
                FilledButton.tonalIcon(
                  onPressed: disabled
                      ? null
                      : () => _downloadPackage('portable'),
                  icon: const Icon(Icons.download_outlined),
                  label: const Text('下载并校验便携包'),
                ),
              if (check.hasInstaller)
                FilledButton.tonalIcon(
                  onPressed: disabled
                      ? null
                      : () => _downloadPackage('installer'),
                  icon: const Icon(Icons.download_outlined),
                  label: const Text('下载并校验安装包'),
                ),
            ],
          ),
        ],
        if (download != null) ...[
          const SizedBox(height: 16),
          const Text('下载完成，SHA256 校验通过。'),
          SelectableText(download.path),
          const SizedBox(height: 4),
          SelectableText(
            'SHA256：${download.sha256}',
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 12),
          Wrap(
            spacing: 12,
            runSpacing: 12,
            children: [
              OutlinedButton.icon(
                onPressed: disabled
                    ? null
                    : () => _perform(
                        '打开下载目录',
                        () => ref
                            .read(updateServiceProvider)
                            .showDownload(download),
                      ),
                icon: const Icon(Icons.folder_open),
                label: const Text('打开下载目录'),
              ),
              if (info?.portable == true &&
                  download.kind == 'portable' &&
                  check?.available == true &&
                  widget.onQuit != null)
                FilledButton.icon(
                  onPressed: disabled ? null : _install,
                  icon: const Icon(Icons.system_update_alt),
                  label: const Text('升级便携版'),
                ),
            ],
          ),
          if (download.kind == 'installer')
            const Padding(
              padding: EdgeInsets.only(top: 8),
              child: Text('退出客户端后运行安装包。若使用系统服务，请先停止服务，完成安装后重新启动。'),
            )
          else if (info?.portable != true)
            const Padding(
              padding: EdgeInsets.only(top: 8),
              child: Text('请解压到新的程序目录后运行；已有用户配置可继续使用。系统服务需先停止，再按原安装方式替换。'),
            ),
        ],
      ],
    );
  }
}
