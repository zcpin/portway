package config

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const MaxImportBytes = 256 * 1024

var ErrRevisionChanged = errors.New("configuration changed; preview again before importing")
var backupNamePattern = regexp.MustCompile(`^\d{8}T\d{6}\.\d{9}-[0-9a-f]{12}\.toml$`)

type ConfigExport struct {
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type ImportRequest struct {
	Content  string `json:"content"`
	Mode     string `json:"mode"`
	Revision string `json:"revision"`
}

type ConfigChange struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

type ImportPreview struct {
	Revision string         `json:"revision"`
	Changes  []ConfigChange `json:"changes"`
}

type ConfigBackup struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Size      int64  `json:"size"`
}

func encodeConfig(cfg *Config) (string, error) {
	var content bytes.Buffer
	if err := toml.NewEncoder(&content).Encode(cfg); err != nil {
		return "", err
	}
	return content.String(), nil
}

func (cio *ConfigIO) exportLocked() (ConfigExport, []byte, error) {
	content, err := encodeConfig(cio.config)
	if err != nil {
		return ConfigExport{}, nil, err
	}
	var disk []byte
	if cio.path != "" {
		disk, err = os.ReadFile(cio.path)
		if err != nil {
			return ConfigExport{}, nil, err
		}
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(content))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(disk)
	return ConfigExport{Content: content, Revision: hex.EncodeToString(hash.Sum(nil))}, disk, nil
}

func (cio *ConfigIO) Export() (ConfigExport, error) {
	cio.mu.RLock()
	defer cio.mu.RUnlock()
	result, _, err := cio.exportLocked()
	return result, err
}

func mergeNamed[T any](base, incoming []T, name func(T) string) ([]T, error) {
	result := append([]T(nil), base...)
	indices := make(map[string]int)
	for i, entry := range result {
		indices[name(entry)] = i
	}
	seen := make(map[string]bool)
	for _, entry := range incoming {
		key := name(entry)
		if seen[key] {
			return nil, fmt.Errorf("duplicate imported name %q", key)
		}
		seen[key] = true
		if i, found := indices[key]; found {
			result[i] = entry
		} else {
			indices[key] = len(result)
			result = append(result, entry)
		}
	}
	return result, nil
}

