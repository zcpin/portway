//go:build linux || darwin

package discovery

import (
	"path/filepath"
)

// sharedPath 返回系统级目录，通常需要 root 权限写入。
func sharedPath() (string, error) {
	return filepath.Join("/var/lib/portway", FileName), nil
}
