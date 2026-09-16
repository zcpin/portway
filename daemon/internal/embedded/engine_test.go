package embedded

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// envelope 是所有导出方法的统一响应形态：{"ok":bool,"data":...,"error":".."}。
type envelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

func parse(t *testing.T, raw string) envelope {
	t.Helper()
	var result envelope
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, raw)
	}
	if result.OK && result.Error != "" {
		t.Fatalf("成功响应不应带 error: %s", raw)
	}
	if !result.OK && result.Error == "" {
		t.Fatalf("失败响应必须带 error: %s", raw)
	}
	return result
}

func parseData[T any](t *testing.T, raw string) T {
	t.Helper()
	result := parse(t, raw)
	if !result.OK {
		t.Fatalf("期望成功，实际失败: %s", result.Error)
	}
	var value T
	if err := json.Unmarshal(result.Data, &value); err != nil {
		t.Fatalf("解析 data 失败: %v (%s)", err, raw)
	}
	return value
}

func expectFailure(t *testing.T, raw string, wantSubstring string) {
	t.Helper()
	result := parse(t, raw)
	if result.OK {
		t.Fatalf("期望失败，实际成功: %s", raw)
	}
	if wantSubstring != "" && !strings.Contains(result.Error, wantSubstring) {
		t.Fatalf("错误信息 %q 不含 %q", result.Error, wantSubstring)
	}
}

