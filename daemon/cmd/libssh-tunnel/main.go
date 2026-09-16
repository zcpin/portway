//go:build cgo

// Command libssh-tunnel 把隧道引擎编译成动态库，供 Flutter 客户端通过 dart:ffi 直接调用。
//
// 与 ssh-tunnel-daemon 的关系：两者是同一个引擎的两种「外壳」——
// daemon 是独立进程 + HTTP/WebSocket 服务发现，本命令是进程内库，不监听端口、
// 不写服务发现文件。业务逻辑都在 internal/app，两边共用。
//
// 需要 cgo，因此必须有一个 C 工具链（Windows 上是 mingw-w64 的 gcc）：
//
//	go build -buildmode=c-shared -o bin/ssh-tunnel.dll ./cmd/libssh-tunnel
//
// 产物除动态库外还有一个同名头文件（ssh-tunnel.h），只用于 C 侧对接，
// Dart 侧不依赖它（接口约定写在 internal/embedded/export.go 的包注释里）。
package main

import "C"

import _ "github.com/byteporter/ssh-tunnel/internal/embedded"

func main() {}
