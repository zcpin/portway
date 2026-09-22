// Command portway-daemon 是本地隧道管理守护进程：管理 SSH 隧道，并通过回环地址上的
// HTTP API 与 WebSocket 事件流对外暴露控制能力。它不提供任何界面。
//
// 用法：
//
//	portway-daemon                     前台运行（默认）
//	portway-daemon autostart enable    随用户登录自动启动（无需管理员权限）
//	portway-daemon service install     安装为系统服务（需要管理员权限）
//	portway-daemon update <动作>       检查版本、下载及校验发布包
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kardianos/service"

	"github.com/byteporter/portway/internal/autostart"
	daemonpkg "github.com/byteporter/portway/internal/daemon"
	"github.com/byteporter/portway/internal/svc"
	"github.com/byteporter/portway/internal/update"
)

// version 由构建脚本注入：-ldflags "-X main.version=v1.2.3"
var version = "dev"

// Release builds inject the actual repository, including forks.
var releaseRepository = "zcpin/portway"

func main() {
	args := os.Args[1:]

	// 第一个非 flag 参数视为子命令；没有子命令时按前台运行处理
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "update":
			if err := update.Run(args[1:], version, releaseRepository, os.Stdin, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "更新失败: %v\n", err)
				os.Exit(1)
			}
		case "autostart":
			os.Exit(handleAutostart(args[1:]))
		case "service":
			os.Exit(handleService(args[1:]))
		case "help", "usage":
			printUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", args[0])
			printUsage()
			os.Exit(2)
		}
		return
	}

	os.Exit(runForeground(args))
}

// ---------- 通用参数 ----------

// commonFlags 是各子命令共用的参数集合。
type commonFlags struct {
	configPath string
	addr       string
	noAuth     bool
	logLevel   string
}

func newFlagSet(name string) (*flag.FlagSet, *commonFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	c := &commonFlags{}
	fs.StringVar(&c.configPath, "config", "", "配置文件路径（默认按 portway.toml / config.toml / ~/.portway/config.toml 顺序查找）")
	fs.StringVar(&c.addr, "addr", "127.0.0.1:0", "监听地址，端口 0 表示由系统分配空闲端口")
	fs.BoolVar(&c.noAuth, "no-auth", false, "关闭 token 认证（仅限本机可信环境）")
	fs.StringVar(&c.logLevel, "log-level", "", "覆盖配置中的日志级别：debug/info/warn/error")
	return fs, c
}

// splitAction 把动作词（enable/disable/install/...）从参数中分离出来，
// 这样 `autostart enable -config x` 和 `autostart -config x enable` 都能工作。
func splitAction(args []string) (string, []string) {
	action := ""
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if action == "" && !strings.HasPrefix(a, "-") {
			action = a
			continue
		}
		rest = append(rest, a)
	}
	return action, rest
}

// ---------- 前台运行 ----------

func runForeground(args []string) int {
	fs, c := newFlagSet("portway-daemon")
	showVersion := fs.Bool("version", false, "显示版本并退出")
	hideConsole := fs.Bool("hide-console", false, "隐藏控制台窗口（开机自启时使用）")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(version)
		return 0
	}

	if err := daemonpkg.Run(daemonpkg.Options{
		ConfigPath:  c.configPath,
		Addr:        c.addr,
		NoAuth:      c.noAuth,
		LogLevel:    c.logLevel,
		Version:     version,
		HideConsole: *hideConsole,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "守护进程退出: %v\n", err)
		return 1
	}
	return 0
}

// ---------- 开机自启 ----------

func handleAutostart(args []string) int {
	action, rest := splitAction(args)
	fs, c := newFlagSet("autostart")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	switch action {
	case "enable", "on", "install":
		extra := []string{}
		if c.configPath != "" {
			extra = append(extra, "-config", c.configPath)
		}
		if c.addr != "127.0.0.1:0" {
			extra = append(extra, "-addr", c.addr)
		}
		if c.noAuth {
			extra = append(extra, "-no-auth")
		}
		if c.logLevel != "" {
			extra = append(extra, "-log-level", c.logLevel)
		}
		if err := autostart.Enable(extra); err != nil {
			fmt.Fprintf(os.Stderr, "设置开机自启失败: %v\n", err)
			return 1
		}
		fmt.Println("已设置开机自启，下次登录后自动运行。")

	case "disable", "off", "uninstall":
		if err := autostart.Disable(); err != nil {
			fmt.Fprintf(os.Stderr, "取消开机自启失败: %v\n", err)
			return 1
		}
		fmt.Println("已取消开机自启。")

	case "status":
		enabled, err := autostart.IsEnabled()
		if err != nil {
			fmt.Fprintf(os.Stderr, "查询开机自启状态失败: %v\n", err)
			return 1
		}
		if enabled {
			fmt.Println("开机自启：已启用")
		} else {
			fmt.Println("开机自启：未启用")
		}

	default:
		fmt.Fprintln(os.Stderr, "用法: portway-daemon autostart enable|disable|status")
		return 2
	}
	return 0
}

