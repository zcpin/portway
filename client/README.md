# ssh_tunnel_client

SSH 隧道管理器的 Flutter 桌面客户端（Windows / macOS / Linux）。

通过本机 HTTP + WebSocket 连接 `daemon`（Go 守护进程），负责隧道、SSH 连接、密钥与日志的界面展示与控制。daemon 未运行时界面会提示启动方式；连接断开后会自动重新发现并恢复。

## 开发

```bash
flutter pub get
dart analyze
flutter test
flutter run -d windows   # 或 macos / linux
```

构建、配置与 API 说明见仓库根目录的 [README](../README.md)。
