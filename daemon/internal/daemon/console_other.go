//go:build !windows

package daemon

// hideConsole 在非 Windows 平台无实际作用（守护进程本就无控制台窗口）。
func hideConsole() {}
