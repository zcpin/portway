package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/byteporter/portway/internal/app"
	"github.com/byteporter/portway/internal/portscan"
)

// 端口占用查询与检测的 HTTP 接口。
//
// 四个接口都只读本机状态，不涉及远程资源；鉴权与其它接口一致（X-Auth-Token）。

// handleListPorts 返回端口占用列表。
//
// 筛选放在服务端：端口条目在几百到几千量级，每次刷新都要重新采集，
// 服务端筛选能避免把全量数据传到 Dart 侧再过滤。同时 with_command_line
// 是按需补全的开关（Windows 上走 WMI，200–500ms），必须在服务端控制。
//
// 注意：界面上的即时输入过滤仍在客户端做（用户敲字不该每次都打 HTTP），
// 两者共用同一套匹配规则（见 portscan.Filter）。
func (s *Server) handleListPorts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// system 是三态：缺失=不过滤，true=仅系统，false=仅非系统
	var system *bool
	if raw := strings.TrimSpace(query.Get("system")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "system 参数必须是 true 或 false")
			return
		}
		system = &value
	}

	protocol := strings.ToLower(strings.TrimSpace(query.Get("protocol")))
	if protocol != "" && protocol != portscan.ProtocolTCP && protocol != portscan.ProtocolUDP {
		writeError(w, http.StatusBadRequest, "protocol 参数必须是 tcp 或 udp")
		return
	}

	scope := strings.ToLower(strings.TrimSpace(query.Get("scope")))
	switch scope {
	case "", portscan.ScopeAll, portscan.ScopeLoopback, portscan.ScopeSpecific:
	default:
		writeError(w, http.StatusBadRequest, "scope 参数必须是 all、loopback 或 specific")
		return
	}

	withCommandLine, _ := strconv.ParseBool(query.Get("with_command_line"))

	snapshot, err := s.app.ScanPorts(app.PortQuery{
		Text:            strings.TrimSpace(query.Get("q")),
		Protocol:        protocol,
		Scope:           scope,
		System:          system,
		WithCommandLine: withCommandLine,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// handleCheckPort 检测单个端口是否被占用。
//
// protocol 省略时同时检查 TCP 与 UDP——用户排查 UDP 冲突时要用。
// 隧道编辑器调用时必须传 protocol=tcp。
func (s *Server) handleCheckPort(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("port"))
	if raw == "" {
		writeError(w, http.StatusBadRequest, "缺少 port 参数")
		return
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "port 参数必须是数字")
		return
	}

	protocol := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	if protocol != "" && protocol != portscan.ProtocolTCP && protocol != portscan.ProtocolUDP {
		writeError(w, http.StatusBadRequest, "protocol 参数必须是 tcp 或 udp")
		return
	}

	check, err := s.app.CheckPort(port, protocol)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, check)
}

// handleKillProcess 结束占用端口的进程。
func (s *Server) handleKillProcess(w http.ResponseWriter, r *http.Request) {
	// PID 用指针接收：缺字段与显式传 0 必须区分开。
	// 缺字段是请求格式错误（400），显式传 0 是「想结束空闲进程」（由保护规则拒绝）。
	var input struct {
		PID   *int `json:"pid"`
		Force bool `json:"force"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.PID == nil {
		writeError(w, http.StatusBadRequest, "缺少 pid 参数")
		return
	}

	result, err := s.app.KillProcess(*input.PID, input.Force)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 保护规则命中不是「接口调用失败」而是「业务上拒绝」，
	// 因此返回 200 并让界面按 result.Code 决定文案。
	writeJSON(w, http.StatusOK, result)
}

// handleRevealProcess 解析进程的可执行文件路径。
//
// 只返回路径，实际打开文件管理器由客户端执行：daemon 可能是系统服务，
// 以 LocalSystem 身份 explorer.exe /select, 打开的窗口不属于用户桌面会话。
func (s *Server) handleRevealProcess(w http.ResponseWriter, r *http.Request) {
	// 同 kill：缺字段与显式 0 语义不同，用指针区分。
	var input struct {
		PID *int `json:"pid"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.PID == nil {
		writeError(w, http.StatusBadRequest, "缺少 pid 参数")
		return
	}

	path, err := s.app.ResolveProcessPath(*input.PID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}
