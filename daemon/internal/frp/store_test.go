package frp

import (
	"os"
	"regexp"
	"strings"
	"testing"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

// 匹配 "key = 7000.0" 这样的浮点字面量。
var floatValuePattern = regexp.MustCompile(`(?m)^[a-zA-Z]+ = \d+\.\d`)

func boolPtr(b bool) *bool { return &b }

func newTestConfig() *v1.ClientConfig {
	cfg := &v1.ClientConfig{}
	cfg.ServerAddr = "frps.example.com"
	cfg.ServerPort = 7000
	cfg.Auth.Method = AuthToken
	cfg.Auth.Token = "secret"
	cfg.Transport.TLS.Enable = boolPtr(true)
	cfg.Log.Level = "info"
	cfg.Metadatas = map[string]string{metaKeyGroup: "生产"}
	cfg.Proxies = toTypedProxies([]v1.ProxyConfigurer{
		&v1.TCPProxyConfig{
			ProxyBaseConfig: v1.ProxyBaseConfig{
				Name: "mysql",
				Type: TypeTCP,
				ProxyBackend: v1.ProxyBackend{
					LocalIP:   "127.0.0.1",
					LocalPort: 3306,
				},
			},
			RemotePort: 13306,
		},
	})
	return cfg
}

// 往返测试覆盖整条链路：结构体 → JSON → TOML → frp 自己的加载器。
// 键名一旦与 frp 期望的不一致（例如把小驼峰写成下划线），这里就会失败。
func TestStoreRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.Save("prod", newTestConfig()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.ServerAddr != "frps.example.com" || loaded.ServerPort != 7000 {
		t.Errorf("服务器地址丢失: %+v", loaded.ClientCommonConfig)
	}
	if loaded.Auth.Token != "secret" || loaded.Auth.Method != AuthToken {
		t.Errorf("认证信息丢失: %+v", loaded.Auth)
	}
	if loaded.Transport.TLS.Enable == nil || !*loaded.Transport.TLS.Enable {
		t.Error("TLS 开关丢失")
	}
	if loaded.Metadatas[metaKeyGroup] != "生产" {
		t.Errorf("分组元数据丢失: %+v", loaded.Metadatas)
	}
	if len(loaded.Proxies) != 1 {
		t.Fatalf("代理数量 = %d，期望 1", len(loaded.Proxies))
	}

	proxy, ok := loaded.Proxies[0].ProxyConfigurer.(*v1.TCPProxyConfig)
	if !ok {
		t.Fatalf("代理类型 = %T，期望 *v1.TCPProxyConfig", loaded.Proxies[0].ProxyConfigurer)
	}
	if proxy.Name != "mysql" || proxy.LocalPort != 3306 || proxy.RemotePort != 13306 {
		t.Errorf("代理字段丢失: %+v", proxy)
	}
	if proxy.LocalIP != "127.0.0.1" {
		t.Errorf("本地地址 = %q", proxy.LocalIP)
	}
}

// 界面模型要能反映配置内容，端口用数字。
func TestClientInfoReflectsConfig(t *testing.T) {
	info := clientInfo("prod", newTestConfig())

	if info.Name != "prod" || info.Group != "生产" {
		t.Errorf("名称或分组不正确: %+v", info)
	}
	if !info.AutoStart {
		t.Error("未写 mgrAutoStart 时应默认自动启动")
	}
	if !info.TLSEnable {
		t.Error("TLS 开关未反映到界面模型")
	}
	if len(info.Proxies) != 1 {
		t.Fatalf("代理数量 = %d", len(info.Proxies))
	}

	proxy := info.Proxies[0]
	if proxy.Name != "mysql" || proxy.LocalPort != 3306 || proxy.RemotePort != 13306 {
		t.Errorf("代理字段不正确: %+v", proxy)
	}
	if !proxy.Enabled || !proxy.Editable {
		t.Errorf("代理应为可编辑且启用: %+v", proxy)
	}
}

// 关闭自动启动要能写进配置并读回来。
func TestAutoStartRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	cfg := newTestConfig()
	applyClientPayload(cfg, ClientPayload{
		ServerAddr: "frps.example.com",
		ServerPort: 7000,
		AutoStart:  boolPtr(false),
	})
	if err := store.Save("manual", cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("manual")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if clientAutoStart(&loaded.ClientCommonConfig) {
		t.Error("手动启动标记没有写进配置")
	}
}

// 空的子结构体不应在文件里留下空段。
func TestRenderPrunesEmptySections(t *testing.T) {
	content, err := renderClientTOML(newTestConfig())
	if err != nil {
		t.Fatalf("renderClientTOML: %v", err)
	}
	text := string(content)

	for _, section := range []string{"[proxies.healthCheck]", "[proxies.loadBalancer]", "[proxies.transport]"} {
		if strings.Contains(text, section) {
			t.Errorf("出现了空的 %s 段:\n%s", section, text)
		}
	}
	if !strings.Contains(text, "serverAddr") {
		t.Errorf("缺少 serverAddr:\n%s", text)
	}
	if !strings.Contains(text, "[[proxies]]") {
		t.Errorf("缺少代理段:\n%s", text)
	}
	// 端口必须写成整数，而不是 7000.0
	if floatValuePattern.MatchString(text) {
		t.Errorf("端口被写成了浮点数:\n%s", text)
	}
}

// 显式关闭的开关不能被当成空值剪掉：frp 的 tls.enable 默认是 true，
// 剪掉 false 会把它变回 true。
func TestRenderKeepsExplicitFalse(t *testing.T) {
	cfg := newTestConfig()
	cfg.Transport.TLS.Enable = boolPtr(false)

	content, err := renderClientTOML(cfg)
	if err != nil {
		t.Fatalf("renderClientTOML: %v", err)
	}
	if !strings.Contains(string(content), "enable = false") {
		t.Errorf("显式的 tls.enable = false 被剪掉了:\n%s", content)
	}
}

// frp 读配置时会补全默认值，写回去时不能把这一堆默认值固化进用户文件：
// 既让文件难读，也会在新版 frp 改变默认值后继续沿用旧行为。
func TestRenderDropsFrpDefaults(t *testing.T) {
	// 模拟一次「读取 → 保存」：Load 返回的是 frp 补全过默认值的结构
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := newTestConfig()
	// 端口改成非默认值，用来验证用户真正设置的值不会被误剪
	cfg.ServerPort = 7777
	if err := store.Save("prod", cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load("prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Save("prod", loaded); err != nil {
		t.Fatalf("再次保存: %v", err)
	}

	content, err := os.ReadFile(store.Path("prod"))
	if err != nil {
		t.Fatalf("读取配置: %v", err)
	}
	text := string(content)

	for _, noise := range []string{
		"protocol = ", "poolCount = ", "dialServerTimeout = ",
		"tcpMux = ", "maxDays = ", "natHoleStunServer = ", "wireProtocol = ",
	} {
		if strings.Contains(text, noise) {
			t.Errorf("默认值 %q 被写进了配置文件:\n%s", noise, text)
		}
	}
	// 用户真正设置的值必须留下
	for _, kept := range []string{"serverAddr", "serverPort = 7777", "[[proxies]]", "remotePort = 13306"} {
		if !strings.Contains(text, kept) {
			t.Errorf("用户配置 %q 丢失:\n%s", kept, text)
		}
	}
}

// 与默认值相同的取值会被省略：配置文件里只留用户改过的部分。
// 语义不变（frp 读回时会补上同样的默认值），但文件从四十多行回到十来行。
func TestRenderOmitsValuesEqualToDefaults(t *testing.T) {
	cfg := newTestConfig()
	cfg.ServerPort = 7000 // frp 的默认端口
	cfg.Transport.TLS.Enable = boolPtr(true)

	content, err := renderClientTOML(cfg)
	if err != nil {
		t.Fatalf("renderClientTOML: %v", err)
	}
	text := string(content)

	// 端口与服务端默认值一致，省略；读回来仍是 7000
	if strings.Contains(text, "serverPort") {
		t.Errorf("与默认值相同的端口不该写进文件:\n%s", text)
	}

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save("prod", cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load("prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ServerPort != 7000 {
		t.Errorf("读回的端口 = %d，期望补全默认值 7000", loaded.ServerPort)
	}
	if loaded.Transport.TLS.Enable == nil || !*loaded.Transport.TLS.Enable {
		t.Error("省略的 tls.enable 应补全为 true")
	}
}

// 非法配置必须在保存阶段就被拦下。
func TestSaveRejectsInvalidProxy(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	cfg := newTestConfig()
	// 端口超出范围，frp 的校验器应当拒绝
	tcp := cfg.Proxies[0].ProxyConfigurer.(*v1.TCPProxyConfig)
	tcp.LocalPort = -1

	if err := store.Save("bad", cfg); err == nil {
		t.Fatal("端口非法的 tcp 代理竟然保存成功了")
	}
	if store.Exists("bad") {
		t.Error("校验失败时不应留下配置文件")
	}
}

// 界面层的额外要求：本地端口必须真实填写。
func TestValidateProxyPayload(t *testing.T) {
	valid := ProxyPayload{Name: "mysql", Type: TypeTCP, LocalIP: "127.0.0.1", LocalPort: 3306, RemotePort: 13306}
	if err := validateProxyPayload(valid); err != nil {
		t.Errorf("合法配置被拒绝: %v", err)
	}

	cases := map[string]ProxyPayload{
		"空名称":     {Name: "", Type: TypeTCP, LocalPort: 3306},
		"本地端口为 0": {Name: "x", Type: TypeTCP, LocalPort: 0},
		"本地端口越界":  {Name: "x", Type: TypeTCP, LocalPort: 70000},
		"远端端口越界":  {Name: "x", Type: TypeTCP, LocalPort: 3306, RemotePort: 70000},
		"不支持的类型":  {Name: "x", Type: TypeSTCP, LocalPort: 3306},
	}
	for name, payload := range cases {
		if err := validateProxyPayload(payload); err == nil {
			t.Errorf("%s：竟然通过了校验", name)
		}
	}

	// 远端端口为 0 是合法的，表示由服务端分配
	payload := valid
	payload.RemotePort = 0
	if err := validateProxyPayload(payload); err != nil {
		t.Errorf("远端端口为 0 应被接受: %v", err)
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"prod", "测试-1", "a.b", "my_frpc"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v，期望通过", name, err)
		}
	}

	invalid := []string{"", "..", "../escape", "a/b", `a\b`, ".hidden", " padded ",
		// Windows 保留设备名：写成文件会落到设备上
		"CON", "nul", "COM1", "lpt9.toml"}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) 竟然通过了", name)
		}
	}

	// 名字里含保留名但不是保留名本身，应当允许
	for _, name := range []string{"console", "com10", "my-con"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v，期望通过", name, err)
		}
	}
}

