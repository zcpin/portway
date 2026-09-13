import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'models/connection_preferences.dart';
import 'providers.dart';
import 'services/connection_preferences.dart';
import 'services/external_connections.dart';

final connectionPreferencesStoreProvider = Provider<ConnectionPreferencesStore>(
  (ref) => ConnectionPreferencesStore(),
);
final externalConnectionLauncherProvider = Provider<ExternalConnectionLauncher>(
  (ref) => ExternalConnectionLauncher(),
);

class ConnectionPreferencesNotifier
    extends AsyncNotifier<ConnectionPreferences> {
  @override
  Future<ConnectionPreferences> build() async {
    final client = await ref.watch(clientProvider.future);
    if (client == null) return ConnectionPreferences();
    return ref
        .watch(connectionPreferencesStoreProvider)
        .load(connectionPreferenceScope(client.info));
  }

  Future<void> _change(
    Future<ConnectionPreferences> Function(ConnectionPreferencesStore, String)
    change,
  ) async {
    final connection = ref.read(clientProvider);
    final client = connection.valueOrNull;
    if (connection.isLoading || client == null) throw StateError('当前实例未连接');
    final next = await change(
      ref.read(connectionPreferencesStoreProvider),
      connectionPreferenceScope(client.info),
    );
    if (ref.mounted &&
        !ref.read(clientProvider).isLoading &&
        identical(ref.read(clientProvider).valueOrNull, client)) {
      state = AsyncData(next);
    }
  }

  Future<void> toggleFavorite(String name) =>
      _change((store, scope) => store.toggleFavorite(scope, name));
  Future<void> move(String name, String neighbor, bool before) {
    final tunnels = ref.read(tunnelsProvider).valueOrNull ?? [];
    return _change(
      (store, scope) => store.move(scope, name, neighbor, before, tunnels),
    );
  }

  Future<void> setOpenAction(String name, ConnectionOpenAction? action) =>
      _change((store, scope) => store.setOpenAction(scope, name, action));
}

final connectionPreferencesProvider =
    AsyncNotifierProvider<ConnectionPreferencesNotifier, ConnectionPreferences>(
      ConnectionPreferencesNotifier.new,
    );
