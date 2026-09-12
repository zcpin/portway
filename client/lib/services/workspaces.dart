import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'daemon_discovery.dart';

class Workspace {
  final String id;
  final String name;
  final String dataDir;
  const Workspace({
    required this.id,
    required this.name,
    required this.dataDir,
  });
  String get configPath => '$dataDir${Platform.pathSeparator}config.toml';
  String get discoveryPath => '$dataDir${Platform.pathSeparator}daemon.json';
  Map<String, dynamic> toJson() => {'id': id, 'name': name};
}

class WorkspacePreferences {
  final String selectedPath;
  final List<Workspace> workspaces;
  const WorkspacePreferences({
    this.selectedPath = '',
    this.workspaces = const [],
  });
}

class WorkspaceStore {
  WorkspaceStore({String? directory})
    : directory = directory ?? _defaultDirectory();
  final String directory;
  Future<void> _pending = Future<void>.value();
  static final _idPattern = RegExp(r'^[0-9a-f]{32}$');
  String get _filePath => '$directory${Platform.pathSeparator}workspaces.json';

  static String _defaultDirectory() {
    final home = DaemonDiscovery.homeDir;
    if (home == null || home.isEmpty) throw StateError('无法确定工作区存储目录');
    return '$home${Platform.pathSeparator}.ssh-tunnel';
  }

  Future<WorkspacePreferences> load() async {
    final file = File(_filePath);
    if (!await file.exists()) return const WorkspacePreferences();
    if (await file.length() > 1024 * 1024) {
      throw const FormatException('工作区配置文件过大');
    }
    final json = jsonDecode(await file.readAsString(encoding: utf8));
    if (json is! Map<String, dynamic> ||
        json['workspaces'] is! List ||
        json['selected_path'] is! String) {
      throw const FormatException('工作区配置格式无效');
    }
    final workspaces = <Workspace>[];
    final seen = <String>{};
    for (final entry in json['workspaces'] as List) {
      if (entry is! Map ||
          entry['id'] is! String ||
          entry['name'] is! String ||
          !_idPattern.hasMatch(entry['id'] as String) ||
          !seen.add(entry['id'] as String)) {
        throw const FormatException('工作区标识无效');
      }
      final name = (entry['name'] as String).trim();
      if (name.isEmpty || name.length > 80) {
        throw const FormatException('工作区名称无效');
      }
      workspaces.add(
        Workspace(
          id: entry['id'] as String,
          name: name,
          dataDir:
              '$directory${Platform.pathSeparator}workspaces${Platform.pathSeparator}${entry['id']}',
        ),
      );
    }
    return WorkspacePreferences(
      selectedPath: json['selected_path'] as String,
      workspaces: List.unmodifiable(workspaces),
    );
  }

  Future<T> _exclusive<T>(Future<T> Function() action) {
    final future = _pending.then((_) async {
      await Directory(directory).create(recursive: true);
      final lock = await File('$_filePath.lock').open(mode: FileMode.append);
      try {
        await lock.lock(FileLock.blockingExclusive);
        return await action();
      } finally {
        await lock.close();
      }
    });
    _pending = future.then<void>((_) {}, onError: (Object _, StackTrace _) {});
    return future;
  }

  Future<void> _save(WorkspacePreferences preferences) async {
    final temporary = File('$_filePath.tmp');
    await temporary.writeAsString(
      jsonEncode({
        'selected_path': preferences.selectedPath,
        'workspaces': preferences.workspaces
            .map((workspace) => workspace.toJson())
            .toList(),
      }),
      encoding: utf8,
      flush: true,
    );
    await temporary.rename(_filePath);
  }

  Future<WorkspacePreferences> select(String path) => _exclusive(() async {
    final current = await load();
    final allowed = [
      ...DaemonDiscovery.candidatePaths,
      ...current.workspaces.map((workspace) => workspace.discoveryPath),
    ];
    if (path.isNotEmpty &&
        !allowed.any(
          (candidate) => DaemonDiscovery.samePath(path, candidate),
        )) {
      throw ArgumentError('请选择已发现的实例或已创建的工作区');
    }
    final next = WorkspacePreferences(
      selectedPath: path,
      workspaces: current.workspaces,
    );
    await _save(next);
    return next;
  });

  Future<WorkspacePreferences> create(String name) => _exclusive(() async {
    name = name.trim();
    if (name.isEmpty || name.length > 80) {
      throw ArgumentError('工作区名称需为 1～80 个字符');
    }
    final current = await load();
    if (current.workspaces.any(
      (workspace) => workspace.name.toLowerCase() == name.toLowerCase(),
    )) {
      throw ArgumentError('工作区名称已存在');
    }
    final random = Random.secure();
    final id = List.generate(
      16,
      (_) => random.nextInt(256).toRadixString(16).padLeft(2, '0'),
    ).join();
    final workspace = Workspace(
      id: id,
      name: name,
      dataDir:
          '$directory${Platform.pathSeparator}workspaces${Platform.pathSeparator}$id',
    );
    if (await Directory(workspace.dataDir).exists()) {
      throw StateError('工作区目录已存在，请重试');
    }
    await Directory(workspace.dataDir).create(recursive: true);
    await File(
      workspace.configPath,
    ).writeAsString('log_level = "info"\n', encoding: utf8, flush: true);
    final next = WorkspacePreferences(
      selectedPath: workspace.discoveryPath,
      workspaces: List.unmodifiable([...current.workspaces, workspace]),
    );
    await _save(next);
    return next;
  });
}
