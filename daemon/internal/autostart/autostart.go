// Package autostart 提供「随当前用户登录自动启动」的能力。
//
// 与系统服务（internal/svc）的区别：本包不需要管理员权限，且以当前用户身份
// 运行 —— 这一点很关键，因为守护进程把连接信息写在用户主目录下
// （~/.ssh-tunnel/daemon.json），换成 LocalSystem / root 身份运行会导致
// 客户端按用户目录找不到服务发现文件。
//
// 桌面场景下这是推荐的开机自启方式。
package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

// Name 是自启项在各平台上登记的名称，同时用于派生相关文件名。
const Name = "portway-daemon"

// Enable 登记开机自启。args 会原样附加到命令后（通常用于指定配置文件路径）。
func Enable(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return enable(exe, args)
}

// Disable 取消开机自启。
func Disable() error {
	return disable()
}

// IsEnabled 返回当前是否已登记开机自启。
func IsEnabled() (bool, error) {
	return isEnabled()
}

// LogPath 返回自启运行时的日志文件路径，用于排查开机自启失败。
func LogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh-tunnel", "autostart.log"), nil
}

// quoteArg 给含空格的参数加引号，Windows 与 Unix 的命令串都接受这种形式。
func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// buildCommand 拼出完整的自启命令行。
func buildCommand(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(exe))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}