// ---------- 系统服务 ----------

func handleService(args []string) int {
	action, rest := splitAction(args)
	fs, c := newFlagSet("service")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	opts := svc.Options{
		ConfigPath: c.configPath,
		Addr:       c.addr,
		NoAuth:     c.noAuth,
		LogLevel:   c.logLevel,
		Version:    version,
	}

	// run 由服务管理器调用，先把输出重定向到日志文件：
	// 服务进程没有控制台，stdout 的内容会直接丢失。
	if action == "run" {
		return runService(opts)
	}

	s, err := svc.New(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化服务失败: %v\n", err)
		return 1
	}

	switch action {
	case "install":
		if err := s.Install(); err != nil {
			fmt.Fprintf(os.Stderr, "安装服务失败（需要管理员/root 权限）: %v\n", err)
			return 1
		}
		fmt.Println("服务已安装，执行 start 启动。")

	case "uninstall":
		if err := s.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "卸载服务失败（需要管理员/root 权限）: %v\n", err)
			return 1
		}
		fmt.Println("服务已卸载。")

	case "start":
		if err := s.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "启动服务失败: %v\n", err)
			return 1
		}
		fmt.Println("服务已启动。")

	case "stop":
		if err := s.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "停止服务失败: %v\n", err)
			return 1
		}
		fmt.Println("服务已停止。")

	case "restart":
		if err := s.Restart(); err != nil {
			fmt.Fprintf(os.Stderr, "重启服务失败: %v\n", err)
			return 1
		}
		fmt.Println("服务已重启。")

	case "status":
		status, err := s.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "查询服务状态失败: %v\n", err)
			return 1
		}
		fmt.Printf("服务状态: %s\n", describeStatus(status))

	default:
		fmt.Fprintln(os.Stderr, "用法: portway-daemon service install|uninstall|start|stop|restart|status")
		return 2
	}
	return 0
}

// runService 在服务管理器下运行守护进程，阻塞直到收到停止指令。
func runService(opts svc.Options) int {
	if err := redirectOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "无法打开日志文件: %v\n", err)
		// 日志文件打不开也要继续，否则服务会直接启动失败
	}

	s, err := svc.New(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化服务失败: %v\n", err)
		return 1
	}
	if err := s.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "服务异常退出: %v\n", err)
		return 1
	}
	return 0
}

// redirectOutput 把标准输出与标准错误重定向到日志文件。
func redirectOutput() error {
	path, err := svc.LogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	os.Stdout = f
	os.Stderr = f
	return nil
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[:i]
	}
	return "."
}

func describeStatus(s service.Status) string {
	switch s {
	case service.StatusRunning:
		return "运行中"
	case service.StatusStopped:
		return "已停止"
	default:
		return "未安装"
	}
}

// ---------- 帮助 ----------

func printUsage() {
	fmt.Print(`portway-daemon —— 本地隧道管理守护进程

用法:
  portway-daemon [选项]                    前台运行
  portway-daemon autostart <动作> [选项]   随用户登录自动启动（推荐，无需管理员权限）
  portway-daemon service <动作> [选项]     系统服务托管（需要管理员/root 权限）
  portway-daemon update <动作> [选项]      检查版本、下载及校验发布包

autostart 动作:
  enable     登记开机自启
  disable    取消开机自启
  status     查看是否已启用

update 动作:
  info                              本地版本与安装方式
  check -channel stable|prerelease  检查稳定版或含预发布的渠道
  download -tag v1.2.3 -kind portable|installer  下载并校验

service 动作:
  install    安装服务
  uninstall  卸载服务
  start      启动服务
  stop       停止服务
  restart    重启服务
  status     查看服务状态

选项:
  -config <路径>      配置文件路径
  -addr <地址>        监听地址，默认 127.0.0.1:0（端口由系统分配）
  -no-auth            关闭 token 认证（仅限本机可信环境）
  -log-level <级别>   覆盖日志级别：debug/info/warn/error
  -version            显示版本

示例:
  portway-daemon -config ~/.portway/config.toml
  portway-daemon autostart enable -config ~/.portway/config.toml
  portway-daemon service install -config ~/.portway/config.toml
`)
}
