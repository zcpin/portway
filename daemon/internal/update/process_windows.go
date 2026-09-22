//go:build windows

package update

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func hideChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err != windows.ERROR_INVALID_PARAMETER
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) != nil || code == 259
}

func lockInstall(root string) (func(), error) {
	return lockDirectory(root, "update", false)
}

func lockDirectory(root, purpose string, shared bool) (func(), error) {
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	file, err := os.OpenFile(filepath.Join(filepath.Dir(root), fmt.Sprintf(".portway-%s-%x.lock", purpose, digest[:8])), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	overlap := &windows.Overlapped{}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if !shared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, overlap); err != nil {
		file.Close()
		return nil, fmt.Errorf("installation is in use: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlap)
		file.Close()
	}, nil
}
