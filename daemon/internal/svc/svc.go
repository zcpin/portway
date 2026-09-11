// Package svc 提供系统级服务托管（Windows 服务 / systemd / launchd）。
//
// 与 internal/autostart 的区别：系统服务由服务管理器拉起，无需用户登录，
// 且崩溃后可自动重启，适合无人值守的机器。代价是安装需要管理员权限，
// 且运行身份为 LocalSystem / root —— 因此守护进程会把服务发现文件写到
// 系统公共目录（见 internal/discovery），客户端需要一并检查该位置。
package svc

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kardianos/service"

	"github.com/byteporter/ssh-tunnel/internal/daemon"
)

// Name 是服务在各平台服务管理器中登记的名称。
const Name = "ssh-tunnel-daemon"

// Options 是服务安装与运行所需的参数。
type Options struct {
	ConfigPath string
	Addr       string
	NoAuth     bool
	LogLevel   string
	Version    string
}

// New 创建 service.Service。安装、卸载、启停等操作都通过它完成。
func New(opts Options) (service.Service, error) {
	cfgPath, err := daemon.ResolveConfigPath(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	prg := &program{
		opts: daemon.Options{
			ConfigPath:  cfgPath,
			Addr:        opts.Addr,
			NoAuth:      opts.NoAuth,
			LogLevel:    opts.LogLevel,
			Version:     opts.Version,
			ServiceMode: true,
		},
	}

	svcCfg := &service.Config{
		Name:        Name,
		DisplayName: "SSH Tunnel Daemon",
		Description: "本地 SSH 隧道管理守护进程，为桌面客户端提供隧道控制接口。",
		Arguments:   runArgs(cfgPath, opts),
	}

	return service.New(prg, svcCfg)
}

// runArgs 构造服务启动时使用的命令行参数。
//
// 服务的当前工作目录是系统目录，任何相对路径都会指向意外位置，
// 因此这里把配置项全部固化成绝对路径。
func runArgs(cfgPath string, opts Options) []string {
	args := []string{"service", "run", "-config", cfgPath}
	if opts.Addr != "" && opts.Addr != defaultAddr {
		args = append(args, "-addr", opts.Addr)
	}
	if opts.NoAuth {
		args = append(args, "-no-auth")
	}
	if opts.LogLevel != "" {
		args = append(args, "-log-level", opts.LogLevel)
	}
	return args
}

const defaultAddr = "127.0.0.1:0"

// program 实现 kardianos/service 要求的接口。
type program struct {
	opts daemon.Options

	mu sync.Mutex
	d  *daemon.Daemon
}

// Start 由服务管理器调用，必须尽快返回，因此实际运行放在 goroutine 里。
func (p *program) Start(s service.Service) error {
	d, err := daemon.New(p.opts)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.d = d
	p.mu.Unlock()

	go func() {
		if err := d.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "守护进程退出: %v\n", err)
		}
	}()
	return nil
}

// Stop 由服务管理器调用，请求守护进程优雅退出。
func (p *program) Stop(s service.Service) error {
	p.mu.Lock()
	d := p.d
	p.mu.Unlock()

	if d != nil {
		d.Shutdown()
	}
	return nil
}

// LogPath 返回服务模式下的日志文件路径。
//
// 服务进程的 stdout 无法查看（Windows 服务没有控制台），
// 因此把输出重定向到文件，便于排查问题。
func LogPath() (string, error) {
	dir, err := logDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ssh-tunnel", "service.log"), nil
}
