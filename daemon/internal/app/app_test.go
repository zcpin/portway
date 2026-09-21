package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/byteporter/portway/internal/logger"
)

func settingsApp(t *testing.T) (*App, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("log_level = \"info\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.GetGlobalLogger().SetLevel(logger.INFO)
	t.Cleanup(func() {
		logger.SetGlobalLogHook(nil)
		a.Stop()
		logger.GetGlobalLogger().SetLevel(logger.INFO)
	})
	return a, path
}

func hasLog(a *App, message string) bool {
	for _, entry := range a.GetLogs() {
		if entry.Message == message {
			return true
		}
	}
	return false
}

func TestLogLevelChangesTakeEffectImmediately(t *testing.T) {
	a, _ := settingsApp(t)
	s := a.GetGlobalSettings()
	s.LogLevel = "debug"
	if err := a.SetGlobalSettings(s); err != nil {
		t.Fatal(err)
	}
	logger.Debug("debug enabled")
	if !hasLog(a, "debug enabled") {
		t.Fatal("保存 debug 后日志仍被旧级别过滤")
	}
	s.LogLevel = "error"
	if err := a.SetGlobalSettings(s); err != nil {
		t.Fatal(err)
	}
	logger.Info("info suppressed")
	logger.Error("error visible")
	if hasLog(a, "info suppressed") || !hasLog(a, "error visible") {
		t.Fatal("保存 error 后未正确过滤日志")
	}
}

func TestFailedSettingsSaveKeepsLogLevel(t *testing.T) {
	a, path := settingsApp(t)
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	s := a.GetGlobalSettings()
	s.LogLevel = "debug"
	if err := a.SetGlobalSettings(s); err == nil {
		t.Fatal("写盘应失败")
	}
	logger.Debug("still filtered")
	logger.Info("still visible")
	if a.GetGlobalSettings().LogLevel != "info" || hasLog(a, "still filtered") || !hasLog(a, "still visible") {
		t.Fatal("保存失败改变了配置或正在使用的日志级别")
	}
}

func TestReloadAppliesLogLevel(t *testing.T) {
	a, path := settingsApp(t)
	if err := os.WriteFile(path, []byte("log_level = \"debug\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	logger.Debug("debug after reload")
	if !hasLog(a, "debug after reload") {
		t.Fatal("Reload 未应用文件中的日志级别")
	}
}

func TestConcurrentSettingsKeepLogLevelInSync(t *testing.T) {
	a, _ := settingsApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, level := range []string{"debug", "error"} {
			wg.Add(1)
			go func(level string) {
				defer wg.Done()
				s := a.GetGlobalSettings()
				s.LogLevel = level
				if err := a.SetGlobalSettings(s); err != nil {
					t.Error(err)
				}
			}(level)
		}
	}
	wg.Wait()
	logger.Debug("final level probe")
	wantDebug := a.GetGlobalSettings().LogLevel == "debug"
	if hasLog(a, "final level probe") != wantDebug {
		t.Fatal("并发保存后内存配置与实际日志级别不一致")
	}
}
