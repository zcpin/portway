// 冒烟测试：验证应用能够构建出主界面。
//
// 客户端依赖本地 daemon，测试中不会真正连接，因此只校验骨架可渲染。
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'package:portway/main.dart';
import 'package:portway/providers.dart';
import 'package:portway/services/workspaces.dart';

class _EmptyWorkspaces extends WorkspacesNotifier {
  @override
  Future<WorkspacePreferences> build() async => const WorkspacePreferences();
}

void main() {
  testWidgets('app shell renders', (WidgetTester tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          clientProvider.overrideWith((ref) async => null),
          discoveryProvider.overrideWith((ref) async => []),
          workspacesProvider.overrideWith(_EmptyWorkspaces.new),
        ],
        child: const PortwayApp(),
      ),
    );
    await tester.pump();

    // 主界面应包含导航栏的四个入口
    expect(find.text('隧道'), findsOneWidget);
    expect(find.text('SSH 连接'), findsOneWidget);
    expect(find.text('密钥'), findsOneWidget);
    expect(find.text('日志'), findsOneWidget);
  });
}
