package frp

import (
	"testing"
	"time"
)

// newTestManager 创建一个指向本机空闲端口的客户端配置：
// 连接一定失败，但实例应当保持运行并重试，这正是界面需要的语义。
func newTestManager(t *testing.T) *Manager {
	t.Helper()

	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Stop)

	payload := ClientPayload{
		Name:       "prod",
		ServerAddr: "127.0.0.1",
		ServerPort: 1,
		AuthMethod: AuthToken,
		AuthToken:  "secret",
		TLSEnable:  false,
	}
	if err := mgr.Add(payload); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return mgr
}

func TestManagerAddAndList(t *testing.T) {
	mgr := newTestManager(t)

	clients := mgr.List()
	if len(clients) != 1 {
		t.Fatalf("客户端数量 = %d，期望 1", len(clients))
	}

	client := clients[0]
	if client.Name != "prod" || client.ServerAddr != "127.0.0.1" || client.ServerPort != 1 {
		t.Errorf("客户端字段不正确: %+v", client)
	}
	if !client.AutoStart {
		t.Error("新建客户端应默认自动启动")
	}
	if client.Running {
		t.Error("新建客户端不应处于运行状态")
	}
	if len(client.Proxies) != 0 {
		t.Errorf("新客户端不应有代理: %+v", client.Proxies)
	}
}

func TestManagerAddRejectsDuplicate(t *testing.T) {
	mgr := newTestManager(t)

	err := mgr.Add(ClientPayload{Name: "prod", ServerAddr: "example.com", ServerPort: 7000})
	if err == nil {
		t.Fatal("重名客户端竟然创建成功了")
	}
}

// 客户端连不上服务器时应当保持运行并重试，而不是直接退出。
func TestManagerStartKeepsRunningWithoutServer(t *testing.T) {
	mgr := newTestManager(t)

	if err := mgr.StartClient("prod"); err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	defer mgr.StopClient("prod")

	// 给连接尝试留出时间：如果 loginFailExit 生效，实例会在这段时间内退出
	time.Sleep(300 * time.Millisecond)

	info, err := mgr.Info("prod")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !info.Running {
		t.Fatalf("服务器不可达时客户端不应退出: %+v", info)
	}
	if info.StartedAt == "" {
		t.Error("运行中的客户端应带有启动时间")
	}

	if err := mgr.StopClient("prod"); err != nil {
		t.Fatalf("StopClient: %v", err)
	}
	if info, _ := mgr.Info("prod"); info.Running {
		t.Error("停止后仍在运行")
	}
}

func TestManagerProxyLifecycle(t *testing.T) {
	mgr := newTestManager(t)

	payload := ProxyPayload{
		Name:       "mysql",
		Type:       TypeTCP,
		LocalIP:    "127.0.0.1",
		LocalPort:  3306,
		RemotePort: 13306,
	}
	if err := mgr.AddProxy("prod", payload); err != nil {
		t.Fatalf("AddProxy: %v", err)
	}

	info, err := mgr.Info("prod")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if len(info.Proxies) != 1 {
		t.Fatalf("代理数量 = %d，期望 1", len(info.Proxies))
	}
	proxy := info.Proxies[0]
	if proxy.Name != "mysql" || proxy.LocalPort != 3306 || proxy.RemotePort != 13306 {
		t.Fatalf("代理字段不正确: %+v", proxy)
	}
	if !proxy.Enabled {
		t.Error("新建代理应默认启用")
	}

	// 改名 + 改端口
	updated := payload
	updated.Name = "mysql-prod"
	updated.RemotePort = 23306
	if err := mgr.UpdateProxy("prod", "mysql", updated); err != nil {
		t.Fatalf("UpdateProxy: %v", err)
	}
	info, _ = mgr.Info("prod")
	if len(info.Proxies) != 1 || info.Proxies[0].Name != "mysql-prod" || info.Proxies[0].RemotePort != 23306 {
		t.Fatalf("更新代理失败: %+v", info.Proxies)
	}

	// 停用
	if err := mgr.ToggleProxy("prod", "mysql-prod", false); err != nil {
		t.Fatalf("ToggleProxy: %v", err)
	}
	info, _ = mgr.Info("prod")
	if info.Proxies[0].Enabled {
		t.Error("停用后 enabled 仍为 true")
	}

	// 删除
	if err := mgr.DeleteProxy("prod", "mysql-prod"); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	info, _ = mgr.Info("prod")
	if len(info.Proxies) != 0 {
		t.Errorf("删除后仍有代理: %+v", info.Proxies)
	}
}

