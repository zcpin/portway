package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const markerName = "ssh-tunnel-portable.json"

// LockRunningExecutable prevents replacement while any daemon from a portable
// bundle is alive, including services or unlisted custom discovery directories.
func LockRunningExecutable(executable string) (func(), error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(resolved)
	if runtime.GOOS == "darwin" {
		root = filepath.Dir(filepath.Dir(root))
	}
	_, _, marker := layout(runtime.GOOS)
	if _, err := os.Stat(filepath.Join(root, marker)); os.IsNotExist(err) {
		return func() {}, nil
	} else if err != nil {
		return nil, err
	}
	unlock, err := lockDirectory(root, "usage", true)
	if os.IsPermission(err) {
		// A read-only installation remains runnable. The updater requires this
		// same writable lock and a writable parent, so it cannot replace it.
		return func() {}, nil
	}
	return unlock, err
}

type manifest struct {
	Schema     int    `json:"schema"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

type Installation struct {
	Root     string `json:"root"`
	Portable bool   `json:"portable"`
	Reason   string `json:"reason"`
}

func layout(platform string) (client, daemon, marker string) {
	switch platform {
	case "windows":
		return "ssh_tunnel_client.exe", "ssh-tunnel-daemon.exe", markerName
	case "darwin":
		return filepath.Join("Contents", "MacOS", "ssh_tunnel_client"), filepath.Join("Contents", "MacOS", "ssh-tunnel-daemon"), filepath.Join("Contents", "MacOS", markerName)
	default:
		return "ssh_tunnel_client", "ssh-tunnel-daemon", markerName
	}
}

func inspectInstallation(executable, version, repository, platform, arch string) Installation {
	root := filepath.Dir(executable)
	if platform == "darwin" {
		root = filepath.Dir(filepath.Dir(root))
	}
	result := Installation{Root: root}
	if _, err := parseVersion(version); err != nil {
		result.Reason = "开发或 CI 构建：请下载正式发布包后手动安装。"
		return result
	}
	if err := validateBundle(root, manifest{1, version, repository, platform, arch}); err != nil {
		result.Reason = "当前目录未通过便携版校验；安装版请运行安装包，系统服务请按服务管理方式更新。"
		return result
	}
	if err := safeInstallRoot(root); err != nil {
		result.Reason = err.Error()
		return result
	}
	result.Portable = true
	return result
}

func within(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func canonicalDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("update paths must be absolute and normalized")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(path, resolved) {
		return errors.New("update directory must not traverse a symlink or junction")
	}
	return nil
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("bundle executable or manifest is not a regular file")
	}
	return nil
}

func validateBundle(root string, expected manifest) error {
	client, daemon, marker := layout(expected.OS)
	for _, relative := range []string{client, daemon, marker} {
		if err := regularFile(filepath.Join(root, relative)); err != nil {
			return err
		}
	}
	var actual manifest
	if err := readJSONFile(filepath.Join(root, marker), &actual); err != nil {
		return err
	}
	if actual != expected {
		return errors.New("portable manifest does not match version, repository, or platform")
	}
	return nil
}

func safeInstallRoot(root string) error {
	if err := canonicalDirectory(root); err != nil {
		return err
	}
	if samePath(root, filepath.Dir(root)) {
		return errors.New("cannot replace a filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(root, home) {
		return errors.New("cannot replace the user home directory")
	}
	for _, name := range []string{".git", "go.mod", "pubspec.yaml", "unins000.exe", "config.toml", "ssh-tunnel.toml", ".ssh-tunnel"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			return fmt.Errorf("安装目录包含 %s；请使用专用便携目录，并将用户配置移至安装目录外。", name)
		}
	}
	return nil
}

type Instance struct {
	PID           int    `json:"pid"`
	DiscoveryPath string `json:"discovery_path"`
	ConfigPath    string `json:"config_path"`
	DataDir       string `json:"data_dir"`
}

type PrepareInput struct {
	Download         Download `json:"download"`
	ClientPID        int      `json:"client_pid"`
	ClientExecutable string   `json:"client_executable"`
	DiscoveryPaths   []string `json:"discovery_paths"`
}

// Plans contain no daemon tokens. The helper rereads and authenticates discovery
// records immediately before requesting shutdown.
type Plan struct {
	Current    manifest   `json:"current"`
	Next       manifest   `json:"next"`
	Root       string     `json:"root"`
	Stage      string     `json:"stage"`
	Backup     string     `json:"backup"`
	Failed     string     `json:"failed"`
	Directory  string     `json:"directory"`
	Helper     string     `json:"helper"`
	ClientPID  int        `json:"client_pid"`
	Instances  []Instance `json:"instances"`
	TreeSHA256 string     `json:"tree_sha256"`
}

type Prepared struct {
	PlanPath   string `json:"plan_path"`
	CancelPath string `json:"cancel_path"`
	ResultPath string `json:"result_path"`
	BackupPath string `json:"backup_path"`
}

func Prepare(ctx context.Context, executable, version, repository, cache string, input PrepareInput) (Prepared, error) {
	var result Prepared
	install := inspectInstallation(executable, version, repository, runtime.GOOS, runtime.GOARCH)
	if !install.Portable {
		return result, errors.New(install.Reason)
	}
	clientName, _, _ := layout(runtime.GOOS)
	clientExecutable, err := filepath.EvalSymlinks(input.ClientExecutable)
	if err != nil || !samePath(clientExecutable, filepath.Join(install.Root, clientName)) {
		return result, errors.New("the running client must belong to the same portable bundle as its updater")
	}
	current, _ := parseVersion(version)
	next, err := parseVersion(input.Download.Version)
	if err != nil || next.compare(current) <= 0 || input.Download.Repository != repository || input.Download.Kind != "portable" || input.ClientPID <= 0 || input.ClientPID == os.Getpid() {
		return result, errors.New("invalid update, current client PID, or version downgrade")
	}
	if err := verifyFile(input.Download.Path, input.Download.SHA256); err != nil {
		return result, err
	}
	instances, err := collectInstances(ctx, install.Root, input.DiscoveryPaths)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return result, err
	}
	if err := canonicalDirectory(cache); err != nil {
		return result, err
	}
	if within(install.Root, cache) || within(cache, install.Root) {
		return result, errors.New("updater cache must be separate from the installation")
	}
	work, err := os.MkdirTemp(cache, "apply-")
	if err != nil {
		return result, err
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(install.Root), ".ssh-tunnel-stage-")
	if err != nil {
		return result, fmt.Errorf("installation parent must be writable: %w", err)
	}
	stage := filepath.Join(stageDir, "bundle")
	if err := extractArchive(input.Download.Path, stage, runtime.GOOS); err != nil {
		return result, err
	}
	if runtime.GOOS == "darwin" {
		stage = filepath.Join(stage, "ssh_tunnel_client.app")
	}
	nextManifest := manifest{1, input.Download.Version, repository, runtime.GOOS, runtime.GOARCH}
	if err := validateBundle(stage, nextManifest); err != nil {
		return result, err
	}
	if err := probeBundle(ctx, stage, nextManifest); err != nil {
		return result, err
	}
	digest, err := treeDigest(stage)
	if err != nil {
		return result, err
	}
	helper := filepath.Join(work, "ssh-tunnel-update")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	if err := copyExecutable(executable, helper); err != nil {
		return result, err
	}
	suffix := filepath.Base(work)
	plan := Plan{
		Current: manifest{1, version, repository, runtime.GOOS, runtime.GOARCH}, Next: nextManifest,
		Root: install.Root, Stage: stage, Directory: work, Helper: helper,
		Backup:    filepath.Join(filepath.Dir(install.Root), ".ssh-tunnel-backup-"+suffix),
		Failed:    filepath.Join(filepath.Dir(install.Root), ".ssh-tunnel-failed-"+suffix),
		ClientPID: input.ClientPID, Instances: instances, TreeSHA256: digest,
	}
	planPath := filepath.Join(work, "plan.json")
	if err := writeJSONFile(planPath, plan); err != nil {
		return result, err
	}
	return Prepared{planPath, filepath.Join(work, "cancel"), filepath.Join(work, "result.json"), plan.Backup}, nil
}

func verifyFile(path, want string) error {
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("invalid SHA256")
	}
	if err := regularFile(path); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, maxDownload+1))
	if err != nil {
		return err
	}
	if n > maxDownload || hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want) {
		return errors.New("downloaded package has changed or failed SHA256 verification")
	}
	return nil
}

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func treeDigest(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(rel), info.Mode(), info.Size())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprint(h, target)
		case info.Mode().IsRegular():
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(h, file)
			file.Close()
			if err != nil {
				return err
			}
		case !info.IsDir():
			return errors.New("staged bundle contains a special file")
		}
		fmt.Fprint(h, "\x00")
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func readJSONFile(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("update metadata is too large")
	}
	return json.Unmarshal(data, value)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func (p Plan) validate() error {
	if p.Current.Schema != 1 || p.Next.Schema != 1 || p.Current.OS != runtime.GOOS || p.Next.OS != p.Current.OS || p.Current.Arch != runtime.GOARCH || p.Next.Arch != p.Current.Arch || p.Current.Repository != p.Next.Repository || p.ClientPID <= 0 {
		return errors.New("invalid update plan platform or identity")
	}
	a, errA := parseVersion(p.Current.Version)
	b, errB := parseVersion(p.Next.Version)
	if errA != nil || errB != nil || b.compare(a) <= 0 {
		return errors.New("update plan must advance the version")
	}
	for _, directory := range []string{p.Root, p.Stage, p.Directory} {
		if err := canonicalDirectory(directory); err != nil {
			return err
		}
	}
	parent := filepath.Dir(p.Root)
	rel, err := filepath.Rel(parent, p.Stage)
	parts := strings.Split(rel, string(filepath.Separator))
	if err != nil || len(parts) < 2 || !strings.HasPrefix(parts[0], ".ssh-tunnel-stage-") || within(p.Root, p.Stage) || within(p.Root, p.Directory) || within(p.Directory, p.Root) {
		return errors.New("update staging/helper directories are not isolated")
	}
	for target, prefix := range map[string]string{p.Backup: ".ssh-tunnel-backup-", p.Failed: ".ssh-tunnel-failed-"} {
		if !samePath(filepath.Dir(target), parent) || !strings.HasPrefix(filepath.Base(target), prefix) {
			return errors.New("backup and rollback paths must be siblings of the installation")
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return errors.New("backup or rollback destination already exists")
		}
	}
	if !samePath(filepath.Dir(p.Helper), p.Directory) {
		return errors.New("helper must live outside the installation")
	}
	if err := safeInstallRoot(p.Root); err != nil {
		return err
	}
	if err := validateBundle(p.Root, p.Current); err != nil {
		return err
	}
	if err := validateBundle(p.Stage, p.Next); err != nil {
		return err
	}
	digest, err := treeDigest(p.Stage)
	if err != nil || digest != p.TreeSHA256 {
		return errors.New("staged bundle changed after verification")
	}
	for _, instance := range p.Instances {
		if instance.PID <= 0 || instance.PID == p.ClientPID || !filepath.IsAbs(instance.ConfigPath) || !filepath.IsAbs(instance.DataDir) || !samePath(filepath.Dir(instance.DiscoveryPath), instance.DataDir) || within(p.Root, instance.ConfigPath) || within(p.Root, instance.DataDir) {
			return errors.New("daemon configuration and discovery must stay outside the installation")
		}
	}
	return nil
}

type applyOperations struct {
	wait   func(context.Context) error
	rename func(string, string) error
	start  func(string, manifest) error
}

type ApplyResult struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Backup  string `json:"backup"`
	Error   string `json:"error,omitempty"`
}

// The complete old directory is retained. Failed replacements are also retained,
// so rollback never needs a recursive deletion and never edits user configuration.
func apply(ctx context.Context, p Plan, ops applyOperations) (ApplyResult, error) {
	result := ApplyResult{Status: "unchanged", Version: p.Current.Version}
	if err := ops.wait(ctx); err != nil {
		return result, err
	}
	if err := p.validate(); err != nil {
		return result, err
	}
	if err := ops.rename(p.Root, p.Backup); err != nil {
		return result, err
	}
	rollback := func(cause error, installed bool) (ApplyResult, error) {
		if installed {
			if err := ops.rename(p.Root, p.Failed); err != nil {
				return ApplyResult{Status: "recovery_required", Version: p.Next.Version, Backup: p.Backup}, errors.Join(cause, fmt.Errorf("cannot move failed installation aside: %w", err))
			}
		}
		if err := ops.rename(p.Backup, p.Root); err != nil {
			return ApplyResult{Status: "recovery_required", Version: p.Current.Version, Backup: p.Backup}, errors.Join(cause, fmt.Errorf("restore backup manually: %w", err))
		}
		result.Status = "rolled_back"
		if err := ops.start(p.Root, p.Current); err != nil {
			return result, errors.Join(cause, fmt.Errorf("original files restored but restart failed: %w", err))
		}
		return result, cause
	}
	if err := ops.rename(p.Stage, p.Root); err != nil {
		return rollback(err, false)
	}
	if err := ops.start(p.Root, p.Next); err != nil {
		return rollback(err, true)
	}
	return ApplyResult{Status: "updated", Version: p.Next.Version, Backup: p.Backup}, nil
}
