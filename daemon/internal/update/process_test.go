package update

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/discovery"
)

// The test binary doubles as a small desktop process and HTTP daemon fixture. Every
// executable, discovery file, config, and PID record stays in temporary fixtures.
func TestMain(m *testing.M) {
	mode := os.Getenv("SSH_TUNNEL_UPDATER_FIXTURE")
	if mode == "" {
		os.Exit(m.Run())
	}
	if len(os.Args) > 2 && os.Args[1] == "update" {
		_ = os.WriteFile(filepath.Join(os.Getenv("SSH_TUNNEL_UPDATER_PID_DIR"), strconv.Itoa(os.Getpid())+".pid"), []byte("helper"), 0600)
		if err := Run(os.Args[2:], "v1.0.0", "owner/repo", os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	executable, _ := os.Executable()
	root := filepath.Dir(executable)
	if len(root) >= len("MacOS") && filepath.Base(root) == "MacOS" {
		root = filepath.Dir(filepath.Dir(root))
	}
	_, _, marker := layout(runtime.GOOS)
	var metadata manifest
	if err := readJSONFile(filepath.Join(root, marker), &metadata); err != nil {
		os.Exit(2)
	}
	if len(os.Args) == 2 && os.Args[1] == "-version" {
		fmt.Println(metadata.Version)
		return
	}
	pidDir := os.Getenv("SSH_TUNNEL_UPDATER_PID_DIR")
	if err := os.WriteFile(filepath.Join(pidDir, strconv.Itoa(os.Getpid())+".pid"), []byte(executable), 0600); err != nil {
		os.Exit(3)
	}
	if len(os.Args) > 1 && os.Args[1] == "-hide-console" {
		fs := flag.NewFlagSet("fixture daemon", flag.ExitOnError)
		_ = fs.Bool("hide-console", false, "")
		config := fs.String("config", "", "")
		addr := fs.String("addr", "127.0.0.1:0", "")
		_ = fs.Parse(os.Args[1:])
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			os.Exit(4)
		}
		serviceMode := false
		info := discovery.Info{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Token: "fixture-token", PID: os.Getpid(), Version: metadata.Version, ConfigPath: *config, ExecutablePath: executable, ServiceMode: &serviceMode}
		infoPath, err := discovery.Write(info, false)
		if err != nil {
			os.Exit(7)
		}
		defer discovery.Remove(infoPath, os.Getpid())
		unlock, err := LockRunningExecutable(executable)
		if err != nil {
			os.Exit(8)
		}
		defer unlock()
		srv := &http.Server{}
		srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Auth-Token") != info.Token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, "{}")
			if r.Method == http.MethodPost && r.URL.Path == "/api/update/shutdown" {
				go srv.Shutdown(context.Background())
			}
		})
		_ = srv.Serve(ln)
		return
	}
	if mode == "exit" || (mode == "fail-next" && metadata.Version == "v2.0.0") {
		os.Exit(5)
	}
	if address := os.Getenv("SSH_TUNNEL_UPDATE_ADDRESS"); address != "" {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			os.Exit(6)
		}
		_, _ = conn.Write([]byte(os.Getenv("SSH_TUNNEL_UPDATE_TOKEN")))
		_ = conn.Close()
	}
	for {
		time.Sleep(time.Minute)
	}
}

