package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func fixtureBundle(t *testing.T, root string, m manifest) {
	t.Helper()
	client, daemon, marker := layout(m.OS)
	for _, name := range []string{client, daemon} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(m.Version), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONFile(filepath.Join(root, marker), m); err != nil {
		t.Fatal(err)
	}
}

func fixturePlan(t *testing.T) Plan {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{
		Current: manifest{1, "v1.0.0", "owner/repo", runtime.GOOS, runtime.GOARCH},
		Next:    manifest{1, "v2.0.0", "owner/repo", runtime.GOOS, runtime.GOARCH},
		Root:    filepath.Join(dir, "installed"), Stage: filepath.Join(dir, ".ssh-tunnel-stage-test", "bundle"),
		Directory: filepath.Join(dir, "cache", "apply-test"), Backup: filepath.Join(dir, ".ssh-tunnel-backup-test"),
		Failed: filepath.Join(dir, ".ssh-tunnel-failed-test"), ClientPID: os.Getpid() + 1,
	}
	p.Helper = filepath.Join(p.Directory, "helper")
	if err := os.MkdirAll(p.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureBundle(t, p.Root, p.Current)
	fixtureBundle(t, p.Stage, p.Next)
	p.TreeSHA256, err = treeDigest(p.Stage)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func assertVersion(t *testing.T, root, version string) {
	t.Helper()
	client, _, _ := layout(runtime.GOOS)
	data, err := os.ReadFile(filepath.Join(root, client))
	if err != nil || string(data) != version {
		t.Fatalf("%s: got %q, err %v, want %s", root, data, err, version)
	}
}

func TestPortableReplacementAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "wait timeout", "backup failure", "replacement failure", "startup failure", "rollback failure"} {
		t.Run(scenario, func(t *testing.T) {
			p := fixturePlan(t)
			config := filepath.Join(filepath.Dir(p.Root), "user-config.toml")
			if err := os.WriteFile(config, []byte("private configuration"), 0600); err != nil {
				t.Fatal(err)
			}
			started := []string{}
			renamed := 0
			ops := applyOperations{
				wait: func(context.Context) error {
					if scenario == "wait timeout" {
						return context.DeadlineExceeded
					}
					return nil
				},
				rename: func(from, to string) error {
					renamed++
					if scenario == "backup failure" && from == p.Root {
						return errors.New("installation locked")
					}
					if scenario == "replacement failure" && from == p.Stage {
						return errors.New("disk failure")
					}
					if scenario == "rollback failure" && from == p.Backup {
						return errors.New("restore failed")
					}
					return os.Rename(from, to)
				},
				start: func(root string, m manifest) error {
					assertVersion(t, root, m.Version)
					started = append(started, m.Version)
					if (scenario == "startup failure" || scenario == "rollback failure") && m.Version == p.Next.Version {
						return errors.New("new client failed")
					}
					return nil
				},
			}
			result, err := apply(context.Background(), p, ops)
			data, _ := os.ReadFile(config)
			if string(data) != "private configuration" {
				t.Fatal("configuration modified")
			}
			switch scenario {
			case "success":
				if err != nil || result.Status != "updated" || len(started) != 1 {
					t.Fatalf("%+v, %v", result, err)
				}
				assertVersion(t, p.Root, p.Next.Version)
				assertVersion(t, p.Backup, p.Current.Version)
			case "rollback failure":
				if err == nil || result.Status != "recovery_required" || result.Backup != p.Backup {
					t.Fatalf("lost recovery information: %+v, %v", result, err)
				}
				assertVersion(t, p.Backup, p.Current.Version)
				assertVersion(t, p.Failed, p.Next.Version)
			default:
				if err == nil {
					t.Fatal("failure ignored")
				}
				assertVersion(t, p.Root, p.Current.Version)
				if scenario == "wait timeout" && (renamed != 0 || len(started) != 0) {
					t.Fatal("mutated installation before process exit")
				}
				if scenario == "replacement failure" || scenario == "startup failure" {
					if result.Status != "rolled_back" || started[len(started)-1] != p.Current.Version {
						t.Fatalf("original app not restarted: %+v %v", result, started)
					}
				}
			}
		})
	}
}

func TestPortablePlanGuards(t *testing.T) {
	for _, scenario := range []string{"tampered stage", "backup outside parent", "helper inside install", "configuration in bundle", "missing marker", "installer", "downgrade", "already backed up"} {
		t.Run(scenario, func(t *testing.T) {
			p := fixturePlan(t)
			switch scenario {
			case "tampered stage":
				client, _, _ := layout(runtime.GOOS)
				_ = os.WriteFile(filepath.Join(p.Stage, client), []byte("tampered"), 0755)
			case "backup outside parent":
				p.Backup = filepath.Join(p.Directory, "other")
			case "helper inside install":
				p.Directory, p.Helper = p.Root, filepath.Join(p.Root, "helper")
			case "configuration in bundle":
				_ = os.WriteFile(filepath.Join(p.Root, "config.toml"), []byte("user config"), 0600)
			case "missing marker":
				_, _, marker := layout(runtime.GOOS)
				_ = os.Rename(filepath.Join(p.Root, marker), filepath.Join(p.Root, "saved-marker"))
			case "installer":
				_ = os.WriteFile(filepath.Join(p.Root, "unins000.exe"), []byte("installer"), 0600)
			case "downgrade":
				p.Next.Version = "v0.9.0"
			case "already backed up":
				_ = os.Mkdir(p.Backup, 0700)
			}
			if err := p.validate(); err == nil {
				t.Fatal("unsafe plan accepted")
			}
			assertVersion(t, p.Root, p.Current.Version)
		})
	}
}

func TestRunningBundleBlocksReplacement(t *testing.T) {
	p := fixturePlan(t)
	_, daemon, _ := layout(runtime.GOOS)
	one, err := LockRunningExecutable(filepath.Join(p.Root, daemon))
	if err != nil {
		t.Fatal(err)
	}
	two, err := LockRunningExecutable(filepath.Join(p.Root, daemon))
	if err != nil {
		one()
		t.Fatal("multiple daemons must share the bundle:", err)
	}
	guard, err := lockDirectory(p.Root, "usage", false)
	if err == nil {
		guard()
		t.Error("running bundle could be replaced")
	}
	one()
	guard, err = lockDirectory(p.Root, "usage", false)
	if err == nil {
		guard()
		t.Error("second daemon did not protect the bundle")
	}
	two()
	guard, err = lockDirectory(p.Root, "usage", false)
	if err != nil {
		t.Fatal("bundle stayed locked after exit:", err)
	}
	guard()
}

func TestWaitForExitAndExclusiveUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := waitForExit(ctx, []int{10}, func() bool { return false }, func(int) bool { return true }); err == nil {
		t.Fatal("live process ignored")
	}
	if err := waitForExit(context.Background(), nil, func() bool { return true }, func(int) bool { return false }); err == nil {
		t.Fatal("cancellation ignored")
	}
	if err := waitForExit(context.Background(), []int{10}, func() bool { return false }, func(int) bool { return false }); err != nil {
		t.Fatal(err)
	}
	p := fixturePlan(t)
	unlock, err := lockInstall(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lockInstall(p.Root)
	if err == nil {
		second()
		t.Error("concurrent update lock was granted")
	}
	unlock()
	unlock, err = lockInstall(p.Root)
	if err != nil {
		t.Fatal("update lock did not release:", err)
	}
	unlock()
}
