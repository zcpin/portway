import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:portway/services/settings_store.dart';

void main() {
  late Directory tempDir;

  setUp(() {
    tempDir = Directory.systemTemp.createTempSync('portway_settings_test_');
  });

  tearDown(() {
    if (tempDir.existsSync()) {
      tempDir.deleteSync(recursive: true);
    }
  });

  test('没有设置文件时默认「总是询问」', () async {
    final store = SettingsStore.forDir(tempDir.path);
    await store.load();
    expect(store.closeAction, CloseAction.ask);
  });

  test('修改关闭动作后写入磁盘，新实例能读回', () async {
    final store = SettingsStore.forDir(tempDir.path);
    await store.load();
    await store.setCloseAction(CloseAction.tray);
    expect(store.closeAction, CloseAction.tray);

    // 模拟重启：用独立实例重新从磁盘加载
    final reloaded = SettingsStore.forDir(tempDir.path);
    await reloaded.load();
    expect(reloaded.closeAction, CloseAction.tray);
  });

  test('文件被写坏时回退到默认值', () async {
    File('${tempDir.path}${Platform.pathSeparator}client_settings.json')
        .writeAsStringSync('not-json{{');
    final store = SettingsStore.forDir(tempDir.path);
    await store.load();
    expect(store.closeAction, CloseAction.ask);
  });

  test('未知字符串值回退到默认值', () async {
    final store = SettingsStore.forDir(tempDir.path);
    await store.setCloseAction(CloseAction.quit);

    File('${tempDir.path}${Platform.pathSeparator}client_settings.json')
        .writeAsStringSync('{"close_action":"mystery"}');
    final reloaded = SettingsStore.forDir(tempDir.path);
    await reloaded.load();
    expect(reloaded.closeAction, CloseAction.ask);
  });
}
