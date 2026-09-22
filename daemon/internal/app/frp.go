package app

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/byteporter/portway/internal/frp"
)

// frpConfigDir 返回 FRP 客户端配置目录。
//
// 放在配置文件旁边（<配置目录>/frp/clients），而不是固定的用户目录：
// 每个工作区因此自带一套 FRP 配置，切换工作区不会串台，
// 自定义数据目录（PORTWAY_DATA_DIR）之类也不需要额外适配。
func frpConfigDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "frp", "clients")
}

// FrpDir 返回 FRP 配置目录，供界面显示配置从哪来。
func (a *App) FrpDir() string {
	if a.frp == nil {
		return frpConfigDir(a.configPath)
	}
	return a.frp.Dir()
}

// frpManager 取出 FRP 管理器，功能不可用时给出明确错误。
func (a *App) frpManager() (*frp.Manager, error) {
	if a.frp == nil {
		return nil, errors.New("FRP 功能不可用：配置目录初始化失败，详见日志")
	}
	return a.frp, nil
}

// GetFrpClients 返回全部 FRP 客户端及其代理、运行状态。
func (a *App) GetFrpClients() []frp.ClientInfo {
	if a.frp == nil {
		return nil
	}
	return a.frp.List()
}

// ---------- 客户端 ----------

// AddFrpClient 新建一个 FRP 客户端。
func (a *App) AddFrpClient(payload frp.ClientPayload) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.Add(payload); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// UpdateFrpClient 更新 FRP 客户端配置。
func (a *App) UpdateFrpClient(name string, payload frp.ClientPayload) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.Update(name, payload); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// DeleteFrpClient 删除 FRP 客户端。
func (a *App) DeleteFrpClient(name string) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.Delete(name); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// StartFrpClient / StopFrpClient / RestartFrpClient 控制单个客户端。
func (a *App) StartFrpClient(name string) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.StartClient(name); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

func (a *App) StopFrpClient(name string) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.StopClient(name); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

func (a *App) RestartFrpClient(name string) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.RestartClient(name); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// ---------- 代理 ----------

// AddFrpProxy 在指定客户端下新增一条代理。
func (a *App) AddFrpProxy(client string, payload frp.ProxyPayload) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.AddProxy(client, payload); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// UpdateFrpProxy 更新一条代理（改名也走这里）。
func (a *App) UpdateFrpProxy(client, proxy string, payload frp.ProxyPayload) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.UpdateProxy(client, proxy, payload); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// DeleteFrpProxy 删除一条代理。
func (a *App) DeleteFrpProxy(client, proxy string) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.DeleteProxy(client, proxy); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// ToggleFrpProxy 启用或停用一条代理，热更新生效，不重连。
func (a *App) ToggleFrpProxy(client, proxy string, enabled bool) error {
	mgr, err := a.frpManager()
	if err != nil {
		return err
	}
	if err := mgr.ToggleProxy(client, proxy, enabled); err != nil {
		return err
	}
	a.emitFrp()
	return nil
}

// ---------- 事件 ----------

// broadcastFrp 兜底广播 FRP 列表。
//
// 代理的连接状态由 frp 自行推进（拨号、重试、被服务端拒绝），
// 不会经过我们这里，轮询是唯一能捕捉到这些变化的方式。
func (a *App) broadcastFrp() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var last string
	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.emitFrpIfChanged(&last)
		}
	}
}

// emitFrp 主动推送一次完整列表，用于配置变更之后。
func (a *App) emitFrp() {
	a.frpMu.Lock()
	defer a.frpMu.Unlock()

	if a.frp == nil {
		return
	}
	if e := a.getEmitter(); e != nil {
		e.Emit("frp_snapshot", a.frp.List())
	}
}

// emitFrpIfChanged 只在内容真的变化时推送，避免每两秒刷一遍界面。
func (a *App) emitFrpIfChanged(last *string) {
	a.frpMu.Lock()
	defer a.frpMu.Unlock()

	if a.frp == nil {
		return
	}

	clients := a.frp.List()
	payload, err := json.Marshal(clients)
	if err != nil {
		return
	}
	if string(payload) == *last {
		return
	}
	*last = string(payload)

	if e := a.getEmitter(); e != nil {
		e.Emit("frp_snapshot", clients)
	}
}
