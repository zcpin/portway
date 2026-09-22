package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateHome 把主目录指到临时目录，避免测试覆盖真实的发现文件。
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows 的 os.UserHomeDir 读它
	t.Setenv("HOME", home)        // Unix 读它
	t.Setenv(EnvDataDir, "")
	return home
}

func sampleInfo() Info {
	return Info{Host: "127.0.0.1", Port: 54321, Token: "token", PID: 42, Version: "test"}
}

func TestWriteUserPath(t *testing.T) {
	home := isolateHome(t)

	path, err := Write(sampleInfo(), false)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := filepath.Join(home, ".portway", FileName)
	if path != want {
		t.Fatalf("写入路径 = %q，期望 %q", path, want)
	}
	assertReadable(t, path)
}

func TestWriteSharedPathUsesDataDirOverride(t *testing.T) {
	isolateHome(t)
	dataDir := t.TempDir()
	t.Setenv(EnvDataDir, dataDir)

	path, err := Write(sampleInfo(), true)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := filepath.Join(dataDir, FileName)
	if path != want {
		t.Fatalf("写入路径 = %q，期望 %q", path, want)
	}
	assertReadable(t, path)

	// 公共目录下的文件要能被其他用户身份的客户端读到
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o644 {
			t.Errorf("文件权限 = %o，期望 644", perm)
		}
		di, err := os.Stat(dataDir)
		if err != nil {
			t.Fatalf("Stat dir: %v", err)
		}
		if perm := di.Mode().Perm(); perm != 0o755 {
			t.Errorf("目录权限 = %o，期望 755", perm)
		}
	}
}

// PORTWAY_DATA_DIR 属于显式指令，前台运行也应遵守。
func TestDataDirOverrideWinsOverForeground(t *testing.T) {
	home := isolateHome(t)
	dataDir := t.TempDir()
	t.Setenv(EnvDataDir, dataDir)

	path, err := Write(sampleInfo(), false)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != filepath.Join(dataDir, FileName) {
		t.Fatalf("写入路径 = %q，期望落在覆盖目录", path)
	}

	userPath := filepath.Join(home, ".portway", FileName)
	if _, err := os.Stat(userPath); !os.IsNotExist(err) {
		t.Errorf("覆盖生效时不应再写用户目录: %v", err)
	}
}

func TestCandidatesOrderAndDedup(t *testing.T) {
	home := isolateHome(t)
	userPath := filepath.Join(home, ".portway", FileName)

	candidates := Candidates()
	if len(candidates) != 2 {
		t.Fatalf("候选数量 = %d，期望 2: %v", len(candidates), candidates)
	}
	if candidates[0] != userPath {
		t.Errorf("首位候选应为用户目录，实际 %q", candidates[0])
	}
	if !strings.Contains(candidates[1], "portway") {
		t.Errorf("次位候选应为系统目录，实际 %q", candidates[1])
	}

	// PORTWAY_DATA_DIR 优先，且与系统级候选去重
	dataDir := t.TempDir()
	t.Setenv(EnvDataDir, dataDir)
	candidates = Candidates()
	if len(candidates) != 2 {
		t.Fatalf("设置覆盖目录后候选数量 = %d，期望 2（去重）: %v", len(candidates), candidates)
	}
	if candidates[0] != filepath.Join(dataDir, FileName) {
		t.Errorf("覆盖目录应排首位，实际 %q", candidates[0])
	}
	if candidates[1] != userPath {
		t.Errorf("次位应为用户目录，实际 %q", candidates[1])
	}
}

func TestRemoveOnlyDeletesOwnFile(t *testing.T) {
	isolateHome(t)

	path, err := Write(sampleInfo(), false)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// 其他 PID 的清理不应删除文件
	Remove(path, sampleInfo().PID+1)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("其他 PID 的 Remove 不应删除文件: %v", err)
	}

	// 自己的 PID 可以删除
	Remove(path, sampleInfo().PID)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("同 PID 的 Remove 应删除文件, err = %v", err)
	}
}

func TestSharedPathFallsBackToPlatformDir(t *testing.T) {
	isolateHome(t)

	path, err := SharedPath()
	if err != nil {
		t.Fatalf("SharedPath: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("portway", FileName)) {
		t.Errorf("系统级路径异常: %q", path)
	}
	if runtime.GOOS == "windows" && !strings.Contains(strings.ToLower(path), "programdata") {
		t.Errorf("Windows 系统级路径应位于 ProgramData 下: %q", path)
	}
}

func assertReadable(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	var got Info
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	if got.Port != 54321 || got.Token != "token" {
		t.Errorf("内容不符: %+v", got)
	}
}
