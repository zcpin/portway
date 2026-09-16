import 'dart:convert';
import 'dart:typed_data';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models.dart';
import '../providers.dart';
import '../services/tunnel_engine.dart';
import '../widgets.dart';

class ConfigTransferPanel extends ConsumerStatefulWidget {
  const ConfigTransferPanel({super.key, this.onApplied});
  final VoidCallback? onApplied;

  @override
  ConsumerState<ConfigTransferPanel> createState() =>
      _ConfigTransferPanelState();
}

class _ConfigTransferPanelState extends ConsumerState<ConfigTransferPanel> {
  static const _maxBytes = 256 * 1024;
  final _content = TextEditingController();
  String _mode = 'merge';
  ImportPreview? _preview;
  List<ConfigBackup> _backups = [];
  bool _busy = false;

  @override
  void dispose() {
    _content.dispose();
    super.dispose();
  }

  Future<void> _run(Future<void> Function(TunnelEngine) action) async {
    if (_busy) return;
    setState(() => _busy = true);
    try {
      final client = await ref.read(clientProvider.future);
      if (!mounted) return;
      if (client == null) throw StateError('未连接到 daemon');
      await action(client);
    } catch (error) {
      if (mounted) showErrorSnack(context, error);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _pickImport() => _run((client) async {
    final file = await FilePicker.pickFile(
      dialogTitle: '选择隧道配置',
      type: FileType.custom,
      allowedExtensions: ['toml'],
    );
    if (file == null || !mounted) return;
    if ((file.lengthSync() ?? 0) > _maxBytes) {
      throw StateError('配置文件不能超过 256 KiB');
    }
    final data = BytesBuilder(copy: false);
    await for (final chunk in file.readAsByteStream()) {
      if (data.length + chunk.length > _maxBytes) {
        throw StateError('配置文件不能超过 256 KiB');
      }
      data.add(chunk);
    }
    final content = utf8.decode(data.takeBytes());
    if (!mounted) return;
    setState(() {
      _content.text = content;
      _preview = null;
    });
  });

  Future<void> _export() => _run((client) async {
    final exported = await client.exportConfig();
    if (!mounted) return;
    final saved = await FilePicker.saveFile(
      fileName: 'ssh-tunnel.toml',
      bytes: Uint8List.fromList(utf8.encode(exported.content)),
      mimeType: 'application/toml',
      dialogTitle: '导出隧道配置',
    );
    if (saved != null && mounted) {
      ScaffoldMessenger.of(
        context,
      ).showSnackBar(const SnackBar(content: Text('配置已导出')));
    }
  });

  Future<void> _makePreview() => _run((client) async {
    setState(() => _preview = null);
    if (utf8.encode(_content.text).length > _maxBytes) {
      throw StateError('配置不能超过 256 KiB');
    }
    final preview = await client.previewImport(_content.text, _mode);
    if (mounted) setState(() => _preview = preview);
  });

  Future<void> _apply() => _run((client) async {
    final preview = _preview;
    if (preview == null) return;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('确认导入配置'),
        content: Text(
          '将应用预览中的 ${preview.changes.length} 项变更，并先备份当前配置。'
          '${_mode == 'replace' ? '\n未包含在导入内容中的隧道和连接会被移除。' : ''}',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('确认导入'),
          ),
        ],
      ),
    );
    if (confirmed != true || !mounted) return;
    final backup = await client.importConfig(
      _content.text,
      _mode,
      preview.revision,
    );
    if (!mounted) return;
    setState(() {
      _preview = null;
      _backups = [backup, ..._backups];
    });
    widget.onApplied?.call();
    ref.invalidate(tunnelsProvider);
    ref.invalidate(sshConnectionsProvider);
    ref.invalidate(keysProvider);
    ref.invalidate(globalSettingsProvider);
    ScaffoldMessenger.of(
      context,
    ).showSnackBar(const SnackBar(content: Text('配置已导入，原配置已备份')));
  });

  Future<void> _loadBackups() => _run((client) async {
    final backups = await client.listConfigBackups();
    if (mounted) setState(() => _backups = backups);
  });

  Future<void> _restore(ConfigBackup backup) => _run((client) async {
    final content = await client.readConfigBackup(backup.name);
    final preview = await client.previewImport(content, 'replace');
    if (mounted) {
      setState(() {
        _content.text = content;
        _mode = 'replace';
        _preview = preview;
      });
    }
  });

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            OutlinedButton.icon(
              onPressed: _busy ? null : _export,
              icon: const Icon(Icons.download),
              label: const Text('导出配置'),
            ),
            OutlinedButton.icon(
              onPressed: _busy ? null : _pickImport,
              icon: const Icon(Icons.upload_file),
              label: const Text('读取 TOML 文件'),
            ),
            TextButton(
              onPressed: _busy ? null : _loadBackups,
              child: const Text('查看备份'),
            ),
          ],
        ),
        const SizedBox(height: 12),
        const Text('配置只包含私钥路径。相对路径以当前 daemon 的配置目录为基准。'),
        const SizedBox(height: 8),
        TextField(
          controller: _content,
          enabled: !_busy,
          minLines: 5,
          maxLines: 8,
          key: const Key('config-import-content'),
          decoration: const InputDecoration(
            labelText: '粘贴或读取 TOML 配置',
            border: OutlineInputBorder(),
          ),
          onChanged: (_) => setState(() => _preview = null),
        ),
        const SizedBox(height: 8),
        DropdownButton<String>(
          value: _mode,
          isExpanded: true,
          items: const [
            DropdownMenuItem(value: 'merge', child: Text('合并配置（覆盖同名项）')),
            DropdownMenuItem(value: 'replace', child: Text('替换完整配置')),
          ],
          onChanged: _busy
              ? null
              : (mode) => setState(() {
                  _mode = mode!;
                  _preview = null;
                }),
        ),
        Wrap(
          spacing: 12,
          runSpacing: 8,
          children: [
            OutlinedButton(
              onPressed: _busy ? null : _makePreview,
              child: const Text('预览变更'),
            ),
            FilledButton(
              onPressed: _busy || _preview == null ? null : _apply,
              child: Text(_busy ? '处理中…' : '应用导入'),
            ),
          ],
        ),
        if (_preview != null) ...[
          const SizedBox(height: 12),
          Text(_preview!.changes.isEmpty ? '配置没有变化' : '将应用以下变更：'),
          for (final change in _preview!.changes)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(change.label),
            ),
        ],
        if (_backups.isNotEmpty) ...[
          const Divider(height: 28),
          const Text('配置备份（恢复前会先预览）'),
          for (final backup in _backups)
            ListTile(
              contentPadding: EdgeInsets.zero,
              title: Text(
                DateTime.tryParse(backup.createdAt)?.toLocal().toString() ??
                    backup.createdAt,
              ),
              subtitle: Text('${backup.size} 字节'),
              trailing: TextButton(
                onPressed: _busy ? null : () => _restore(backup),
                child: const Text('预览恢复'),
              ),
            ),
        ],
      ],
    );
  }
}