func (cio *ConfigIO) importCandidate(input ImportRequest) (*Config, error) {
	if len(input.Content) > MaxImportBytes {
		return nil, fmt.Errorf("configuration exceeds %d bytes", MaxImportBytes)
	}
	var incoming Config
	metadata, err := toml.Decode(input.Content, &incoming)
	if err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if unknown := metadata.Undecoded(); len(unknown) != 0 {
		return nil, fmt.Errorf("unknown configuration field: %s", unknown[0])
	}
	incoming.configDir = cio.config.configDir
	if input.Mode == "replace" {
		if err := validateConfig(&incoming); err != nil {
			return nil, err
		}
		return &incoming, nil
	}
	if input.Mode != "merge" {
		return nil, fmt.Errorf("mode must be merge or replace")
	}
	candidate := cloneConfig(cio.config)
	if metadata.IsDefined("log_level") {
		candidate.LogLevel = incoming.LogLevel
	}
	if metadata.IsDefined("reconnect_strategy") {
		candidate.ReconnectStrategy = incoming.ReconnectStrategy
	}
	if metadata.IsDefined("reconnect_interval") {
		candidate.ReconnectInterval = incoming.ReconnectInterval
	}
	if metadata.IsDefined("max_reconnect_attempts") {
		candidate.MaxReconnectAttempts = incoming.MaxReconnectAttempts
	}
	candidate.SSHConnections, err = mergeNamed(candidate.SSHConnections, incoming.SSHConnections, func(c SSHConnection) string { return c.Name })
	if err != nil {
		return nil, err
	}
	candidate.Tunnels, err = mergeNamed(candidate.Tunnels, incoming.Tunnels, func(t Tunnel) string { return t.Name })
	if err != nil {
		return nil, err
	}
	if err := validateConfig(candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

type configEntry struct {
	kind, name string
	value      any
}

func configEntries(cfg *Config) map[string]configEntry {
	entries := map[string]configEntry{"global": {"global", "defaults", GlobalSettings{
		LogLevel: cfg.LogLevel, ReconnectStrategy: cfg.ReconnectStrategy,
		ReconnectInterval: cfg.ReconnectInterval, MaxReconnectAttempts: cfg.MaxReconnectAttempts}}}
	for _, entry := range cfg.Tunnels {
		entries["tunnel/"+entry.Name] = configEntry{"tunnel", entry.Name, entry}
	}
	for _, entry := range cfg.SSHConnections {
		entries["ssh/"+entry.Name] = configEntry{"ssh", entry.Name, entry}
	}
	return entries
}

func changesBetween(before, after *Config) []ConfigChange {
	old, next := configEntries(before), configEntries(after)
	changes := make([]ConfigChange, 0)
	for key, entry := range next {
		prior, found := old[key]
		if found && reflect.DeepEqual(prior.value, entry.value) {
			continue
		}
		action := "add"
		if found {
			action = "replace"
		}
		changes = append(changes, ConfigChange{entry.kind, entry.name, action})
	}
	for key, entry := range old {
		if _, exists := next[key]; !exists {
			changes = append(changes, ConfigChange{entry.kind, entry.name, "remove"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Kind+changes[i].Name < changes[j].Kind+changes[j].Name })
	return changes
}

func (cio *ConfigIO) PreviewImport(input ImportRequest) (ImportPreview, error) {
	cio.mu.RLock()
	defer cio.mu.RUnlock()
	candidate, err := cio.importCandidate(input)
	if err != nil {
		return ImportPreview{}, err
	}
	exported, _, err := cio.exportLocked()
	if err != nil {
		return ImportPreview{}, err
	}
	return ImportPreview{Revision: exported.Revision, Changes: changesBetween(cio.config, candidate)}, nil
}

func (cio *ConfigIO) backupDir() (string, error) {
	if cio.path == "" {
		return "", errors.New("configuration file path is required for backups")
	}
	path, err := filepath.Abs(cio.path)
	if err != nil {
		return "", err
	}
	return path + ".backups", nil
}

func (cio *ConfigIO) createBackup(data []byte) (ConfigBackup, error) {
	dir, err := cio.backupDir()
	if err != nil {
		return ConfigBackup{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ConfigBackup{}, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return ConfigBackup{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ConfigBackup{}, errors.New("backup directory must be a regular directory")
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return ConfigBackup{}, err
	}
	now := time.Now().UTC()
	name := now.Format("20060102T150405.000000000") + "-" + hex.EncodeToString(random) + ".toml"
	file, err := os.CreateTemp(dir, ".backup-*.tmp")
	if err != nil {
		return ConfigBackup{}, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return ConfigBackup{}, err
	}
	if closeErr != nil {
		return ConfigBackup{}, closeErr
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		return ConfigBackup{}, err
	}
	return ConfigBackup{Name: name, CreatedAt: now.Format(time.RFC3339), Size: int64(len(data))}, nil
}

func (cio *ConfigIO) Import(input ImportRequest) (ConfigBackup, error) {
	cio.mu.Lock()
	defer cio.mu.Unlock()
	current, disk, err := cio.exportLocked()
	if err != nil {
		return ConfigBackup{}, err
	}
	if input.Revision == "" || input.Revision != current.Revision {
		return ConfigBackup{}, ErrRevisionChanged
	}
	candidate, err := cio.importCandidate(input)
	if err != nil {
		return ConfigBackup{}, err
	}
	backup, err := cio.createBackup(disk)
	if err != nil {
		return ConfigBackup{}, fmt.Errorf("backup failed: %w", err)
	}
	latest, _, err := cio.exportLocked()
	if err != nil {
		return ConfigBackup{}, err
	}
	if latest.Revision != current.Revision {
		return ConfigBackup{}, ErrRevisionChanged
	}
	if err := cio.commit(candidate); err != nil {
		return ConfigBackup{}, err
	}
	return backup, nil
}

func (cio *ConfigIO) ListBackups() ([]ConfigBackup, error) {
	dir, err := cio.backupDir()
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return []ConfigBackup{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid backup directory")
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []ConfigBackup{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]ConfigBackup, 0)
	for _, entry := range entries {
		if !backupNamePattern.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		result = append(result, ConfigBackup{Name: entry.Name(), CreatedAt: info.ModTime().UTC().Format(time.RFC3339), Size: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name > result[j].Name })
	if len(result) > 100 {
		result = result[:100]
	}
	return result, nil
}

func (cio *ConfigIO) ReadBackup(name string) (string, error) {
	if !backupNamePattern.MatchString(name) || strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid backup name")
	}
	dir, err := cio.backupDir()
	if err != nil {
		return "", err
	}
	directory, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid backup directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("backup must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxImportBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxImportBytes {
		return "", errors.New("backup exceeds import size limit")
	}
	return string(data), nil
}
