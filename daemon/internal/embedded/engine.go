// Package embedded 让隧道引擎以「进程内库」的方式运行。
//
// 与 internal/daemon 的差别在于对外形态：daemon 监听回环端口并用 HTTP/WebSocket
// 暴露能力，需要在磁盘上写服务发现文件、协商令牌；这里不监听任何端口、不落盘
// 发现文件，调用方（Flutter 客户端）通过 FFI 直接调用下面的方法，事件通过回调推回。
//
// 业务能力全部复用 internal/app，因此进程内调用与 HTTP API 的行为一致：
// 同样的参数校验、同样的返回结构、同样的推送格式（{"type":..,"<type>":..}）。
//
// 同一进程内同一时间只应存在一个引擎实例：日志级别与日志钩子是包级全局状态
// （见 internal/logger），多个实例会互相覆盖。切换工作区时应先 Stop 再 New。
package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/app"
	"github.com/byteporter/ssh-tunnel/internal/config"
	daemonpkg "github.com/byteporter/ssh-tunnel/internal/daemon"
)

// Version 由构建脚本注入：-ldflags "-X .../internal/embedded.Version=v1.2.3"。
var Version = "dev"

// maxRequestBytes 与 HTTP API 的请求体上限保持一致（1 MiB）。
const maxRequestBytes = 1 << 20

// callTimeout 是诊断类调用的兜底超时。
//
// HTTP 版本用 r.Context() 随客户端断开取消；进程内没有连接可断，
// 因此这里用一个足够宽松的上限兜底，避免异常目标把 goroutine 永久挂住。
const callTimeout = 60 * time.Second

// EventHandler 接收一条事件 JSON，由 FFI 层实现并转发给 Dart。
//
// 事件格式与 WebSocket 事件流完全一致，例如：
//
//	{"type":"snapshot","snapshot":[...]}
//	{"type":"runtime","runtime":{"mysql":{...}}}
//	{"type":"log","log":{"timestamp":"..","level":"info","message":".."}}
//
// 该函数会在引擎的多个 goroutine 上被调用（状态广播、日志、隧道重连），
// 实现方必须自行保证线程安全，并且要快速返回——阻塞这里会拖慢状态广播。
type EventHandler func(eventJSON string)

// Engine 是进程内的隧道引擎。
type Engine struct {
	mu         sync.Mutex
	app        *app.App
	configPath string

	handlerMu sync.RWMutex
	handler   EventHandler

	cancel    context.CancelFunc
	watchDone chan struct{}
	started   bool
}

// New 按配置路径创建引擎（不启动隧道、不监听端口）。
//
// 配置路径为空时按 daemon 的默认规则查找，不存在时创建一份最小配置。
func New(configPath string) (*Engine, error) {
	resolved, err := daemonpkg.ResolveConfigPath(configPath)
	if err != nil {
		return nil, err
	}

	application, err := app.New(resolved)
	if err != nil {
		return nil, err
	}

	e := &Engine{app: application, configPath: resolved}
	// 事件出口用桥接实现，这样 Dart 侧随时可以重新注册回调（例如热重启）
	// 而不需要重建引擎。
	application.SetEmitter(bridge{e})
	return e, nil
}

// Start 启动随配置自动启动的隧道，并开启状态广播与网络变化监测。
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return nil
	}

	if err := e.app.Start(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.cancel = cancel
	e.watchDone = done
	e.started = true

	// 休眠与换网恢复在进程内同样有效：客户端退出即进程退出，无需常驻。
	go func() {
		defer close(done)
		e.app.WatchNetwork(ctx)
	}()
	return nil
}

// Stop 停止所有隧道与后台监测。可重复调用。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return
	}
	cancel, done := e.cancel, e.watchDone
	e.cancel, e.watchDone, e.started = nil, nil, false
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	e.app.Stop()
}

// Running 报告引擎是否已启动。
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

// ConfigPath 返回实际使用的配置文件绝对路径。
func (e *Engine) ConfigPath() string { return e.configPath }

// SetEventHandler 注册事件出口，随后立即补发一次完整快照。
//
// 等价于 WebSocket 连接建立时的行为：先给全量列表，再靠增量事件保持同步，
// 因此客户端无需在订阅前后额外拉一次列表。传入 nil 表示注销。
func (e *Engine) SetEventHandler(handler EventHandler) {
	e.handlerMu.Lock()
	e.handler = handler
	e.handlerMu.Unlock()

	if handler != nil {
		e.app.SendSnapshot(bridge{e})
	}
}

// bridge 把 app 的事件转给当前注册的 EventHandler。
type bridge struct{ e *Engine }

func (b bridge) Emit(event string, data interface{}) {
	b.e.dispatch(event, data)
}

