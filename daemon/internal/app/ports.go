package app

import (
	"errors"
	"fmt"

	"github.com/byteporter/portway/internal/portscan"
)

// 端口扫描相关的业务门面。
//
// 这一层很薄，只做三件事：
//  1. 把隧道的本地监听端口汇总给 portscan.Annotate，让列表能标出「这是自己的隧道」
//  2. 把 portscan 的返回值原样透出（HTTP 与 FFI 两侧共用同一个结构）
//  3. 在结束进程后推一次快照事件，让界面刷新
//
// 真正的平台逻辑都在 portscan 包里——这里不做任何平台判断，两种引擎形态
// （独立 daemon 与进程内引擎）因此共用完全相同的实现。

// PortQuery 是端口列表的筛选条件。
//
// 字段与 portscan.Query 一一对应，但用指针表达「未指定」：
// 查询参数缺失与显式传 false 是不同的语义（前者不过滤，后者只看非系统进程）。
type PortQuery struct {
	Text            string
	Protocol        string
	Scope           string
	System          *bool
	WithCommandLine bool
}

// ScanPorts 扫描本机端口占用，按 query 筛选后返回。
//
// 隧道标注在这里完成：扫描结果里属于本机隧道的条目标上隧道名，
// 用户看到 13306 被占用时能立刻认出「这是我自己的隧道」。
func (a *App) ScanPorts(query PortQuery) (portscan.Snapshot, error) {
	snapshot, err := portscan.Scan(query.WithCommandLine)
	if err != nil {
		return portscan.Snapshot{}, err
	}

	portscan.Annotate(snapshot.Ports, a.tunnelPorts())

	snapshot.Ports = portscan.Filter(snapshot.Ports, portscan.Query{
		Text:     query.Text,
		Protocol: query.Protocol,
		Scope:    query.Scope,
		System:   query.System,
	})
	return snapshot, nil
}

// CheckPort 检测单个端口是否被占用。
//
// protocol 为空时同时检查 TCP 与 UDP。隧道编辑器必须传 "tcp"：隧道不涉及 UDP，
// 把 UDP 冲突报成「端口不可用」会误导用户。
//
// 检测失败（而不是「端口空闲」）会返回 error，调用方要区分这两种情况——
// 端口检测是辅助信息，失败不应阻断保存。
func (a *App) CheckPort(port int, protocol string) (portscan.Check, error) {
	if port < 1 || port > 65535 {
		return portscan.Check{}, fmt.Errorf("端口号 %d 超出范围（1-65535）", port)
	}
	snapshot, err := portscan.Scan(false)
	if err != nil {
		return portscan.Check{}, err
	}
	portscan.Annotate(snapshot.Ports, a.tunnelPorts())
	return portscan.CheckPort(snapshot.Ports, port, protocol), nil
}

// KillProcess 结束占用端口的进程。
//
// 保护规则在 portscan 里判定（拒绝 PID 0/4、自身进程、系统关键进程），
// 命中时返回的 result.Code 是 CodeProtected，界面据此给出不同文案。
//
// 负数 PID 在这里直接拒绝；PID 0 交给 portscan 判定，因为它是「系统空闲进程」
// 而不是「无效输入」——两者给用户的提示完全不同，不该合并成一句「无效的进程号」。
//
// 结束后推一次快照事件：进程退出需要时间，界面应重新扫描而不是只信返回值。
func (a *App) KillProcess(pid int, force bool) (portscan.KillResult, error) {
	if pid < 0 {
		return portscan.KillResult{}, errors.New("无效的进程号")
	}
	// 先取一次进程名：kill 之后进程就没了，名字查不到，而界面要用它
	// 拼「已结束 xxx」的提示。取不到也不影响结束操作本身。
	name := ""
	if info, err := portscan.ReadProcessInfo(pid); err == nil {
		name = info.Name
	}
	result := portscan.Kill(pid, name, force)
	if result.OK {
		a.emitSnapshot()
	}
	return result, nil
}

// ResolveProcessPath 把 PID 解析成可执行文件的绝对路径。
//
// 只返回路径，不打开文件管理器：daemon 可能是系统服务，以 LocalSystem 身份
// explorer.exe /select, 打开的窗口不属于用户桌面会话，用户看不到。
// 实际打开由客户端执行。
func (a *App) ResolveProcessPath(pid int) (string, error) {
	return portscan.ResolvePath(pid)
}

// tunnelPorts 汇总本机隧道的本地监听端口，供 Annotate 标注使用。
//
// 只统计在本地监听隧道的模式：
//   - local（本地转发）：本机监听 local_host:local_port
//   - dynamic（SOCKS5）：同上
//   - remote（反向转发）：端口在远端，本机列表里看不到，不参与匹配
func (a *App) tunnelPorts() []portscan.TunnelPort {
	tunnels := a.GetTunnels()
	ports := make([]portscan.TunnelPort, 0, len(tunnels))
	for _, t := range tunnels {
		switch t.Mode {
		case "local", "dynamic", "":
			// 空模式按 local 处理：这是配置的默认值
		default:
			continue
		}
		if t.LocalPort <= 0 {
			continue
		}
		host := t.LocalHost
		if host == "" {
			host = "127.0.0.1"
		}
		ports = append(ports, portscan.TunnelPort{
			Name:  t.Name,
			Host:  host,
			Port:  t.LocalPort,
			Proto: portscan.ProtocolTCP,
		})
	}
	return ports
}
