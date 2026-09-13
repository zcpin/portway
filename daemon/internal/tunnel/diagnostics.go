package tunnel

import (
	"context"
	"net"
	"strconv"
	"time"
)

// DiagnosticCheck describes one bounded check, without changing tunnel state.
type DiagnosticCheck struct {
	Stage     string `json:"stage"`
	Status    string `json:"status"` // ok, failed, skipped
	Address   string `json:"address,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Message   string `json:"message"`
	Error     string `json:"error,omitempty"`
}

type TunnelDiagnostic struct {
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	OK        bool              `json:"ok"`
	ElapsedMS int64             `json:"elapsed_ms"`
	Checks    []DiagnosticCheck `json:"checks"`
}

// Diagnose uses a separate SSH connection, preserving the running forwarder and
// its retry state. TCP probes connect and close without sending application data.
func (t *Tunnel) Diagnose(ctx context.Context) TunnelDiagnostic {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mode := t.config.Mode
	if mode == "" {
		mode = "local"
	}
	result := TunnelDiagnostic{Name: t.config.Name, Mode: mode, OK: true}
	result.Checks = append(result.Checks, t.diagnoseListener(ctx, mode))

	sshStarted := time.Now()
	sshCtx, cancelSSH := context.WithTimeout(ctx, 8*time.Second)
	client, err := t.createSSHConnection(sshCtx)
	if err == nil && sshCtx.Err() != nil {
		err = sshCtx.Err()
	}
	cancelSSH()
	sshCheck := DiagnosticCheck{Stage: "ssh", Status: "ok", Address: t.config.SSHHost,
		ElapsedMS: time.Since(sshStarted).Milliseconds(), Message: "SSH 认证与配置的跳板链已通过"}
	if err != nil {
		sshCheck.Status, sshCheck.Error = "failed", err.Error()
		sshCheck.Message = "请检查 SSH 主机、网络、认证和主机指纹；跳板失败会在错误中标明"
	}
	if client != nil {
		stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
		defer func() { stopClose(); _ = client.Close() }()
	}
	result.Checks = append(result.Checks, sshCheck)

	switch {
	case mode == "dynamic":
		result.Checks = append(result.Checks, DiagnosticCheck{Stage: "target", Status: "skipped",
			Message: "SOCKS5 的目标由每次请求指定，本次未检查具体目标服务"})
	case mode == "remote":
		result.Checks = append(result.Checks, diagnoseTarget(ctx, nil, t.localDiagnosticAddress()))
	case err != nil:
		result.Checks = append(result.Checks, DiagnosticCheck{Stage: "target", Status: "skipped",
			Address: net.JoinHostPort(t.config.RemoteHost, strconv.Itoa(t.config.RemotePort)),
			Message: "SSH 连接未通过，无法从 SSH 服务端检查目标"})
	default:
		address := net.JoinHostPort(t.config.RemoteHost, strconv.Itoa(t.config.RemotePort))
		result.Checks = append(result.Checks, diagnoseTarget(ctx, client, address))
	}
	for _, check := range result.Checks {
		if check.Status == "failed" {
			result.OK = false
		}
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	return result
}

func (t *Tunnel) localDiagnosticAddress() string {
	host := t.config.LocalHost
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(t.config.LocalPort))
}

func (t *Tunnel) diagnoseListener(ctx context.Context, mode string) (check DiagnosticCheck) {
	started := time.Now()
	defer func() { check.ElapsedMS = time.Since(started).Milliseconds() }()
	check = DiagnosticCheck{Stage: "local_listener", Status: "ok", Address: t.localDiagnosticAddress()}
	if mode == "remote" {
		check.Stage = "remote_listener"
		check.Address = net.JoinHostPort(t.config.RemoteHost, strconv.Itoa(t.config.RemotePort))
	}

	// Serialize the short local bind check with Start/Stop. Never bind over an
	// active tunnel, including one that is still establishing its listener.
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isRunning {
		listening := false
		if f := t.forwarder; f != nil {
			f.mu.Lock()
			listening = f.listener != nil
			select {
			case <-f.done:
				listening = false
			default:
			}
			f.mu.Unlock()
		}
		if listening {
			check.Message = "当前隧道已建立监听，此端口由隧道自身使用"
		} else {
			check.Status = "failed"
			check.Message = "当前隧道尚未建立监听；未尝试占用端口，以免干扰启动或重连"
			check.Error = t.status.LastError
		}
		return check
	}
	if mode == "remote" {
		check.Status = "skipped"
		check.Message = "隧道已停止，远端监听需在启动后验证；本次不会申请远端端口"
		return check
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", check.Address)
	if err != nil {
		check.Status, check.Error = "failed", err.Error()
		check.Message = "本地端口无法绑定，请检查端口占用和监听地址"
		return check
	}
	_ = listener.Close()
	check.Message = "本地端口可以绑定，探测结束后已释放"
	return check
}

func diagnoseTarget(ctx context.Context, client sshConn, address string) DiagnosticCheck {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	check := DiagnosticCheck{Stage: "target", Status: "ok", Address: address,
		Message: "本机可以连接目标 TCP 服务"}
	var conn net.Conn
	var err error
	if client == nil {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
	} else {
		// Closing this diagnostic-only transport also releases a pending SSH
		// channel if the server never answers its open request.
		stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
		defer stopClose()
		forwarder := Forwarder{sshClient: client}
		conn, err = forwarder.dialSSH(ctx, address)
		check.Message = "SSH 服务端可以连接目标 TCP 服务"
	}
	if conn != nil {
		_ = conn.Close()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		check.Status, check.Error = "failed", err.Error()
		if client == nil {
			check.Message = "本机无法连接目标，请检查服务是否启动、地址和端口是否正确"
		} else {
			check.Message = "SSH 已通过，但目标 TCP 连接失败；请检查目标服务、地址和 SSH 转发策略"
		}
	}
	check.ElapsedMS = time.Since(started).Milliseconds()
	return check
}
