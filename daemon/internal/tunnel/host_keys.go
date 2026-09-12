package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type HostKeyInfo struct {
	Host        string `json:"host"`
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	File        string `json:"file"`
	Known       bool   `json:"known"`
	Changed     bool   `json:"changed"`
}

var hostTrustMu sync.Mutex
var errHostKeyCaptured = errors.New("host key captured")

// Scan only reaches host verification and never sends user credentials.
func scanHostKey(ctx context.Context, cfg config.ParsedTunnel) (ssh.PublicKey, net.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", cfg.SSHHost)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()
	var key ssh.PublicKey
	sshConfig := &ssh.ClientConfig{User: cfg.SSHUser, HostKeyCallback: func(_ string, _ net.Addr, offered ssh.PublicKey) error {
		key = offered
		return errHostKeyCaptured
	}}
	_, _, _, err = ssh.NewClientConn(conn, cfg.SSHHost, sshConfig)
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if key == nil || !errors.Is(err, errHostKeyCaptured) {
		return nil, nil, fmt.Errorf("unable to read host key: %w", err)
	}
	return key, conn.RemoteAddr(), nil
}

func readKnownHosts(path string) ([]byte, error) {
	before, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > 1<<20 {
		return nil, errors.New("known_hosts must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("known_hosts must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if len(data) > 1<<20 {
		return nil, errors.New("known_hosts exceeds 1 MiB")
	}
	return data, err
}

func hostKeyStatus(cfg config.ParsedTunnel, key ssh.PublicKey, remote net.Addr) (HostKeyInfo, []byte, []knownhosts.KnownKey, error) {
	info := HostKeyInfo{Host: cfg.SSHHost, Fingerprint: ssh.FingerprintSHA256(key), Algorithm: key.Type(), File: cfg.KnownHostsFile}
	data, err := readKnownHosts(cfg.KnownHostsFile)
	if err != nil {
		return info, nil, nil, err
	}
	if data == nil {
		return info, nil, nil, nil
	}
	callback, err := knownhosts.New(cfg.KnownHostsFile)
	if os.IsNotExist(err) {
		return info, data, nil, nil
	}
	if err != nil {
		return info, nil, nil, err
	}
	err = callback(cfg.SSHHost, remote, key)
	if err == nil {
		info.Known = true
		return info, data, nil, nil
	}
	var mismatch *knownhosts.KeyError
	if !errors.As(err, &mismatch) {
		return info, nil, nil, err
	}
	info.Changed = len(mismatch.Want) != 0
	return info, data, mismatch.Want, nil
}

func InspectHostKey(ctx context.Context, cfg config.ParsedTunnel) (HostKeyInfo, error) {
	key, remote, err := scanHostKey(ctx, cfg)
	if err != nil {
		return HostKeyInfo{}, err
	}
	info, _, _, err := hostKeyStatus(cfg, key, remote)
	return info, err
}

func TrustHostKey(ctx context.Context, cfg config.ParsedTunnel, fingerprint string, replace bool) (HostKeyInfo, error) {
	if fingerprint == "" {
		return HostKeyInfo{}, errors.New("confirmed fingerprint is required")
	}
	key, remote, err := scanHostKey(ctx, cfg)
	if err != nil {
		return HostKeyInfo{}, err
	}
	if ssh.FingerprintSHA256(key) != fingerprint {
		return HostKeyInfo{}, errors.New("host fingerprint changed since inspection; inspect again")
	}
	hostTrustMu.Lock()
	defer hostTrustMu.Unlock()
	info, original, oldKeys, err := hostKeyStatus(cfg, key, remote)
	if err != nil {
		return HostKeyInfo{}, err
	}
	if info.Known {
		return info, nil
	}
	if info.Changed && !replace {
		return HostKeyInfo{}, errors.New("host key changed; explicit replacement confirmation is required")
	}
	lines := strings.Split(string(original), "\n")
	host := knownhosts.Normalize(cfg.SSHHost)
	seen := make(map[int]bool)
	for _, old := range oldKeys {
		if seen[old.Line] {
			continue
		}
		seen[old.Line] = true
		if old.Line < 1 || old.Line > len(lines) {
			return HostKeyInfo{}, errors.New("known_hosts changed; inspect again")
		}
		fields := strings.Fields(lines[old.Line-1])
		if len(fields) < 3 {
			return HostKeyInfo{}, errors.New("invalid known_hosts entry")
		}
		hostIndex := 0
		if strings.HasPrefix(fields[0], "@") {
			hostIndex = 1
		}
		if strings.HasPrefix(fields[hostIndex], "|") {
			// A hashed entry represents one host. Preserve it as a comment.
			lines[old.Line-1] = "# replaced: " + lines[old.Line-1]
		} else {
			// Exclude this host while retaining any aliases/wildcards on the line.
			fields[hostIndex] = "!" + host + "," + fields[hostIndex]
			lines[old.Line-1] = strings.Join(fields, " ")
		}
	}
	updated := strings.TrimRight(strings.Join(lines, "\n"), "\r\n") + "\n" + knownhosts.Line([]string{host}, key) + "\n"
	latest, err := readKnownHosts(cfg.KnownHostsFile)
	if err != nil {
		return HostKeyInfo{}, err
	}
	if !bytes.Equal(latest, original) {
		return HostKeyInfo{}, errors.New("known_hosts changed; inspect again")
	}
	if err := writeKnownHosts(cfg.KnownHostsFile, []byte(updated)); err != nil {
		return HostKeyInfo{}, err
	}
	info.Known, info.Changed = true, false
	return info, nil
}

func writeKnownHosts(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".known-hosts-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