func TestManagerAddProxyRejectsBadPayload(t *testing.T) {
	mgr := newTestManager(t)

	cases := map[string]ProxyPayload{
		"缺本地端口": {Name: "db", Type: TypeTCP, RemotePort: 13306},
		"未支持类型": {Name: "db", Type: TypeSTCP, LocalPort: 3306},
	}
	for name, payload := range cases {
		if err := mgr.AddProxy("prod", payload); err == nil {
			t.Errorf("%s：竟然创建成功了", name)
		}
	}

	// 校验失败时不应该留下半截配置
	if info, _ := mgr.Info("prod"); len(info.Proxies) != 0 {
		t.Errorf("校验失败却写入了代理: %+v", info.Proxies)
	}
}

func TestManagerProxyNameConflict(t *testing.T) {
	mgr := newTestManager(t)

	payload := ProxyPayload{Name: "web", Type: TypeTCP, LocalIP: "127.0.0.1", LocalPort: 8080, RemotePort: 18080}
	if err := mgr.AddProxy("prod", payload); err != nil {
		t.Fatalf("AddProxy: %v", err)
	}
	if err := mgr.AddProxy("prod", payload); err == nil {
		t.Error("同名代理竟然创建成功了")
	}
}

func TestManagerDeleteRemovesClient(t *testing.T) {
	mgr := newTestManager(t)

	if err := mgr.StartClient("prod"); err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	if err := mgr.Delete("prod"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if clients := mgr.List(); len(clients) != 0 {
		t.Errorf("删除后列表仍非空: %+v", clients)
	}
	if mgr.instance("prod") != nil {
		t.Error("删除后实例仍在运行")
	}
}

// 运行中修改客户端级配置要能生效，且不因"没改任何东西"而白重启。
func TestManagerUpdate(t *testing.T) {
	mgr := newTestManager(t)

	if err := mgr.StartClient("prod"); err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	defer mgr.StopClient("prod")

	startedAt := func() string {
		info, _ := mgr.Info("prod")
		return info.StartedAt
	}
	before := startedAt()

	// 原样保存：不应该重启
	if err := mgr.Update("prod", ClientPayload{
		ServerAddr: "127.0.0.1", ServerPort: 1, AuthMethod: AuthToken, AuthToken: "secret",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := startedAt(); got != before {
		t.Errorf("配置未变化时不应重启（%s → %s）", before, got)
	}

	// 改服务器端口：应当重启
	if err := mgr.Update("prod", ClientPayload{
		ServerAddr: "127.0.0.1", ServerPort: 2, AuthMethod: AuthToken, AuthToken: "secret",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if info, _ := mgr.Info("prod"); info.ServerPort != 2 {
		t.Errorf("服务器端口未更新: %+v", info)
	}
}

// 自动启动标记为 false 的客户端不应在 Start 时被拉起。
func TestManagerStartSkipsManualClients(t *testing.T) {
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()

	auto := false
	payload := ClientPayload{
		Name: "manual", ServerAddr: "127.0.0.1", ServerPort: 1, AutoStart: &auto,
	}
	if err := mgr.Add(payload); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info, _ := mgr.Info("manual"); info.Running {
		t.Error("手动启动的客户端被自动拉起了")
	}

	// 手动启动仍然可用
	if err := mgr.StartClient("manual"); err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	if info, _ := mgr.Info("manual"); !info.Running {
		t.Error("手动启动失败")
	}
}

// 配置写坏的客户端也要出现在列表里，否则用户没法在界面上删掉它。
func TestManagerListsBrokenConfig(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()

	if err := writeFile(t, mgr.store.Path("broken"), "这不是合法的 TOML ==="); err != nil {
		t.Fatalf("写入损坏配置: %v", err)
	}

	clients := mgr.List()
	if len(clients) != 1 {
		t.Fatalf("客户端数量 = %d，期望 1", len(clients))
	}
	if clients[0].LastError == "" {
		t.Error("损坏的配置应给出错误说明")
	}
	if err := mgr.Delete("broken"); err != nil {
		t.Errorf("损坏的配置应仍可删除: %v", err)
	}
}
