package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func configIOFixture(t *testing.T) (*ConfigIO, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	data := `log_level = "info"
reconnect_strategy = "fixed"
reconnect_interval = "5s"
max_reconnect_attempts = 3

[[ssh_connections]]
name = "used"
host = "example.com:22"
user = "tester"
key_file = "used.key"

[[ssh_connections]]
name = "spare"
host = "spare.example.com:22"
user = "tester"
key_file = "spare.key"

[[tunnels]]
name = "first"
local_port = 15001
remote_host = "127.0.0.1"
remote_port = 3306
ssh_connection = "used"

[[tunnels]]
name = "second"
local_port = 15002
remote_host = "127.0.0.1"
remote_port = 5432
ssh_connection = "used"
reconnect_strategy = "exponential"
reconnect_interval = "17s"
max_reconnect_attempts = 2
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cio, err := NewConfigIO(path)
	if err != nil {
		t.Fatal(err)
	}
	return cio, path
}

func assertConfigUnchanged(t *testing.T, cio *ConfigIO, path string, before *Config, diskBefore []byte) {
	t.Helper()
	if after := cio.GetConfig(); !reflect.DeepEqual(before, after) {
		t.Errorf("失败操作改变了内存配置: before=%+v after=%+v", before, after)
	}
	diskAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskBefore, diskAfter) {
		t.Error("失败操作改变了磁盘配置")
	}
}

func TestConfigMutationsRollBackWhenSaveFails(t *testing.T) {
	cases := map[string]func(*ConfigIO) error{
		"settings": func(cio *ConfigIO) error {
			s := cio.GlobalSettings()
			s.LogLevel = "debug"
			return cio.SetGlobalSettings(s)
		},
		"add tunnel": func(cio *ConfigIO) error {
			tun := cio.GetConfig().Tunnels[0]
			tun.Name, tun.LocalPort = "third", 15003
			return cio.AddTunnel(tun)
		},
		"update tunnel": func(cio *ConfigIO) error {
			tun := cio.GetConfig().Tunnels[0]
			tun.RemotePort = 3307
			return cio.UpdateTunnel("first", tun)
		},
		"delete tunnel": func(cio *ConfigIO) error { return cio.DeleteTunnel("first") },
		"add connection": func(cio *ConfigIO) error {
			conn := cio.GetConfig().SSHConnections[0]
			conn.Name = "third"
			return cio.AddSSHConnection(conn)
		},
		"update connection": func(cio *ConfigIO) error {
			conn := cio.GetConfig().SSHConnections[0]
			conn.Host = "new.example.com:22"
			return cio.UpdateSSHConnection("used", conn)
		},
		"delete connection": func(cio *ConfigIO) error { return cio.DeleteSSHConnection("spare") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cio, path := configIOFixture(t)
			before := cio.GetConfig()
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// 用目录占住临时文件位置，在 Windows/Unix 上都能稳定模拟写盘失败。
			if err := os.Mkdir(path+".tmp", 0700); err != nil {
				t.Fatal(err)
			}
			if err := mutate(cio); err == nil {
				t.Fatal("写盘失败应返回错误")
			}
			assertConfigUnchanged(t, cio, path, before, diskBefore)
		})
	}
}

func TestConfigMutationsValidateBeforeSaving(t *testing.T) {
	cases := map[string]func(*ConfigIO) error{
		"missing SSH reference": func(cio *ConfigIO) error {
			tun := cio.GetConfig().Tunnels[0]
			tun.Name, tun.LocalPort, tun.SSHConnection = "third", 15003, "missing"
			return cio.AddTunnel(tun)
		},
		"duplicate tunnel name": func(cio *ConfigIO) error {
			tun := cio.GetConfig().Tunnels[0]
			tun.Name = "second"
			return cio.UpdateTunnel("first", tun)
		},
		"invalid port": func(cio *ConfigIO) error {
			tun := cio.GetConfig().Tunnels[0]
			tun.LocalPort = -1
			return cio.UpdateTunnel("first", tun)
		},
		"invalid reconnect interval": func(cio *ConfigIO) error {
			tun := cio.GetConfig().Tunnels[0]
			tun.ReconnectInterval = "not-a-duration"
			return cio.UpdateTunnel("first", tun)
		},
		"duplicate connection name": func(cio *ConfigIO) error {
			conn := cio.GetConfig().SSHConnections[0]
			conn.Name = "spare"
			return cio.UpdateSSHConnection("used", conn)
		},
		"rename referenced connection": func(cio *ConfigIO) error {
			conn := cio.GetConfig().SSHConnections[0]
			conn.Name = "renamed"
			return cio.UpdateSSHConnection("used", conn)
		},
		"invalid SSH host": func(cio *ConfigIO) error {
			conn := cio.GetConfig().SSHConnections[1]
			conn.Host = "example.com:not-a-port"
			return cio.UpdateSSHConnection("spare", conn)
		},
		"invalid log level": func(cio *ConfigIO) error {
			s := cio.GlobalSettings()
			s.LogLevel = "loud"
			return cio.SetGlobalSettings(s)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cio, path := configIOFixture(t)
			before := cio.GetConfig()
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := mutate(cio); err == nil {
				t.Fatal("无效配置应在保存前被拒绝")
			}
			assertConfigUnchanged(t, cio, path, before, diskBefore)
		})
	}
}

func TestConfigCommitPreservesInheritedDefaults(t *testing.T) {
	cio, path := configIOFixture(t)
	s := cio.GlobalSettings()
	s.ReconnectInterval, s.MaxReconnectAttempts = "9s", 7
	if err := cio.SetGlobalSettings(s); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Tunnels[0].ReconnectInterval != "" || saved.Tunnels[0].MaxReconnectAttempts != 0 {
		t.Fatal("校验过程把继承的默认值写成了显式覆盖")
	}
	if err := cio.Replace(saved); err != nil {
		t.Fatal(err)
	}
	if cio.GetConfig().Tunnels[0].ReconnectInterval != "" {
		t.Fatal("Reload 丢失了继承关系")
	}
	if err := saved.Validate(); err != nil {
		t.Fatal(err)
	}
	parsed, err := saved.ParseTunnels()
	if err != nil {
		t.Fatal(err)
	}
	if parsed[0].ReconnectInterval != 9*time.Second || parsed[0].MaxReconnectAttempts != 7 {
		t.Fatalf("未使用新的全局默认值: %+v", parsed[0])
	}
	if parsed[1].ReconnectInterval != 17*time.Second || parsed[1].MaxReconnectAttempts != 2 {
		t.Fatalf("显式配置被全局默认值覆盖: %+v", parsed[1])
	}
}

func TestDeleteMissingResourceFromEmptyConfig(t *testing.T) {
	for _, resource := range []string{"tunnel", "connection"} {
		t.Run(resource, func(t *testing.T) {
			cio, err := NewConfigIO("")
			if err != nil {
				t.Fatal(err)
			}
			if resource == "tunnel" {
				err = cio.DeleteTunnel("missing")
			} else {
				err = cio.DeleteSSHConnection("missing")
			}
			if err == nil {
				t.Fatal("空列表删除应返回未找到错误")
			}
		})
	}
}