func fixtureProcesses(t *testing.T, mode string) (Plan, string) {
	t.Helper()
	p := fixturePlan(t)
	pidDir := filepath.Join(p.Directory, "pids")
	if err := os.Mkdir(pidDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_TUNNEL_UPDATER_FIXTURE", mode)
	t.Setenv("SSH_TUNNEL_UPDATER_PID_DIR", pidDir)
	t.Setenv("SSH_TUNNEL_UPDATE_ADDRESS", "")
	t.Setenv("SSH_TUNNEL_UPDATE_TOKEN", "")
	t.Cleanup(func() {
		files, _ := filepath.Glob(filepath.Join(pidDir, "*.pid"))
		for _, file := range files {
			pid, err := strconv.Atoi(filepath.Base(file)[:len(filepath.Base(file))-4])
			if err != nil {
				t.Error(err)
				continue
			}
			process, err := os.FindProcess(pid)
			if err == nil {
				_ = process.Kill()
				_ = process.Release()
			}
			deadline := time.Now().Add(3 * time.Second)
			for processAlive(pid) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
	})
	executable, _ := os.Executable()
	client, daemon, _ := layout(p.Current.OS)
	for _, root := range []string{p.Root, p.Stage} {
		for _, relative := range []string{client, daemon} {
			path := filepath.Join(root, relative)
			if err := os.Rename(path, path+".fixture"); err != nil {
				t.Fatal(err)
			}
			if err := copyExecutable(executable, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	if p.Current.OS == "windows" {
		p.Helper += ".exe"
	}
	if err := copyExecutable(executable, p.Helper); err != nil {
		t.Fatal(err)
	}
	var err error
	p.TreeSHA256, err = treeDigest(p.Stage)
	if err != nil {
		t.Fatal(err)
	}
	return p, pidDir
}

func TestRealStartupHandshakeAndFailedClientCleanup(t *testing.T) {
	for _, mode := range []string{"ready", "exit"} {
		t.Run(mode, func(t *testing.T) {
			p, pidDir := fixtureProcesses(t, mode)
			dataDir := filepath.Join(p.Directory, "daemon-data")
			if err := os.Mkdir(dataDir, 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dataDir, "config.toml")
			if err := os.WriteFile(config, []byte("log_level = 'error'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			p.Instances = []Instance{{PID: 1 << 29, ConfigPath: config, DataDir: dataDir, DiscoveryPath: filepath.Join(dataDir, "daemon.json")}}
			err := startInstalled(p, p.Stage, p.Next, true)
			if mode == "ready" && err != nil {
				t.Fatal(err)
			}
			if mode == "exit" {
				if err == nil {
					t.Fatal("failed client accepted as healthy")
				}
				files, _ := filepath.Glob(filepath.Join(pidDir, "*.pid"))
				for _, file := range files {
					name := filepath.Base(file)
					pid, _ := strconv.Atoi(name[:len(name)-4])
					if processAlive(pid) {
						t.Errorf("replacement process %d left alive after startup failure", pid)
					}
				}
			}
		})
	}
}

func TestCopiedHelperUpgradeAndStartupRollback(t *testing.T) {
	for _, mode := range []string{"ready", "fail-next"} {
		t.Run(mode, func(t *testing.T) { testCopiedHelper(t, mode) })
	}
}

func testCopiedHelper(t *testing.T, mode string) {
	p, _ := fixtureProcesses(t, mode)
	client, daemon, _ := layout(p.Current.OS)
	oldClient, err := startChild(exec.Command(filepath.Join(p.Root, client)))
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.stop()
	p.ClientPID = oldClient.cmd.Process.Pid
	dataDir := filepath.Join(p.Directory, "user-data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dataDir, "config.toml")
	if err := os.WriteFile(config, []byte("log_level = 'error'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	daemonCommand := exec.Command(filepath.Join(p.Root, daemon), "-hide-console", "-config", config, "-addr", "127.0.0.1:0")
	daemonCommand.Env = childEnvironment(map[string]string{discovery.EnvDataDir: dataDir})
	oldDaemon, err := startChild(daemonCommand)
	if err != nil {
		t.Fatal(err)
	}
	defer oldDaemon.stop()
	infoPath := filepath.Join(dataDir, discovery.FileName)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := waitDaemonReady(ctx, oldDaemon, infoPath, p.Current.Version); err != nil {
		t.Fatal(err)
	}
	p.Instances = []Instance{{PID: oldDaemon.cmd.Process.Pid, DiscoveryPath: infoPath, ConfigPath: config, DataDir: dataDir}}
	planPath := filepath.Join(p.Directory, "plan.json")
	if err := writeJSONFile(planPath, p); err != nil {
		t.Fatal(err)
	}
	if err := Launch(planPath); err != nil {
		t.Fatal(err)
	}
	if err := validateBundle(p.Root, p.Current); err != nil {
		t.Fatal("helper touched live installation:", err)
	}
	oldClient.stop()
	deadline := time.Now().Add(20 * time.Second)
	var result ApplyResult
	for {
		if readJSONFile(filepath.Join(p.Directory, "result.json"), &result) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not complete")
		}
		time.Sleep(50 * time.Millisecond)
	}
	expected := p.Next
	if mode == "ready" {
		if result.Status != "updated" {
			t.Fatalf("helper failed: %+v", result)
		}
		if err := validateBundle(p.Backup, p.Current); err != nil {
			t.Fatal("backup missing:", err)
		}
	} else {
		expected = p.Current
		if result.Status != "rolled_back" {
			t.Fatalf("helper did not roll back: %+v", result)
		}
		if err := validateBundle(p.Failed, p.Next); err != nil {
			t.Fatal("failed replacement missing:", err)
		}
	}
	if err := validateBundle(p.Root, expected); err != nil {
		t.Fatal(err)
	}
	var newInfo discovery.Info
	if readJSONFile(infoPath, &newInfo) != nil || newInfo.PID == oldDaemon.cmd.Process.Pid || newInfo.Version != expected.Version {
		t.Fatal("daemon was not restarted with the new version")
	}
	data, _ := os.ReadFile(config)
	if string(data) != "log_level = 'error'\n" {
		t.Fatal("configuration changed during upgrade")
	}
}