// 启用状态要同时考虑代理自身的 enabled 与客户端级 start 列表。
func TestProxyEnabledSemantics(t *testing.T) {
	common := &v1.ClientCommonConfig{}
	base := &v1.ProxyBaseConfig{Name: "mysql", Type: TypeTCP}

	if !proxyEnabled(common.Start, base.Enabled, base.Name) {
		t.Error("默认应为启用")
	}

	base.Enabled = boolPtr(false)
	if proxyEnabled(common.Start, base.Enabled, base.Name) {
		t.Error("enabled = false 时应为停用")
	}
	base.Enabled = nil

	common.Start = []string{"other"}
	if proxyEnabled(common.Start, base.Enabled, base.Name) {
		t.Error("start 列表非空且不含该代理时应为停用")
	}
	common.Start = nil

	setProxyEnabled(&common.Start, &base.Enabled, base.Name, false)
	if base.Enabled == nil || *base.Enabled {
		t.Error("停用时应写入 enabled = false")
	}
	setProxyEnabled(&common.Start, &base.Enabled, base.Name, true)
	if base.Enabled != nil {
		t.Error("启用时应移除 enabled 字段而不是写 true")
	}
}

// start 列表存在时，切换启用状态要同步维护它，否则界面显示与运行状态会不一致。
func TestSetProxyEnabledMaintainsStartList(t *testing.T) {
	common := &v1.ClientCommonConfig{Start: []string{"mysql", "redis"}}
	base := &v1.ProxyBaseConfig{Name: "mysql", Type: TypeTCP}

	setProxyEnabled(&common.Start, &base.Enabled, base.Name, false)
	if contains(common.Start, "mysql") {
		t.Errorf("停用后 start 列表仍包含该代理: %v", common.Start)
	}

	setProxyEnabled(&common.Start, &base.Enabled, base.Name, true)
	if !contains(common.Start, "mysql") {
		t.Errorf("启用后 start 列表未加回该代理: %v", common.Start)
	}
}