func (e *Engine) dispatch(event string, data interface{}) {
	e.handlerMu.RLock()
	handler := e.handler
	e.handlerMu.RUnlock()
	if handler == nil {
		return
	}

	payload, err := json.Marshal(map[string]interface{}{
		"type": event,
		event:  data,
	})
	if err != nil {
		return
	}
	handler(string(payload))
}

// ---------- 查询 ----------

// Tunnels 返回隧道列表，结构与 /api/tunnels 一致（含运行状态）。
func (e *Engine) Tunnels() string { return ok(e.app.GetTunnels()) }

// Status 返回 {隧道名: 是否运行}，结构与 /api/status 一致。
func (e *Engine) Status() string { return ok(e.app.GetStatus()) }

// Logs 返回日志缓冲，结构与 /api/logs 一致。
func (e *Engine) Logs() string { return ok(e.app.GetLogs()) }

// Keys 返回配置引用到的私钥列表。
func (e *Engine) Keys() string { return ok(e.app.ListKeys()) }

// SSHConnections 返回 SSH 连接列表。
func (e *Engine) SSHConnections() string { return ok(e.app.GetSSHConnections()) }

// GlobalSettings 返回全局配置项（日志级别、重连默认值）。
func (e *Engine) GlobalSettings() string { return ok(e.app.GetGlobalSettings()) }

// StatKey 校验一个私钥路径是否可读。
func (e *Engine) StatKey(path string) string {
	return result(e.app.StatKey(path))
}

// Info 返回引擎自身的描述信息，供客户端状态栏显示。
func (e *Engine) Info() string {
	return ok(map[string]interface{}{
		"mode":        "embedded",
		"version":     Version,
		"config_path": e.configPath,
		"pid":         os.Getpid(),
		"started":     e.Running(),
	})
}

// ExportConfig 导出当前配置的 TOML 与修订号。
func (e *Engine) ExportConfig() string { return result(e.app.ExportConfig()) }

// ListBackups 列出配置备份。
func (e *Engine) ListBackups() string { return result(e.app.ListBackups()) }

// ReadBackup 读取指定备份内容，返回 {"content": "..."}。
func (e *Engine) ReadBackup(name string) string {
	content, err := e.app.ReadBackup(name)
	if err != nil {
		return fail(err)
	}
	return ok(map[string]string{"content": content})
}

// ---------- 隧道操作 ----------

// StartTunnel / StopTunnel / RestartTunnel / DeleteTunnel 按名称操作单条隧道。
func (e *Engine) StartTunnel(name string) string {
	return e.void(func() error { return e.app.StartTunnel(name) })
}
func (e *Engine) StopTunnel(name string) string {
	return e.void(func() error { return e.app.StopTunnel(name) })
}
func (e *Engine) RestartTunnel(name string) string {
	return e.void(func() error { return e.app.RestartTunnel(name) })
}
func (e *Engine) DeleteTunnel(name string) string {
	return e.void(func() error { return e.app.DeleteTunnel(name) })
}

// AddTunnel 新增隧道；请求体为 Tunnel 的 JSON，字段名写错会直接报错（与 HTTP 一致）。
func (e *Engine) AddTunnel(payload string) string {
	var input config.Tunnel
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return e.void(func() error { return e.app.AddTunnel(input) })
}

// UpdateTunnel 按名称更新隧道。
func (e *Engine) UpdateTunnel(name, payload string) string {
	var input config.Tunnel
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return e.void(func() error { return e.app.UpdateTunnel(name, input) })
}

// BatchTunnels 批量启停，请求体为 {"action":"start|stop","names":[...]}。
func (e *Engine) BatchTunnels(payload string) string {
	var input struct {
		Action string   `json:"action"`
		Names  []string `json:"names"`
	}
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return result(e.app.BatchTunnels(input.Action, input.Names))
}

// DiagnoseTunnel 检查一条隧道的监听、SSH 认证与目标可达性，不启停隧道。
func (e *Engine) DiagnoseTunnel(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return result(e.app.DiagnoseTunnel(ctx, name))
}

// ---------- SSH 连接 ----------

// AddSSHConnection / UpdateSSHConnection / DeleteSSHConnection 维护 SSH 连接。
func (e *Engine) AddSSHConnection(payload string) string {
	var input config.SSHConnection
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return e.void(func() error { return e.app.AddSSHConnection(input) })
}

func (e *Engine) UpdateSSHConnection(name, payload string) string {
	var input config.SSHConnection
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return e.void(func() error { return e.app.UpdateSSHConnection(name, input) })
}

func (e *Engine) DeleteSSHConnection(name string) string {
	return e.void(func() error { return e.app.DeleteSSHConnection(name) })
}

// TestSSHConnection 测试待保存的 SSH 配置，返回耗时与结果，不建立转发。
func (e *Engine) TestSSHConnection(payload string) string {
	var input config.SSHConnection
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return result(e.app.TestSSHConnection(ctx, input))
}

