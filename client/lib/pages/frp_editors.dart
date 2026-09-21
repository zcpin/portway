import 'package:flutter/material.dart';

import '../models/frp.dart';

/// 新建 / 编辑 FRP 客户端的对话框。
///
/// 名称对应配置文件名，因此编辑时不可修改——改名需要重命名文件，
/// 让用户「删除后重建」比做一个隐式的重命名更不容易出错。
class FrpClientEditorDialog extends StatefulWidget {
  const FrpClientEditorDialog({super.key, this.editing});

  final FrpClient? editing;

  @override
  State<FrpClientEditorDialog> createState() => _FrpClientEditorDialogState();
}

class _FrpClientEditorDialogState extends State<FrpClientEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _group;
  late final TextEditingController _serverAddr;
  late final TextEditingController _serverPort;
  late final TextEditingController _token;
  late String _authMethod;
  late bool _tlsEnable;
  late bool _autoStart;

  bool get _isEditing => widget.editing != null;

  @override
  void initState() {
    super.initState();
    final editing = widget.editing;
    _name = TextEditingController(text: editing?.name ?? '');
    _group = TextEditingController(text: editing?.group ?? '');
    _serverAddr = TextEditingController(text: editing?.serverAddr ?? '');
    _serverPort = TextEditingController(text: '${editing?.serverPort ?? 7000}');
    _token = TextEditingController(text: editing?.authToken ?? '');
    _authMethod = editing?.authMethod.isNotEmpty == true ? editing!.authMethod : 'token';
    // 新建时默认开启 TLS：frp 0.50 之后这也是它自己的默认值。
    _tlsEnable = editing?.tlsEnable ?? true;
    _autoStart = editing?.autoStart ?? true;
  }

  @override
  void dispose() {
    _name.dispose();
    _group.dispose();
    _serverAddr.dispose();
    _serverPort.dispose();
    _token.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text(_isEditing ? '编辑 FRP 客户端' : '新建 FRP 客户端'),
      content: SizedBox(
        width: 420,
        child: SingleChildScrollView(
          child: Form(
            key: _formKey,
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                TextFormField(
                  controller: _name,
                  enabled: !_isEditing,
                  decoration: InputDecoration(
                    labelText: '名称',
                    helperText: _isEditing ? '名称对应配置文件名，如需修改请删除后重建' : '同时用作配置文件名',
                  ),
                  validator: (value) => _validateName(value),
                ),
                const SizedBox(height: 12),
                TextFormField(
                  controller: _group,
                  decoration: const InputDecoration(labelText: '分组（可选）'),
                ),
                const SizedBox(height: 12),
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Expanded(
                      flex: 3,
                      child: TextFormField(
                        controller: _serverAddr,
                        decoration: const InputDecoration(labelText: '服务器地址'),
                        validator: (value) =>
                            (value ?? '').trim().isEmpty ? '请填写 frps 地址' : null,
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      flex: 2,
                      child: TextFormField(
                        controller: _serverPort,
                        decoration: const InputDecoration(labelText: '端口'),
                        keyboardType: TextInputType.number,
                        validator: (value) => _validatePort(value, '服务器端口'),
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 12),
                DropdownButtonFormField<String>(
                  initialValue: _authMethod,
                  decoration: const InputDecoration(labelText: '认证方式'),
                  items: const [
                    DropdownMenuItem(value: 'token', child: Text('令牌（token）')),
                    DropdownMenuItem(value: 'oidc', child: Text('OIDC')),
                  ],
                  onChanged: (value) => setState(() => _authMethod = value ?? 'token'),
                ),
                const SizedBox(height: 12),
                if (_authMethod == 'token')
                  TextFormField(
                    controller: _token,
                    obscureText: true,
                    decoration: const InputDecoration(
                      labelText: '令牌',
                      helperText: '与服务端 frps 的 token 一致，留空表示不校验',
                    ),
                  )
                else
                  const Padding(
                    padding: EdgeInsets.symmetric(vertical: 6),
                    child: Text('OIDC 的 issuer、clientId 等参数沿用配置文件里的设置，界面暂不支持编辑。'),
                  ),
                const SizedBox(height: 4),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('启用 TLS'),
                  subtitle: const Text('与 frps 之间加密传输，frp 0.50 起默认开启'),
                  value: _tlsEnable,
                  onChanged: (value) => setState(() => _tlsEnable = value),
                ),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('随引擎自动启动'),
                  subtitle: const Text('关闭后只能手动启动'),
                  value: _autoStart,
                  onChanged: (value) => setState(() => _autoStart = value),
                ),
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(context), child: const Text('取消')),
        FilledButton(
          onPressed: _submit,
          child: const Text('保存'),
        ),
      ],
    );
  }

  void _submit() {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    Navigator.pop(
      context,
      FrpClientPayload(
        name: _name.text.trim(),
        group: _group.text.trim(),
        autoStart: _autoStart,
        serverAddr: _serverAddr.text.trim(),
        serverPort: int.tryParse(_serverPort.text.trim()) ?? 7000,
        authMethod: _authMethod,
        authToken: _token.text,
        tlsEnable: _tlsEnable,
      ),
    );
  }
}

/// 新建 / 编辑代理的对话框。
///
/// 阶段 1 只提供普通代理（tcp / udp / http / https / tcpmux）。
/// 编辑已有代理时类型不可更改：换类型要重建整个代理配置，删除后新建更清楚。
class FrpProxyEditorDialog extends StatefulWidget {
  const FrpProxyEditorDialog({super.key, required this.client, this.editing});

  final String client;
  final FrpProxy? editing;

  @override
  State<FrpProxyEditorDialog> createState() => _FrpProxyEditorDialogState();
}

