//go:build windows

package svc

import "os"

// logDir 返回服务日志存放的根目录（所有用户可读的公共目录）。
func logDir() (string, error) {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return dir, nil
}
