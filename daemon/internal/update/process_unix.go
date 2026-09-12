//go:build !windows

package update

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func hideChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func processAlive(pid int) bool {
	return pid > 0 && unix.Kill(pid, 0) != unix.ESRCH
}

func lockInstall(root string) (func(), error) {
	return lockDirectory(root, "update", false)
}

func lockDirectory(root, purpose string, shared bool) (func(), error) {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	file, err := os.OpenFile(filepath.Join(filepath.Dir(root), fmt.Sprintf(".ssh-tunnel-%s-%x.lock", purpose, digest[:8])), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	flags := unix.LOCK_EX | unix.LOCK_NB
	if shared {
		flags = unix.LOCK_SH | unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), flags); err != nil {
		file.Close()
		return nil, fmt.Errorf("installation is in use: %w", err)
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close() }, nil
}
