//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func desktopPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", Name+".desktop"), nil
}

func enable(exe string, args []string) error {
	path, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("无法创建 autostart 目录: %w", err)
	}

	logFile, err := LogPath()
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("[Desktop Entry]\n")
	sb.WriteString("Type=Application\n")
	sb.WriteString("Name=SSH Tunnel Daemon\n")
	sb.WriteString("Comment=本地 SSH 隧道管理守护进程\n")
	sb.WriteString("Exec=" + buildCommand(exe, args) + " >> " + logFile + " 2>&1\n")
	sb.WriteString("Terminal=false\n")
	sb.WriteString("Hidden=false\n")
	sb.WriteString("NoDisplay=true\n")
	sb.WriteString("X-GNOME-Autostart-enabled=true\n")

	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("无法写入 autostart 项: %w", err)
	}
	return nil
}

func disable() error {
	path, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法删除 autostart 项: %w", err)
	}
	return nil
}

func isEnabled() (bool, error) {
	path, err := desktopPath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
