package update

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/discovery"
)

func localRequest(ctx context.Context, info discovery.Info, method, endpoint string, body io.Reader) error {
	address := net.ParseIP(info.Host)
	if (address == nil || !address.IsLoopback()) && info.Host != "localhost" {
		return errors.New("daemon address must be loopback")
	}
	if info.Port <= 0 || info.Port > 65535 || info.Token == "" {
		return errors.New("upgrades require an authenticated user daemon")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+net.JoinHostPort(info.Host, strconv.Itoa(info.Port))+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Token", info.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("daemon redirect refused") }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("daemon is unavailable; retry after it has stopped or reconnected")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon rejected update coordination (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func collectInstances(ctx context.Context, root string, extraPaths []string) ([]Instance, error) {
	if len(extraPaths) > 200 {
		return nil, errors.New("too many daemon discovery paths")
	}
	paths := append(discovery.Candidates(), extraPaths...)
	seen := map[int]bool{}
	instances := []Instance{}
	for _, path := range paths {
		var info discovery.Info
		if err := readJSONFile(path, &info); err != nil {
			if os.IsNotExist(err) || os.IsPermission(err) {
				continue
			}
			return nil, fmt.Errorf("cannot read discovery metadata: %w", err)
		}
		executable, err := filepath.EvalSymlinks(info.ExecutablePath)
		if err != nil || !within(root, executable) || info.PID <= 0 || !processAlive(info.PID) || seen[info.PID] {
			continue
		}
		if info.ServiceMode == nil || *info.ServiceMode || info.Token == "" {
			return nil, errors.New("同一安装正在用于系统服务或未鉴权实例，请停止服务并手动更新。")
		}
		config, err := filepath.EvalSymlinks(info.ConfigPath)
		if err != nil || !filepath.IsAbs(config) || within(root, config) {
			return nil, errors.New("请先将 daemon 配置移至安装目录外，再进行便携升级。")
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(resolvedPath) || within(root, resolvedPath) {
			return nil, errors.New("daemon discovery data must be outside the installation")
		}
		if err := localRequest(ctx, info, http.MethodGet, "/api/status", nil); err != nil {
			return nil, err
		}
		seen[info.PID] = true
		instances = append(instances, Instance{info.PID, resolvedPath, config, filepath.Dir(resolvedPath)})
	}
	return instances, nil
}

func shutdownInstances(ctx context.Context, p Plan) error {
	for _, instance := range p.Instances {
		if !processAlive(instance.PID) {
			continue
		}
		var info discovery.Info
		if err := readJSONFile(instance.DiscoveryPath, &info); err != nil {
			return err
		}
		executable, err := filepath.EvalSymlinks(info.ExecutablePath)
		if err != nil || !within(p.Root, executable) || info.PID != instance.PID || info.ServiceMode == nil || *info.ServiceMode {
			return errors.New("daemon changed since upgrade confirmation")
		}
		if err := localRequest(ctx, info, http.MethodPost, "/api/update/shutdown", strings.NewReader(fmt.Sprintf(`{"pid":%d}`, instance.PID))); err != nil {
			return err
		}
	}
	return nil
}

func waitForExit(ctx context.Context, pids []int, cancelled func() bool, alive func(int) bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cancelled() {
			return errors.New("upgrade cancelled")
		}
		allExited := true
		for _, pid := range pids {
			if alive(pid) {
				allExited = false
				break
			}
		}
		if allExited {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for application exit: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func probeBundle(ctx context.Context, root string, expected manifest) error {
	_, daemon, _ := layout(expected.OS)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, daemon), "-version")
	hideChild(cmd)
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != expected.Version {
		return errors.New("new daemon failed the executable version check")
	}
	if expected.OS == "darwin" {
		if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", root).Run(); err != nil {
			return fmt.Errorf("app signature verification failed: %w", err)
		}
	}
	return nil
}

type childProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func startChild(cmd *exec.Cmd) (*childProcess, error) {
	hideChild(cmd)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &childProcess{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(child.done) }()
	return child, nil
}

func (c *childProcess) stop() {
	_ = c.cmd.Process.Kill()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
	}
}