// InspectHostKey 探测目标主机公钥指纹，经过跳板时先按配置认证跳板。
func (e *Engine) InspectHostKey(payload string) string {
	var input config.SSHConnection
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return result(e.app.InspectHostKey(ctx, input))
}

// TrustHostKey 写入信任的主机密钥，请求体为
// {"connection":{...},"fingerprint":"...","replace":false}。
func (e *Engine) TrustHostKey(payload string) string {
	var input struct {
		Connection  config.SSHConnection `json:"connection"`
		Fingerprint string               `json:"fingerprint"`
		Replace     bool                 `json:"replace"`
	}
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return result(e.app.TrustHostKey(ctx, input.Connection, input.Fingerprint, input.Replace))
}

// ---------- 私钥 ----------

// UnlockKey 用口令解锁私钥，请求体为 {"path":"...","passphrase":"..."}。
//
// 口令只用于本次解锁并立即从入参中清除，解析后的密钥保存在进程内存里；
// 进程退出（客户端关闭）后需要重新解锁。
func (e *Engine) UnlockKey(payload string) string {
	var input struct {
		Path       string `json:"path"`
		Passphrase string `json:"passphrase"`
	}
	if err := decodeStrict(payload, &input); err != nil {
		return fail(errors.New("invalid unlock request"))
	}
	if input.Path == "" || len(input.Passphrase) > 4096 {
		return fail(errors.New("invalid path or passphrase length"))
	}

	passphrase := []byte(input.Passphrase)
	input.Passphrase = ""
	defer clear(passphrase)

	return e.void(func() error { return e.app.UnlockKey(input.Path, passphrase) })
}

// LockKey 锁定已解锁的私钥。已建立的连接继续运行，原私钥文件不会被改写。
func (e *Engine) LockKey(path string) string {
	if path == "" {
		return fail(errors.New("path is required"))
	}
	return e.void(func() error {
		e.app.LockKey(path)
		return nil
	})
}

// ---------- 配置 ----------

// Reload 从磁盘重新加载配置并推送快照。
func (e *Engine) Reload() string { return e.void(e.app.ReloadConfig) }

// SetGlobalSettings 更新全局配置项，套用新默认值的运行中隧道会被重启。
func (e *Engine) SetGlobalSettings(payload string) string {
	var input config.GlobalSettings
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return e.void(func() error { return e.app.SetGlobalSettings(input) })
}

// PreviewImport 校验并预览导入内容，请求体为 ImportRequest。
func (e *Engine) PreviewImport(payload string) string {
	var input config.ImportRequest
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return result(e.app.PreviewImport(input))
}

// ImportConfig 应用导入内容（先备份原文件）。
func (e *Engine) ImportConfig(payload string) string {
	var input config.ImportRequest
	if err := decodeStrict(payload, &input); err != nil {
		return fail(err)
	}
	return result(e.app.ImportConfig(input))
}

// ---------- 内部工具 ----------

// void 执行一个无返回值的动作。
func (e *Engine) void(action func() error) string {
	if err := action(); err != nil {
		return fail(err)
	}
	return okPayload(nil)
}

func result[T any](value T, err error) string {
	if err != nil {
		return fail(err)
	}
	return ok(value)
}

// ok 生成成功响应 {"ok":true,"data":...}。
func ok(data interface{}) string { return okPayload(data) }

// fail 生成失败响应 {"ok":false,"error":"..."}。
//
// 所有导出方法都只用这一种错误形态，调用方无需为每个接口判断成功与否。
func fail(err error) string {
	message := "未知错误"
	if err != nil {
		message = err.Error()
	}
	if message == "" {
		message = "操作失败"
	}
	payload, marshalErr := json.Marshal(map[string]interface{}{"ok": false, "error": message})
	if marshalErr != nil {
		return `{"ok":false,"error":"操作失败"}`
	}
	return string(payload)
}

func okPayload(data interface{}) string {
	payload, err := json.Marshal(map[string]interface{}{"ok": true, "data": data})
	if err != nil {
		return fail(fmt.Errorf("序列化结果失败: %w", err))
	}
	return string(payload)
}

// decodeStrict 解析调用方传入的 JSON，拒绝未知字段并限制体积，
// 与 HTTP 侧的 decodeJSON 行为一致（字段名写错不会被静默忽略）。
func decodeStrict(raw string, target interface{}) error {
	if len(raw) > maxRequestBytes {
		return fmt.Errorf("请求体超过 %d 字节上限", maxRequestBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// Call 执行 action 并兜住 panic，把崩溃转换成一条错误响应。
//
// 引擎与界面同进程后，引擎里的 panic 会直接带走整个客户端，因此在每个
// FFI 入口处都要拦住。注意这只覆盖调用它的 goroutine：后台 goroutine
// （状态广播、隧道重连）里的 panic 仍然会终止进程，这是 Go 的语义。
func Call(action func() string) (out string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = fail(fmt.Errorf("引擎内部错误: %v", recovered))
		}
	}()
	return action()
}
