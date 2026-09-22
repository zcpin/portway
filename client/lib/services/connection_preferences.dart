import 'dart:convert';
import 'dart:io';

import '../models.dart';
import '../models/connection_preferences.dart';
import 'daemon_discovery.dart';

String connectionPreferenceScope(DaemonInfo info) {
  final path = info.configPath.isNotEmpty
      ? info.configPath
      : info.discoveryPath;
  if (path.isEmpty) return info.httpBase;
  final absolute = File(path).absolute.uri.normalizePath().toFilePath();
  return Platform.isWindows ? absolute.toLowerCase() : absolute;
}

class ConnectionPreferencesStore {
  ConnectionPreferencesStore({String? directory})
    : directory = directory ?? _defaultDirectory();
  final String directory;
  static final _pending = <String, Future<void>>{};
  String get filePath =>
      '$directory${Platform.pathSeparator}connection_preferences.json';

  static String _defaultDirectory() {
    final home = DaemonDiscovery.homeDir;
    if (home == null || home.isEmpty) throw StateError('无法确定客户端偏好目录');
    return '$home${Platform.pathSeparator}.portway';
  }

  Future<Map<String, ConnectionPreferences>> _loadAll() async {
    final file = File(filePath);
    if (!await file.exists()) return {};
    if (await file.length() > 1024 * 1024) {
      throw const FormatException('连接偏好文件过大');
    }
    final json = jsonDecode(await file.readAsString(encoding: utf8));
    if (json is! Map<String, dynamic> ||
        json['version'] != 1 ||
        json['instances'] is! Map<String, dynamic>) {
      throw const FormatException('连接偏好文件格式无效');
    }
    final instances = json['instances'] as Map<String, dynamic>;
    final result = <String, ConnectionPreferences>{};
    for (final entry in instances.entries) {
      if (entry.value is! Map<String, dynamic>) {
        throw const FormatException('实例偏好格式无效');
      }
      result[entry.key] = ConnectionPreferences.fromJson(
        entry.value as Map<String, dynamic>,
      );
    }
    return result;
  }

  Future<ConnectionPreferences> load(String scope) async =>
      (await _loadAll())[scope] ?? ConnectionPreferences();

  Future<ConnectionPreferences> _change(
    String scope,
    ConnectionPreferences Function(ConnectionPreferences) change,
  ) {
    final key = File(filePath).absolute.path;
    final future = (_pending[key] ?? Future<void>.value()).then((_) async {
      await Directory(directory).create(recursive: true);
      final lock = await File('$filePath.lock').open(mode: FileMode.append);
      try {
        await lock.lock(FileLock.blockingExclusive);
        final all = await _loadAll();
        final current = all[scope] ?? ConnectionPreferences();
        final next = change(current);
        if (identical(next, current)) return current;
        all[scope] = next;
        final content = jsonEncode({
          'version': 1,
          'instances': all.map(
            (scope, value) => MapEntry(scope, value.toJson()),
          ),
        });
        if (utf8.encode(content).length > 1024 * 1024) {
          throw const FormatException('连接偏好文件过大');
        }
        final temporary = File('$filePath.tmp');
        await temporary.writeAsString(content, encoding: utf8, flush: true);
        await temporary.rename(filePath);
        return next;
      } finally {
        await lock.close();
      }
    });
    final completed = future.then<void>(
      (_) {},
      onError: (Object _, StackTrace _) {},
    );
    _pending[key] = completed;
    completed.then((_) {
      if (identical(_pending[key], completed)) _pending.remove(key);
    });
    return future;
  }

  Future<ConnectionPreferences> toggleFavorite(String scope, String name) =>
      _change(scope, (current) {
        final favorites = {...current.favorites};
        if (!favorites.remove(name)) favorites.add(name);
        return current.copyWith(favorites: favorites);
      });

  Future<ConnectionPreferences> move(
    String scope,
    String name,
    String neighbor,
    bool before,
    List<Tunnel> tunnels,
  ) => _change(scope, (current) {
    if (current.favorites.contains(name) !=
        current.favorites.contains(neighbor)) {
      throw StateError('收藏与普通隧道分别排序');
    }
    final order = current.sorted(tunnels).map((t) => t.name).toList();
    if (!order.contains(name) ||
        !order.contains(neighbor) ||
        name == neighbor) {
      throw StateError('隧道列表已变化，请重试');
    }
    order.remove(name);
    order.insert(order.indexOf(neighbor) + (before ? 0 : 1), name);
    return current.copyWith(
      order: [
        ...order,
        ...current.order.where((name) => !order.contains(name)),
      ],
    );
  });

  Future<ConnectionPreferences> setOpenAction(
    String scope,
    String name,
    ConnectionOpenAction? action,
  ) {
    action?.validate();
    return _change(scope, (current) {
      final actions = {...current.openActions};
      if (action == null) {
        actions.remove(name);
      } else {
        actions[name] = action;
      }
      return current.copyWith(openActions: actions);
    });
  }

  Future<ConnectionPreferences> rename(
    String scope,
    String oldName,
    String newName,
  ) => _change(scope, (current) {
    bool tracked(String name) =>
        current.favorites.contains(name) ||
        current.order.contains(name) ||
        current.openActions.containsKey(name);
    if (oldName == newName || (!tracked(oldName) && !tracked(newName))) {
      return current;
    }
    final favorites = {...current.favorites};
    final wasFavorite = favorites.remove(oldName);
    favorites.remove(newName);
    if (wasFavorite) favorites.add(newName);
    final actions = {...current.openActions};
    final action = actions.remove(oldName);
    actions.remove(newName);
    if (action != null) actions[newName] = action;
    return current.copyWith(
      favorites: favorites,
      order: current.order
          .where((name) => name != newName)
          .map((name) => name == oldName ? newName : name)
          .toSet(),
      openActions: actions,
    );
  });
}
