import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../providers.dart';

/// GitHub 仓库地址。占位：发布后替换为真实地址。
const String kRepoUrl = 'https://github.com/zcpin/portway';

/// 左侧导航「关于」页。
class AboutPage extends ConsumerWidget {
  const AboutPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final client = ref.watch(clientProvider).valueOrNull;

    return SingleChildScrollView(
      padding: const EdgeInsets.all(24),
      child: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 720),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text('关于', style: theme.textTheme.headlineSmall),
              const SizedBox(height: 20),
              Card(
                margin: EdgeInsets.zero,
                child: Padding(
                  padding: const EdgeInsets.all(24),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          ClipRRect(
                            borderRadius: BorderRadius.circular(8),
                            child: Image.asset(
                              'assets/app_icon.png',
                              width: 40,
                              height: 40,
                              filterQuality: FilterQuality.medium,
                            ),
                          ),
                          const SizedBox(width: 16),
                          Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text('端口通',
                                  style: theme.textTheme.titleLarge),
                              const SizedBox(height: 2),
                              Text(
                                client == null
                                    ? '版本：-'
                                    : 'daemon 版本：${client.info.version}',
                                style: theme.textTheme.bodySmall,
                              ),
                            ],
                          ),
                        ],
                      ),
                      const SizedBox(height: 16),
                      Text(
                        '本地端口转发管理工具：SSH 隧道把远端端口映射到本机，'
                        'FRP 客户端把本地服务通过 frps 暴露出去。'
                        '核心逻辑在 Go 引擎里，客户端提供直观的界面进行管理。',
                        style: theme.textTheme.bodyMedium,
                      ),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: 16),
              Card(
                margin: EdgeInsets.zero,
                child: Padding(
                  padding: const EdgeInsets.all(24),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text('快速上手', style: theme.textTheme.titleMedium),
                      const SizedBox(height: 12),
                      _Step(index: 1, text: '在「SSH 连接」页添加远端主机与私钥。'),
                      _Step(index: 2, text: '在「隧道」页新建隧道：选择连接、本地端口与远端端口。'),
                      _Step(index: 3, text: '在「隧道」页启动隧道，随后即可连接本地端口使用。'),
                      _Step(index: 4, text: '关闭窗口会收进系统托盘，隧道仍持续运行；'
                          '默认行为可在「设置」页调整。'),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: 16),
              Card(
                margin: EdgeInsets.zero,
                child: Padding(
                  padding: const EdgeInsets.all(24),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text('项目地址', style: theme.textTheme.titleMedium),
                      const SizedBox(height: 12),
                      Row(
                        children: [
                          const Icon(Icons.link, size: 18),
                          const SizedBox(width: 8),
                          Expanded(
                            child: SelectableText(
                              kRepoUrl,
                              style: theme.textTheme.bodyMedium
                                  ?.copyWith(fontFamily: 'monospace'),
                            ),
                          ),
                          IconButton(
                            tooltip: '复制地址',
                            icon: const Icon(Icons.copy, size: 18),
                            onPressed: () {
                              Clipboard.setData(ClipboardData(text: kRepoUrl));
                              ScaffoldMessenger.of(context).showSnackBar(
                                const SnackBar(
                                    content: Text('仓库地址已复制到剪贴板')),
                              );
                            },
                          ),
                        ],
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _Step extends StatelessWidget {
  const _Step({required this.index, required this.text});

  final int index;
  final String text;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          CircleAvatar(
            radius: 12,
            backgroundColor: theme.colorScheme.primaryContainer,
            child: Text('$index',
                style: theme.textTheme.labelSmall?.copyWith(
                  color: theme.colorScheme.onPrimaryContainer,
                )),
          ),
          const SizedBox(width: 12),
          Expanded(child: Text(text, style: theme.textTheme.bodyMedium)),
        ],
      ),
    );
  }
}
