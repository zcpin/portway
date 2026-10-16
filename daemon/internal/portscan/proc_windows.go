//go:build windows

package portscan

import (
	"os/exec"
	"syscall"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// hiddenCommand 构造一个不弹出控制台窗口的命令。
//
// 客户端是 GUI 程序，默认情况下每次 exec 都会闪一个黑色控制台窗口。
// CREATE_NO_WINDOW 是唯一可靠的抑制手段（STARTF_USESHOWWINDOW 对
// 控制台程序无效）。
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}

// decodeConsole 把外部命令的输出转成 UTF-8。
//
// Windows 控制台程序的输出编码取决于当前代码页（中文系统是 GBK），
// 直接当 UTF-8 处理会得到乱码。这里先判断是否已是合法 UTF-8，不是才按
// GBK 解码——避免在 UTF-8 输出（如 PowerShell 7）上做多余的转换。
func decodeConsole(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	decoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
	if err != nil {
		return string(raw)
	}
	return string(decoded)
}