// newTestEngine 建一个不自动启动任何隧道的引擎，避免测试依赖网络。
func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	config := `log_level = "info"

[[tunnels]]
  name = "alpha"
  local_port = 47001
  remote_host = "127.0.0.1"
  remote_port = 3306
  ssh_host = "127.0.0.1:1"
  ssh_user = "nobody"
  key_file = "` + filepath.ToSlash(filepath.Join(dir, "id_alpha")) + `"
  auto_start = false
  reconnect_strategy = "fixed"
  reconnect_interval = "1s"
  max_reconnect_attempts = 1
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	engine, err := New(path)
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}
	t.Cleanup(engine.Stop)
	return engine, path
}

func TestEngineLifecycleAndSnapshot(t *testing.T) {
	engine, path := newTestEngine(t)

	if engine.ConfigPath() != path {
		t.Fatalf("配置路径不一致: %s != %s", engine.ConfigPath(), path)
	}
	if engine.Running() {
		t.Fatal("引擎在 Start 之前不应处于运行状态")
	}

	// 注册回调后应立即收到完整快照，客户端不必额外拉一次列表。
	var mu sync.Mutex
	var events []map[string]json.RawMessage
	engine.SetEventHandler(func(eventJSON string) {
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(eventJSON), &parsed); err != nil {
			t.Errorf("事件不是合法 JSON: %v (%s)", err, eventJSON)
			return
		}
		mu.Lock()
		events = append(events, parsed)
		mu.Unlock()
	})

	mu.Lock()
	total := len(events)
	mu.Unlock()
	if total == 0 {
		t.Fatal("注册回调后没有收到初始快照")
	}

	first := events[0]
	if string(first["type"]) != `"snapshot"` {
		t.Fatalf("首个事件应为 snapshot，实际为 %s", first["type"])
	}
	if _, ok := first["snapshot"]; !ok {
		t.Fatalf("snapshot 事件缺少 snapshot 字段: %v", first)
	}

	tunnels := parseData[[]map[string]any](t, engine.Tunnels())
	if len(tunnels) != 1 || tunnels[0]["name"] != "alpha" {
		t.Fatalf("隧道列表不符合预期: %v", tunnels)
	}

	if err := engine.Start(); err != nil {
		t.Fatalf("启动引擎失败: %v", err)
	}
	if !engine.Running() {
		t.Fatal("Start 之后应处于运行状态")
	}
	if again := engine.Start(); again != nil {
		t.Fatalf("重复 Start 应幂等，实际报错: %v", again)
	}

	engine.Stop()
	if engine.Running() {
		t.Fatal("Stop 之后不应处于运行状态")
	}
	engine.Stop() // 可重复调用

	info := parseData[map[string]any](t, engine.Info())
	if info["mode"] != "embedded" {
		t.Fatalf("Info 的 mode 应为 embedded，实际 %v", info["mode"])
	}
	// 停止事件推送后不应再收到事件。
	engine.SetEventHandler(nil)
	mu.Lock()
	before := len(events)
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	after := len(events)
	mu.Unlock()
	if after != before {
		t.Fatalf("注销回调后仍收到事件: %d -> %d", before, after)
	}
}

func TestTunnelCRUDAndStrictDecoding(t *testing.T) {
	engine, _ := newTestEngine(t)

	added := `{"name":"beta","local_port":47002,"remote_host":"127.0.0.1","remote_port":6379,` +
		`"ssh_host":"127.0.0.1:1","ssh_user":"nobody","key_file":"~/id_beta","auto_start":false}`
	parse(t, engine.AddTunnel(added))

	tunnels := parseData[[]map[string]any](t, engine.Tunnels())
	if len(tunnels) != 2 {
		t.Fatalf("新增后应有 2 条隧道，实际 %d", len(tunnels))
	}

	// 端口与远端是必填项，缺字段应被配置层拒绝。
	expectFailure(t, engine.AddTunnel(`{"name":"broken","local_port":47003}`), "remote_host")

	// 字段名写错必须报错，而不是被静默忽略（与 HTTP API 的 DisallowUnknownFields 行为一致）。
	expectFailure(t, engine.AddTunnel(
		`{"name":"typo","local_port":47004,"remote_host":"127.0.0.1","remote_port":1,"local_prot":1}`),
		"unknown field")

	parse(t, engine.UpdateTunnel("beta", `{"name":"beta","local_port":47005,`+
		`"remote_host":"127.0.0.1","remote_port":6379,"ssh_host":"127.0.0.1:1","ssh_user":"nobody",`+
		`"key_file":"~/id_beta","auto_start":false}`))

	tunnels = parseData[[]map[string]any](t, engine.Tunnels())
	doubts := 0
	for _, entry := range tunnels {
		if entry["name"] == "beta" {
			if entry["local_port"] != float64(47005) {
				t.Fatalf("更新未生效: %v", entry["local_port"])
			}
			doubts++
		}
	}
	if doubts != 1 {
		t.Fatalf("更新后 beta 应唯一存在，实际匹配 %d 次", doubts)
	}

	// 批量启停返回逐条结果，未知名称报错但不影响其他条目。
	batch := parseData[[]map[string]any](t, engine.BatchTunnels(`{"action":"stop","names":["beta","ghost"]}`))
	if len(batch) != 2 {
		t.Fatalf("批量结果应逐条返回，实际 %d 条", len(batch))
	}

	expectFailure(t, engine.StartTunnel("ghost"), "not found")
	parse(t, engine.DeleteTunnel("beta"))
	tunnels = parseData[[]map[string]any](t, engine.Tunnels())
	if len(tunnels) != 1 {
		t.Fatalf("删除后应剩 1 条隧道，实际 %d", len(tunnels))
	}
}

func TestKeyAndConfigSurface(t *testing.T) {
	engine, _ := newTestEngine(t)

	// 不存在的私钥路径不算失败，而是 exists=false，界面据此提示用户。
	info := parseData[map[string]any](t, engine.StatKey(filepath.Join(t.TempDir(), "missing")))
	if info["exists"] != false {
		t.Fatalf("缺失的私钥应报告 exists=false，实际 %v", info["exists"])
	}
	expectFailure(t, engine.StatKey("   "), "路径不能为空")

	// 口令为空或路径为空属于非法解锁请求。
	expectFailure(t, engine.UnlockKey(`{"path":"","passphrase":"x"}`), "invalid path")
	expectFailure(t, engine.LockKey(""), "path is required")

	keys := parseData[[]map[string]any](t, engine.Keys())
	if len(keys) != 1 {
		t.Fatalf("配置里引用了 1 个私钥，实际 %d", len(keys))
	}

	exported := parseData[map[string]any](t, engine.ExportConfig())
	if !strings.Contains(exported["content"].(string), "alpha") {
		t.Fatalf("导出的 TOML 不含隧道定义: %v", exported["content"])
	}

	// 全局配置往返：改日志级别与重连默认值。
	settings := parseData[map[string]any](t, engine.GlobalSettings())
	if settings["log_level"] == "" {
		t.Fatalf("全局配置缺少 log_level: %v", settings)
	}
	parse(t, engine.SetGlobalSettings(`{"log_level":"warn","reconnect_strategy":"fixed",`+
		`"reconnect_interval":"7s","max_reconnect_attempts":3}`))

	updated := parseData[map[string]any](t, engine.GlobalSettings())
	if updated["log_level"] != "warn" {
		t.Fatalf("全局日志级别未生效: %v", updated["log_level"])
	}
	if updated["reconnect_interval"] != "7s" {
		t.Fatalf("全局重连间隔未生效: %v", updated["reconnect_interval"])
	}

	expectFailure(t, engine.SetGlobalSettings(`{"log_level":"loud"}`), "log level")
	parse(t, engine.Reload())

	logs := parseData[[]map[string]any](t, engine.Logs())
	if len(logs) == 0 {
		t.Fatal("日志缓冲不应为空")
	}
	for _, entry := range logs {
		if entry["timestamp"] == nil || entry["level"] == nil || entry["message"] == nil {
			t.Fatalf("日志条目缺少必要字段: %v", entry)
		}
	}
}

func TestBackupListingAndRead(t *testing.T) {
	engine, _ := newTestEngine(t)

	backups := parseData[[]map[string]any](t, engine.ListBackups())
	if len(backups) != 0 {
		t.Fatalf("初始不应有备份，实际 %d 条", len(backups))
	}
	expectFailure(t, engine.ReadBackup("nope.toml"), "")
}
