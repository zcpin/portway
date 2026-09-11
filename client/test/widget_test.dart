// 冒烟测试：验证应用能够构建出主界面。
//
// 客户端依赖本地 daemon，测试中不会真正连接，因此只校验骨架可渲染。
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'package:ssh_tunnel_client/main.dart';

void main() {
  testWidgets('app shell renders', (WidgetTester tester) async {
    await tester.pumpWidget(const ProviderScope(child: SshTunnelApp()));
    await tester.pump();

    // 主界面应包含导航栏的四个入口
    expect(find.text('隧道'), findsOneWidget);
    expect(find.text('SSH 连接'), findsOneWidget);
    expect(find.text('密钥'), findsOneWidget);
    expect(find.text('日志'), findsOneWidget);
  });
}
