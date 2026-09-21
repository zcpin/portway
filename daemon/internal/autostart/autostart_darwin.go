//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// label 是 launchd 用来标识任务的唯一标签。
const label = "com.byteporter.portway"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func enable(exe string, args []string) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("无法创建 LaunchAgents 目录: %w", err)
	}

	logFile, err := LogPath()
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	sb.WriteString(`<plist version="1.0">` + "\n")
	sb.WriteString(`  <dict>` + "\n")
	sb.WriteString(`    <key>Label</key>` + "\n")
	sb.WriteString(`    <string>` + label + `</string>` + "\n")
	sb.WriteString(`    <key>ProgramArguments</key>` + "\n")
	sb.WriteString(`    <array>` + "\n")
	sb.WriteString(`      <string>` + exe + `</string>` + "\n")
	for _, a := range args {
		sb.WriteString(`      <string>` + a + `</string>` + "\n")
	}
	sb.WriteString(`    </array>` + "\n")
	sb.WriteString(`    <key>RunAtLoad</key>` + "\n")
	sb.WriteString(`    <true/>` + "\n")
	sb.WriteString(`    <key>StandardOutPath</key>` + "\n")
	sb.WriteString(`    <string>` + logFile + `</string>` + "\n")
	sb.WriteString(`    <key>StandardErrorPath</key>` + "\n")
	sb.WriteString(`    <string>` + logFile + `</string>` + "\n")
	sb.WriteString(`  </dict>` + "\n")
	sb.WriteString(`</plist>` + "\n")

	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("无法写入 LaunchAgent: %w", err)
	}
	return nil
}

func disable() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法删除 LaunchAgent: %w", err)
	}
	return nil
}

func isEnabled() (bool, error) {
	path, err := plistPath()
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}
