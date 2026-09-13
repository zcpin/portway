import 'package:tray_manager/tray_manager.dart';

import '../models.dart';
import '../models/connection_preferences.dart';

class TrayTunnelAction {
  final String command;
  final List<String> names;
  final String? address;
  final ConnectionOpenAction? openAction;

  TrayTunnelAction(
    this.command,
    Iterable<String> names, {
    this.address,
    this.openAction,
  }) : names = List.unmodifiable(names);
}

/// A menu and its commands share one revision, so clicks from replaced menus
/// cannot act on another workspace or on a renamed tunnel.
class TunnelTrayMenu {
  final Menu menu;
  final Map<String, TrayTunnelAction> actions;

  TunnelTrayMenu._(this.menu, this.actions);

  factory TunnelTrayMenu.build({
    required int revision,
    List<Tunnel>? tunnels,
    String? instanceLabel,
    ConnectionPreferences? preferences,
    bool busy = false,
  }) {
    final actions = <String, TrayTunnelAction>{};
    var commandIndex = 0;
    MenuItem command(
      String label,
      TrayTunnelAction action, {
      bool disabled = false,
    }) {
      final key = 'tunnel_${revision}_${commandIndex++}';
      if (!busy && !disabled) actions[key] = action;
      return MenuItem(key: key, label: label, disabled: busy || disabled);
    }

    MenuItem tunnelItem(Tunnel tunnel) {
      final openAction = preferences?.openActions[tunnel.name];
      return MenuItem.submenu(
        label: '${tunnel.name} · ${tunnel.stateLabel}',
        submenu: Menu(
          items: [
            command(
              tunnel.isRunning ? '停止' : '启动',
              TrayTunnelAction(tunnel.isRunning ? 'stop' : 'start', [
                tunnel.name,
              ]),
            ),
            if (!tunnel.isRunning && tunnel.desiredRunning)
              command('停止自动恢复', TrayTunnelAction('stop', [tunnel.name])),
            if (openAction != null &&
                (tunnel.mode.isEmpty || tunnel.mode == 'local'))
              command(
                tunnel.state == 'connected' ? '打开服务' : '启动并打开服务',
                TrayTunnelAction('open', [tunnel.name], openAction: openAction),
              ),
            command(
              tunnel.mode == 'remote' ? '复制远端监听地址' : '复制本地连接地址',
              TrayTunnelAction('copy', [
                tunnel.name,
              ], address: _address(tunnel)),
            ),
          ],
        ),
      );
    }

    final ordered = preferences?.sorted(tunnels ?? []) ?? tunnels ?? <Tunnel>[];
    final favorites = ordered
        .where((t) => preferences?.favorites.contains(t.name) == true)
        .toList();
    final groups = <String, List<Tunnel>>{};
    for (final tunnel in ordered) {
      groups.putIfAbsent(tunnel.group, () => []).add(tunnel);
    }
    final groupNames = groups.keys.toList()..sort();
    final running = tunnels?.where((tunnel) => tunnel.isRunning).length;
    final menu = Menu(
      items: [
        MenuItem(
          label: running == null ? 'daemon 未连接' : '$running 条隧道运行中',
          disabled: true,
        ),
        if (instanceLabel != null)
          MenuItem(label: '当前实例：$instanceLabel', disabled: true),
        if (busy) MenuItem(label: '正在执行操作…', disabled: true),
        if (tunnels != null && tunnels.isEmpty)
          MenuItem(label: '尚未配置隧道', disabled: true),
        if (favorites.isNotEmpty)
          MenuItem.submenu(
            label: '收藏（${favorites.length}）',
            submenu: Menu(items: favorites.map(tunnelItem).toList()),
          ),
        for (final group in groupNames)
          MenuItem.submenu(
            label: group.isEmpty ? '未分组' : group,
            submenu: Menu(
              items: [
                command(
                  '启动本组全部',
                  TrayTunnelAction('start', groups[group]!.map((t) => t.name)),
                  disabled: groups[group]!.every((t) => t.isRunning),
                ),
                command(
                  '停止本组全部',
                  TrayTunnelAction('stop', groups[group]!.map((t) => t.name)),
                  disabled: groups[group]!.every(
                    (t) => !t.isRunning && !t.desiredRunning,
                  ),
                ),
                MenuItem.separator(),
                for (final tunnel in groups[group]!) tunnelItem(tunnel),
              ],
            ),
          ),
        MenuItem.separator(),
        MenuItem(key: 'show_window', label: '显示窗口'),
        MenuItem.separator(),
        MenuItem(key: 'exit_app', label: '退出'),
      ],
    );
    return TunnelTrayMenu._(menu, Map.unmodifiable(actions));
  }

  static String _address(Tunnel tunnel) {
    final remote = tunnel.mode == 'remote';
    final host = remote ? tunnel.remoteHost : tunnel.localHost;
    final port = remote ? tunnel.remotePort : tunnel.localPort;
    return host.contains(':') && !host.startsWith('[')
        ? '[$host]:$port'
        : '$host:$port';
  }
}
