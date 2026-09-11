//go:build linux || darwin

package svc

// logDir 返回服务日志存放的根目录。
func logDir() (string, error) {
	return "/var/log", nil
}
