import 'dart:io';

import '../models.dart';

class ConnectionOpenAction {
  final String kind;
  final String target;
  final List<String> arguments;

  ConnectionOpenAction({
    required this.kind,
    required this.target,
    Iterable<String> arguments = const [],
  }) : arguments = List.unmodifiable(arguments);

  factory ConnectionOpenAction.fromJson(Map<String, dynamic> json) {
    if (json['kind'] is! String ||
        json['target'] is! String ||
        json['arguments'] is! List ||
        (json['arguments'] as List).any((value) => value is! String)) {
      throw const FormatException('打开方式格式无效');
    }
    final action = ConnectionOpenAction(
      kind: json['kind'] as String,
      target: json['target'] as String,
      arguments: (json['arguments'] as List).cast<String>(),
    );
    action.validate();
    return action;
  }

  Map<String, dynamic> toJson() => {
    'kind': kind,
    'target': target,
    'arguments': arguments,
  };

  void validate() {
    if (target.trim().isEmpty ||
        target.length > 4096 ||
        target.contains('\u0000') ||
        arguments.length > 64 ||
        arguments.any((arg) => arg.length > 4096 || arg.contains('\u0000'))) {
      throw const FormatException('打开地址或参数无效');
    }
    if (kind == 'url') {
      _webUri(_expand(target, '127.0.0.1', 1234));
    } else if (kind == 'program') {
      if (!File(target).isAbsolute ||
          (Platform.isWindows && !target.toLowerCase().endsWith('.exe'))) {
        throw const FormatException('请选择程序的完整路径；Windows 需要 .exe 文件');
      }
      for (final argument in arguments) {
        _expand(argument, '127.0.0.1', 1234);
      }
    } else {
      throw const FormatException('请选择网页或本地程序');
    }
  }

  Uri urlFor(Tunnel tunnel) {
    final host = tunnel.localHost.isEmpty ? '127.0.0.1' : tunnel.localHost;
    return _webUri(
      _expand(target, host.contains(':') ? '[$host]' : host, tunnel.localPort),
    );
  }

  List<String> argumentsFor(Tunnel tunnel) => [
    for (final argument in arguments)
      _expand(
        argument,
        tunnel.localHost.isEmpty ? '127.0.0.1' : tunnel.localHost,
        tunnel.localPort,
      ),
  ];

  static String _expand(String value, String host, int port) {
    final expanded = value
        .replaceAll('{host}', host)
        .replaceAll('{port}', '$port');
    if (RegExp(r'\{[a-zA-Z_][a-zA-Z0-9_]*\}').hasMatch(expanded)) {
      throw const FormatException('仅支持 {host} 和 {port} 占位符');
    }
    return expanded;
  }

  static Uri _webUri(String value) {
    final uri = Uri.parse(value);
    if ((uri.scheme != 'http' && uri.scheme != 'https') ||
        uri.host.isEmpty ||
        uri.userInfo.isNotEmpty) {
      throw const FormatException('请输入不含账号密码的 http 或 https 网址');
    }
    return uri;
  }
}

class ConnectionPreferences {
  final Set<String> favorites;
  final List<String> order;
  final Map<String, ConnectionOpenAction> openActions;

  ConnectionPreferences({
    Iterable<String> favorites = const [],
    Iterable<String> order = const [],
    Map<String, ConnectionOpenAction> openActions = const {},
  }) : favorites = Set.unmodifiable(favorites),
       order = List.unmodifiable(order),
       openActions = Map.unmodifiable(openActions);

  factory ConnectionPreferences.fromJson(Map<String, dynamic> json) {
    List<String> names(String field) {
      final values = json[field];
      if (values is! List ||
          values.any((value) => value is! String || value.isEmpty)) {
        throw const FormatException('收藏或排序配置无效');
      }
      return values.cast<String>();
    }

    final actions = json['open_actions'];
    if (actions is! Map<String, dynamic>) {
      throw const FormatException('打开方式配置无效');
    }
    if (actions.entries.any(
      (entry) => entry.key.isEmpty || entry.value is! Map<String, dynamic>,
    )) {
      throw const FormatException('打开方式配置无效');
    }
    return ConnectionPreferences(
      favorites: names('favorites'),
      order: names('order'),
      openActions: {
        for (final entry in actions.entries)
          entry.key: ConnectionOpenAction.fromJson(
            (entry.value as Map).cast<String, dynamic>(),
          ),
      },
    );
  }

  Map<String, dynamic> toJson() => {
    'favorites': favorites.toList(),
    'order': order,
    'open_actions': openActions.map(
      (name, action) => MapEntry(name, action.toJson()),
    ),
  };

  ConnectionPreferences copyWith({
    Iterable<String>? favorites,
    Iterable<String>? order,
    Map<String, ConnectionOpenAction>? openActions,
  }) => ConnectionPreferences(
    favorites: favorites ?? this.favorites,
    order: order ?? this.order,
    openActions: openActions ?? this.openActions,
  );

  List<Tunnel> sorted(List<Tunnel> tunnels) {
    final ranks = {for (var i = 0; i < order.length; i++) order[i]: i};
    final original = {
      for (var i = 0; i < tunnels.length; i++) tunnels[i].name: i,
    };
    return [...tunnels]..sort((a, b) {
      final pinned =
          (favorites.contains(b.name) ? 1 : 0) -
          (favorites.contains(a.name) ? 1 : 0);
      if (pinned != 0) return pinned;
      return (ranks[a.name] ?? order.length + original[a.name]!).compareTo(
        ranks[b.name] ?? order.length + original[b.name]!,
      );
    });
  }
}
