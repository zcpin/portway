// Package discovery 负责把守护进程的连接信息写到一个约定位置，供客户端自动发现。
//
// 存在两个候选位置：
//
//   - 用户级：<home>/.ssh-tunnel/daemon.json —— 以当前用户身份运行时使用
//   - 系统级：平台公共目录 —— 以系统服务身份（LocalSystem / root）运行时使用，
//     此时 os.UserHomeDir() 指向的是服务账户目录，客户端按用户目录读不到
//
// 客户端会依次尝试两个位置并探活，因此守护进程只需写其中一个。
//
// 另外支持用 SSH_TUNNEL_DATA_DIR 显式指定位置（客户端读取同一个变量），
// 用于自定义部署，以及在没有管理员权限的机器上验证系统级路径。
package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// FileName 是服务发现文件的固定名称。
const FileName = "daemon.json"

// EnvDataDir 可显式指定发现文件所在目录，优先级高于按运行方式推断的默认位置。
const EnvDataDir = "SSH_TUNNEL_DATA_DIR"

// Info 是客户端做本地服务发现时读取的内容。
type Info struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Token          string `json:"token,omitempty"`
	PID            int    `json:"pid"`
	Version        string `json:"version"`
	ConfigPath     string `json:"config_path,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	ServiceMode    *bool  `json:"service_mode,omitempty"`
}

// UserPath 返回用户级发现文件路径。
//
// 刻意选择用户主目录下的固定路径而非 os.UserConfigDir()：后者在 Windows 上
// 依赖 AppData 环境变量，在部分 shell 环境中取不到；而主目录路径三平台一致。
func UserPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh-tunnel", FileName), nil
}

// SharedPath 返回系统级发现文件路径。
//
// 设置 SSH_TUNNEL_DATA_DIR 时以它为准（此时与「是否服务模式」无关），
// 便于自定义部署，也便于在普通权限下验证多路径发现。
func SharedPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvDataDir)); dir != "" {
		return filepath.Join(dir, FileName), nil
	}
	return sharedPath()
}

// Candidates 返回客户端应当依次尝试的位置，顺序即优先级。
//
// 与客户端 daemon_discovery.dart 中的候选顺序保持一致：
// SSH_TUNNEL_DATA_DIR 覆盖 → 用户目录 → 系统公共目录。
func Candidates() []string {
	candidates := make([]string, 0, 3)
	add := func(path string) {
		for _, existing := range candidates {
			if samePath(existing, path) {
				return
			}
		}
		candidates = append(candidates, path)
	}

	if dir := strings.TrimSpace(os.Getenv(EnvDataDir)); dir != "" {
		add(filepath.Join(dir, FileName))
	}
	if p, err := UserPath(); err == nil {
		add(p)
	}
	if p, err := SharedPath(); err == nil {
		add(p)
	}
	return candidates
}

// samePath 忽略分隔符与大小写差异（Windows 下同一路径可能多种写法）。
func samePath(a, b string) bool {
	return strings.EqualFold(
		strings.ReplaceAll(a, "\\", "/"),
		strings.ReplaceAll(b, "\\", "/"),
	)
}

// Write 写入发现文件，返回实际写入的路径。
//
// 优先使用 SSH_TUNNEL_DATA_DIR 指定的位置；否则 serviceMode 为真时写到
// 系统公共目录，为假时写用户目录。
func Write(info Info, serviceMode bool) (string, error) {
	path, shared, err := targetPath(serviceMode)
	if err != nil {
		return "", err
	}

	// 用户目录只有本人访问，收紧权限；公共目录需要被普通用户身份的客户端
	// 读到（服务进程以 root / LocalSystem 运行），因此必须放宽。
	dirPerm, filePerm := os.FileMode(0700), os.FileMode(0600)
	if shared {
		dirPerm, filePerm = 0755, 0644
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", err
	}
	if shared {
		// 目录可能已由更早的版本以 0700 创建，MkdirAll 不会修正既有权限
		_ = os.Chmod(dir, dirPerm)
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		return "", err
	}
	return path, nil
}

// Remove 删除发现文件（进程退出时清理）。
//
// 仅当文件仍由 pid 对应的进程写入时才删除：否则先退出的实例会把
// 后启动实例写下的发现文件一并删掉，客户端便再也发现不了新实例。
func Remove(path string, pid int) {
	if path == "" {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return
	}
	if info.PID != pid {
		return
	}
	_ = os.Remove(path)
}

// targetPath 决定写到哪个位置，第二个返回值表示是否为公共目录。
func targetPath(serviceMode bool) (string, bool, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvDataDir)); dir != "" {
		return filepath.Join(dir, FileName), true, nil
	}
	if serviceMode {
		if p, err := sharedPath(); err == nil {
			return p, true, nil
		}
	}
	p, err := UserPath()
	return p, false, err
}