class _FrpProxyEditorDialogState extends State<FrpProxyEditorDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _localIp;
  late final TextEditingController _localPort;
  late final TextEditingController _remotePort;
  late final TextEditingController _domains;
  late final TextEditingController _subdomain;
  late String _type;
  late bool _enabled;

  bool get _isEditing => widget.editing != null;

  /// 远端端口只对 tcp / udp 有意义。
  bool get _usesRemotePort => _type == 'tcp' || _type == 'udp';

  /// 域名相关字段只对 http / https / tcpmux 有意义。
  bool get _usesDomain => _type == 'http' || _type == 'https' || _type == 'tcpmux';

  @override
  void initState() {
    super.initState();
    final editing = widget.editing;
    _name = TextEditingController(text: editing?.name ?? '');
    _localIp = TextEditingController(text: editing?.localIp.isNotEmpty == true ? editing!.localIp : '127.0.0.1');
    _localPort = TextEditingController(text: editing == null || editing.localPort == 0 ? '' : '${editing.localPort}');
    _remotePort = TextEditingController(text: editing == null || editing.remotePort == 0 ? '' : '${editing.remotePort}');
    _domains = TextEditingController(text: editing?.customDomains.join(', ') ?? '');
    _subdomain = TextEditingController(text: editing?.subdomain ?? '');
    _type = editing?.type ?? 'tcp';
    _enabled = editing?.enabled ?? true;
  }

  @override
  void dispose() {
    _name.dispose();
    _localIp.dispose();
    _localPort.dispose();
    _remotePort.dispose();
    _domains.dispose();
    _subdomain.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text(_isEditing ? '编辑代理' : '新建代理'),
      content: SizedBox(
        width: 420,
        child: SingleChildScrollView(
          child: Form(
            key: _formKey,
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                TextFormField(
                  controller: _name,
                  decoration: const InputDecoration(
                    labelText: '代理名称',
                    helperText: '在 frps 上唯一标识这条代理',
                  ),
                  validator: (value) => _validateName(value),
                ),
                const SizedBox(height: 12),
                DropdownButtonFormField<String>(
                  initialValue: _type,
                  decoration: InputDecoration(
                    labelText: '类型',
                    helperText: _isEditing ? '类型不可修改，需要换类型请删除后重建' : null,
                  ),
                  items: [
                    for (final entry in frpProxyTypes.entries)
                      DropdownMenuItem(value: entry.key, child: Text(entry.value)),
                  ],
                  onChanged: _isEditing ? null : (value) => setState(() => _type = value ?? 'tcp'),
                ),
                const SizedBox(height: 12),
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Expanded(
                      flex: 3,
                      child: TextFormField(
                        controller: _localIp,
                        decoration: const InputDecoration(labelText: '本地地址'),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      flex: 2,
                      child: TextFormField(
                        controller: _localPort,
                        decoration: const InputDecoration(labelText: '本地端口'),
                        keyboardType: TextInputType.number,
                        validator: (value) => _validatePort(value, '本地端口', required: true),
                      ),
                    ),
                  ],
                ),
                if (_usesRemotePort) ...[
                  const SizedBox(height: 12),
                  TextFormField(
                    controller: _remotePort,
                    decoration: const InputDecoration(
                      labelText: '远端端口',
                      helperText: 'frps 上对外暴露的端口，留空由服务端分配',
                    ),
                    keyboardType: TextInputType.number,
                    validator: (value) => _validatePort(value, '远端端口'),
                  ),
                ],
                if (_usesDomain) ...[
                  const SizedBox(height: 12),
                  TextFormField(
                    controller: _domains,
                    decoration: const InputDecoration(
                      labelText: '自定义域名',
                      helperText: '多个域名用逗号分隔',
                    ),
                  ),
                  const SizedBox(height: 12),
                  TextFormField(
                    controller: _subdomain,
                    decoration: const InputDecoration(labelText: '子域名'),
                  ),
                ],
                const SizedBox(height: 4),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('启用'),
                  subtitle: const Text('停用的代理保留在配置里，但不向服务端注册'),
                  value: _enabled,
                  onChanged: (value) => setState(() => _enabled = value),
                ),
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(onPressed: () => Navigator.pop(context), child: const Text('取消')),
        FilledButton(onPressed: _submit, child: const Text('保存')),
      ],
    );
  }

  void _submit() {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    Navigator.pop(
      context,
      FrpProxyPayload(
        name: _name.text.trim(),
        type: _type,
        enabled: _enabled,
        localIp: _localIp.text.trim(),
        localPort: int.tryParse(_localPort.text.trim()) ?? 0,
        remotePort: _usesRemotePort ? (int.tryParse(_remotePort.text.trim()) ?? 0) : 0,
        customDomains: _usesDomain ? _splitDomains(_domains.text) : const [],
        subdomain: _usesDomain ? _subdomain.text.trim() : '',
      ),
    );
  }
}

String? _validateName(String? value) {
  final name = (value ?? '').trim();
  if (name.isEmpty) return '请填写名称';
  if (name.length > 64) return '名称不能超过 64 个字符';
  if (name.startsWith('.')) return '名称不能以点开头';
  if (name.contains(RegExp(r'[/\:*?"<>|]'))) return r'名称不能包含 / \ : * ? " < > |';
  return null;
}

String? _validatePort(String? value, String label, {bool required = false}) {
  final text = (value ?? '').trim();
  if (text.isEmpty) return required ? '请填写$label' : null;
  final port = int.tryParse(text);
  if (port == null) return '$label必须是数字';
  if (port < (required ? 1 : 0) || port > 65535) return '$label必须在 1-65535 之间';
  return null;
}

List<String> _splitDomains(String text) => text
    .split(RegExp(r'[,\s，]+'))
    .map((domain) => domain.trim())
    .where((domain) => domain.isNotEmpty)
    .toList();
