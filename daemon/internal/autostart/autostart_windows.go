//go:build windows

package autostart

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKeyPath 是 Windows 的用户级开机自启注册表项（HKCU，无需管理员权限）。
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func enable(exe string, args []string) error {
	// 自启时隐藏控制台窗口，避免登录后闪出一个黑框
	full := append([]string{"-hide-console"}, args...)

	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("无法打开自启注册表项: %w", err)
	}
	defer key.Close()

	return key.SetStringValue(Name, buildCommand(exe, full))
}

func disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if isNotExist(err) {
			return nil // 本来就没登记过，视为成功
		}
		return fmt.Errorf("无法打开自启注册表项: %w", err)
	}
	defer key.Close()

	if err := key.DeleteValue(Name); err != nil && !isNotExist(err) {
		return fmt.Errorf("无法删除自启项: %w", err)
	}
	return nil
}

func isEnabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("无法打开自启注册表项: %w", err)
	}
	defer key.Close()

	value, _, err := key.GetStringValue(Name)
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// 记录了值但指向的可执行文件已不存在，视为未启用
	if idx := strings.Index(value, ".exe"); idx > 0 {
		exe := strings.Trim(value[:idx+4], `"`)
		if _, statErr := os.Stat(exe); statErr != nil {
			return false, nil
		}
	}
	return strings.TrimSpace(value) != "", nil
}

func isNotExist(err error) bool {
	return err == registry.ErrNotExist || os.IsNotExist(err)
}
