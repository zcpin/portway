//go:build windows

package daemon

import "syscall"

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")

	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
)

const swHide = 0

// hideConsole 隐藏本进程的控制台窗口。
//
// 开机自启场景下守护进程会被系统拉起，若不做处理会闪出一个控制台黑框。
func hideConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}