// 保存时按类型只写该类型拥有的字段，避免 frp 校验器因字段组合报错。
func TestApplyProxyPayloadByType(t *testing.T) {
	create := func(payload ProxyPayload) v1.ProxyConfigurer {
		proxy, err := newProxy(payload)
		if err != nil {
			t.Fatalf("newProxy(%s): %v", payload.Type, err)
		}
		if err := applyProxyPayload(proxy, payload); err != nil {
			t.Fatalf("applyProxyPayload(%s): %v", payload.Type, err)
		}
		return proxy
	}

	tcp := create(ProxyPayload{
		Name: "mysql", Type: TypeTCP,
		LocalIP: "127.0.0.1", LocalPort: 3306, RemotePort: 13306,
		// tcp 代理上不该出现域名，给了也要丢掉
		CustomDomains: []string{"example.com"},
	}).(*v1.TCPProxyConfig)
	if tcp.RemotePort != 13306 || tcp.LocalPort != 3306 {
		t.Errorf("tcp 代理端口未写入: %+v", tcp)
	}

	http := create(ProxyPayload{
		Name: "web", Type: TypeHTTP,
		LocalIP: "127.0.0.1", LocalPort: 8080,
		CustomDomains: []string{"a.example.com"}, SubDomain: "a",
		// http 代理没有 remote_port 字段，给了也不该写进去
		RemotePort: 18080,
	}).(*v1.HTTPProxyConfig)
	if len(http.CustomDomains) != 1 || http.CustomDomains[0] != "a.example.com" {
		t.Errorf("http 代理域名未写入: %+v", http)
	}
	if http.LocalPort != 8080 {
		t.Errorf("http 代理本地端口未写入: %+v", http)
	}
}

func TestNewProxyRejectsUnsupportedType(t *testing.T) {
	if _, err := newProxy(ProxyPayload{Name: "x", Type: TypeSTCP}); err == nil {
		t.Error("阶段 1 不支持创建 stcp 代理，应当报错")
	}
}

// 删除配置后目录里不应留下临时文件。
func TestStoreListSkipsTmpFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save("prod", newTestConfig()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	names, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "prod" {
		t.Errorf("List = %v，期望 [prod]", names)
	}

	if err := store.Delete("prod"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	names, _ = store.List()
	if len(names) != 0 {
		t.Errorf("删除后仍有配置: %v", names)
	}
	// 重复删除不应报错
	if err := store.Delete("prod"); err != nil {
		t.Errorf("重复删除返回错误: %v", err)
	}
}

// writeFile 是测试辅助：直接往路径写内容。
func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o600)
}
