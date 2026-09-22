//go:build windows

package discovery

import (
	"os"
	"path/filepath"
)

// sharedPath 返回所有用户可读的公共目录（通常是 C:\ProgramData）。
func sharedPath() (string, error) {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, "portway", FileName), nil
}