func childEnvironment(values map[string]string) []string {
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, replace := values[key]; !replace {
			env = append(env, value)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func startInstalled(p Plan, root string, expected manifest, startClient bool) (err error) {
	children := []*childProcess{}
	defer func() {
		if err != nil {
			for _, child := range children {
				child.stop()
			}
		}
	}()
	clientName, daemonName, _ := layout(expected.OS)
	for _, instance := range p.Instances {
		if processAlive(instance.PID) {
			continue // Recovery after a timeout must not duplicate a running daemon.
		}
		cmd := exec.Command(filepath.Join(root, daemonName), "-hide-console", "-config", instance.ConfigPath, "-addr", "127.0.0.1:0")
		cmd.Dir = root
		cmd.Env = childEnvironment(map[string]string{discovery.EnvDataDir: instance.DataDir})
		child, startErr := startChild(cmd)
		if startErr != nil {
			return startErr
		}
		children = append(children, child)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = waitDaemonReady(ctx, child, instance.DiscoveryPath, expected.Version)
		cancel()
		if err != nil {
			return err
		}
	}
	if !startClient {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(secret[:])
	cmd := exec.Command(filepath.Join(root, clientName))
	cmd.Dir = root
	cmd.Env = childEnvironment(map[string]string{
		"SSH_TUNNEL_UPDATE_ADDRESS": listener.Addr().String(),
		"SSH_TUNNEL_UPDATE_TOKEN":   token,
	})
	child, err := startChild(cmd)
	if err != nil {
		return err
	}
	children = append(children, child)
	ready := make(chan error, 1)
	go func() {
		_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(25 * time.Second))
		for {
			conn, err := listener.Accept()
			if err != nil {
				ready <- errors.New("client did not report successful startup")
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			data, err := io.ReadAll(io.LimitReader(conn, 65))
			conn.Close()
			if err == nil && subtle.ConstantTimeCompare(data, []byte(token)) == 1 {
				ready <- nil
				return
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			return err
		}
	case <-child.done:
		return errors.New("client exited during startup")
	}
	select {
	case <-child.done:
		return errors.New("client exited immediately after startup")
	case <-time.After(time.Second):
		return nil
	}
}

func waitDaemonReady(ctx context.Context, child *childProcess, path, version string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var info discovery.Info
		if readJSONFile(path, &info) == nil && info.PID == child.cmd.Process.Pid && info.Version == version {
			if err := localRequest(ctx, info, http.MethodGet, "/api/status", nil); err == nil {
				return nil
			}
		}
		select {
		case <-child.done:
			return errors.New("daemon exited during startup")
		case <-ctx.Done():
			return errors.New("daemon did not become ready")
		case <-ticker.C:
		}
	}
}

func ApplyFile(planPath string) error {
	var p Plan
	if err := readJSONFile(planPath, &p); err != nil {
		return err
	}
	if !samePath(filepath.Dir(planPath), p.Directory) {
		return errors.New("plan must reside in its helper directory")
	}
	executable, err := os.Executable()
	if err != nil || !samePath(executable, p.Helper) {
		return errors.New("apply must run from the copied helper outside the installation")
	}
	if err := p.validate(); err != nil {
		return err
	}
	unlock, err := lockInstall(p.Root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.WriteFile(filepath.Join(p.Directory, "ready"), []byte("ready"), 0600); err != nil {
		return err
	}
	cancelled := func() bool {
		_, err := os.Stat(filepath.Join(p.Directory, "cancel"))
		return !os.IsNotExist(err)
	}
	var usageUnlock func()
	releaseUsage := func() {
		if usageUnlock != nil {
			usageUnlock()
			usageUnlock = nil
		}
	}
	defer releaseUsage()
	ops := applyOperations{
		rename: func(from, to string) error {
			// Startup releases the usage lock so new daemons can run. Reacquire
			// it before rollback in case another process has since used the bundle.
			if from == p.Root && to == p.Failed && usageUnlock == nil {
				var err error
				usageUnlock, err = lockDirectory(p.Root, "usage", false)
				if err != nil {
					return err
				}
			}
			return os.Rename(from, to)
		},
		start: func(root string, expected manifest) error {
			releaseUsage()
			return startInstalled(p, root, expected, true)
		},
		wait: func(ctx context.Context) error {
			waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			if err := waitForExit(waitCtx, []int{p.ClientPID}, cancelled, processAlive); err != nil {
				return err
			}
			if err := shutdownInstances(ctx, p); err != nil {
				return err
			}
			pids := []int{}
			for _, instance := range p.Instances {
				pids = append(pids, instance.PID)
			}
			daemonCtx, cancelDaemons := context.WithTimeout(ctx, 60*time.Second)
			defer cancelDaemons()
			if err := waitForExit(daemonCtx, pids, cancelled, processAlive); err != nil {
				return err
			}
			var err error
			usageUnlock, err = lockDirectory(p.Root, "usage", false)
			if err != nil {
				return fmt.Errorf("还有此安装的 daemon 或系统服务正在运行，请关闭后重试: %w", err)
			}
			return nil
		},
	}
	result, err := apply(context.Background(), p, ops)
	if err != nil && result.Status == "unchanged" {
		releaseUsage()
		if validateBundle(p.Root, p.Current) == nil && safeInstallRoot(p.Root) == nil {
			err = errors.Join(err, startInstalled(p, p.Root, p.Current, !processAlive(p.ClientPID)))
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	writeErr := writeJSONFile(filepath.Join(p.Directory, "result.json"), result)
	latestErr := writeJSONFile(filepath.Join(filepath.Dir(p.Directory), "last-result.json"), result)
	return errors.Join(err, writeErr, latestErr)
}

func Launch(planPath string) error {
	var p Plan
	if err := readJSONFile(planPath, &p); err != nil {
		return err
	}
	if !samePath(filepath.Dir(planPath), p.Directory) {
		return errors.New("invalid plan location")
	}
	if err := p.validate(); err != nil {
		return err
	}
	readyPath := filepath.Join(p.Directory, "ready")
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		return errors.New("this update plan has already been launched")
	}
	cmd := exec.Command(p.Helper, "update", "apply", "-plan", planPath)
	cmd.Dir = p.Directory
	hideChild(cmd)
	log, err := os.OpenFile(filepath.Join(p.Directory, "helper.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return nil
		}
		select {
		case <-done:
			return errors.New("upgrade helper failed to start; see helper.log")
		case <-deadline.C:
			_ = cmd.Process.Kill()
			return errors.New("upgrade helper did not become ready")
		case <-tick.C:
		}
	}
}
